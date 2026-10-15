// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestWaitPlaylistRebindsAcrossRestartRegistrationGap(t *testing.T) {
	t.Run("replacement manifest is returned", func(t *testing.T) {
		fixture := newHLSFixture(t, "hold")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)

		requestA := fixture.request("playlist-registration-gap-a", 4)
		requestA.Live = true
		if err := manager.StartHLS(context.Background(), requestA); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "playlist-registration-gap-a")
		pidA := fixture.waitPID(t, "playlist-registration-gap-a")

		registrationEntered := make(chan struct{})
		registrationRelease := make(chan struct{})
		var registrationReleaseOnce sync.Once
		manager.beforeHLSRestartRegister = func() {
			close(registrationEntered)
			<-registrationRelease
		}
		t.Cleanup(func() {
			manager.beforeHLSRestartRegister = nil
			registrationReleaseOnce.Do(func() { close(registrationRelease) })
		})

		waits := make(chan hlsPlaylistResult, 1)
		go func() {
			playlist, err := manager.WaitPlaylist(context.Background(), requestA.PlaylistPath, 1)
			waits <- hlsPlaylistResult{playlist: playlist, err: err}
		}()
		waitForHLSWaiterLease(t, manager, requestA.PlaylistPath, 1)

		requestB := requestA
		requestB.Args = []string{"--fixture-label=playlist-registration-gap-b"}
		restarted := make(chan error, 1)
		go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 0) }()
		waitForProgressiveSignal(t, registrationEntered, "playlist replacement registration gap")
		waitForProcessExit(t, pidA)
		assertHLSPlaylistPending(t, waits)

		registrationReleaseOnce.Do(func() { close(registrationRelease) })
		fixture.waitStarted(t, "playlist-registration-gap-b")
		want := "#EXTM3U\n#EXTINF:4,\nplaylist-registration-gap-b0.ts\n"
		fixture.writePlaylistText(t, requestB, want)
		fixture.writeSegment(t, requestB, 0, "playlist replacement segment")
		if err := receiveProgressiveError(t, restarted); err != nil {
			t.Fatalf("RestartHLSAt B: %v", err)
		}

		result := receiveHLSPlaylist(t, waits)
		if result.err != nil {
			t.Fatalf("WaitPlaylist after B registration: %v", result.err)
		}
		if result.playlist != want {
			t.Fatalf("WaitPlaylist returned %q, want B manifest %q", result.playlist, want)
		}
	})

	t.Run("failed replacement terminates without a stale lease", func(t *testing.T) {
		fixture := newHLSFixture(t, "hold")
		manager := fixture.manager(t, clock.NewReal())
		defer closeHLSManager(t, manager)

		requestA := fixture.request("playlist-registration-failure-a", 4)
		requestA.Live = true
		if err := manager.StartHLS(context.Background(), requestA); err != nil {
			t.Fatal(err)
		}
		fixture.waitStarted(t, "playlist-registration-failure-a")
		pidA := fixture.waitPID(t, "playlist-registration-failure-a")

		registrationEntered := make(chan struct{})
		registrationRelease := make(chan struct{})
		var registrationReleaseOnce sync.Once
		manager.beforeHLSRestartRegister = func() {
			close(registrationEntered)
			<-registrationRelease
		}
		t.Cleanup(func() {
			manager.beforeHLSRestartRegister = nil
			registrationReleaseOnce.Do(func() { close(registrationRelease) })
		})

		waits := make(chan hlsPlaylistResult, 1)
		go func() {
			playlist, err := manager.WaitPlaylist(context.Background(), requestA.PlaylistPath, 1)
			waits <- hlsPlaylistResult{playlist: playlist, err: err}
		}()
		waitForHLSWaiterLease(t, manager, requestA.PlaylistPath, 1)

		requestB := requestA
		requestB.Args = []string{"--fixture-label=playlist-registration-failure-b"}
		restarted := make(chan error, 1)
		go func() { restarted <- manager.RestartHLSAt(context.Background(), requestB, 0) }()
		waitForProgressiveSignal(t, registrationEntered, "failed playlist replacement registration gap")
		waitForProcessExit(t, pidA)
		assertHLSPlaylistPending(t, waits)

		if err := manager.StopPlaySession(context.Background(), requestA.PlaySessionID, requestA.DeviceID); err != nil {
			t.Fatalf("StopPlaySession during replacement registration gap: %v", err)
		}
		registrationReleaseOnce.Do(func() { close(registrationRelease) })
		if err := receiveProgressiveError(t, restarted); err == nil {
			t.Fatal("RestartHLSAt unexpectedly registered B after StopPlaySession")
		}
		result := receiveHLSPlaylist(t, waits)
		if result.playlist != "" {
			t.Fatalf("WaitPlaylist returned stale manifest %q after B registration failed", result.playlist)
		}
		if result.err == nil || errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("WaitPlaylist did not terminate after B registration failed: %v", result.err)
		}
	})
}

func waitForHLSWaiterLease(t *testing.T, manager *TranscodeManager, playlist string, want int) {
	t.Helper()
	canonical, err := manager.normalizeOutputPath(playlist)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for {
		manager.mu.Lock()
		job := manager.jobs[canonical]
		requests := 0
		if job != nil {
			requests = job.requests
		}
		manager.mu.Unlock()
		if requests >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d HLS leases, got %d", want, requests)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestHLSSegmentCloseUsesExactRequestTimingForPositionAndSidecars(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(base)
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, observed)
	manager.options.PKeySupported = true
	defer closeHLSManager(t, manager)
	t.Setenv("HLS_EMIT_PROGRESS", "1")
	t.Setenv("HLS_PROGRESS_TIME", "00:01:30.000")

	request := fixture.request("exact-segment-timing", 6)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "exact-segment-timing")
	waitForHLSProgress(t, manager, request.PlaylistPath)
	fixture.writePlaylist(t, request)
	for index := range 5 {
		fixture.writeSegment(t, request, index, "segment")
	}

	body, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 3, &HLSSegmentTiming{
		RuntimeTicks:             312_400_000,
		ActualSegmentLengthTicks: 37_600_000,
	})
	if err != nil {
		t.Fatalf("WaitSegment exact timing: %v", err)
	}
	if got := readHLSSegment(t, body); got != "segment" {
		_ = body.Close()
		t.Fatalf("segment body = %q, want segment", got)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close exact timing body: %v", err)
	}
	if got, want := hlsDownloadPosition(t, manager, request.PlaylistPath), int64(350_000_000); got != want {
		t.Fatalf("download position after exact-timing Close = %d, want %d", got, want)
	}

	observed.waitForTimer(t, 5*time.Second)
	base.Advance(5 * time.Second)
	if _, err := os.Stat(fixture.marker("p", "exact-segment-timing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("throttler paused despite exact 35s downloaded position: %v", err)
	}
	observed.waitForTimer(t, 20*time.Second)
	base.Advance(15 * time.Second)
	observed.waitForTimer(t, 1500*time.Millisecond)
	base.Advance(1500 * time.Millisecond)
	for index := range 3 {
		waitForHLSSegmentAbsent(t, fixture.segmentPath(request, index))
	}
	if _, err := os.Stat(fixture.segmentPath(request, 3)); err != nil {
		t.Fatalf("cleaner removed current segment: %v", err)
	}
}

func TestHLSSegmentCloseFallsBackToNominalTimingWithoutExactFacts(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("nominal-segment-timing", 6)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "nominal-segment-timing")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 3, "segment")
	fixture.writeSegment(t, request, 4, "successor")

	body, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 3, nil)
	if err != nil {
		t.Fatalf("WaitSegment nominal fallback: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close nominal fallback body: %v", err)
	}
	if got, want := hlsDownloadPosition(t, manager, request.PlaylistPath), int64(240_000_000); got != want {
		t.Fatalf("download position after nominal fallback Close = %d, want %d", got, want)
	}
}

func TestHLSSegmentCloseSaturatesOverflowingExactTiming(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("overflow-segment-timing", 6)
	if err := manager.StartHLS(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t, "overflow-segment-timing")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 3, "segment")
	fixture.writeSegment(t, request, 4, "successor")

	body, err := manager.WaitSegment(context.Background(), request.PlaylistPath, 3, &HLSSegmentTiming{
		RuntimeTicks:             math.MaxInt64 - 1,
		ActualSegmentLengthTicks: 2,
	})
	if err != nil {
		t.Fatalf("WaitSegment overflowing exact timing: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close overflowing exact timing body: %v", err)
	}
	if got, want := hlsDownloadPosition(t, manager, request.PlaylistPath), int64(math.MaxInt64); got != want {
		t.Fatalf("download position after overflowing exact timing Close = %d, want saturated %d", got, want)
	}
}

func hlsDownloadPosition(t *testing.T, manager *TranscodeManager, playlist string) int64 {
	t.Helper()
	canonical, err := manager.normalizeOutputPath(playlist)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.jobs[canonical]
	if job == nil {
		t.Fatalf("HLS job for %q disappeared before accounting assertion", playlist)
	}
	return job.downloadPositionTicks
}

func waitForHLSSegmentAbsent(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("stat HLS segment %q while waiting for cleaner: %v", path, err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("cleaner retained expired HLS segment %q", path)
		case <-time.After(time.Millisecond):
		}
	}
}
