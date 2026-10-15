// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryIsMovieSourceGateSQLite verifies the source-compatible
// IsMovie/include-type behavior through SQLite.
func TestItemStoreQueryIsMovieSourceGateSQLite(t *testing.T) {
	runItemStoreQueryIsMovieSourceGate(t, openItemStore)
}

// TestItemStoreQueryIsMovieSourceGatePostgres verifies the same public
// ItemStore contract against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is set.
func TestItemStoreQueryIsMovieSourceGatePostgres(t *testing.T) {
	runItemStoreQueryIsMovieSourceGate(t, openPostgresItemStore)
}

func runItemStoreQueryIsMovieSourceGate(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	movieFalse := scalarOrderStoreItem(t, 601, "a-movie-false", 0)
	movieFalse.Kind, movieFalse.IsMovie = item.KindMovie, false
	movieTrue := scalarOrderStoreItem(t, 602, "b-movie-true", 0)
	movieTrue.Kind, movieTrue.IsMovie = item.KindMovie, true
	trailerFalse := scalarOrderStoreItem(t, 603, "c-trailer-false", 0)
	trailerFalse.Kind, trailerFalse.IsMovie = item.KindTrailer, false
	trailerTrue := scalarOrderStoreItem(t, 604, "d-trailer-true", 0)
	trailerTrue.Kind, trailerTrue.IsMovie = item.KindTrailer, true
	unrelatedFalse := scalarOrderStoreItem(t, 605, "e-episode-false", 0)
	unrelatedFalse.Kind, unrelatedFalse.IsMovie = item.KindEpisode, false
	unrelatedTrue := scalarOrderStoreItem(t, 606, "f-episode-true", 0)
	unrelatedTrue.Kind, unrelatedTrue.IsMovie = item.KindEpisode, true
	for _, it := range []item.Item{movieFalse, movieTrue, trailerFalse, trailerTrue, unrelatedFalse, unrelatedTrue} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting IsMovie source-gate fixture item %s: %v", it.ID, err)
		}
	}

	trueValue, falseValue := true, false
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{
			name:  "true without include types preserves every source row",
			query: store.ItemQuery{IsMovie: &trueValue},
			want:  []dto.UUID{movieFalse.ID, movieTrue.ID, trailerFalse.ID, trailerTrue.ID, unrelatedFalse.ID, unrelatedTrue.ID},
		},
		{
			name:  "true with Movie include preserves false movie source row",
			query: store.ItemQuery{IsMovie: &trueValue, IncludeItemTypes: []item.Kind{item.KindMovie}},
			want:  []dto.UUID{movieFalse.ID, movieTrue.ID},
		},
		{
			name:  "true with Trailer include preserves false trailer source row",
			query: store.ItemQuery{IsMovie: &trueValue, IncludeItemTypes: []item.Kind{item.KindTrailer}},
			want:  []dto.UUID{trailerFalse.ID, trailerTrue.ID},
		},
		{
			name:  "true with unrelated include retains IsMovie predicate",
			query: store.ItemQuery{IsMovie: &trueValue, IncludeItemTypes: []item.Kind{item.KindEpisode}},
			want:  []dto.UUID{unrelatedTrue.ID},
		},
		{
			name:  "false without include types retains IsMovie predicate",
			query: store.ItemQuery{IsMovie: &falseValue},
			want:  []dto.UUID{movieFalse.ID, trailerFalse.ID, unrelatedFalse.ID},
		},
		{
			name:  "false with Movie include retains IsMovie predicate",
			query: store.ItemQuery{IsMovie: &falseValue, IncludeItemTypes: []item.Kind{item.KindMovie}},
			want:  []dto.UUID{movieFalse.ID},
		},
		{
			name:  "false with Trailer include retains IsMovie predicate",
			query: store.ItemQuery{IsMovie: &falseValue, IncludeItemTypes: []item.Kind{item.KindTrailer}},
			want:  []dto.UUID{trailerFalse.ID},
		},
		{
			name:  "false with unrelated include retains IsMovie predicate",
			query: store.ItemQuery{IsMovie: &falseValue, IncludeItemTypes: []item.Kind{item.KindEpisode}},
			want:  []dto.UUID{unrelatedFalse.ID},
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
				t.Fatalf("querying IsMovie source-gate fixture: %v", err)
			}
			if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != len(tc.want) {
				t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, len(tc.want))
			}
		})
	}
}
