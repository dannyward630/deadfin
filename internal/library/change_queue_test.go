// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/library"
)

// armedClock reports every timer arm (NewTimer or Reset), so a test advances
// fake time only after the queue's goroutine has armed its timer.
type armedClock struct {
	*clock.Fake
	armed chan time.Duration
}

func (c armedClock) NewTimer(delay time.Duration) clock.Timer {
	timer := c.Fake.NewTimer(delay)
	c.armed <- delay
	return armedTimer{Timer: timer, armed: c.armed}
}

type armedTimer struct {
	clock.Timer
	armed chan time.Duration
}

func (t armedTimer) Reset(delay time.Duration) bool {
	active := t.Timer.Reset(delay)
	t.armed <- delay
	return active
}

func newTestQueue(t *testing.T) (*library.ChangeQueue, armedClock, chan struct{}) {
	t.Helper()
	clk := armedClock{Fake: clock.NewFake(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)), armed: make(chan time.Duration, 8)}
	scans := make(chan struct{}, 8)
	queue := library.NewChangeQueue(clk, func() time.Duration { return time.Minute }, func() error {
		scans <- struct{}{}
		return nil
	}, nil)
	t.Cleanup(func() {
		if err := queue.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return queue, clk, scans
}

func waitArmed(t *testing.T, clk armedClock) {
	t.Helper()
	select {
	case <-clk.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("queue never armed its timer")
	}
}

func assertScans(t *testing.T, scans chan struct{}, want int) {
	t.Helper()
	got := 0
	for {
		select {
		case <-scans:
			got++
			continue
		default:
		}
		break
	}
	if got != want {
		t.Fatalf("scans = %d, want %d", got, want)
	}
}

func waitScan(t *testing.T, scans chan struct{}) {
	t.Helper()
	select {
	case <-scans:
	case <-time.After(5 * time.Second):
		t.Fatal("no scan after the quiet period")
	}
}

func TestChangeQueueCoalescesABurstIntoOneScan(t *testing.T) {
	queue, clk, scans := newTestQueue(t)

	queue.Report()
	waitArmed(t, clk)
	clk.Advance(50 * time.Second)
	queue.Report() // a second import before the quiet period ends
	waitArmed(t, clk)
	clk.Advance(50 * time.Second)
	assertScans(t, scans, 0)

	clk.Advance(10 * time.Second)
	waitScan(t, scans)
	assertScans(t, scans, 0)

	// A later report starts a new quiet period and a new scan.
	queue.Report()
	waitArmed(t, clk)
	clk.Advance(time.Minute)
	waitScan(t, scans)
}

func TestChangeQueueCloseDropsAPendingScan(t *testing.T) {
	queue, clk, scans := newTestQueue(t)
	queue.Report()
	waitArmed(t, clk)
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	assertScans(t, scans, 0)
	queue.Report() // after Close, a report is harmless
}
