// SPDX-License-Identifier: GPL-2.0-only

package remote

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// rateLimiter is a token bucket that paces outbound requests. It refills at a
// steady rate up to a burst ceiling and blocks callers, respecting context
// cancellation, until a token is available. Time comes from an injected
// clock.Clock (CONVENTIONS 1.4) so tests advance it deterministically.
type rateLimiter struct {
	clock    clock.Clock
	interval time.Duration // time to accrue one token
	burst    float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// newRateLimiter builds a limiter allowing burst requests immediately and one
// further request per interval thereafter. A non-positive interval or burst
// yields an unlimited limiter (wait always returns immediately).
func newRateLimiter(clk clock.Clock, interval time.Duration, burst int) *rateLimiter {
	if clk == nil {
		clk = clock.NewReal()
	}
	if interval <= 0 || burst <= 0 {
		return &rateLimiter{clock: clk}
	}
	return &rateLimiter{
		clock:    clk,
		interval: interval,
		burst:    float64(burst),
		tokens:   float64(burst),
		last:     clk.Now(),
	}
}

// wait blocks until a token is available or ctx is done. An unlimited limiter
// returns as soon as ctx is live.
func (limiter *rateLimiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if limiter.interval <= 0 {
		return nil
	}
	for {
		delay := limiter.reserve()
		if delay <= 0 {
			return nil
		}
		timer := limiter.clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
		}
	}
}

// reserve refills the bucket for elapsed real time, then either consumes a
// token (returning 0) or returns how long the caller must wait before a token
// will exist. It never consumes a token it does not grant, so concurrent
// callers each re-reserve after waiting.
func (limiter *rateLimiter) reserve() time.Duration {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := limiter.clock.Now()
	if elapsed := now.Sub(limiter.last); elapsed > 0 {
		limiter.tokens += float64(elapsed) / float64(limiter.interval)
		if limiter.tokens > limiter.burst {
			limiter.tokens = limiter.burst
		}
		limiter.last = now
	}
	if limiter.tokens >= 1 {
		limiter.tokens--
		return 0
	}
	deficit := 1 - limiter.tokens
	return time.Duration(deficit * float64(limiter.interval))
}

// errClosed is returned when a limiter or client is used after shutdown; kept
// here so both files share one sentinel.
var errClosed = errors.New("remote: client is closed")
