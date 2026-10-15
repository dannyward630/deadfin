// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"sort"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/store"
)

const libraryChangeDebounce = 30 * time.Second

// UpdateInfo is the LibraryChanged WebSocket payload assembled from committed
// library mutations. Its six non-null arrays mirror LibraryUpdateInfo.cs.
type UpdateInfo struct {
	FoldersAddedTo     []dto.UUID `json:"FoldersAddedTo"`
	FoldersRemovedFrom []dto.UUID `json:"FoldersRemovedFrom"`
	ItemsAdded         []dto.UUID `json:"ItemsAdded"`
	ItemsRemoved       []dto.UUID `json:"ItemsRemoved"`
	ItemsUpdated       []dto.UUID `json:"ItemsUpdated"`
	CollectionFolders  []dto.UUID `json:"CollectionFolders"`
}

// Empty reports whether the update carries any visible mutation.
func (info UpdateInfo) Empty() bool {
	return len(info.FoldersAddedTo) == 0 &&
		len(info.FoldersRemovedFrom) == 0 &&
		len(info.ItemsAdded) == 0 &&
		len(info.ItemsRemoved) == 0 &&
		len(info.ItemsUpdated) == 0 &&
		len(info.CollectionFolders) == 0
}

// ChangeBus owns scanner mutation subscriptions. Each subscriber gets its own
// 30-second trailing aggregation window, matching LibraryChangedNotifier.cs.
type ChangeBus struct {
	bus *events.Bus[UpdateInfo]
}

func newChangeBus(c clock.Clock) *ChangeBus {
	return &ChangeBus{bus: events.NewBus[UpdateInfo](c)}
}

// Subscribe returns a bounded, trailing-aggregated LibraryChanged feed.
func (b *ChangeBus) Subscribe(buffer int) *events.Subscription[UpdateInfo] {
	return b.bus.SubscribeWith(
		events.SubscriptionOptions{Coalesce: libraryChangeDebounce, Buffer: buffer},
		func(info UpdateInfo) bool { return !info.Empty() },
		mergeUpdates,
	)
}

func (b *ChangeBus) publish(parentID, collectionFolderID dto.UUID, mutation store.ReconcileChildrenResult) {
	info := UpdateInfo{
		ItemsAdded:         cloneUUIDs(mutation.Inserted),
		ItemsUpdated:       cloneUUIDs(mutation.Updated),
		ItemsRemoved:       cloneUUIDs(mutation.Deleted),
		FoldersAddedTo:     parentFor(parentID, mutation.Inserted),
		FoldersRemovedFrom: parentFor(parentID, mutation.Deleted),
	}
	if (!parentID.IsNil() && (len(mutation.Inserted) != 0 || len(mutation.Deleted) != 0)) && !collectionFolderID.IsNil() {
		info.CollectionFolders = []dto.UUID{collectionFolderID}
	}
	b.bus.Publish(info)
}

// PublishItemUpdated publishes an item-updated mutation through the same debounced
// bus as scans, so identify/download reach WebSocket LibraryChanged consumers
// (ws/server.go:49). It delegates to publish with an updated-only mutation: no
// added or removed items, so CollectionFolders is intentionally not set (publish
// emits it only for insert/delete mutations).
func (b *ChangeBus) PublishItemUpdated(parentID, collectionFolderID dto.UUID, ids []dto.UUID) {
	if b == nil || b.bus == nil || len(ids) == 0 {
		return
	}
	b.publish(parentID, collectionFolderID, store.ReconcileChildrenResult{Updated: ids})
}

// PublishItemRemoved publishes an item deleted outside a scan, such as
// DELETE /Items/{itemId}. It mirrors LibraryManager.ReportItemRemoved: the
// removed item and the folder it was removed from.
func (b *ChangeBus) PublishItemRemoved(parentID, collectionFolderID dto.UUID, id dto.UUID) {
	if b == nil || b.bus == nil || id.IsNil() {
		return
	}
	b.publish(parentID, collectionFolderID, store.ReconcileChildrenResult{Deleted: []dto.UUID{id}})
}

func (b *ChangeBus) close(ctx context.Context) error {
	return b.bus.Close(ctx)
}

func parentFor(parentID dto.UUID, values []dto.UUID) []dto.UUID {
	if parentID.IsNil() || len(values) == 0 {
		return []dto.UUID{}
	}
	return []dto.UUID{parentID}
}

func mergeUpdates(current, next UpdateInfo) UpdateInfo {
	added := mergeUUIDs(current.ItemsAdded, next.ItemsAdded)
	updated := mergeUUIDs(current.ItemsUpdated, next.ItemsUpdated)
	updated = removeUUIDs(updated, added)
	return UpdateInfo{
		FoldersAddedTo:     mergeUUIDs(current.FoldersAddedTo, next.FoldersAddedTo),
		FoldersRemovedFrom: mergeUUIDs(current.FoldersRemovedFrom, next.FoldersRemovedFrom),
		ItemsAdded:         added,
		ItemsRemoved:       mergeUUIDs(current.ItemsRemoved, next.ItemsRemoved),
		ItemsUpdated:       updated,
		CollectionFolders:  mergeUUIDs(current.CollectionFolders, next.CollectionFolders),
	}
}

func mergeUUIDs(left, right []dto.UUID) []dto.UUID {
	seen := make(map[dto.UUID]struct{}, len(left)+len(right))
	result := make([]dto.UUID, 0, len(left)+len(right))
	for _, value := range append(cloneUUIDs(left), right...) {
		if value.IsNil() {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func removeUUIDs(values, removed []dto.UUID) []dto.UUID {
	excluded := make(map[dto.UUID]struct{}, len(removed))
	for _, value := range removed {
		excluded[value] = struct{}{}
	}
	result := make([]dto.UUID, 0, len(values))
	for _, value := range values {
		if _, found := excluded[value]; !found {
			result = append(result, value)
		}
	}
	return result
}

func cloneUUIDs(values []dto.UUID) []dto.UUID {
	if len(values) == 0 {
		return []dto.UUID{}
	}
	return append([]dto.UUID(nil), values...)
}
