// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// The pinned stock fixture uses SQLite binary text order. Keep spaces,
// punctuation, case, and UTF-8 bytes in the same order on both backends.
func TestItemStoreQueryTextCollationSQLite(t *testing.T) {
	runItemStoreQueryTextCollation(t, openItemStore)
}

func TestItemStoreQueryTextCollationPostgres(t *testing.T) {
	runItemStoreQueryTextCollation(t, openPostgresItemStore)
}

func runItemStoreQueryTextCollation(t *testing.T, open func(*testing.T) *store.ItemStore) {
	items := open(t)
	parent := queryUUID(t, 80)
	fixtures := []struct {
		id       dto.UUID
		sortName string
	}{
		{queryUUID(t, 81), "Z upper"},
		{queryUUID(t, 82), "z space"},
		{queryUUID(t, 83), "z!mark"},
		{queryUUID(t, 84), "z/slash"},
		{queryUUID(t, 85), "z0digit"},
		{queryUUID(t, 86), "za letter"},
		{queryUUID(t, 87), "zé accent"},
	}
	if err := items.Upsert(t.Context(), item.Item{ID: parent, Kind: item.KindFolder, IsFolder: true, Name: queryString("parent")}); err != nil {
		t.Fatalf("upserting parent: %v", err)
	}
	itemIDs := make([]dto.UUID, 0, len(fixtures))
	for _, fixture := range fixtures {
		itemIDs = append(itemIDs, fixture.id)
		value := item.Item{
			ID:       fixture.id,
			Kind:     item.KindMovie,
			ParentID: &parent,
			Name:     queryString("same display name"),
			SortName: queryString(fixture.sortName),
		}
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting %q: %v", fixture.sortName, err)
		}
	}

	ascending := append([]dto.UUID(nil), itemIDs...)
	descending := make([]dto.UUID, len(itemIDs))
	for index := range itemIDs {
		descending[index] = itemIDs[len(itemIDs)-1-index]
	}
	for _, tc := range []struct {
		name      string
		direction store.SortOrder
		want      []dto.UUID
	}{
		{name: "ascending", direction: store.SortOrderAscending, want: ascending},
		{name: "descending", direction: store.SortOrderDescending, want: descending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := items.Query(t.Context(), store.ItemQuery{
				ItemIDs:   itemIDs,
				SortBy:    []store.ItemSortBy{store.ItemSortBySortName},
				SortOrder: []store.SortOrder{tc.direction},
			})
			if err != nil {
				t.Fatalf("querying text order: %v", err)
			}
			if got := queryIDs(result.Items); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("text order IDs = %v, want %v", got, tc.want)
			}
		})
	}
}
