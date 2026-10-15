// SPDX-License-Identifier: GPL-2.0-only

package clock_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestFakeAdvanceMovesNowAndFiresTimersWithoutWallClockSleep(t *testing.T) {
	// CONVENTIONS.md §1.4 requires injected clocks and fake time in timer tests.
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	timer := fake.NewTimer(5 * time.Second)

	fake.Advance(4 * time.Second)
	if got, want := fake.Now(), start.Add(4*time.Second); !got.Equal(want) {
		t.Fatalf("now = %s, want %s", got, want)
	}
	mustNotFire(t, timer.C())

	fake.Advance(time.Second)
	if got, want := receiveTime(t, timer.C()), start.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("timer fired at %s, want %s", got, want)
	}
}

func TestFakeTimerStopAndResetControlItsFakeTimeDeadline(t *testing.T) {
	// The fake timer backs WP-P0-01's required fake-time event coalescing tests.
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	timer := fake.NewTimer(5 * time.Second)

	if !timer.Stop() {
		t.Fatal("stop = false, want active timer stopped")
	}
	fake.Advance(5 * time.Second)
	mustNotFire(t, timer.C())

	if timer.Reset(3 * time.Second) {
		t.Fatal("reset = true, want stopped timer reported inactive")
	}
	fake.Advance(2 * time.Second)
	mustNotFire(t, timer.C())
	fake.Advance(time.Second)
	if got, want := receiveTime(t, timer.C()), start.Add(8*time.Second); !got.Equal(want) {
		t.Fatalf("reset timer fired at %s, want %s", got, want)
	}
}

func TestFakeTimerStopDrainsAnUnreceivedFiring(t *testing.T) {
	start := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	timer := fake.NewTimer(time.Second)

	fake.Advance(time.Second)
	if timer.Stop() {
		t.Fatal("stop = true after firing, want false")
	}
	mustNotFire(t, timer.C())
}

func mustNotFire(t *testing.T, timer <-chan time.Time) {
	t.Helper()
	select {
	case got := <-timer:
		t.Fatalf("timer fired early at %s", got)
	default:
	}
}

func receiveTime(t *testing.T, timer <-chan time.Time) time.Time {
	t.Helper()
	select {
	case got := <-timer:
		return got
	default:
		t.Fatal("timer did not fire when fake time reached deadline")
		return time.Time{}
	}
}
