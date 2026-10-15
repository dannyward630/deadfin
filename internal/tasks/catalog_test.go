// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

func TestCatalogPublishesSortedVisibleScheduledTasks(t *testing.T) {
	catalog := tasks.New(clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	subscription := catalog.Subscribe(events.SubscriptionOptions{Buffer: 3})

	for _, info := range []tasks.Info{
		{ID: "hidden", Name: "Hidden", IsHidden: true},
		{ID: "z", Name: "Refresh metadata", State: tasks.StateRunning},
		{ID: "a", Name: "Clean cache"},
	} {
		if err := catalog.Upsert(t.Context(), info); err != nil {
			t.Fatalf("upsert task %q: %v", info.ID, err)
		}
	}

	_ = receiveTasks(t, subscription.Events())
	firstVisible := receiveTasks(t, subscription.Events())
	if len(firstVisible) != 1 || firstVisible[0].ID != "z" {
		t.Fatalf("first visible catalog = %+v, want running refresh task", firstVisible)
	}
	got := receiveTasks(t, subscription.Events())
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "z" || got[0].Triggers == nil {
		t.Fatalf("scheduled task catalog = %+v, want two sorted visible tasks with non-null triggers", got)
	}
}

func receiveTasks(t *testing.T, values <-chan []tasks.Info) []tasks.Info {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("task subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("task catalog update was not delivered")
		return nil
	}
}
