// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// Two play sessions that stream one item at the same time own separate
// playlist families, as upstream keys GetOutputFilePath by DeviceId and
// PlaySessionId. Each session gets one ffmpeg start for a sequential
// run, its own continuous segments, and no error. A seek or a stop in one
// session does not restart, stop, or remove the files of the other.
func TestHLSConcurrentSessionsOnOneItemStayIndependent(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.October, 12, 12, 0, 0, 0, time.UTC))
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, base)
	defer closeHLSManager(t, manager)

	// Both sessions use one DeviceId; the play sessions differ.
	sessions := []HLSRequest{
		fixture.requestWithExtension("tv-a", 6, ".mp4"),
		fixture.requestWithExtension("tv-b", 6, ".mp4"),
	}
	sessions[0].PlaySessionID, sessions[1].PlaySessionID = "play-session-a", "play-session-b"
	sessions[0].DeviceID, sessions[1].DeviceID = "qamatrix-atv", "qamatrix-atv"
	labels := []string{"tv-a", "tv-b"}
	const sequential = 8

	// play requests the init segment and then segments 0..last-1 in order,
	// as an HLS client does. The fake ffmpeg is held; this goroutine writes
	// each successor segment before it requests a segment, so the run never
	// waits on the fake clock.
	play := func(ctx context.Context, request HLSRequest, label string, first, last int) error {
		for index := first; index < last; index++ {
			if err := manager.RestartHLSAt(ctx, request, index); err != nil {
				return fmt.Errorf("%s RestartHLSAt(%d): %w", label, index, err)
			}
			if index == first {
				if err := os.WriteFile(fmt.Sprintf(request.SegmentPattern, index), []byte(fmt.Sprintf("%s-%d", label, index)), 0o600); err != nil {
					return err
				}
			}
			if err := os.WriteFile(fmt.Sprintf(request.SegmentPattern, index+1), []byte(fmt.Sprintf("%s-%d", label, index+1)), 0o600); err != nil {
				return err
			}
			body, err := manager.WaitSegment(ctx, request.PlaylistPath, index)
			if err != nil {
				return fmt.Errorf("%s WaitSegment(%d): %w", label, index, err)
			}
			data := make([]byte, 64)
			count, _ := body.Read(data)
			if closeErr := body.Close(); closeErr != nil {
				return closeErr
			}
			if got, want := string(data[:count]), fmt.Sprintf("%s-%d", label, index); got != want {
				return fmt.Errorf("%s segment %d body = %q, want %q", label, index, got, want)
			}
		}
		return nil
	}

	var wait sync.WaitGroup
	errs := make([]error, len(sessions))
	for index := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs[index] = play(t.Context(), sessions[index], labels[index], -1, sequential)
		}()
	}
	wait.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range labels {
		fixture.waitStarted(t, label)
		if got := hlsStartCount(t, fixture, label); got != 1 {
			t.Fatalf("%s ffmpeg starts = %d, want 1: a restart cuts the segment timeline", label, got)
		}
	}
	pidA := fixture.waitPID(t, "tv-a")

	// Session B seeks far ahead. Only B restarts.
	seek := 300
	if err := play(t.Context(), sessions[1], "tv-b", seek, seek+2); err != nil {
		t.Fatal(err)
	}
	waitForHLSStarts(t, fixture, "tv-b", 2)
	if got := hlsStartCount(t, fixture, "tv-a"); got != 1 {
		t.Fatalf("tv-a ffmpeg starts after tv-b seek = %d, want 1", got)
	}
	if err := syscall.Kill(pidA, 0); err != nil {
		t.Fatalf("tv-b seek stopped tv-a ffmpeg: %v", err)
	}
	if _, err := os.Stat(fmt.Sprintf(sessions[0].SegmentPattern, sequential-1)); err != nil {
		t.Fatalf("tv-b seek removed tv-a segments: %v", err)
	}
	if err := play(t.Context(), sessions[0], "tv-a", sequential, sequential+2); err != nil {
		t.Fatalf("tv-a continues after tv-b seek: %v", err)
	}
	if got := hlsStartCount(t, fixture, "tv-a"); got != 1 {
		t.Fatalf("tv-a ffmpeg starts after its continuation = %d, want 1", got)
	}

	// Session A stops. B keeps its process and its segments.
	pidB := fixture.waitPID(t, "tv-b")
	if err := manager.StopPlaySession(t.Context(), sessions[0].PlaySessionID, sessions[0].DeviceID); err != nil {
		t.Fatalf("StopPlaySession tv-a: %v", err)
	}
	waitForProcessExit(t, pidA)
	if err := syscall.Kill(pidB, 0); err != nil {
		t.Fatalf("tv-a stop stopped tv-b ffmpeg: %v", err)
	}
	if err := play(t.Context(), sessions[1], "tv-b", seek+2, seek+4); err != nil {
		t.Fatalf("tv-b continues after tv-a stop: %v", err)
	}
	if got := hlsStartCount(t, fixture, "tv-b"); got != 2 {
		t.Fatalf("tv-b ffmpeg starts after tv-a stop = %d, want 2", got)
	}
}
