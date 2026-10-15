// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryMinDateLastSavedForUserParitySQLite exercises the public
// query boundary with a null saved date, the inclusive lower boundary, and a
// later value. PostgreSQL uses the exact same persisted fixture below.
// Source: BaseItemRepository.TranslateQuery.cs:276-279 at upstream/jellyfin.
func TestItemStoreQueryMinDateLastSavedForUserParitySQLite(t *testing.T) {
	runItemStoreQueryMinDateLastSavedForUserParity(t, openItemStore)
}

// TestItemStoreQueryMinDateLastSavedForUserParityPostgres exercises the same
// filter and deterministic DateCreated order against PostgreSQL when
// DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreQueryMinDateLastSavedForUserParityPostgres(t *testing.T) {
	runItemStoreQueryMinDateLastSavedForUserParity(t, openPostgresItemStore)
}

func runItemStoreQueryMinDateLastSavedForUserParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	boundary := time.Date(2026, time.July, 27, 6, 44, 50, 0, time.UTC)
	owner := dateSavedParityUUID(t, 700)
	before := dateSavedParityItem(t, 701, "before", boundary.Add(-time.Second), boundary.Add(-time.Second))
	exact := dateSavedParityItem(t, 702, "exact", boundary, boundary)
	after := dateSavedParityItem(t, 703, "after", boundary.Add(time.Second), boundary.Add(time.Second))
	nilSaved := dateSavedParityItem(t, 704, "nil-saved", boundary.Add(2*time.Second), time.Time{})
	for _, value := range []item.Item{before, exact, after, nilSaved} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert saved-date fixture %s: %v", value.ID, err)
		}
	}

	query := store.ItemQuery{
		UserID:                  &owner,
		MinDateLastSavedForUser: &boundary,
		SortBy:                  []store.ItemSortBy{store.ItemSortByDateCreated},
		SortOrder:               []store.SortOrder{store.SortOrderAscending},
		EnableTotalRecordCount:  true,
	}
	result, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("query minimum saved date: %v", err)
	}
	if got, want := dateSavedParityIDs(result.Items), []dto.UUID{exact.ID, after.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MinDateLastSavedForUser IDs = %v, want inclusive saved-date boundary in DateCreated order %v", got, want)
	}
	if got, want := result.TotalRecordCount, 2; got != want {
		t.Fatalf("MinDateLastSavedForUser total = %d, want %d before pagination", got, want)
	}

	limit, start := 1, 1
	query.Limit, query.StartIndex = &limit, &start
	page, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("query minimum saved date page: %v", err)
	}
	if got, want := dateSavedParityIDs(page.Items), []dto.UUID{after.ID}; !reflect.DeepEqual(got, want) || page.TotalRecordCount != 2 {
		t.Fatalf("MinDateLastSavedForUser page/count = %v/%d, want %v/2", got, page.TotalRecordCount, want)
	}
}

func dateSavedParityItem(t *testing.T, value int, name string, created, saved time.Time) item.Item {
	t.Helper()
	result := item.Item{
		ID:          dateSavedParityUUID(t, value),
		Kind:        item.KindMovie,
		Name:        &name,
		SortName:    &name,
		DateCreated: &created,
	}
	if !saved.IsZero() {
		result.DateLastSaved = &saved
	}
	return result
}

func dateSavedParityUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse date-saved UUID %d: %v", value, err)
	}
	return id
}

func dateSavedParityIDs(values []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}
