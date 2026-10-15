// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// TestP6ProcessShutdownBudgetOutlivesManagerKillGraceAndRetries proves the
// process owner gives TranscodeManager enough time to progress from graceful
// interruption to reap, then makes one fresh bounded retry if that first drain
// reports a timeout. It uses a recording closer rather than a process so the
// timing contract is deterministic and does not consume the 5-second grace.
func TestP6ProcessShutdownBudgetOutlivesManagerKillGraceAndRetries(t *testing.T) {
	// TranscodeManagerOptions defaults KillGracePeriod to five seconds. The
	// process budget must include a distinct post-grace reap interval rather
	// than expiring at the moment SIGKILL becomes eligible.
	const defaultManagerKillGrace = 5 * time.Second
	const minimumReapMargin = 5 * time.Second
	if gracefulShutdownTimeout <= defaultManagerKillGrace+minimumReapMargin {
		t.Fatalf("graceful shutdown timeout = %s, want strictly more than manager kill grace %s plus reap margin %s", gracefulShutdownTimeout, defaultManagerKillGrace, minimumReapMargin)
	}

	first := context.DeadlineExceeded
	closer := &p6ShutdownCloser{results: []error{first, nil}}
	started := clock.NewReal().Now()
	err := closeApplicationWithRetry(closer)
	if !errors.Is(err, first) {
		t.Fatalf("shutdown result = %v, want joined first drain error %v", err, first)
	}
	if len(closer.calls) != 2 {
		t.Fatalf("application Close calls = %d, want initial drain plus one retry", len(closer.calls))
	}
	p6AssertShutdownContext(t, "initial", closer.calls[0], started, gracefulShutdownTimeout)
	p6AssertShutdownContext(t, "retry", closer.calls[1], started, shutdownRetryTimeout)
	if !closer.active[1] {
		t.Fatal("retry Close received an already canceled context")
	}
}

func p6AssertShutdownContext(t *testing.T, phase string, ctx context.Context, started time.Time, want time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("%s shutdown context has no deadline", phase)
	}
	// The helper should allocate its full budget. Allow scheduler time only;
	// there is no deliberate sleep in this regression test.
	if remaining := deadline.Sub(started); remaining < want-100*time.Millisecond || remaining > want+100*time.Millisecond {
		t.Fatalf("%s shutdown deadline budget = %s, want %s", phase, remaining, want)
	}
}

type p6ShutdownCloser struct {
	results []error
	calls   []context.Context
	active  []bool
}

func (closer *p6ShutdownCloser) Close(ctx context.Context) error {
	closer.calls = append(closer.calls, ctx)
	closer.active = append(closer.active, ctx.Err() == nil)
	return closer.results[len(closer.calls)-1]
}
