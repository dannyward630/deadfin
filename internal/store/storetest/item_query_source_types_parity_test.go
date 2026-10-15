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

// TestItemStoreQuerySourceTypesParitySQLite verifies through the persisted store
// that SourceTypes narrows candidates before its total and page calculations.
func TestItemStoreQuerySourceTypesParitySQLite(t *testing.T) {
	runItemStoreQuerySourceTypesParity(t, openItemStore)
}

// TestItemStoreQuerySourceTypesParityPostgres runs the same public store
// contract against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreQuerySourceTypesParityPostgres(t *testing.T) {
	runItemStoreQuerySourceTypesParity(t, openPostgresItemStore)
}

func runItemStoreQuerySourceTypesParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	libraryFirst := sourceTypesStoreItem(t, 9301, "alpha library", item.SourceTypeLibrary)
	channel := sourceTypesStoreItem(t, 9302, "bravo channel", item.SourceTypeChannel)
	liveTV := sourceTypesStoreItem(t, 9303, "charlie live tv", item.SourceTypeLiveTV)
	libraryLast := sourceTypesStoreItem(t, 9304, "delta library", item.SourceTypeLibrary)
	for _, it := range []item.Item{libraryFirst, channel, liveTV, libraryLast} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting source fixture %s: %v", it.ID, err)
		}
	}

	limit, start := 1, 1
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
		total int
	}{
		{
			name: "mixed sources filter before count and page",
			query: store.ItemQuery{
				SourceTypes:            []item.SourceType{item.SourceTypeLibrary, item.SourceTypeLiveTV},
				SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
				SortOrder:              []store.SortOrder{store.SortOrderAscending},
				StartIndex:             &start,
				Limit:                  &limit,
				EnableTotalRecordCount: true,
			},
			want:  []dto.UUID{liveTV.ID},
			total: 3,
		},
		{
			name: "empty sources are unrestricted",
			query: store.ItemQuery{
				SourceTypes:            []item.SourceType{},
				SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
				SortOrder:              []store.SortOrder{store.SortOrderAscending},
				EnableTotalRecordCount: true,
			},
			want:  []dto.UUID{libraryFirst.ID, channel.ID, liveTV.ID, libraryLast.ID},
			total: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := items.Query(t.Context(), tc.query)
			if err != nil {
				t.Fatalf("querying source-type fixture: %v", err)
			}
			if ids := sourceTypesStoreIDs(got.Items); !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != tc.total {
				t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, tc.total)
			}
		})
	}
}

func sourceTypesStoreItem(t *testing.T, value int, name string, sourceType item.SourceType) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing source-type fixture UUID %d: %v", value, err)
	}
	return item.Item{
		ID:         id,
		Kind:       item.KindMovie,
		Name:       &name,
		SortName:   &name,
		SourceType: sourceType,
	}
}

func sourceTypesStoreIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
