// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestPeriodicFeedHonorsDuePeriodForceAndStopOnFakeClock(t *testing.T) {
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	ctx, cancel := context.WithCancel(t.Context())
	sent := make(chan MessageType, 8)
	feed := newPeriodicFeed(ctx, fake, MessageSessions, func(context.Context) (any, error) {
		return []string{"session"}, nil
	}, func(messageType MessageType, _ any) bool {
		sent <- messageType
		return true
	}, cancel)

	feed.start(periodicInterval{due: 100 * time.Millisecond, period: time.Second})
	fake.Advance(99 * time.Millisecond)
	assertNoPeriodicMessage(t, sent)
	fake.Advance(time.Millisecond)
	assertPeriodicMessage(t, sent, MessageSessions)

	feed.changed(false)
	assertNoPeriodicMessage(t, sent)
	fake.Advance(999 * time.Millisecond)
	assertNoPeriodicMessage(t, sent)
	fake.Advance(time.Millisecond)
	assertPeriodicMessage(t, sent, MessageSessions)

	feed.changed(true)
	assertPeriodicMessage(t, sent, MessageSessions)
	feed.stop()
	fake.Advance(10 * time.Second)
	assertNoPeriodicMessage(t, sent)

	cancel()
	feed.wait()
}

func assertPeriodicMessage(t *testing.T, messages <-chan MessageType, want MessageType) {
	t.Helper()
	for range 10_000 {
		select {
		case got := <-messages:
			if got != want {
				t.Fatalf("periodic message = %q, want %q", got, want)
			}
			return
		default:
			runtime.Gosched()
		}
	}
	t.Fatalf("periodic message = none, want %q", want)
}

func assertNoPeriodicMessage(t *testing.T, messages <-chan MessageType) {
	t.Helper()
	for range 100 {
		select {
		case got := <-messages:
			t.Fatalf("unexpected periodic message %q", got)
		default:
			runtime.Gosched()
		}
	}
}
