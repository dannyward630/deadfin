// SPDX-License-Identifier: GPL-2.0-only

package activitylog_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/events"
)

func TestLogPublishesCommittedEntryWithClockAndSequence(t *testing.T) {
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	log := activitylog.New(clock.NewFake(start))
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	subscription := log.Subscribe(events.SubscriptionOptions{Buffer: 1})

	written, err := log.Write(t.Context(), activitylog.Entry{Name: "Server started", Type: "ServerStarted"})
	if err != nil {
		t.Fatalf("write activity entry: %v", err)
	}
	if written.ID != 1 || !written.Date.Time.Equal(start) || written.Severity != "Information" {
		t.Fatalf("written activity = %+v, want id/date/default severity", written)
	}
	if got := receiveActivity(t, subscription.Events()); got != written {
		t.Fatalf("published activity = %+v, want %+v", got, written)
	}
}

func receiveActivity(t *testing.T, values <-chan activitylog.Entry) activitylog.Entry {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("activity subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("activity entry was not delivered")
		return activitylog.Entry{}
	}
}
