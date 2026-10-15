// SPDX-License-Identifier: GPL-2.0-only

// Package clock supplies injected time for code that compares time or owns timers.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the current time and creates timers. Implementations are owned
// by their callers; no package-level clock is installed.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the portion of time.Timer needed by services that use an injected
// Clock. Callers must stop timers they no longer need.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

// Real is the production wall-clock implementation.
type Real struct{}

// NewReal returns a production Clock.
func NewReal() Clock {
	return Real{}
}

// Now returns the current wall-clock time.
func (Real) Now() time.Time {
	return time.Now()
}

// NewTimer returns a production timer.
func (Real) NewTimer(delay time.Duration) Timer {
	return realTimer{timer: time.NewTimer(delay)}
}

type realTimer struct {
	timer *time.Timer
}

func (t realTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t realTimer) Stop() bool {
	return t.timer.Stop()
}

func (t realTimer) Reset(delay time.Duration) bool {
	return t.timer.Reset(delay)
}

// Fake is a manually advanced Clock for deterministic tests. It is safe for
// concurrent use and has no relation to wall-clock time.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

// NewFake returns a Fake starting at start.
func NewFake(start time.Time) *Fake {
	return &Fake{
		now:    start,
		timers: make(map[*fakeTimer]struct{}),
	}
}

// Now returns the Fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer registers a timer that fires when Advance reaches its deadline.
func (f *Fake) NewTimer(delay time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()

	timer := &fakeTimer{
		clock:  f,
		at:     f.now.Add(delay),
		active: true,
		ch:     make(chan time.Time, 1),
	}
	f.timers[timer] = struct{}{}
	if delay <= 0 {
		timer.fireLocked()
	}
	return timer
}

// Advance moves fake time forward and fires every timer due at the new time.
func (f *Fake) Advance(delay time.Duration) {
	if delay < 0 {
		panic("clock: cannot advance backwards")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = f.now.Add(delay)
	for timer := range f.timers {
		if timer.active && !timer.at.After(f.now) {
			timer.fireLocked()
		}
	}
}

type fakeTimer struct {
	clock  *Fake
	at     time.Time
	active bool
	ch     chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time {
	return t.ch
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()

	wasActive := t.active
	t.active = false
	select {
	case <-t.ch:
	default:
	}
	return wasActive
}

func (t *fakeTimer) Reset(delay time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()

	wasActive := t.active
	t.active = true
	t.at = t.clock.now.Add(delay)
	select {
	case <-t.ch:
	default:
	}
	if delay <= 0 {
		t.fireLocked()
	}
	return wasActive
}

func (t *fakeTimer) fireLocked() {
	t.active = false
	select {
	case t.ch <- t.at:
	default:
	}
}
