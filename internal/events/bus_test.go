// SPDX-License-Identifier: GPL-2.0-only

package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/events"
)

// CONVENTIONS.md §1.5 and WP-P0-01 require typed cross-cutting events whose
// coalescing belongs to each subscription, never the publisher.
type libraryChange struct {
	Revision int
}

func TestBusDeliversTypedEventsWithoutCoalescing(t *testing.T) {
	bus, _ := newBus(t)
	subscription := bus.Subscribe(events.SubscriptionOptions{Buffer: 2})

	bus.Publish(libraryChange{Revision: 1})
	bus.Publish(libraryChange{Revision: 2})

	if got := receive(t, subscription.Events()); got != (libraryChange{Revision: 1}) {
		t.Fatalf("first event = %+v, want revision 1", got)
	}
	if got := receive(t, subscription.Events()); got != (libraryChange{Revision: 2}) {
		t.Fatalf("second event = %+v, want revision 2", got)
	}
}

func TestSubscriptionCoalescesTrailingLatestEventOnFakeTime(t *testing.T) {
	// CONVENTIONS.md §1.4 rejects wall-clock timing in tests; §1.5 requires
	// subscription-local trailing coalescing.
	bus, fake := newBus(t)
	subscription := bus.Subscribe(events.SubscriptionOptions{Coalesce: 30 * time.Second})

	bus.Publish(libraryChange{Revision: 1})
	fake.Advance(29 * time.Second)
	mustNotReceive(t, subscription.Events())

	bus.Publish(libraryChange{Revision: 2})
	fake.Advance(29 * time.Second)
	mustNotReceive(t, subscription.Events())

	fake.Advance(time.Second)
	if got := receive(t, subscription.Events()); got != (libraryChange{Revision: 2}) {
		t.Fatalf("coalesced event = %+v, want latest revision 2", got)
	}
}

func TestSubscriptionsWithDifferentCoalescingWindowsAreIndependent(t *testing.T) {
	// The WP-P0-01 acceptance test requires fake-time coalescing, and
	// CONVENTIONS.md §1.5 makes the debounce a property of each subscription.
	bus, fake := newBus(t)
	fast := bus.Subscribe(events.SubscriptionOptions{Coalesce: time.Second})
	slow := bus.Subscribe(events.SubscriptionOptions{Coalesce: 5 * time.Second})

	bus.Publish(libraryChange{Revision: 1})
	fake.Advance(time.Second)
	if got := receive(t, fast.Events()); got != (libraryChange{Revision: 1}) {
		t.Fatalf("fast event = %+v, want revision 1", got)
	}
	mustNotReceive(t, slow.Events())

	bus.Publish(libraryChange{Revision: 2})
	fake.Advance(time.Second)
	if got := receive(t, fast.Events()); got != (libraryChange{Revision: 2}) {
		t.Fatalf("fast event = %+v, want revision 2", got)
	}

	fake.Advance(3 * time.Second)
	mustNotReceive(t, slow.Events())
	fake.Advance(time.Second)
	if got := receive(t, slow.Events()); got != (libraryChange{Revision: 2}) {
		t.Fatalf("slow event = %+v, want its independently coalesced latest revision 2", got)
	}
}

func TestSubscriptionFiltersAndReducesDuringTrailingWindow(t *testing.T) {
	bus, fake := newBus(t)
	subscription := bus.SubscribeWith(
		events.SubscriptionOptions{Coalesce: time.Second},
		func(change libraryChange) bool { return change.Revision > 0 },
		func(current, next libraryChange) libraryChange {
			return libraryChange{Revision: current.Revision + next.Revision}
		},
	)

	bus.Publish(libraryChange{Revision: -100})
	bus.Publish(libraryChange{Revision: 2})
	fake.Advance(500 * time.Millisecond)
	bus.Publish(libraryChange{Revision: 3})
	fake.Advance(999 * time.Millisecond)
	mustNotReceive(t, subscription.Events())
	fake.Advance(time.Millisecond)

	if got := receive(t, subscription.Events()); got != (libraryChange{Revision: 5}) {
		t.Fatalf("filtered reduced event = %+v, want revision sum 5", got)
	}
}

func TestClosingSubscriptionCancelsPendingDeliveryAndUnsubscribes(t *testing.T) {
	// CONVENTIONS.md §1.5 requires subscription-local delivery; cancellation
	// must discard a pending coalesced event rather than publish after unsubscribe.
	bus, fake := newBus(t)
	subscription := bus.Subscribe(events.SubscriptionOptions{Coalesce: time.Second})
	bus.Publish(libraryChange{Revision: 1})

	if err := subscription.Close(context.Background()); err != nil {
		t.Fatalf("close subscription: %v", err)
	}
	fake.Advance(time.Second)
	mustBeClosed(t, subscription.Events())

	// Publishing after unsubscribe must not revive the closed subscription.
	bus.Publish(libraryChange{Revision: 2})
	mustBeClosed(t, subscription.Events())
}

func TestSlowConsumerIsDroppedWithoutBlockingOtherSubscriptions(t *testing.T) {
	// CONVENTIONS.md §1.5: the bus is never blocked; only the slow consumer is
	// dropped when its bounded outbound channel fills.
	bus, _ := newBus(t)
	slow := bus.Subscribe(events.SubscriptionOptions{Buffer: 1})
	fast := bus.Subscribe(events.SubscriptionOptions{Buffer: 4})

	bus.Publish(libraryChange{Revision: 1})
	bus.Publish(libraryChange{Revision: 2})

	if got := receive(t, fast.Events()); got != (libraryChange{Revision: 1}) {
		t.Fatalf("fast first event = %+v, want revision 1", got)
	}
	if got := receive(t, fast.Events()); got != (libraryChange{Revision: 2}) {
		t.Fatalf("fast second event = %+v, want revision 2", got)
	}
	if got := receive(t, slow.Events()); got != (libraryChange{Revision: 1}) {
		t.Fatalf("slow buffered event = %+v, want revision 1", got)
	}
	mustBeClosed(t, slow.Events())
}

func TestBusCloseClosesAllSubscriptions(t *testing.T) {
	// CONVENTIONS.md §1.3 requires owning services to prove their goroutines stop.
	bus, _ := newBus(t)
	first := bus.Subscribe(events.SubscriptionOptions{})
	second := bus.Subscribe(events.SubscriptionOptions{})

	if err := bus.Close(context.Background()); err != nil {
		t.Fatalf("close bus: %v", err)
	}
	mustBeClosed(t, first.Events())
	mustBeClosed(t, second.Events())
}

func newBus(t *testing.T) (*events.Bus[libraryChange], *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := events.NewBus[libraryChange](fake)
	t.Cleanup(func() {
		if err := bus.Close(context.Background()); err != nil {
			t.Errorf("close bus during cleanup: %v", err)
		}
	})
	return bus, fake
}

func receive[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("events channel closed before expected event")
		}
		return event
	case <-timer.C:
		t.Fatal("event was not delivered after deterministic work yielded")
		var zero T
		return zero
	}
}

func mustNotReceive[T any](t *testing.T, events <-chan T) {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		t.Fatalf("received event before coalescing window elapsed: %+v", event)
	default:
	}
}

func mustBeClosed[T any](t *testing.T, events <-chan T) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case _, ok := <-events:
		if !ok {
			return
		}
		t.Fatal("events channel contained an unexpected event")
	case <-timer.C:
		t.Fatal("events channel did not close after shutdown")
	}
}
