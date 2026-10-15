// SPDX-License-Identifier: GPL-2.0-only

// Package events provides typed, non-blocking cross-cutting notifications.
package events

import (
	"context"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// SubscriptionOptions controls delivery for one subscriber.
// Coalesce is a trailing quiet-period debounce measured by the Bus clock.
// Buffer bounds the subscriber's outbound queue. A non-positive value uses one slot.
type SubscriptionOptions struct {
	Coalesce time.Duration
	Buffer   int
}

// Bus publishes values of one concrete event type. It owns every subscription
// it creates and Close waits for their workers to stop.
type Bus[T any] struct {
	clock clock.Clock

	mu            sync.Mutex
	closed        bool
	subscriptions map[*Subscription[T]]struct{}
	closing       []*Subscription[T]
}

// NewBus creates a typed event bus using c for all subscription timing.
func NewBus[T any](c clock.Clock) *Bus[T] {
	if c == nil {
		panic("events: clock is required")
	}
	return &Bus[T]{
		clock:         c,
		subscriptions: make(map[*Subscription[T]]struct{}),
	}
}

// Subscribe registers a new subscriber. Subscribing after Close returns an
// already-closed subscription.
func (b *Bus[T]) Subscribe(options SubscriptionOptions) *Subscription[T] {
	return b.SubscribeWith(options, nil, nil)
}

// SubscribeWith registers a subscription-local filter and reducer. Filtered
// values never enter the debounce window. Reduce combines accepted values
// during a trailing Coalesce window. When it is nil, the latest value wins.
// Both behaviors belong to the subscriber. Publishers remain unaware of
// transport-specific batching.
func (b *Bus[T]) SubscribeWith(options SubscriptionOptions, filter func(T) bool, reduce func(T, T) T) *Subscription[T] {
	if options.Buffer <= 0 {
		options.Buffer = 1
	}

	subscription := newSubscription(b, options, filter, reduce)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		subscription.close()
		return subscription
	}
	b.subscriptions[subscription] = struct{}{}
	b.mu.Unlock()

	subscription.start()
	return subscription
}

// Publish offers event to every current subscriber without blocking. A slow
// subscriber is closed rather than being allowed to stall publishers.
func (b *Bus[T]) Publish(event T) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subscriptions := make([]*Subscription[T], 0, len(b.subscriptions))
	for subscription := range b.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	b.mu.Unlock()

	for _, subscription := range subscriptions {
		subscription.publish(event)
	}
}

// Close stops every subscription and waits for its workers.
// You can call Close more than once. A canceled ctx reports its cancellation
// after the shutdown work that already completed.
func (b *Bus[T]) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.closing = make([]*Subscription[T], 0, len(b.subscriptions))
		for subscription := range b.subscriptions {
			b.closing = append(b.closing, subscription)
		}
		b.subscriptions = nil
	}
	subscriptions := b.closing
	b.mu.Unlock()

	for _, subscription := range subscriptions {
		subscription.close()
	}
	for _, subscription := range subscriptions {
		if err := subscription.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus[T]) remove(subscription *Subscription[T]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscriptions != nil {
		delete(b.subscriptions, subscription)
	}
}

// Subscription receives values from one Bus. Its Events channel is closed when
// the subscription is closed, dropped for slow consumption, or its Bus closes.
type Subscription[T any] struct {
	bus     *Bus[T]
	options SubscriptionOptions
	filter  func(T) bool
	reduce  func(T, T) T

	mu      sync.Mutex
	closed  bool
	started bool
	pending T
	hasItem bool
	due     time.Time
	timer   clock.Timer

	events   chan T
	done     chan struct{}
	wake     chan struct{}
	finished chan struct{}
}

func newSubscription[T any](bus *Bus[T], options SubscriptionOptions, filter func(T) bool, reduce func(T, T) T) *Subscription[T] {
	return &Subscription[T]{
		bus:      bus,
		options:  options,
		filter:   filter,
		reduce:   reduce,
		events:   make(chan T, options.Buffer),
		done:     make(chan struct{}),
		wake:     make(chan struct{}, 1),
		finished: make(chan struct{}),
	}
}

func (s *Subscription[T]) start() {
	if s.options.Coalesce <= 0 {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.runCoalescer()
}

// Events returns the bounded delivery channel for this subscription.
func (s *Subscription[T]) Events() <-chan T {
	return s.events
}

// Close unregisters the subscription and waits for its worker to stop.
func (s *Subscription[T]) Close(ctx context.Context) error {
	s.close()
	return s.wait(ctx)
}

func (s *Subscription[T]) publish(event T) {
	if s.filter != nil && !s.filter(event) {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	if s.options.Coalesce <= 0 {
		select {
		case s.events <- event:
			s.mu.Unlock()
			return
		default:
			s.mu.Unlock()
			s.close()
			return
		}
	}

	if s.reduce != nil {
		s.pending = s.reduce(s.pending, event)
	} else {
		s.pending = event
	}
	s.hasItem = true
	s.due = s.bus.clock.Now().Add(s.options.Coalesce)
	if s.timer == nil {
		s.timer = s.bus.clock.NewTimer(s.options.Coalesce)
	} else {
		s.stopAndDrainTimerLocked()
		s.timer.Reset(s.options.Coalesce)
	}
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription[T]) runCoalescer() {
	defer close(s.events)
	defer close(s.finished)

	for {
		s.mu.Lock()
		timer := s.timer
		s.mu.Unlock()

		if timer == nil {
			select {
			case <-s.done:
				return
			case <-s.wake:
				continue
			}
		}

		select {
		case <-s.done:
			return
		case <-s.wake:
			continue
		case <-timer.C():
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return
			}
			now := s.bus.clock.Now()
			if s.hasItem && now.Before(s.due) {
				s.stopAndDrainTimerLocked()
				s.timer.Reset(s.due.Sub(now))
				s.mu.Unlock()
				continue
			}
			if !s.hasItem {
				s.mu.Unlock()
				continue
			}
			event := s.pending
			s.hasItem = false
			s.mu.Unlock()

			select {
			case s.events <- event:
			case <-s.done:
				return
			default:
				s.close()
				return
			}
		}
	}
}

func (s *Subscription[T]) stopAndDrainTimerLocked() {
	if s.timer.Stop() {
		return
	}
	select {
	case <-s.timer.C():
	default:
	}
}

func (s *Subscription[T]) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.stopAndDrainTimerLocked()
	}
	close(s.done)
	if s.options.Coalesce <= 0 || !s.started {
		close(s.events)
		close(s.finished)
	}
	s.mu.Unlock()
	s.bus.remove(s)
}

func (s *Subscription[T]) wait(ctx context.Context) error {
	select {
	case <-s.finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
