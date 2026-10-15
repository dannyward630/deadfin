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

// TestItemStoreQuerySQLiteExecutesCombinedAiringScalars exercises real SQLite
// query and count execution for repeated clock and season predicates. SQLite
// requires one bound value per positional placeholder, unlike PostgreSQL's
// reusable numbered placeholders.
func TestItemStoreQuerySQLiteExecutesCombinedAiringScalars(t *testing.T) {
	items := openItemStore(t)
	reference := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	positiveSeason, negativeSeason := 7, -1
	current := airingScalarItem(t, 8101, reference.Add(-time.Hour), reference.Add(time.Hour), positiveSeason)
	boundary := airingScalarItem(t, 8102, reference.Add(-time.Hour), reference, negativeSeason)
	inverted := airingScalarItem(t, 8103, reference.Add(time.Hour), reference.Add(-time.Hour), positiveSeason)
	for _, it := range []item.Item{current, boundary, inverted} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting airing fixture %s: %v", it.ID, err)
		}
	}

	isAiring, isNotAiring := true, false
	hasAired, hasNotAired := true, false
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{
			name: "airing and not aired during a positive season",
			query: store.ItemQuery{
				IsAiring:          &isAiring,
				HasAired:          &hasNotAired,
				AiredDuringSeason: &positiveSeason,
				ReferenceTime:     &reference,
			},
			want: []dto.UUID{current.ID},
		},
		{
			name: "airing and aired during a negative season",
			query: store.ItemQuery{
				IsAiring:          &isAiring,
				HasAired:          &hasAired,
				AiredDuringSeason: &negativeSeason,
				ReferenceTime:     &reference,
			},
			want: []dto.UUID{boundary.ID},
		},
		{
			name: "not airing and aired during a positive season",
			query: store.ItemQuery{
				IsAiring:          &isNotAiring,
				HasAired:          &hasAired,
				AiredDuringSeason: &positiveSeason,
				ReferenceTime:     &reference,
			},
			want: []dto.UUID{inverted.ID},
		},
		{
			name: "not airing and not aired during a negative season is empty",
			query: store.ItemQuery{
				IsAiring:          &isNotAiring,
				HasAired:          &hasNotAired,
				AiredDuringSeason: &negativeSeason,
				ReferenceTime:     &reference,
			},
			want: []dto.UUID{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			query.EnableTotalRecordCount = true
			got, err := items.Query(t.Context(), query)
			if err != nil {
				t.Fatalf("executing combined airing query: %v", err)
			}
			if ids := airingScalarIDs(got.Items); !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("item IDs = %v, want %v", ids, tc.want)
			}
			if got.TotalRecordCount != len(tc.want) {
				t.Fatalf("total record count = %d, want %d", got.TotalRecordCount, len(tc.want))
			}
		})
	}
}

func airingScalarItem(t *testing.T, value int, start, end time.Time, parentIndex int) item.Item {
	t.Helper()
	return item.Item{
		ID:                airingScalarUUID(t, value),
		Kind:              item.KindMovie,
		Name:              airingScalarString("airing scalar fixture"),
		StartDate:         &start,
		EndDate:           &end,
		ParentIndexNumber: &parentIndex,
	}
}

func airingScalarUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing airing fixture UUID %d: %v", value, err)
	}
	return id
}

func airingScalarString(value string) *string { return &value }

func airingScalarIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
