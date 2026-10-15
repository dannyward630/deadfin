// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestChangeBusAggregatesCommittedMutationsAfterThirtySecondsOfQuiet(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := newChangeBus(fake)
	t.Cleanup(func() {
		if err := bus.close(context.Background()); err != nil {
			t.Errorf("close library change bus: %v", err)
		}
	})
	subscription := bus.Subscribe(1)

	parent, collection := changeUUID(1), changeUUID(2)
	added, updated, removed := changeUUID(3), changeUUID(4), changeUUID(5)
	bus.publish(parent, collection, store.ReconcileChildrenResult{
		Inserted: []dto.UUID{added},
		Updated:  []dto.UUID{updated},
	})
	fake.Advance(29 * time.Second)
	assertNoLibraryUpdate(t, subscription.Events())

	// An update to a newly-added item is excluded, like
	// LibraryChangedNotifier's _itemsUpdated.Where(!_itemsAdded.Contains).
	bus.publish(parent, collection, store.ReconcileChildrenResult{
		Updated: []dto.UUID{added, updated},
		Deleted: []dto.UUID{removed},
	})
	fake.Advance(29 * time.Second)
	assertNoLibraryUpdate(t, subscription.Events())
	fake.Advance(time.Second)

	got := receiveLibraryUpdate(t, subscription.Events())
	want := UpdateInfo{
		FoldersAddedTo:     []dto.UUID{parent},
		FoldersRemovedFrom: []dto.UUID{parent},
		ItemsAdded:         []dto.UUID{added},
		ItemsRemoved:       []dto.UUID{removed},
		ItemsUpdated:       []dto.UUID{updated},
		CollectionFolders:  []dto.UUID{collection},
	}
	assertLibraryUpdate(t, got, want)
}

func TestChangeBusDebounceIsIndependentPerSubscription(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := newChangeBus(fake)
	t.Cleanup(func() { _ = bus.close(context.Background()) })
	first := bus.Subscribe(1)
	second := bus.Subscribe(1)

	bus.publish(changeUUID(10), changeUUID(11), store.ReconcileChildrenResult{Inserted: []dto.UUID{changeUUID(12)}})
	fake.Advance(libraryChangeDebounce)

	_ = receiveLibraryUpdate(t, first.Events())
	_ = receiveLibraryUpdate(t, second.Events())
}

func receiveLibraryUpdate(t *testing.T, values <-chan UpdateInfo) UpdateInfo {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("library update subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("library update was not delivered")
		return UpdateInfo{}
	}
}

func assertNoLibraryUpdate(t *testing.T, values <-chan UpdateInfo) {
	t.Helper()
	select {
	case value := <-values:
		t.Fatalf("library update arrived before trailing debounce: %+v", value)
	default:
	}
}

func assertLibraryUpdate(t *testing.T, got, want UpdateInfo) {
	t.Helper()
	if !sameUUIDs(got.FoldersAddedTo, want.FoldersAddedTo) ||
		!sameUUIDs(got.FoldersRemovedFrom, want.FoldersRemovedFrom) ||
		!sameUUIDs(got.ItemsAdded, want.ItemsAdded) ||
		!sameUUIDs(got.ItemsRemoved, want.ItemsRemoved) ||
		!sameUUIDs(got.ItemsUpdated, want.ItemsUpdated) ||
		!sameUUIDs(got.CollectionFolders, want.CollectionFolders) {
		t.Fatalf("library update = %+v, want %+v", got, want)
	}
}

func sameUUIDs(left, right []dto.UUID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func changeUUID(value byte) dto.UUID {
	var id dto.UUID
	id[15] = value
	return id
}

// TestPublishItemUpdatedIsUpdatedOnly verifies PublishItemUpdated publishes an
// updated-only mutation through the existing publish path: no added/removed
// items, no CollectionFolders (publish emits it only for insert/delete).
func TestPublishItemUpdatedIsUpdatedOnly(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := newChangeBus(fake)
	t.Cleanup(func() { _ = bus.close(context.Background()) })
	subscription := bus.Subscribe(1)

	parent, collection := changeUUID(1), changeUUID(2)
	updated := changeUUID(3)
	bus.PublishItemUpdated(parent, collection, []dto.UUID{updated})
	fake.Advance(31 * time.Second)

	got := receiveLibraryUpdate(t, subscription.Events())
	want := UpdateInfo{
		ItemsUpdated: []dto.UUID{updated},
		// publish does not set CollectionFolders for an updated-only mutation.
	}
	assertLibraryUpdate(t, got, want)
}

// TestPublishItemUpdatedNilBusIsNoOp verifies a nil bus does not panic.
func TestPublishItemUpdatedNilBusIsNoOp(t *testing.T) {
	var bus *ChangeBus
	bus.PublishItemUpdated(dto.NilUUID, dto.NilUUID, []dto.UUID{changeUUID(1)})
}
