// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
)

func TestManagerDeadReckonsPlayingSessionAtOneHertz(t *testing.T) {
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	manager := session.New(fake)
	defer func() { _ = manager.Close() }()
	snapshot := watchdogSession(t, manager)
	position := int64(0)
	runTime := int64(40_000_000)
	if err := manager.StartPlayback(t.Context(), snapshot.Target, session.PlaybackState{
		ItemID: dto.UUID{0x42}, PositionTicks: &position, RunTimeTicks: &runTime,
	}); err != nil {
		t.Fatalf("start playback: %v", err)
	}
	checkIn := sessionSnapshot(t, manager, snapshot.ID).LastPlaybackCheckIn

	for tick, want := range []int64{10_000_000, 20_000_000, 30_000_000} {
		fake.Advance(time.Second)
		waitForPosition(t, manager, snapshot.ID, want)
		got := sessionSnapshot(t, manager, snapshot.ID)
		if !got.LastPlaybackCheckIn.Equal(checkIn) {
			t.Fatalf("automated tick %d changed LastPlaybackCheckIn from %s to %s", tick+1, checkIn, got.LastPlaybackCheckIn)
		}
	}
	fake.Advance(time.Second)
	waitForPosition(t, manager, snapshot.ID, 30_000_000)

	pausedAt := int64(30_000_000)
	if err := manager.ProgressPlayback(t.Context(), snapshot.Target, session.PlaybackState{
		ItemID: dto.UUID{0x42}, PositionTicks: &pausedAt, RunTimeTicks: &runTime, IsPaused: true,
	}); err != nil {
		t.Fatalf("pause playback: %v", err)
	}
	fake.Advance(10 * time.Second)
	waitForPosition(t, manager, snapshot.ID, pausedAt)
}

func TestManagerIdleAndInactiveWatchdogsUseInjectedClock(t *testing.T) {
	t.Run("idle playback after five minute threshold", func(t *testing.T) {
		fake := clock.NewFake(time.Unix(1_700_000_000, 0))
		observed := newWatchdogObservedClock(fake)
		idle := make(chan session.Snapshot, 1)
		manager := session.NewWithOptions(observed, session.Options{
			OnIdlePlayback: func(_ context.Context, snapshot session.Snapshot, _ session.PlaybackState) error {
				idle <- snapshot
				return nil
			},
		})
		defer func() { _ = manager.Close() }()
		snapshot := watchdogSession(t, manager)
		if err := manager.StartPlayback(t.Context(), snapshot.Target, session.PlaybackState{ItemID: dto.UUID{0x51}}); err != nil {
			t.Fatalf("start playback: %v", err)
		}

		idleTimer := observed.watchdogTimer(t, 5*time.Minute)
		waitForTimerReady(t, idleTimer, "idle")
		fake.Advance(5*time.Minute + time.Nanosecond)
		waitForTimerReset(t, idleTimer, "idle")

		select {
		case got := <-idle:
			if got.ID != snapshot.ID {
				t.Fatalf("idle watchdog session = %q, want %q", got.ID, snapshot.ID)
			}
		default:
			t.Fatalf("idle watchdog did not emit a session after its reset cycle")
		}
	})

	t.Run("paused threshold checked each minute", func(t *testing.T) {
		fake := clock.NewFake(time.Unix(1_700_000_000, 0))
		observed := newWatchdogObservedClock(fake)
		inactive := make(chan session.Snapshot, 1)
		manager := session.NewWithOptions(observed, session.Options{
			InactiveSessionThreshold: func() int { return 2 },
			OnInactivePlayback: func(_ context.Context, snapshot session.Snapshot, _ session.PlaybackState) error {
				inactive <- snapshot
				return nil
			},
		})
		defer func() { _ = manager.Close() }()
		snapshot := watchdogSession(t, manager)
		if err := manager.StartPlayback(t.Context(), snapshot.Target, session.PlaybackState{
			ItemID: dto.UUID{0x52}, IsPaused: true,
		}); err != nil {
			t.Fatalf("start paused playback: %v", err)
		}

		inactiveTimer := observed.watchdogTimer(t, time.Minute)
		waitForTimerReady(t, inactiveTimer, "inactive")
		fake.Advance(2*time.Minute + time.Nanosecond)
		waitForTimerReset(t, inactiveTimer, "inactive")

		select {
		case got := <-inactive:
			if got.ID != snapshot.ID {
				t.Fatalf("inactive watchdog session = %q, want %q", got.ID, snapshot.ID)
			}
		default:
			t.Fatalf("inactive watchdog did not emit a session after its reset cycle")
		}
	})

	t.Run("zero inactive threshold disables action", func(t *testing.T) {
		fake := clock.NewFake(time.Unix(1_700_000_000, 0))
		observed := newWatchdogObservedClock(fake)
		inactive := make(chan session.Snapshot, 1)
		manager := session.NewWithOptions(observed, session.Options{
			InactiveSessionThreshold: func() int { return 0 },
			OnInactivePlayback: func(_ context.Context, snapshot session.Snapshot, _ session.PlaybackState) error {
				inactive <- snapshot
				return nil
			},
		})
		defer func() { _ = manager.Close() }()
		snapshot := watchdogSession(t, manager)
		if err := manager.StartPlayback(t.Context(), snapshot.Target, session.PlaybackState{
			ItemID: dto.UUID{0x53}, IsPaused: true,
		}); err != nil {
			t.Fatalf("start paused playback: %v", err)
		}

		inactiveTimer := observed.watchdogTimer(t, time.Minute)
		waitForTimerReady(t, inactiveTimer, "inactive")
		fake.Advance(10 * time.Minute)
		waitForTimerReset(t, inactiveTimer, "inactive")

		select {
		case got := <-inactive:
			t.Fatalf("disabled inactive watchdog emitted session %#v", got)
		default:
		}
	})
}

func watchdogSession(t *testing.T, manager *session.Manager) session.Snapshot {
	t.Helper()
	userID, err := dto.ParseUUID("11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}
	snapshot, err := manager.LogActivity(t.Context(), session.Activity{
		Client: "Swiftfin", Version: "1", DeviceID: "apple-tv", UserID: userID,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return snapshot
}

func sessionSnapshot(t *testing.T, manager *session.Manager, id string) session.Snapshot {
	t.Helper()
	snapshot, found := manager.SessionByID(id)
	if !found {
		t.Fatalf("session %q not found", id)
	}
	return snapshot
}

func waitForPosition(t *testing.T, manager *session.Manager, id string, want int64) {
	t.Helper()
	for range 10_000 {
		snapshot := sessionSnapshot(t, manager, id)
		if snapshot.NowPlaying != nil && snapshot.NowPlaying.PositionTicks != nil && *snapshot.NowPlaying.PositionTicks == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("session %q did not reach position %d: %#v", id, want, sessionSnapshot(t, manager, id).NowPlaying)
}

// watchdogObservedClock delegates clock.Clock unchanged while routing every
// NewTimer through a watchdogObservedTimer that exposes one-shot readiness and
// reset signals. NewWithOptions creates the idle (5m) and inactive (1m)
// watchdog timers synchronously before starting the watchdog goroutine, so the
// test can wait on the selected timer's signals to establish a causal edge with
// the goroutine rather than relying on bounded scheduler yields.
type watchdogObservedClock struct {
	clock.Clock
	mu     sync.Mutex
	timers map[time.Duration]*watchdogObservedTimer
}

func newWatchdogObservedClock(base clock.Clock) *watchdogObservedClock {
	return &watchdogObservedClock{Clock: base, timers: make(map[time.Duration]*watchdogObservedTimer)}
}

func (c *watchdogObservedClock) NewTimer(delay time.Duration) clock.Timer {
	timer := newWatchdogObservedTimer(c.Clock.NewTimer(delay))
	c.mu.Lock()
	c.timers[delay] = timer
	c.mu.Unlock()
	return timer
}

func (c *watchdogObservedClock) watchdogTimer(t *testing.T, delay time.Duration) *watchdogObservedTimer {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	timer, ok := c.timers[delay]
	if !ok {
		t.Fatalf("no observed timer registered for %s watchdog", delay)
	}
	return timer
}

// watchdogObservedTimer delegates clock.Timer unchanged. Its first C() call
// proves the watchdog goroutine has obtained the timer's channel and parked in
// its select loop (readiness); a Reset() call proves the watchdog completed its
// check cycle and re-armed the timer. Both signals fire once via sync.Once so
// later select iterations or reset cycles cannot re-signal.
type watchdogObservedTimer struct {
	clock.Timer
	readyOnce  sync.Once
	resetOnce  sync.Once
	ready      chan struct{}
	resetCycle chan struct{}
}

func newWatchdogObservedTimer(base clock.Timer) *watchdogObservedTimer {
	return &watchdogObservedTimer{
		Timer:      base,
		ready:      make(chan struct{}),
		resetCycle: make(chan struct{}),
	}
}

func (t *watchdogObservedTimer) C() <-chan time.Time {
	t.readyOnce.Do(func() { close(t.ready) })
	return t.Timer.C()
}

func (t *watchdogObservedTimer) Reset(delay time.Duration) bool {
	wasActive := t.Timer.Reset(delay)
	t.resetOnce.Do(func() { close(t.resetCycle) })
	return wasActive
}

// waitForTimerReady blocks until the watchdog goroutine has called C() on the
// selected timer and entered its select loop. A wall-clock guard detects a hung
// watchdog without making synchronization depend on wall-clock time.
func waitForTimerReady(t *testing.T, timer *watchdogObservedTimer, name string) {
	t.Helper()
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	select {
	case <-timer.ready:
	case <-guard.C:
		t.Fatalf("%s watchdog never reached its select loop", name)
	}
}

// waitForTimerReset blocks until the selected timer has been reset, proving the
// watchdog completed its check cycle (and therefore its callback opportunity).
func waitForTimerReset(t *testing.T, timer *watchdogObservedTimer, name string) {
	t.Helper()
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	select {
	case <-timer.resetCycle:
	case <-guard.C:
		t.Fatalf("%s watchdog never completed its reset cycle", name)
	}
}
