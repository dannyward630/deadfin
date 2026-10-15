// SPDX-License-Identifier: GPL-2.0-only

package keyframes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

const extractionRaceTimeout = 5 * time.Second

func openExtractionFixtureSource(t *testing.T) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.mkv")
	if err := os.WriteFile(path, []byte("fixture media source"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func TestExtractorJoinsStartedGroupKillCallbackBeforeReturning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	readyFile := filepath.Join(t.TempDir(), "ready")
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("EXTRACTION_READY_FILE", readyFile)
	t.Setenv("EXTRACTION_PID_FILE", pidFile)

	path := writeExtractionFakeFFProbe(t, `printf '%s\n' "$$" > "$EXTRACTION_PID_FILE"
: > "$EXTRACTION_READY_FILE"
exec sleep 30`)
	extractor, err := NewExtractor(path, extractionRaceTimeout, 4096)
	if err != nil {
		t.Fatal(err)
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	previousKill := killProcessGroupFn
	killProcessGroupFn = func(processID int) error {
		startOnce.Do(func() { close(callbackStarted) })
		<-releaseCallback
		return killProcessGroup(processID)
	}
	t.Cleanup(func() { killProcessGroupFn = previousKill })
	t.Cleanup(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, extractErr := extractor.Extract(ctx, openExtractionFixtureSource(t))
		result <- extractErr
	}()

	waitForExtractionFile(t, readyFile, extractionRaceTimeout)
	processID := readExtractionPID(t, pidFile)
	cancel()
	waitForExtractionCallback(t, callbackStarted, extractionRaceTimeout)

	// Make the command exit while its already-started cancellation callback is
	// frozen. Extract must wait for that callback rather than return while it
	// can still act on the now-reaped process group.
	if err := syscall.Kill(-processID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("killing process group %d: %v", processID, err)
	}
	select {
	case extractErr := <-result:
		t.Fatalf("Extract returned %v while its started kill callback was blocked", extractErr)
	case <-time.After(150 * time.Millisecond):
	}

	release()
	if extractErr := waitForExtractionResult(t, result, extractionRaceTimeout); !errors.Is(extractErr, context.Canceled) {
		t.Fatalf("Extract error = %v, want caller cancellation", extractErr)
	}
	waitForExtractionProcessExit(t, processID, extractionRaceTimeout)
}

func TestExtractorArbitratesCallerCancellationAndOutputCapByWinningCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}
	callerCause := errors.New("caller cancelled keyframe extraction")

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, extractor *Extractor, cause error, releaseFile, capFile string)
	}{
		{
			name: "caller cancellation before cap crossing",
			run: func(t *testing.T, extractor *Extractor, cause error, releaseFile, capFile string) {
				previousKill := killProcessGroupFn
				killProcessGroupFn = func(int) error { return nil }
				t.Cleanup(func() { killProcessGroupFn = previousKill })

				ctx, cancelCause := context.WithCancelCause(context.Background())
				result := make(chan error, 1)
				go func() {
					_, extractErr := extractor.Extract(ctx, openExtractionFixtureSource(t))
					result <- extractErr
				}()
				waitForExtractionFile(t, filepath.Join(filepath.Dir(releaseFile), "ready"), extractionRaceTimeout)
				cancelCause(cause)
				writeExtractionRelease(t, releaseFile)
				waitForExtractionFile(t, capFile, extractionRaceTimeout)
				if extractErr := waitForExtractionResult(t, result, extractionRaceTimeout); extractErr != cause {
					t.Fatalf("Extract error = %v, want the exact caller cancellation cause %v", extractErr, cause)
				}
			},
		},
		{
			name: "output cap crossing before caller cancellation",
			run: func(t *testing.T, extractor *Extractor, cause error, releaseFile, capFile string) {
				callbackStarted := make(chan struct{})
				releaseCallback := make(chan struct{})
				var startOnce, releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
				previousKill := killProcessGroupFn
				killProcessGroupFn = func(processID int) error {
					startOnce.Do(func() { close(callbackStarted) })
					<-releaseCallback
					return killProcessGroup(processID)
				}
				t.Cleanup(func() { killProcessGroupFn = previousKill })
				t.Cleanup(release)

				ctx, cancelCause := context.WithCancelCause(context.Background())
				result := make(chan error, 1)
				go func() {
					_, extractErr := extractor.Extract(ctx, openExtractionFixtureSource(t))
					result <- extractErr
				}()
				waitForExtractionFile(t, filepath.Join(filepath.Dir(releaseFile), "ready"), extractionRaceTimeout)
				writeExtractionRelease(t, releaseFile)
				waitForExtractionFile(t, capFile, extractionRaceTimeout)
				waitForExtractionCallback(t, callbackStarted, extractionRaceTimeout)
				cancelCause(cause)
				release()
				if extractErr := waitForExtractionResult(t, result, extractionRaceTimeout); extractErr != ErrOutputTooLarge {
					t.Fatalf("Extract error = %v, want exact output cap cause %v", extractErr, ErrOutputTooLarge)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			readyFile := filepath.Join(directory, "ready")
			releaseFile := filepath.Join(directory, "release")
			capFile := filepath.Join(directory, "cap")
			t.Setenv("EXTRACTION_READY_FILE", readyFile)
			t.Setenv("EXTRACTION_RELEASE_FILE", releaseFile)
			t.Setenv("EXTRACTION_CAP_FILE", capFile)
			path := writeExtractionFakeFFProbe(t, `: > "$EXTRACTION_READY_FILE"
while [ ! -e "$EXTRACTION_RELEASE_FILE" ]; do sleep 0.01; done
: > "$EXTRACTION_CAP_FILE"
printf '012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789'`)
			extractor, err := NewExtractor(path, extractionRaceTimeout, 16)
			if err != nil {
				t.Fatal(err)
			}
			tc.run(t, extractor, callerCause, releaseFile, capFile)
		})
	}
}

func writeExtractionFakeFFProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForExtractionFile(t *testing.T, path string, timeout time.Duration) {
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
			t.Fatalf("timed out waiting for helper signal %q", path)
		case <-poll.C:
		}
	}
}

func writeExtractionRelease(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readExtractionPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	processID, err := strconv.Atoi(string(data[:len(data)-1]))
	if err != nil || processID <= 0 {
		t.Fatalf("helper PID %q = %d, %v; want a positive PID", data, processID, err)
	}
	return processID
}

func waitForExtractionCallback(t *testing.T, callbackStarted <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-callbackStarted:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for group kill callback to start")
	}
}

func waitForExtractionResult(t *testing.T, result <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for Extract")
		return nil
	}
}

func waitForExtractionProcessExit(t *testing.T, processID int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if err := syscall.Kill(processID, 0); errors.Is(err, syscall.ESRCH) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("process %d survived extractor completion", processID)
		case <-poll.C:
		}
	}
}
