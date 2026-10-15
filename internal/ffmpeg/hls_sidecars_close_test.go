// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// The pump must retain its worker registration until it has returned. In
// particular, a disconnected log reader must not let manager.Close report a
// completed shutdown while the pump is still blocked reading the stderr pipe.
func TestHLSSidecarPumpCloseWaitsForReturnedPumpAfterDestinationFailure(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	for _, tc := range []struct {
		name            string
		startCloseFirst bool
	}{
		{name: "write failure precedes Close"},
		{name: "write failure races with Close", startCloseFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newSidecarCloseTestManager(t)
			source, sourceWriter := newSidecarCloseTestPipe(t)
			stderr, stderrReader := newSidecarCloseTestPipe(t)
			destinationReader, destination := io.Pipe()
			if err := destinationReader.CloseWithError(errors.New("client disconnected")); err != nil {
				t.Fatalf("disconnect sidecar destination: %v", err)
			}

			sourceClosed := newSidecarCloseProbe(source)
			stderrClosed := newSidecarCloseProbe(stderr)
			job := &transcodeJob{
				manager:       manager,
				sidecarSource: sourceClosed,
				sidecarStderr: stderrClosed,
			}
			t.Cleanup(func() {
				manager.stopHLSSidecars(job)
				_ = sourceWriter.Close()
				_ = stderrReader.Close()
				_ = destination.Close()
				closeSidecarManager(t, manager)
			})

			manager.mu.Lock()
			manager.startHLSSidecarPumpLocked(job, source, stderr, destination)
			manager.mu.Unlock()

			closed := make(chan error, 1)
			startClose := func() {
				go func() { closed <- manager.Close(context.Background()) }()
			}
			if tc.startCloseFirst {
				startClose()
			}
			if _, err := io.WriteString(sourceWriter, "sidecar destination failure"); err != nil {
				t.Fatalf("write sidecar source: %v", err)
			}
			if !tc.startCloseFirst {
				startClose()
			}

			assertSidecarClosePending(t, closed)

			// This is the manager-owned sidecar shutdown path: it closes the source
			// and stderr descriptors, making the blocked pump return. Only then may
			// manager.Close complete.
			manager.stopHLSSidecars(job)
			awaitSidecarCloseProbe(t, sourceClosed.done, "sidecar source descriptor")
			awaitSidecarCloseProbe(t, stderrClosed.done, "sidecar stderr descriptor")
			awaitSidecarResult(t, closed, "manager Close after sidecar pump return")
			assertNoSidecarWorkers(t, manager)
		})
	}
}

func newSidecarCloseTestManager(t *testing.T) *TranscodeManager {
	t.Helper()
	manager, err := NewTranscodeManager("/bin/true", t.TempDir(), TranscodeManagerOptions{})
	if err != nil {
		t.Fatalf("NewTranscodeManager: %v", err)
	}
	return manager
}

func newSidecarCloseTestPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create sidecar pipe: %v", err)
	}
	return reader, writer
}

type sidecarCloseProbe struct {
	io.Closer
	once sync.Once
	done chan struct{}
}

func newSidecarCloseProbe(closer io.Closer) *sidecarCloseProbe {
	return &sidecarCloseProbe{Closer: closer, done: make(chan struct{})}
}

func (probe *sidecarCloseProbe) Close() error {
	err := probe.Closer.Close()
	probe.once.Do(func() { close(probe.done) })
	return err
}

func assertSidecarClosePending(t *testing.T, closed <-chan error) {
	t.Helper()
	select {
	case err := <-closed:
		t.Fatalf("manager Close returned while the sidecar pump was still blocked: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
}

func awaitSidecarCloseProbe(t *testing.T, done <-chan struct{}, descriptor string) {
	t.Helper()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatalf("%s was not closed: %v", descriptor, t.Context().Err())
	}
}

func assertNoSidecarWorkers(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	manager.mu.Lock()
	workers := manager.workerCount
	manager.mu.Unlock()
	if workers != 0 {
		t.Fatalf("sidecar workers after Close = %d, want zero", workers)
	}
}

func closeSidecarManager(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("manager cleanup Close: %v", err)
	}
}
