// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryExplicitIDsPreserveAccessAndDedupe verifies captured stock
// precedence: explicit IDs are unioned with normally filtered search candidates,
// while the explicit-ID branch bypasses ordinary filters. Both branches remain
// subject to item visibility; repeated and nonexistent IDs do not affect count.
func TestItemStoreQueryExplicitIDsPreserveAccessAndDedupeSQLite(t *testing.T) {
	runItemStoreQueryExplicitIDsPreserveAccessAndDedupe(t, openItemStore)
}

func TestItemStoreQueryExplicitIDsPreserveAccessAndDedupePostgres(t *testing.T) {
	runItemStoreQueryExplicitIDsPreserveAccessAndDedupe(t, openPostgresItemStore)
}

func runItemStoreQueryExplicitIDsPreserveAccessAndDedupe(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	fixture := newExplicitIDQueryFixture(t, items)

	query := store.ItemQuery{
		User:                   &store.User{ID: fixture.viewer},
		ItemIDs:                []dto.UUID{fixture.visibleSecond.ID, fixture.hidden.ID, fixture.visibleFirst.ID, fixture.searchMiss.ID, fixture.filterMiss.ID, fixture.normal.ID, fixture.visibleFirst.ID, fixture.missing},
		IncludeItemTypes:       []item.Kind{item.KindPlaylist},
		Genres:                 []string{"Focus"},
		SearchTerm:             "matching",
		EnableTotalRecordCount: true,
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
	}
	got, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying explicit ID access/dedupe: %v", err)
	}

	want := []dto.UUID{fixture.searchMiss.ID, fixture.visibleFirst.ID, fixture.filterMiss.ID, fixture.visibleSecond.ID, fixture.normal.ID, fixture.searchCandidate.ID}
	if ids := explicitIDQueryIDs(got.Items); !reflect.DeepEqual(ids, want) {
		t.Fatalf("explicit ID/search query item IDs = %v, want %v; explicit and search branches must preserve sort order while access still excludes hidden IDs", ids, want)
	}
	if got.TotalRecordCount != len(want) {
		t.Fatalf("explicit ID/search total record count = %d, want %d; duplicate or nonexistent IDs must not inflate the count", got.TotalRecordCount, len(want))
	}
}

type explicitIDQueryFixture struct {
	viewer                                                                       dto.UUID
	visibleFirst, visibleSecond, hidden, searchMiss, filterMiss, searchCandidate item.Item
	normal                                                                       item.Item
	missing                                                                      dto.UUID
}

func newExplicitIDQueryFixture(t *testing.T, items *store.ItemStore) explicitIDQueryFixture {
	t.Helper()
	viewer, owner := explicitIDQueryUUID(t, 1), explicitIDQueryUUID(t, 2)
	visibleFirst := explicitIDQueryPlaylist(t, 10, owner, "bravo matching", "Focus")
	visibleSecond := explicitIDQueryPlaylist(t, 11, owner, "delta matching", "Focus")
	hidden := explicitIDQueryPlaylist(t, 12, owner, "alpha matching", "Focus")
	searchMiss := explicitIDQueryPlaylist(t, 13, owner, "aardvark unrelated", "Focus")
	filterMiss := explicitIDQueryPlaylist(t, 14, owner, "charlie matching", "Other")
	normal := item.Item{
		ID:       explicitIDQueryUUID(t, 15),
		Kind:     item.KindMovie,
		Name:     explicitIDQueryString("echo matching"),
		SortName: explicitIDQueryString("echo matching"),
		Values:   item.ItemValues{Genres: []string{"Focus"}},
	}
	searchCandidate := explicitIDQueryPlaylist(t, 16, owner, "foxtrot matching", "Focus")
	for _, value := range []item.Item{visibleFirst, visibleSecond, hidden, searchMiss, filterMiss, normal, searchCandidate} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting explicit-ID fixture %s: %v", value.ID, err)
		}
	}
	for _, value := range []item.Item{visibleFirst, visibleSecond, searchMiss, filterMiss, searchCandidate} {
		if err := items.ReplaceShares(t.Context(), value.ID, []store.ItemShare{{UserID: viewer}}); err != nil {
			t.Fatalf("sharing private playlist %s with viewer: %v", value.ID, err)
		}
	}
	return explicitIDQueryFixture{
		viewer:          viewer,
		visibleFirst:    visibleFirst,
		visibleSecond:   visibleSecond,
		hidden:          hidden,
		searchMiss:      searchMiss,
		filterMiss:      filterMiss,
		normal:          normal,
		searchCandidate: searchCandidate,
		missing:         explicitIDQueryUUID(t, 99),
	}
}

func explicitIDQueryPlaylist(t *testing.T, value int, owner dto.UUID, name, genre string) item.Item {
	t.Helper()
	return item.Item{
		ID:                  explicitIDQueryUUID(t, value),
		Kind:                item.KindPlaylist,
		Name:                explicitIDQueryString(name),
		SortName:            explicitIDQueryString(name),
		PlaylistOwnerUserID: owner,
		Values:              item.ItemValues{Genres: []string{genre}},
	}
}

func explicitIDQueryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	result, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing explicit-ID fixture UUID %d: %v", value, err)
	}
	return result
}

func explicitIDQueryIDs(items []item.Item) []dto.UUID {
	result := make([]dto.UUID, len(items))
	for index, value := range items {
		result[index] = value.ID
	}
	return result
}

func explicitIDQueryString(value string) *string { return &value }
