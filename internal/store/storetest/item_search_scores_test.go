// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// SearchScores ports SqlSearchProvider: candidates come from the Items
// search filter, are scored by clean name, and are ordered by score, then id.
// The score arguments come before the id relation in the statement, so the
// test runs on both placeholder styles.

func TestItemSearchScoresSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertItemSearchScores(t, stores)
}

func TestItemSearchScoresPostgres(t *testing.T) {
	assertItemSearchScores(t, storetestpkg.OpenPostgres(t))
}

func assertItemSearchScores(t *testing.T, stores store.Stores) {
	t.Helper()
	f := newRecursiveParentFixture(t, stores)
	items := store.NewItemStore(stores)
	id := func(n int) dto.UUID { return itemQueryPlanUUID(t, 90_000+n) }
	name := itemQueryPlanString
	physical := id(10)
	for _, value := range []item.Item{
		{ID: id(60), Kind: item.KindMovie, ParentID: &physical, Name: name("Movie")},
		{ID: id(61), Kind: item.KindMovie, ParentID: &physical, Name: name("Movie_Night 100%")},
		{ID: id(62), Kind: item.KindMovie, ParentID: &physical, Name: name("Late Movie Show")},
		{ID: id(63), Kind: item.KindMovie, ParentID: &physical, Name: name("Moviegoer")},
		{ID: id(64), Kind: item.KindMovie, ParentID: &physical, Name: name("Movie Ghost"), IsVirtualItem: true},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	if err := items.RepairDerived(t.Context()); err != nil {
		t.Fatalf("repair derived: %v", err)
	}
	notVirtual := false
	movies := []item.Kind{item.KindMovie}
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
		limit int
		want  []store.SearchScore
	}{
		{
			name:  "tiers",
			query: store.ItemQuery{SearchTerm: "movie", IncludeItemTypes: movies, IsVirtualItem: &notVirtual},
			limit: 100,
			// "Movie A" (fixture) and "Moviegoer" and "Movie_Night 100%" start
			// with the term; "Late Movie Show" contains "movie ".
			want: []store.SearchScore{
				{ID: id(60), Score: store.SearchScoreExact},
				{ID: id(15), Score: store.SearchScorePrefix},
				{ID: id(20), Score: store.SearchScorePrefix},
				{ID: id(21), Score: store.SearchScorePrefix},
				{ID: id(61), Score: store.SearchScorePrefix},
				{ID: id(63), Score: store.SearchScorePrefix},
				{ID: id(62), Score: store.SearchScoreWordPrefix},
			},
		},
		{
			name:  "limit",
			query: store.ItemQuery{SearchTerm: "movie", IncludeItemTypes: movies, IsVirtualItem: &notVirtual},
			limit: 2,
			want:  []store.SearchScore{{ID: id(60), Score: store.SearchScoreExact}, {ID: id(15), Score: store.SearchScorePrefix}},
		},
		{
			name:  "parent scope",
			query: store.ItemQuery{SearchTerm: "movie", IncludeItemTypes: movies, IsVirtualItem: &notVirtual, Recursive: true, ParentID: &f.nestedLibrary, User: &f.user, UserID: &f.user.ID},
			limit: 100,
			want:  []store.SearchScore{{ID: id(20), Score: store.SearchScorePrefix}},
		},
		{
			name:  "like wildcards in the term are literal",
			query: store.ItemQuery{SearchTerm: "movie_night 100%", IncludeItemTypes: movies},
			limit: 100,
			want:  []store.SearchScore{{ID: id(61), Score: store.SearchScoreExact}},
		},
		{
			name:  "contains",
			query: store.ItemQuery{SearchTerm: "100", IncludeItemTypes: movies},
			limit: 100,
			want:  []store.SearchScore{{ID: id(61), Score: store.SearchScoreContains}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := items.SearchScores(t.Context(), tc.query, tc.limit)
			if err != nil {
				t.Fatalf("search scores: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("scores = %v, want %v", got, tc.want)
			}
		})
	}
}
