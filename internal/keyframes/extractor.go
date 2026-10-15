// SPDX-License-Identifier: GPL-2.0-only

package keyframes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

const ticksPerSecond = int64(10_000_000)

const sourceFD = 3

var (
	// ErrOutputTooLarge means ffprobe wrote more than the configured bounded
	// output budget. Both stdout and retained diagnostic stderr use this cap.
	ErrOutputTooLarge = errors.New("ffprobe output exceeds limit")

	// ErrMalformedOutput means ffprobe returned a CSV row without the delimiters
	// required by its row kind.
	ErrMalformedOutput = errors.New("malformed ffprobe CSV")
)
var killProcessGroupFn = killProcessGroup

// Data is the duration and keyframe positions reported by ffprobe. Values are
// expressed in .NET ticks (100 nanoseconds) to match Jellyfin's media model.
type Data struct {
	TotalDurationTicks int64
	KeyframeTicks      []int64
}

// Extractor invokes one ffprobe executable with a caller-bounded lifetime and
// bounded captured output. It is immutable after construction and safe for
// concurrent use.
type Extractor struct {
	path      string
	timeout   time.Duration
	maxOutput int
}

// NewExtractor constructs an ffprobe keyframe extractor. The output limit is
// applied independently to stdout and diagnostic stderr so an untrusted media
// file cannot make either stream allocate without bound.
func NewExtractor(path string, timeout time.Duration, maxOutput int) (*Extractor, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("ffprobe path is empty")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("ffprobe timeout must be positive: %s", timeout)
	}
	if maxOutput <= 0 {
		return nil, fmt.Errorf("ffprobe output limit must be positive: %d", maxOutput)
	}

	return &Extractor{path: path, timeout: timeout, maxOutput: maxOutput}, nil
}

// Extract probes the borrowed source descriptor and converts packet timestamps
// and duration to 100-nanosecond ticks. The derived command context always
// retains the caller's cancellation and earlier deadline.
func (extractor *Extractor) Extract(ctx context.Context, source *os.File) (Data, error) {
	if extractor == nil {
		return Data{}, errors.New("keyframe extractor is nil")
	}
	if ctx == nil {
		return Data{}, errors.New("keyframe extraction context is nil")
	}
	if source == nil {
		return Data{}, errors.New("ffprobe source file is nil")
	}
	if err := ctx.Err(); err != nil {
		return Data{}, err
	}

	commandCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	command := exec.CommandContext(commandCtx, extractor.path,
		"-fflags", "+genpts",
		"-v", "error",
		"-skip_frame", "nokey",
		"-show_entries", "format=duration",
		"-show_entries", "stream=duration",
		"-show_entries", "packet=pts_time,flags",
		"-select_streams", "v",
		"-of", "csv",
		sourceFDPath(),
	)
	// ffprobe can start helpers that inherit its output descriptors. Keep the
	// probe and every helper in a process group so cancellation reaps all of
	// them rather than allowing a descendant to survive behind an open pipe.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.ExtraFiles = []*os.File{source}
	command.Cancel = func() error {
		return killProcessGroupFn(command.Process.Pid)
	}
	// A child which leaves a pipe open after its parent exits must not make a
	// one-shot extraction wait forever. The group cancellation registered after
	// Start bounds this wait by the derived caller context as well.
	command.WaitDelay = time.Second

	tracker := newProgressTracker(clock.NewReal())
	outputOverflow := newOutputOverflow(cancel)
	var stdout, stderr cappedBuffer
	stdout.limit, stdout.progress, stdout.overflow = extractor.maxOutput, func() { outputOverflow.MarkProgress(tracker) }, outputOverflow.Exceed
	stderr.limit, stderr.progress, stderr.overflow = extractor.maxOutput, func() { outputOverflow.MarkProgress(tracker) }, outputOverflow.Exceed
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Start(); err != nil {
		if contextErr := commandCtx.Err(); contextErr != nil {
			return Data{}, contextErr
		}
		return Data{}, fmt.Errorf("starting ffprobe: %w", err)
	}

	processID := command.Process.Pid
	stopWatchdog := startNoOutputWatchdog(extractor.timeout, tracker, cancel)
	defer stopWatchdog()
	groupCancellationDone := make(chan struct{})
	stopGroupCancellation := context.AfterFunc(commandCtx, func() {
		defer close(groupCancellationDone)
		_ = killProcessGroupFn(processID)
	})

	waitErr := command.Wait()
	stopWatchdog()
	if !stopGroupCancellation() {
		<-groupCancellationDone
	}
	// Wait reaps only ffprobe itself. Its process group can still contain a
	// helper that inherited an output descriptor, including when ffprobe exits
	// successfully or with a nonzero status (where Wait preserves that status
	// instead of returning ErrWaitDelay). Kill the group on every completed
	// extraction so no helper can outlive this call.
	_ = killProcessGroupFn(processID)
	// ErrWaitDelay after a successful leader means os/exec closed inherited
	// pipes. The CSV was produced by that successful leader and remains valid.
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		waitErr = nil
	}
	cause := context.Cause(commandCtx)
	if errors.Is(cause, ErrOutputTooLarge) {
		return Data{}, ErrOutputTooLarge
	}
	if contextErr := ctx.Err(); contextErr != nil {
		if callerCause := context.Cause(ctx); callerCause != nil {
			return Data{}, callerCause
		}
		return Data{}, contextErr
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return Data{}, context.DeadlineExceeded
	}
	if waitErr != nil {
		return Data{}, ffprobeExitError(waitErr, stderr.bytes())
	}

	return parseCSV(stdout.bytes())
}

func sourceFDPath() string {
	return "/dev/fd/" + strconv.Itoa(sourceFD)
}

func killProcessGroup(processID int) error {
	return syscall.Kill(-processID, syscall.SIGKILL)
}

// startNoOutputWatchdog cancels extraction only when neither ffprobe stream
// makes byte progress for timeout. It is deliberately independent of total
// runtime: a long probe that continues producing bytes remains healthy.
func startNoOutputWatchdog(timeout time.Duration, tracker *progressTracker, cancel context.CancelCauseFunc) func() {
	stopped := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(done)
		timer := tracker.clock.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C():
				elapsed := tracker.Since()
				if elapsed < timeout {
					timer.Reset(timeout - elapsed)
					continue
				}
				cancel(context.DeadlineExceeded)
				return
			case <-stopped:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopped)
			<-done
		})
	}
}

// progressTracker retains time.Time's monotonic component, so wall-clock
// corrections cannot alter the configured no-output interval.
type progressTracker struct {
	mu    sync.Mutex
	clock clock.Clock
	last  time.Time
}

func newProgressTracker(timeSource clock.Clock) *progressTracker {
	return &progressTracker{clock: timeSource, last: timeSource.Now()}
}

func (tracker *progressTracker) Mark() {
	tracker.mu.Lock()
	tracker.last = tracker.clock.Now()
	tracker.mu.Unlock()
}

func (tracker *progressTracker) Since() time.Duration {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.clock.Now().Sub(tracker.last)
}

// outputOverflow serializes output-cap state across stdout and stderr. Once
// the cap is crossed, later output cannot keep the no-output watchdog alive.
// Cancellation cause arbitration is owned by commandCtx: the first cause to
// reach it wins, and Extract reads that cause after command completion.
type outputOverflow struct {
	mu       sync.Mutex
	cancel   context.CancelCauseFunc
	exceeded bool
}

func newOutputOverflow(cancel context.CancelCauseFunc) *outputOverflow {
	return &outputOverflow{cancel: cancel}
}

func (overflow *outputOverflow) MarkProgress(tracker *progressTracker) {
	overflow.mu.Lock()
	defer overflow.mu.Unlock()
	if !overflow.exceeded {
		tracker.Mark()
	}
}

func (overflow *outputOverflow) Exceed() {
	overflow.mu.Lock()
	if overflow.exceeded {
		overflow.mu.Unlock()
		return
	}
	overflow.exceeded = true
	overflow.mu.Unlock()

	// Cancel immediately so the process group is killed at the output cap.
	// WithCancelCause retains whichever concurrent caller, watchdog, or cap
	// cause reached it first; Extract resolves that winner after Wait.
	overflow.cancel(ErrOutputTooLarge)
}

func ffprobeExitError(waitErr error, diagnostic []byte) error {
	if len(diagnostic) == 0 {
		return fmt.Errorf("ffprobe exited unsuccessfully: %w", waitErr)
	}
	return fmt.Errorf("ffprobe exited unsuccessfully: %w: %s", waitErr, strings.TrimSpace(string(diagnostic)))
}

// cappedBuffer retains at most limit bytes. Its first write beyond that limit
// latches overflow and immediately cancels the probe; later writes neither
// retain data nor report progress. It still reports successful writes so the
// os/exec pipe-copy goroutine can complete its cancellation cleanup.
type cappedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
	progress func()
	overflow func()
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	if len(data) == 0 || buffer.exceeded {
		return len(data), nil
	}

	remaining := buffer.limit - len(buffer.data)
	if len(data) > remaining {
		if remaining > 0 {
			buffer.data = append(buffer.data, data[:remaining]...)
		}
		buffer.exceeded = true
		if buffer.overflow != nil {
			buffer.overflow()
		}
		return len(data), nil
	}

	buffer.data = append(buffer.data, data...)
	if buffer.progress != nil {
		buffer.progress()
	}
	return len(data), nil
}

func (buffer *cappedBuffer) bytes() []byte {
	return buffer.data
}

func parseCSV(raw []byte) (Data, error) {
	result := Data{KeyframeTicks: make([]int64, 0)}
	var streamDuration, formatDuration float64

	for start := 0; start < len(raw); {
		end := bytes.IndexByte(raw[start:], '\n')
		if end < 0 {
			end = len(raw)
		} else {
			end += start
		}
		line := raw[start:end]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if err := parseCSVLine(line, &result, &streamDuration, &formatDuration); err != nil {
			return Data{}, err
		}
		if end == len(raw) {
			break
		}
		start = end + 1
	}

	duration := formatDuration
	if streamDuration > 0 {
		duration = streamDuration
	}
	if ticks, ok := durationSecondsToTicks(duration); ok {
		result.TotalDurationTicks = ticks
	}
	return result, nil
}

func parseCSVLine(line []byte, result *Data, streamDuration, formatDuration *float64) error {
	if len(line) == 0 {
		return nil
	}

	firstComma := bytes.IndexByte(line, ',')
	if firstComma < 0 {
		return fmt.Errorf("%w: row is missing its first comma", ErrMalformedOutput)
	}
	lineType, rest := line[:firstComma], line[firstComma+1:]
	switch {
	case bytes.EqualFold(lineType, []byte("packet")):
		secondComma := bytes.IndexByte(rest, ',')
		if secondComma < 0 {
			return fmt.Errorf("%w: packet row is missing its second comma", ErrMalformedOutput)
		}
		if !bytes.HasPrefix(rest[secondComma+1:], []byte("K_")) {
			return nil
		}
		seconds, ok := parseFiniteDecimal(rest[:secondComma])
		if !ok {
			return nil
		}
		ticks, ok := packetSecondsToTicks(seconds)
		if !ok {
			return nil
		}
		result.KeyframeTicks = append(result.KeyframeTicks, ticks)
	case bytes.EqualFold(lineType, []byte("stream")):
		if seconds, ok := parseFiniteDecimal(rest); ok {
			*streamDuration = seconds
		}
	case bytes.EqualFold(lineType, []byte("format")):
		if seconds, ok := parseFiniteDecimal(rest); ok {
			*formatDuration = seconds
		}
	}
	return nil
}

func parseFiniteDecimal(value []byte) (float64, bool) {
	if !isAllowDecimalPointNumber(value) {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(string(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

// isAllowDecimalPointNumber mirrors NumberStyles.AllowDecimalPoint, the
// upstream parser's deliberately narrow grammar: decimal digits with at most
// one decimal point. In particular, signs, whitespace, and exponents are not
// accepted as ffprobe duration or packet timestamp values.
func isAllowDecimalPointNumber(value []byte) bool {
	digits, decimalPoint := false, false
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
			digits = true
		case character == '.' && !decimalPoint:
			decimalPoint = true
		default:
			return false
		}
	}
	return digits
}

// packetSecondsToTicks matches Convert.ToInt64 in the pinned extractor,
// including midpoint-to-even rounding for packet positions.
func packetSecondsToTicks(seconds float64) (int64, bool) {
	ticks := math.RoundToEven(seconds * float64(ticksPerSecond))
	if math.IsNaN(ticks) || math.IsInf(ticks, 0) || ticks < float64(math.MinInt64) || ticks >= -float64(math.MinInt64) {
		return 0, false
	}
	return int64(ticks), true
}

// durationSecondsToTicks matches TimeSpan.FromSeconds(...).Ticks. TimeSpan
// truncates the scaled double toward zero rather than using packet rounding.
func durationSecondsToTicks(seconds float64) (int64, bool) {
	ticks := seconds * float64(ticksPerSecond)
	if math.IsNaN(ticks) || math.IsInf(ticks, 0) || ticks < float64(math.MinInt64) || ticks > -float64(math.MinInt64) {
		return 0, false
	}
	// math.MaxInt64 rounds to 2^63 as a float64. TimeSpan accepts that
	// double boundary and represents it as its largest integral tick value.
	if ticks == -float64(math.MinInt64) {
		return math.MaxInt64, true
	}
	return int64(ticks), true
}
