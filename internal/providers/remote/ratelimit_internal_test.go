// SPDX-License-Identifier: GPL-2.0-only

package remote

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestRateLimiterTokenMath(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0))
	limiter := newRateLimiter(fake, time.Second, 2)

	// Burst of two grants two immediate tokens.
	if delay := limiter.reserve(); delay != 0 {
		t.Fatalf("first reserve delay = %v, want 0", delay)
	}
	if delay := limiter.reserve(); delay != 0 {
		t.Fatalf("second reserve delay = %v, want 0", delay)
	}
	// The third within the same instant must wait one full interval.
	if delay := limiter.reserve(); delay != time.Second {
		t.Fatalf("third reserve delay = %v, want 1s", delay)
	}
	// After half an interval, only half a token has accrued.
	fake.Advance(500 * time.Millisecond)
	if delay := limiter.reserve(); delay != 500*time.Millisecond {
		t.Fatalf("reserve after 500ms delay = %v, want 500ms", delay)
	}
	// Advancing a full interval grants exactly one token.
	fake.Advance(time.Second)
	if delay := limiter.reserve(); delay != 0 {
		t.Fatalf("reserve after refill delay = %v, want 0", delay)
	}
}

func TestRateLimiterUnlimitedWhenDisabled(t *testing.T) {
	limiter := newRateLimiter(clock.NewFake(time.Unix(0, 0)), 0, 0)
	if delay := limiter.reserve(); delay != 0 {
		t.Fatalf("disabled limiter reserve delay = %v, want 0", delay)
	}
	if err := limiter.wait(t.Context()); err != nil {
		t.Fatalf("disabled limiter wait = %v, want nil", err)
	}
}

func TestFreshnessLifetimePrecedence(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		cacheControl string
		expires      string
		date         string
		fallback     time.Duration
		wantLifetime time.Duration
		wantOK       bool
	}{
		{name: "max-age wins", cacheControl: "public, max-age=3600", wantLifetime: time.Hour, wantOK: true},
		{name: "no-store forbids", cacheControl: "no-store", wantOK: false},
		{name: "no-cache forbids", cacheControl: "no-cache", wantOK: false},
		{name: "expires relative to date", expires: "Wed, 21 Oct 2026 07:28:00 GMT", date: "Wed, 21 Oct 2026 07:00:00 GMT", wantLifetime: 28 * time.Minute, wantOK: true},
		{name: "fallback when unspecified", fallback: 15 * time.Minute, wantLifetime: 15 * time.Minute, wantOK: true},
		{name: "uncacheable without hints", wantOK: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			header := map[string][]string{}
			if testCase.cacheControl != "" {
				header["Cache-Control"] = []string{testCase.cacheControl}
			}
			if testCase.expires != "" {
				header["Expires"] = []string{testCase.expires}
			}
			if testCase.date != "" {
				header["Date"] = []string{testCase.date}
			}
			lifetime, ok := freshnessLifetime(header, testCase.fallback)
			if ok != testCase.wantOK || (ok && lifetime != testCase.wantLifetime) {
				t.Fatalf("freshnessLifetime = %v, %t; want %v, %t", lifetime, ok, testCase.wantLifetime, testCase.wantOK)
			}
		})
	}
}
