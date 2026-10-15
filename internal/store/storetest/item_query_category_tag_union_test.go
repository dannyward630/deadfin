// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryCategoryTagUnionSQLite verifies that caller tags and true
// category flags select either source tag through SQLite.
func TestItemStoreQueryCategoryTagUnionSQLite(t *testing.T) {
	runItemStoreQueryCategoryTagUnion(t, openItemStore)
}

// TestItemStoreQueryCategoryTagUnionPostgres verifies the same ItemStore
// contract against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is set.
func TestItemStoreQueryCategoryTagUnionPostgres(t *testing.T) {
	runItemStoreQueryCategoryTagUnion(t, openPostgresItemStore)
}

func runItemStoreQueryCategoryTagUnion(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	dramaOnly := categoryTagUnionItem(t, 801, "a-drama", "Drama")
	sportsOnly := categoryTagUnionItem(t, 802, "b-sports", "Sports")
	kidsOnly := categoryTagUnionItem(t, 803, "c-kids", "Kids")
	combined := categoryTagUnionItem(t, 804, "d-combined", "Drama", "Sports", "Kids")
	untagged := categoryTagUnionItem(t, 805, "e-untagged")
	for _, it := range []item.Item{dramaOnly, sportsOnly, kidsOnly, combined, untagged} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting category/tag fixture item %s: %v", it.ID, err)
		}
	}

	trueValue, falseValue := true, false
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{
			name:  "caller tags and sports category select either source tag",
			query: store.ItemQuery{Tags: []string{"Drama"}, IsSports: &trueValue},
			want:  []dto.UUID{dramaOnly.ID, sportsOnly.ID, combined.ID},
		},
		{
			name:  "sports and kids categories select either category tag",
			query: store.ItemQuery{IsSports: &trueValue, IsKids: &trueValue},
			want:  []dto.UUID{sportsOnly.ID, kidsOnly.ID, combined.ID},
		},
		{
			name:  "false sports excludes a positive caller tag match",
			query: store.ItemQuery{Tags: []string{"Drama"}, IsSports: &falseValue},
			want:  []dto.UUID{dramaOnly.ID},
		},
		{
			name:  "false sports joins excluded caller tags as one exclusion set",
			query: store.ItemQuery{ExcludeTags: []string{"Kids"}, IsSports: &falseValue},
			want:  []dto.UUID{dramaOnly.ID, untagged.ID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
			query.SortOrder = []store.SortOrder{store.SortOrderAscending}
			query.EnableTotalRecordCount = true
			got, err := items.Query(t.Context(), query)
			if err != nil {
				t.Fatalf("querying category/tag fixture: %v", err)
			}
			if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != len(tc.want) {
				t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, len(tc.want))
			}
		})
	}
}

func categoryTagUnionItem(t *testing.T, value int, name string, tags ...string) item.Item {
	t.Helper()
	it := scalarOrderStoreItem(t, value, name, 0)
	it.Values.Tags = tags
	return it
}
