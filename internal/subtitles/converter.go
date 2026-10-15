// SPDX-License-Identifier: GPL-2.0-only

package subtitles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

var ErrOutputTooLarge = errors.New("subtitle converter output exceeds limit")

// Converter invokes one ffmpeg process for each Convert call. It holds no
// process state between requests and is safe to use concurrently.
type Converter struct {
	path      string
	timeout   time.Duration
	maxOutput int
}

// ConvertJob describes either an embedded subtitle stream (StreamIndex >= 0)
// or an external subtitle input (StreamIndex == -1).
type ConvertJob struct {
	// Source is a borrowed descriptor. Convert never closes it.
	Source       *os.File
	StreamIndex  int
	OutputFormat string
}

const subtitleInputFD = 3

// NewConverter constructs a bounded request-scoped ffmpeg converter. A binary
// path containing a slash is resolved now, before a request changes process
// working-directory assumptions; a bare command remains PATH-resolved by exec.
func NewConverter(path string, timeout time.Duration, maxOutput int) (*Converter, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("ffmpeg path is empty")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("ffmpeg conversion timeout must be positive: %s", timeout)
	}
	if maxOutput <= 0 {
		return nil, fmt.Errorf("ffmpeg output limit must be positive: %d", maxOutput)
	}
	if strings.ContainsRune(path, filepath.Separator) && !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("absolutize ffmpeg path %q: %w", path, err)
		}
		path = absolute
	}
	return &Converter{path: path, timeout: timeout, maxOutput: maxOutput}, nil
}

// Convert runs ffmpeg and returns its complete, bounded stdout. The caller's
// context owns the process lifetime: cancellation kills the full process group
// and Convert does not return until the ffmpeg leader has been reaped.
func (converter *Converter) Convert(ctx context.Context, job ConvertJob) ([]byte, error) {
	if converter == nil {
		return nil, errors.New("subtitle converter is nil")
	}
	if ctx == nil {
		return nil, errors.New("subtitle conversion context is nil")
	}
	if job.Source == nil {
		return nil, errors.New("ffmpeg subtitle source is nil")
	}
	if job.StreamIndex < -1 {
		return nil, fmt.Errorf("ffmpeg subtitle stream index is invalid: %d", job.StreamIndex)
	}
	if job.OutputFormat != "webvtt" && job.OutputFormat != "srt" {
		return nil, fmt.Errorf("ffmpeg subtitle output format is unsupported: %q", job.OutputFormat)
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}

	deadlineCtx, stopDeadline := context.WithTimeoutCause(ctx, converter.timeout, context.DeadlineExceeded)
	defer stopDeadline()
	commandCtx, cancel := context.WithCancelCause(deadlineCtx)
	defer cancel(nil)

	streamMap := "0:s:0"
	if job.StreamIndex >= 0 {
		streamMap = fmt.Sprintf("0:%d", job.StreamIndex)
	}
	command := exec.CommandContext(commandCtx, converter.path,
		"-v", "error",
		"-i", fmt.Sprintf("/dev/fd/%d", subtitleInputFD),
		"-map", streamMap,
		"-an", "-vn",
		"-c:s", job.OutputFormat,
		"-f", job.OutputFormat,
		"pipe:1",
	)
	command.ExtraFiles = []*os.File{job.Source}
	// ffmpeg may leave helper processes holding its output descriptors. A new
	// group lets every cancellation path kill all descendants, not just leader.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return killSubtitleProcessGroup(command.Process.Pid)
	}
	command.WaitDelay = time.Second

	tracker := newSubtitleProgressTracker(clock.NewReal())
	overflow := newSubtitleOutputOverflow(cancel)
	var stdout, stderr subtitleCappedBuffer
	stdout.limit, stdout.progress, stdout.overflow = converter.maxOutput, func() { overflow.MarkProgress(tracker) }, overflow.Exceed
	stderr.limit, stderr.progress, stderr.overflow = converter.maxOutput, func() { overflow.MarkProgress(tracker) }, overflow.Exceed
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Start(); err != nil {
		if cause := context.Cause(commandCtx); cause != nil {
			return nil, cause
		}
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	processID := command.Process.Pid
	stopWatchdog := startSubtitleNoOutputWatchdog(converter.timeout, tracker, cancel)
	defer stopWatchdog()

	groupDone := make(chan struct{})
	stopGroupCancellation := context.AfterFunc(commandCtx, func() {
		defer close(groupDone)
		_ = killSubtitleProcessGroup(processID)
	})

	waitErr := command.Wait()
	// Each stop joins its callback when it was already running. This prevents a
	// late watchdog/group-killer goroutine from outliving the request.
	stopWatchdog()
	if !stopGroupCancellation() {
		<-groupDone
	}
	// Wait only reaps the command leader. Kill its group on every path, even a
	// successful exit, so descendants cannot survive behind inherited pipes.
	_ = killSubtitleProcessGroup(processID)

	// WithCancelCause records the first cancellation cause. Do not infer a
	// later cause from ctx.Err: a concurrent overflow, caller cancellation, and
	// watchdog must report the cause which actually won the race.
	if cause := context.Cause(commandCtx); cause != nil {
		if errors.Is(cause, ErrOutputTooLarge) {
			return nil, ErrOutputTooLarge
		}
		return nil, cause
	}
	if waitErr != nil {
		return nil, ffmpegExitError(waitErr, stderr.bytes())
	}
	return stdout.bytes(), nil
}

func killSubtitleProcessGroup(processID int) error {
	return syscall.Kill(-processID, syscall.SIGKILL)
}

// subtitleProgressTracker uses time.Time's monotonic component, so wall-clock
// updates cannot make a healthy ffmpeg trip the no-output watchdog.
type subtitleProgressTracker struct {
	mu    sync.Mutex
	clock clock.Clock
	last  time.Time
}

func newSubtitleProgressTracker(timeSource clock.Clock) *subtitleProgressTracker {
	return &subtitleProgressTracker{clock: timeSource, last: timeSource.Now()}
}

func (tracker *subtitleProgressTracker) Mark() {
	tracker.mu.Lock()
	tracker.last = tracker.clock.Now()
	tracker.mu.Unlock()
}

func (tracker *subtitleProgressTracker) Since() time.Duration {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.clock.Now().Sub(tracker.last)
}

func startSubtitleNoOutputWatchdog(timeout time.Duration, tracker *subtitleProgressTracker, cancel context.CancelCauseFunc) func() {
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

// subtitleOutputOverflow latches globally across the two independently capped
// buffers. Once either cap is crossed no later bytes reset the watchdog.
type subtitleOutputOverflow struct {
	mu       sync.Mutex
	cancel   context.CancelCauseFunc
	exceeded bool
}

func newSubtitleOutputOverflow(cancel context.CancelCauseFunc) *subtitleOutputOverflow {
	return &subtitleOutputOverflow{cancel: cancel}
}

func (overflow *subtitleOutputOverflow) MarkProgress(tracker *subtitleProgressTracker) {
	overflow.mu.Lock()
	defer overflow.mu.Unlock()
	if !overflow.exceeded {
		tracker.Mark()
	}
}

func (overflow *subtitleOutputOverflow) Exceed() {
	overflow.mu.Lock()
	if overflow.exceeded {
		overflow.mu.Unlock()
		return
	}
	overflow.exceeded = true
	overflow.mu.Unlock()
	overflow.cancel(ErrOutputTooLarge)
}

// subtitleCappedBuffer retains no more than one stream's configured limit and
// returns successful writes to allow os/exec's pipe copier to drain cleanly.
type subtitleCappedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
	progress func()
	overflow func()
}

func (buffer *subtitleCappedBuffer) Write(data []byte) (int, error) {
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

func (buffer *subtitleCappedBuffer) bytes() []byte {
	return buffer.data
}

func ffmpegExitError(waitErr error, diagnostic []byte) error {
	if len(diagnostic) == 0 {
		return fmt.Errorf("ffmpeg exited unsuccessfully: %w", waitErr)
	}
	return fmt.Errorf("ffmpeg exited unsuccessfully: %w: %s", waitErr, strings.TrimSpace(string(diagnostic)))
}
