// SPDX-License-Identifier: GPL-2.0-only

package subtitles_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/subtitles"
	"go.uber.org/goleak"
)

const converterFixtureTimeout = 3 * time.Second

func TestNewConverterRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		path      string
		timeout   time.Duration
		maxOutput int
	}{
		{name: "empty executable", timeout: time.Second, maxOutput: 1},
		{name: "zero watchdog", path: "ffmpeg", maxOutput: 1},
		{name: "negative watchdog", path: "ffmpeg", timeout: -time.Second, maxOutput: 1},
		{name: "zero output cap", path: "ffmpeg", timeout: time.Second},
		{name: "negative output cap", path: "ffmpeg", timeout: time.Second, maxOutput: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if converter, err := subtitles.NewConverter(tc.path, tc.timeout, tc.maxOutput); err == nil || converter != nil {
				t.Fatalf("NewConverter(%q, %s, %d) = (%v, %v), want nil converter and error", tc.path, tc.timeout, tc.maxOutput, converter, err)
			}
		})
	}
}

func TestConverterBorrowsFD3ForEmbeddedAndExternalStreams(t *testing.T) {
	for _, tc := range []struct {
		name        string
		streamIndex int
		output      string
		want        []string
	}{
		{
			name:        "embedded webvtt",
			streamIndex: 7,
			output:      "webvtt",
			want: []string{
				"-v", "error", "-i", "/dev/fd/3", "-map", "0:7",
				"-an", "-vn", "-c:s", "webvtt", "-f", "webvtt", "pipe:1",
			},
		},
		{
			name:        "external srt",
			streamIndex: -1,
			output:      "srt",
			want: []string{
				"-v", "error", "-i", "/dev/fd/3", "-map", "0:s:0",
				"-an", "-vn", "-c:s", "srt", "-f", "srt", "pipe:1",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := filepath.Join(t.TempDir(), "argv")
			contents := filepath.Join(t.TempDir(), "fd3-contents")
			t.Setenv("SUBTITLE_ARGV_FILE", argv)
			t.Setenv("SUBTITLE_FD3_FILE", contents)
			path := writeFakeFFmpeg(t, `printf '%s\n' "$@" > "$SUBTITLE_ARGV_FILE"
cat /dev/fd/3 > "$SUBTITLE_FD3_FILE"
printf 'WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nok\n'`)
			converter, err := subtitles.NewConverter(path, converterFixtureTimeout, 4096)
			if err != nil {
				t.Fatal(err)
			}
			source := converterInput(t, "borrowed subtitle bytes")

			got, err := converter.Convert(context.Background(), subtitles.ConvertJob{
				Source:       source,
				StreamIndex:  tc.streamIndex,
				OutputFormat: tc.output,
			})
			if err != nil {
				t.Fatal(err)
			}
			const wantOutput = "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nok\n"
			if string(got) != wantOutput {
				t.Fatalf("Convert stdout = %q, want %q", got, wantOutput)
			}
			if got := readSubtitleLines(t, argv); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ffmpeg argv = %#v\nwant %#v", got, tc.want)
			}
			if got, err := os.ReadFile(contents); err != nil || string(got) != "borrowed subtitle bytes" {
				t.Fatalf("ffmpeg fd3 contents = %q, %v; want borrowed descriptor bytes", got, err)
			}
			if _, err := source.Stat(); err != nil {
				t.Fatalf("converter closed borrowed source: %v", err)
			}
		})
	}
}

func TestConverterSurfacesNonzeroExitDiagnostic(t *testing.T) {
	path := writeFakeFFmpeg(t, "printf 'unsupported subtitle codec\\n' >&2\nexit 27")
	converter, err := subtitles.NewConverter(path, converterFixtureTimeout, 4096)
	if err != nil {
		t.Fatal(err)
	}

	_, err = converter.Convert(context.Background(), subtitles.ConvertJob{
		Source: converterInput(t, "diagnostic input"), StreamIndex: 2, OutputFormat: "webvtt",
	})
	if err == nil {
		t.Fatal("Convert succeeded, want nonzero ffmpeg error")
	}
	if !strings.Contains(err.Error(), "unsupported subtitle codec") {
		t.Fatalf("Convert error = %q, want stderr diagnostic", err)
	}
}

func TestConverterBoundsBothCapturedStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "stdout", body: "printf '01234567890123456789'"},
		{name: "stderr", body: "printf '01234567890123456789' >&2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			converter, err := subtitles.NewConverter(writeFakeFFmpeg(t, tc.body), converterFixtureTimeout, 16)
			if err != nil {
				t.Fatal(err)
			}
			_, err = converter.Convert(context.Background(), subtitles.ConvertJob{
				Source: converterInput(t, "bounded input"), StreamIndex: 0, OutputFormat: "webvtt",
			})
			if !errors.Is(err, subtitles.ErrOutputTooLarge) {
				t.Fatalf("Convert error = %v, want %v", err, subtitles.ErrOutputTooLarge)
			}
		})
	}
}

func TestConverterWaitDelayRejectsTruncatedOutputAndReapsProcessGroup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("SUBTITLE_PID_FILE", pidFile)
	t.Setenv("SUBTITLE_READY_FILE", readyFile)
	path := writeFakeFFmpeg(t, `(
	printf 'TRUNCATION-SENTINEL\n'
	printf 'truncation diagnostic sentinel\n' >&2
	while :; do
		printf x
		printf x >&2
		sleep 0.01
	done
) &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$SUBTITLE_PID_FILE"
: > "$SUBTITLE_READY_FILE"
exit 0`)
	converter, err := subtitles.NewConverter(path, converterFixtureTimeout, 4096)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type conversionResult struct {
		output []byte
		err    error
	}
	result := make(chan conversionResult, 1)
	done := make(chan struct{})
	input := converterInput(t, "wait delay input")
	go func() {
		defer close(done)
		output, convertErr := converter.Convert(ctx, subtitles.ConvertJob{
			Source: input, StreamIndex: 0, OutputFormat: "webvtt",
		})
		result <- conversionResult{output: output, err: convertErr}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(converterFixtureTimeout):
			t.Error("Convert did not finish during test cleanup")
		}
	})

	waitForSubtitleFile(t, readyFile, converterFixtureTimeout)
	processIDs := readSubtitlePIDs(t, pidFile)
	testClock := clock.NewReal()
	started := testClock.Now()
	select {
	case got := <-result:
		if !errors.Is(got.err, exec.ErrWaitDelay) {
			t.Fatalf("Convert error = %v, want wrapped %v", got.err, exec.ErrWaitDelay)
		}
		if !strings.Contains(got.err.Error(), "truncation diagnostic sentinel") {
			t.Fatalf("Convert error = %q, want truncation diagnostic sentinel", got.err)
		}
		if len(got.output) != 0 {
			t.Fatalf("Convert returned incomplete output %q, want no cacheable content", got.output)
		}
	case <-time.After(converterFixtureTimeout):
		t.Fatal("Convert did not bound cleanup after the leader exited")
	}
	if elapsed := testClock.Now().Sub(started); elapsed > converterFixtureTimeout {
		t.Fatalf("Convert cleanup took %s, want at most %s", elapsed, converterFixtureTimeout)
	}
	for _, processID := range processIDs {
		waitForSubtitleProcessExit(t, processID, converterFixtureTimeout)
	}
}

func TestConverterSilentProcessHitsNoOutputWatchdog(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	converter, err := subtitles.NewConverter(writeFakeFFmpeg(t, "exec sleep 30"), 75*time.Millisecond, 4096)
	if err != nil {
		t.Fatal(err)
	}
	testClock := clock.NewReal()
	started := testClock.Now()
	_, err = converter.Convert(context.Background(), subtitles.ConvertJob{
		Source: converterInput(t, "silent input"), StreamIndex: 0, OutputFormat: "webvtt",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Convert error = %v, want no-output deadline exceeded", err)
	}
	if elapsed := testClock.Now().Sub(started); elapsed > converterFixtureTimeout {
		t.Fatalf("silent conversion returned after %s, want bounded watchdog cleanup", elapsed)
	}
}

func TestConverterAbsoluteDeadlineKillsActiveProcessGroup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	const conversionDeadline = 500 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("SUBTITLE_PID_FILE", pidFile)
	t.Setenv("SUBTITLE_READY_FILE", readyFile)
	path := writeFakeFFmpeg(t, `emit_activity() {
	i=0
	while [ "$i" -lt 240 ]; do
		printf x
		printf x >&2
		sleep 0.01
		i=$((i + 1))
	done
}
emit_activity &
child_pid=$!
printf '%s\n%s\n' "$$" "$child_pid" > "$SUBTITLE_PID_FILE"
: > "$SUBTITLE_READY_FILE"
wait "$child_pid"`)
	converter, err := subtitles.NewConverter(path, conversionDeadline, 4096)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	finished := false
	defer func() {
		if finished {
			return
		}
		cancel()
		select {
		case <-result:
		case <-time.After(converterFixtureTimeout):
			t.Error("Convert did not stop after test cleanup cancellation")
		}
	}()

	testClock := clock.NewReal()
	started := testClock.Now()
	go func() {
		_, convertErr := converter.Convert(ctx, subtitles.ConvertJob{
			Source: converterInput(t, "deadline input"), StreamIndex: 0, OutputFormat: "webvtt",
		})
		result <- convertErr
	}()
	waitForSubtitleFile(t, readyFile, 2*conversionDeadline)
	processIDs := readSubtitlePIDs(t, pidFile)

	deadline := time.NewTimer(3 * conversionDeadline)
	defer deadline.Stop()
	select {
	case err = <-result:
		finished = true
	case <-deadline.C:
		t.Fatal("Convert outlived its absolute deadline despite continuous output activity")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Convert error = %v, want absolute deadline exceeded", err)
	}
	for _, processID := range processIDs {
		waitForSubtitleProcessExit(t, processID, conversionDeadline)
	}
	if elapsed := testClock.Now().Sub(started); elapsed > 3*conversionDeadline {
		t.Fatalf("Convert returned after %s, want bounded absolute deadline cleanup", elapsed)
	}
}

func TestConverterCallerCancellationKillsAndReapsProcessGroupRepeatedly(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	for attempt := range 3 {
		t.Run("attempt-"+strconv.Itoa(attempt), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			readyFile := filepath.Join(t.TempDir(), "ready")
			t.Setenv("SUBTITLE_PID_FILE", pidFile)
			t.Setenv("SUBTITLE_READY_FILE", readyFile)
			path := writeFakeFFmpeg(t, `sleep 30 &
child_pid=$!
printf '%s\n%s\n' "$$" "$child_pid" > "$SUBTITLE_PID_FILE"
: > "$SUBTITLE_READY_FILE"
wait "$child_pid"`)
			converter, err := subtitles.NewConverter(path, converterFixtureTimeout, 4096)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				_, convertErr := converter.Convert(ctx, subtitles.ConvertJob{
					Source: converterInput(t, "cancellation input"), StreamIndex: 0, OutputFormat: "webvtt",
				})
				result <- convertErr
			}()
			waitForSubtitleFile(t, readyFile, converterFixtureTimeout)
			cancel()

			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Convert error = %v, want caller cancellation", err)
				}
			case <-time.After(converterFixtureTimeout):
				t.Fatal("Convert did not return after caller cancellation")
			}
			for _, processID := range readSubtitlePIDs(t, pidFile) {
				waitForSubtitleProcessExit(t, processID, converterFixtureTimeout)
			}
		})
	}
}

func writeFakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSubtitleLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func waitForSubtitleFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for fake ffmpeg readiness %q", path)
		case <-poll.C:
		}
	}
}

func readSubtitlePIDs(t *testing.T, path string) []int {
	t.Helper()
	lines := readSubtitleLines(t, path)
	result := make([]int, 0, len(lines))
	for _, line := range lines {
		processID, err := strconv.Atoi(line)
		if err != nil || processID <= 0 {
			t.Fatalf("fake ffmpeg pid %q is invalid: %v", line, err)
		}
		result = append(result, processID)
	}
	return result
}

func waitForSubtitleProcessExit(t *testing.T, processID int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		err := syscall.Kill(processID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probing process %d: %v", processID, err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("process %d survived converter return", processID)
		case <-poll.C:
		}
	}
}

func converterInput(t *testing.T, contents string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subtitle-input")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
