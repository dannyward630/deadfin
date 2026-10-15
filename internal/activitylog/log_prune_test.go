// SPDX-License-Identifier: GPL-2.0-only

package activitylog_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestLogPruneDropsOnlyOlderEntries(t *testing.T) {
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	log := activitylog.New(clock.NewFake(start))
	t.Cleanup(func() { _ = log.Close(context.Background()) })

	// Write an old entry, then advance the clock and write a new one.
	old, err := log.Write(t.Context(), activitylog.Entry{Name: "old", Type: "Test"})
	if err != nil {
		t.Fatalf("write old entry: %v", err)
	}

	c := clock.NewFake(start.Add(48 * time.Hour))
	newLog := activitylog.New(c)
	t.Cleanup(func() { _ = newLog.Close(context.Background()) })
	recent, err := newLog.Write(t.Context(), activitylog.Entry{Name: "recent", Type: "Test"})
	if err != nil {
		t.Fatalf("write recent entry: %v", err)
	}

	// Prune the old log at a cutoff between the two entries.
	cutoff := old.Date.Time.Add(24 * time.Hour)
	removed, err := log.Prune(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Prune removed %d, want 1", removed)
	}
	entries := log.Entries()
	if len(entries) != 0 {
		t.Fatalf("after Prune, %d entries remain, want 0", len(entries))
	}

	// Prune the new log at the same cutoff — nothing should be removed.
	removed, err = newLog.Prune(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("Prune new log: %v", err)
	}
	if removed != 0 {
		t.Fatalf("Prune new log removed %d, want 0", removed)
	}
	entries = newLog.Entries()
	if len(entries) != 1 {
		t.Fatalf("after Prune, %d entries remain, want 1", len(entries))
	}
	if entries[0].ID != recent.ID {
		t.Fatalf("remaining entry ID = %d, want %d", entries[0].ID, recent.ID)
	}
}

func TestLogPruneOnClosedLogReturnsError(t *testing.T) {
	now := clock.NewReal().Now()
	log := activitylog.New(clock.NewFake(now))
	if err := log.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := log.Prune(context.Background(), now); err == nil {
		t.Fatal("Prune on closed log returned nil, want error")
	}
}
