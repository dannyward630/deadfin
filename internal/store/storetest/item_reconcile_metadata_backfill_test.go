// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStoreReconcileMetadataBackfillSQLite(t *testing.T) {
	runItemStoreReconcileMetadataBackfill(t, openItemStore)
}

func TestItemStoreReconcileMetadataBackfillPostgres(t *testing.T) {
	runItemStoreReconcileMetadataBackfill(t, openPostgresItemStore)
}

func runItemStoreReconcileMetadataBackfill(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()

	items := open(t)
	parent := hierarchyItem(t, 11800, item.KindFolder, nil)
	if err := items.Upsert(t.Context(), parent); err != nil {
		t.Fatalf("upsert metadata backfill parent: %v", err)
	}

	candidate := reconciliationChild(t, 11801, item.KindMovie, parent.ID, "unchanged movie")
	firstSaved := time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	result, err := items.ReconcileScannedPlanAt(t.Context(), parent.ID, []item.Item{candidate}, nil, nil, firstSaved)
	if err != nil {
		t.Fatalf("seed movie without provider metadata: %v", err)
	}
	assertReconcileResult(t, result, []dto.UUID{candidate.ID}, nil, nil)

	candidate.DateLastSaved = &firstSaved
	candidate.ProviderIDs.Set("Tmdb", "603")
	candidate.Overview = stringPtr("A provider-sourced overview")
	secondSaved := firstSaved.Add(time.Hour)
	result, err = items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, []item.Item{candidate}, nil, nil, secondSaved, store.ReconcileOptions{ForceRefresh: true})
	if err != nil {
		t.Fatalf("backfill metadata on unchanged movie: %v", err)
	}
	assertReconcileResult(t, result, nil, []dto.UUID{candidate.ID}, nil)

	refreshed, err := items.Get(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("get metadata-backfilled movie: %v", err)
	}
	if providerID, ok := refreshed.ProviderIDs.Get("Tmdb"); !ok || providerID != "603" {
		t.Fatalf("backfilled TMDb id = %q, %t; want 603, true", providerID, ok)
	}
	if refreshed.Overview == nil || *refreshed.Overview != "A provider-sourced overview" {
		t.Fatalf("backfilled overview = %v; want provider-sourced overview", refreshed.Overview)
	}
	if refreshed.DateLastSaved == nil || !refreshed.DateLastSaved.Equal(secondSaved) {
		t.Fatalf("backfilled DateLastSaved = %v; want %v", refreshed.DateLastSaved, secondSaved)
	}

	candidate.DateLastSaved = &secondSaved
	thirdSaved := secondSaved.Add(time.Hour)
	result, err = items.ReconcileScannedPlanAt(t.Context(), parent.ID, []item.Item{candidate}, nil, nil, thirdSaved)
	if err != nil {
		t.Fatalf("reconcile identical metadata: %v", err)
	}
	assertReconcileResult(t, result, nil, nil, nil)

	unchanged, err := items.Get(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("get unchanged metadata movie: %v", err)
	}
	if unchanged.DateLastSaved == nil || !unchanged.DateLastSaved.Equal(secondSaved) {
		t.Fatalf("no-op DateLastSaved = %v; want unchanged %v", unchanged.DateLastSaved, secondSaved)
	}

	result, err = items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, []item.Item{candidate}, nil, nil, thirdSaved, store.ReconcileOptions{ForceRefresh: true})
	if err != nil {
		t.Fatalf("force persistence of identical refreshed metadata: %v", err)
	}
	assertReconcileResult(t, result, nil, []dto.UUID{candidate.ID}, nil)

	forced, err := items.Get(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("get force-refreshed metadata movie: %v", err)
	}
	if forced.DateLastSaved == nil || !forced.DateLastSaved.Equal(thirdSaved) {
		t.Fatalf("force-refreshed DateLastSaved = %v; want %v", forced.DateLastSaved, thirdSaved)
	}
}
