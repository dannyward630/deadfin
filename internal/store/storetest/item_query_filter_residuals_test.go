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

// TestItemStoreQuerySQLiteResidualFilterFacts defends the query facts that are
// easy to mistranslate because the persisted user-data identity is a custom key,
// not merely an item ID, and because provider/search operators have distinct
// source semantics.
func TestItemStoreQuerySQLiteResidualFilterFacts(t *testing.T) {
	fixture := newResidualQueryFixture(t)
	trueValue, falseValue := true, false
	zeroID := dto.UUID{}

	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{
			name:  "played state follows a shared custom key across duplicate items",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.sharedMember.ID}, UserID: &fixture.userID, IsPlayed: &trueValue},
			want:  []dto.UUID{fixture.sharedMember.ID},
		},
		{
			name:  "unplayed means no positive played fact",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.sharedSource.ID, fixture.unplayed.ID}, UserID: &fixture.userID, IsPlayed: &falseValue},
			want:  []dto.UUID{fixture.unplayed.ID},
		},
		{
			name:  "resumable depends on positive position even when played",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.resumable.ID}, UserID: &fixture.userID, IsResumable: &trueValue},
			want:  []dto.UUID{fixture.resumable.ID},
		},
		{
			name:  "liked uses rating threshold rather than legacy likes count",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.highRated.ID, fixture.legacyLiked.ID}, UserID: &fixture.userID, IsLiked: &trueValue},
			want:  []dto.UUID{fixture.highRated.ID},
		},
		{
			name:  "favorite or liked remains the pinned favorite-only branch",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.legacyLiked.ID}, UserID: &fixture.userID, IsFavoriteOrLiked: &trueValue},
			want:  []dto.UUID{},
		},
		{
			name:  "single and multiple provider filters each must match",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID, fixture.providerIMDB.ID, fixture.providerBoth.ID}, HasAnyProviderID: map[string]string{"Tmdb": "603"}, HasAnyProviderIDs: map[string][]string{"Imdb": {"tt0084787"}}},
			want:  []dto.UUID{fixture.providerBoth.ID},
		},
		{
			name:  "empty single provider value means provider existence",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID, fixture.providerIMDB.ID}, HasAnyProviderID: map[string]string{"Tmdb": ""}},
			want:  []dto.UUID{fixture.providerTMDB.ID},
		},
		{
			name:  "empty excluded provider value remains an exact value",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID, fixture.providerIMDB.ID}, ExcludeProviderIDs: map[string]string{"Tmdb": ""}},
			want:  []dto.UUID{fixture.providerIMDB.ID, fixture.providerTMDB.ID},
		},
		{
			name:  "multiple provider values do not treat asterisk as a wildcard",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID}, HasAnyProviderIDs: map[string][]string{"Tmdb": {"*"}}},
			want:  []dto.UUID{},
		},
		{
			name:  "series custom provider keys share user data",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.sharedSeriesMember.ID}, UserID: &fixture.userID, IsPlayed: &trueValue},
			want:  []dto.UUID{fixture.sharedSeriesMember.ID},
		},
		{
			name:  "search term matches original title with source wildcard behavior",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.originalTitle.ID}, SearchTerm: "hidden%needle"},
			want:  []dto.UUID{fixture.originalTitle.ID},
		},
		{
			name:  "zero parent UUID leaves parent scope unset",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.sharedSource.ID, fixture.unplayed.ID}, ParentID: &zeroID},
			want:  []dto.UUID{fixture.sharedSource.ID, fixture.unplayed.ID},
		},
		{
			name:  "whitespace person leaves person filtering unset",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID, fixture.providerIMDB.ID}, Person: "  \t "},
			want:  []dto.UUID{fixture.providerIMDB.ID, fixture.providerTMDB.ID},
		},
		{
			name:  "whitespace person leaves Person ID filtering active while PersonTypes is ignored",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.providerTMDB.ID, fixture.providerIMDB.ID}, Person: " ", PersonIDs: []dto.UUID{fixture.actorID}, PersonTypes: []string{"Actor"}},
			want:  []dto.UUID{fixture.providerTMDB.ID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fixture.query(t, tc.query)
			if err != nil {
				t.Fatalf("querying residual fixture: %v", err)
			}
			if ids := residualQueryIDs(got.Items); !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("item IDs = %v, want %v", ids, tc.want)
			}
			if got.TotalRecordCount != len(tc.want) {
				t.Fatalf("total record count = %d, want %d", got.TotalRecordCount, len(tc.want))
			}
		})
	}
}

type residualQueryFixture struct {
	items              *store.ItemStore
	userID             dto.UUID
	sharedSource       item.Item
	sharedMember       item.Item
	unplayed           item.Item
	resumable          item.Item
	highRated          item.Item
	legacyLiked        item.Item
	providerTMDB       item.Item
	providerIMDB       item.Item
	providerBoth       item.Item
	originalTitle      item.Item
	sharedSeriesMember item.Item
	actorID            dto.UUID
}

func newResidualQueryFixture(t *testing.T) residualQueryFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite residual query fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite residual query fixture: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID := residualQueryUUID(t, 100)

	sharedSource := residualQueryItem(t, 1, "shared source", "Tmdb", "shared-603")
	actorID := residualQueryUUID(t, 200)
	sharedMember := residualQueryItem(t, 2, "shared member", "Tmdb", "shared-603")
	unplayed := residualQueryItem(t, 3, "unplayed", "Tmdb", "unplayed-603")
	resumable := residualQueryItem(t, 4, "resumable", "Tmdb", "resumable-603")
	highRated := residualQueryItem(t, 5, "rating high", "Tmdb", "rated-high")
	legacyLiked := residualQueryItem(t, 6, "likes legacy", "Tmdb", "liked-legacy")
	providerTMDB := residualQueryItem(t, 7, "provider tmdb", "Tmdb", "603")
	providerIMDB := residualQueryItem(t, 8, "provider imdb", "Imdb", "tt0084787")
	providerBoth := residualQueryItem(t, 9, "provider both", "Tmdb", "603")
	providerBoth.ProviderIDs.Set("Imdb", "tt0084787")
	originalTitle := residualQueryItem(t, 10, "catalog name", "Tmdb", "original-title")
	providerTMDB.People = []item.PersonCredit{{PersonID: actorID, Name: "Residual Actor", Type: item.PersonKindActor}}
	originalTitle.OriginalTitle = residualQueryString("Hidden Needle")
	sharedSeriesSource := item.Item{ID: residualQueryUUID(t, 11), Kind: item.KindSeries, Name: residualQueryString("shared series source"), SortName: residualQueryString("shared series source")}
	sharedSeriesSource.ProviderIDs.Set("Custom", "shared-series")
	sharedSeriesMember := item.Item{ID: residualQueryUUID(t, 12), Kind: item.KindSeries, Name: residualQueryString("shared series member"), SortName: residualQueryString("shared series member")}
	sharedSeriesMember.ProviderIDs.Set("Custom", "shared-series")

	all := []item.Item{sharedSource, sharedMember, unplayed, resumable, highRated, legacyLiked, providerTMDB, providerIMDB, providerBoth, originalTitle, sharedSeriesSource, sharedSeriesMember}
	for _, it := range all {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting residual fixture item %s: %v", it.ID, err)
		}
	}
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, sharedSource), ItemID: sharedSource.ID, Played: true})
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, unplayed), ItemID: unplayed.ID, Played: false})
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, resumable), ItemID: resumable.ID, Played: true, PositionTicks: 1})
	rating := float32(8)
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, highRated), ItemID: highRated.ID, Rating: &rating})
	likes := 1
	lowRating := float32(1)
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, legacyLiked), ItemID: legacyLiked.ID, Rating: &lowRating, Likes: &likes})
	residualQueryUpsertUserData(t, items, store.UserData{UserID: userID, CustomKey: residualQueryKey(t, sharedSeriesSource), ItemID: sharedSeriesSource.ID, Played: true})

	return residualQueryFixture{items: items, userID: userID, sharedSource: sharedSource, sharedMember: sharedMember, sharedSeriesMember: sharedSeriesMember, unplayed: unplayed, resumable: resumable, highRated: highRated, legacyLiked: legacyLiked, providerTMDB: providerTMDB, providerIMDB: providerIMDB, providerBoth: providerBoth, originalTitle: originalTitle, actorID: actorID}
}

func (f residualQueryFixture) query(t *testing.T, query store.ItemQuery) (store.ItemQueryResult, error) {
	t.Helper()
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	query.EnableTotalRecordCount = true
	return f.items.Query(t.Context(), query)
}

func residualQueryItem(t *testing.T, value int, name, provider, providerValue string) item.Item {
	t.Helper()
	it := item.Item{ID: residualQueryUUID(t, value), Kind: item.KindMovie, Name: residualQueryString(name), SortName: residualQueryString(name)}
	it.ProviderIDs.Set(provider, providerValue)
	return it
}

func residualQueryKey(t *testing.T, it item.Item) string {
	t.Helper()
	keys := item.UserDataKeys(it, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatalf("user data keys for %s are empty", it.ID)
	}
	return keys[0]
}

func residualQueryUpsertUserData(t *testing.T, items *store.ItemStore, data store.UserData) {
	t.Helper()
	if err := items.UpsertUserData(t.Context(), data); err != nil {
		t.Fatalf("upserting residual user data %q: %v", data.CustomKey, err)
	}
}

func residualQueryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing residual fixture UUID %d: %v", value, err)
	}
	return id
}

func residualQueryString(value string) *string { return &value }

func residualQueryIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
