// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreQuerySQLiteIDsPrecedeParentScope defends upstream explicit-ID
// precedence: ParentID does not remove explicitly selected items.
func TestItemStoreQuerySQLiteFiltersCountsAndPages(t *testing.T) {
	items := openQueryItemStore(t)
	parent := queryUUID(t, 1)
	otherParent := queryUUID(t, 2)
	first := queryItem(t, 10, parent, "same")
	second := queryItem(t, 11, parent, "same")
	foreign := queryItem(t, 12, otherParent, "same")
	for _, it := range []item.Item{
		{ID: parent, Kind: item.KindFolder, Name: queryString("parent")},
		{ID: otherParent, Kind: item.KindFolder, Name: queryString("other parent")},
		first,
		second,
		foreign,
	} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	result, err := items.Query(t.Context(), store.ItemQuery{
		ItemIDs:                []dto.UUID{second.ID, foreign.ID, first.ID},
		ParentID:               &parent,
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		StartIndex:             queryInt(1),
		Limit:                  queryInt(1),
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("querying filtered page: %v", err)
	}
	if got, want := queryIDs(result.Items), []dto.UUID{second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paged filtered item IDs = %v, want %v", got, want)
	}
	if got, want := result.TotalRecordCount, 3; got != want {
		t.Fatalf("total record count = %d, want %d explicit IDs before pagination", got, want)
	}
}

// TestItemStoreQuerySQLitePaginatesNullableSortNamesFirst defends the stock
// SQLite behavior: ascending nullable sort values precede named items.
func TestItemStoreQuerySQLitePaginatesNullableSortNamesFirst(t *testing.T) {
	items := openQueryItemStore(t)
	parent := queryUUID(t, 1)
	first := queryItem(t, 10, parent, "alpha")
	second := queryItem(t, 11, parent, "bravo")
	missingSortName := queryItem(t, 12, parent, "ignored")
	missingSortName.SortName = nil
	for _, it := range []item.Item{
		{ID: parent, Kind: item.KindFolder, Name: queryString("parent")},
		first,
		second,
		missingSortName,
	} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	result, err := items.Query(t.Context(), store.ItemQuery{
		ParentID:   &parent,
		SortBy:     []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:  []store.SortOrder{store.SortOrderAscending},
		StartIndex: queryInt(1),
		Limit:      queryInt(1),
	})
	if err != nil {
		t.Fatalf("querying nullable SortName page: %v", err)
	}
	if got, want := queryIDs(result.Items), []dto.UUID{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page after an absent SortName = %v, want %v; NULL SortName must be first", got, want)
	}
}

// TestItemStoreQuerySQLiteAcceptsSwiftfinSeriesFallbackOrder reproduces
// Swiftfin's firstAvailableItem request: recursive episodes below a series,
// Limit=1, and SortOrder=Ascending without SortBy. Jellyfin's
// RequestHelpers.GetOrderBy ignores that unpaired direction and uses the
// repository's default SortName order.
func TestItemStoreQuerySQLiteAcceptsSwiftfinSeriesFallbackOrder(t *testing.T) {
	items := openQueryItemStore(t)
	seriesID, seasonID := queryUUID(t, 30), queryUUID(t, 31)
	first, second := queryItem(t, 32, seasonID, "alpha"), queryItem(t, 33, seasonID, "bravo")
	first.Kind, second.Kind = item.KindEpisode, item.KindEpisode
	for _, value := range []item.Item{
		{ID: seriesID, Kind: item.KindSeries, IsFolder: true, Name: queryString("series")},
		{ID: seasonID, Kind: item.KindSeason, IsFolder: true, ParentID: &seriesID, Name: queryString("season")},
		first,
		second,
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting Swiftfin series fixture %s: %v", value.ID, err)
		}
	}
	missing := false
	result, err := items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindEpisode},
		ParentID:         &seriesID,
		Recursive:        true,
		IsMissing:        &missing,
		Limit:            queryInt(1),
		SortOrder:        []store.SortOrder{store.SortOrderAscending},
	})
	if err != nil {
		t.Fatalf("querying Swiftfin first available episode: %v", err)
	}
	if got, want := queryIDs(result.Items), []dto.UUID{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Swiftfin first available episode = %v, want %v", got, want)
	}
}

// TestItemStoreQuerySQLiteTreatsEmptyItemIDsAsNoFilter defends the caller
// contract that an empty ID slice means no ID constraint, not a query that can
// never match. It must remain equivalent to a nil ID slice.
func TestItemStoreQuerySQLiteTreatsEmptyItemIDsAsNoFilter(t *testing.T) {
	items := openQueryItemStore(t)
	parent := queryUUID(t, 1)
	first := queryItem(t, 10, parent, "alpha")
	second := queryItem(t, 11, parent, "bravo")
	for _, it := range []item.Item{
		{ID: parent, Kind: item.KindFolder, Name: queryString("parent")},
		first,
		second,
	} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	query := store.ItemQuery{
		ParentID:               &parent,
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		EnableTotalRecordCount: true,
	}
	nilIDs, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying nil item IDs: %v", err)
	}
	query.ItemIDs = []dto.UUID{}
	emptyIDs, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying empty item IDs: %v", err)
	}

	want := []dto.UUID{first.ID, second.ID}
	if got := queryIDs(nilIDs.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("nil item IDs returned %v, want %v", got, want)
	}
	if got := queryIDs(emptyIDs.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("empty item IDs returned %v, want %v", got, want)
	}
	if nilIDs.TotalRecordCount != len(want) || emptyIDs.TotalRecordCount != len(want) {
		t.Fatalf("nil and empty item IDs counts = (%d, %d), want (%d, %d)", nilIDs.TotalRecordCount, emptyIDs.TotalRecordCount, len(want), len(want))
	}
}

func openQueryItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite query fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite query fixture: %v", err)
		}
	})
	return store.NewItemStore(stores)
}

func queryItem(t *testing.T, value int, parent dto.UUID, sortName string) item.Item {
	t.Helper()
	return item.Item{
		ID:       queryUUID(t, value),
		Kind:     item.KindMovie,
		ParentID: &parent,
		Name:     queryString(fmt.Sprintf("item %d", value)),
		SortName: queryString(sortName),
	}
}

func queryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func queryIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}

func queryInt(value int) *int { return &value }

func queryString(value string) *string { return &value }
