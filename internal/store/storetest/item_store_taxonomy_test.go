// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreTaxonomyFactsSQLite pins the facts the taxonomy routes read
// in place of full items: names of matched items, distinct credits, and
// distinct value names, each over exactly the items the query matches.
func TestItemStoreTaxonomyFactsSQLite(t *testing.T) {
	runItemStoreTaxonomyFacts(t, openItemStore)
}

func TestItemStoreTaxonomyFactsPostgres(t *testing.T) {
	runItemStoreTaxonomyFacts(t, openPostgresItemStore)
}

func runItemStoreTaxonomyFacts(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	ada, bob := scalarOrderStoreUUID(t, 950), scalarOrderStoreUUID(t, 951)
	movie := scalarOrderStoreItem(t, 960, "movie", 0)
	movie.Values = item.ItemValues{Genres: []string{"Drama", "Comedy"}, Studios: []string{"Acme"}, Tags: []string{"tagged"}}
	movie.People = []item.PersonCredit{{PersonID: ada, Name: "Ada", Type: item.PersonKindDirector}, {PersonID: bob, Name: "Bob", Type: item.PersonKindActor}}
	series := scalarOrderStoreItem(t, 961, "series", 0)
	series.Kind = item.KindSeries
	series.Values = item.ItemValues{Genres: []string{"Drama"}, Studios: []string{"Zeta"}}
	series.People = []item.PersonCredit{{PersonID: ada, Name: "Ada", Type: item.PersonKindActor}}
	// An alternate version is outside every default query; its facts must not
	// leak into the aggregate.
	alternate := scalarOrderStoreItem(t, 962, "alternate", 0)
	alternate.PrimaryVersionID = &movie.ID
	alternate.Values = item.ItemValues{Genres: []string{"Hidden"}}
	alternate.People = []item.PersonCredit{{PersonID: scalarOrderStoreUUID(t, 952), Name: "Hidden", Type: item.PersonKindActor}}
	for _, it := range []item.Item{movie, series, alternate} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	all := store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie, item.KindSeries}}
	names, err := items.QueryItemNames(t.Context(), all)
	if err != nil {
		t.Fatalf("QueryItemNames: %v", err)
	}
	gotNames := make([]string, len(names))
	for index, name := range names {
		if name.Name == nil {
			t.Fatalf("name %s is nil", name.ID)
		}
		gotNames[index] = name.Kind.String() + ":" + *name.Name
	}
	slices.Sort(gotNames)
	if want := []string{"Movie:movie", "Series:series"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("QueryItemNames = %v, want %v", gotNames, want)
	}

	credits, err := items.QueryPersonCredits(t.Context(), all)
	if err != nil {
		t.Fatalf("QueryPersonCredits: %v", err)
	}
	gotCredits := make([]string, len(credits))
	for index, credit := range credits {
		gotCredits[index] = credit.PersonID.String() + ":" + credit.Name + ":" + credit.Type.String()
	}
	slices.Sort(gotCredits)
	wantCredits := []string{ada.String() + ":Ada:Actor", ada.String() + ":Ada:Director", bob.String() + ":Bob:Actor"}
	slices.Sort(wantCredits)
	if !reflect.DeepEqual(gotCredits, wantCredits) {
		t.Fatalf("QueryPersonCredits = %v, want %v", gotCredits, wantCredits)
	}

	for _, tc := range []struct {
		query store.ItemQuery
		kinds []string
		want  []string
	}{
		{all, []string{"genre"}, []string{"Comedy", "Drama"}},
		{all, []string{"studio", "tag"}, []string{"Acme", "Zeta", "tagged"}},
		{store.ItemQuery{ItemIDs: []dto.UUID{series.ID}}, []string{"studio"}, []string{"Zeta"}},
		{all, nil, nil},
	} {
		got, err := items.QueryItemValues(t.Context(), tc.query, tc.kinds...)
		if err != nil {
			t.Fatalf("QueryItemValues %v: %v", tc.kinds, err)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("QueryItemValues %v = %v, want %v", tc.kinds, got, tc.want)
		}
	}
}
