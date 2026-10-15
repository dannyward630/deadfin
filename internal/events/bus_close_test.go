// SPDX-License-Identifier: GPL-2.0-only

package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"

	"go.uber.org/goleak"
)

func TestBusCloseCanRetryAfterCanceledContext(t *testing.T) {
	bus := NewBus[int](clock.NewFake(time.Unix(0, 0).UTC()))
	subscription := &Subscription[int]{
		bus:      bus,
		options:  SubscriptionOptions{Coalesce: time.Second},
		started:  true,
		events:   make(chan int),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	bus.subscriptions[subscription] = struct{}{}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bus.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first close error = %v, want context canceled", err)
	}

	retried := make(chan error, 1)
	go func() {
		retried <- bus.Close(context.Background())
	}()
	select {
	case err := <-retried:
		t.Fatalf("retry returned before the in-flight subscription worker finished: %v", err)
	default:
	}

	close(subscription.finished)
	if err := <-retried; err != nil {
		t.Fatalf("retry close: %v", err)
	}
}

func TestBusCloseStopsCoalescerWithoutGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := NewBus[int](fake)
	bus.Subscribe(SubscriptionOptions{Coalesce: time.Second})

	if err := bus.Close(context.Background()); err != nil {
		t.Fatalf("close bus: %v", err)
	}
}
