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
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreQuerySQLiteFilterFamilies defends the externally visible
// ItemStore contract for each first-wave filter family. The fixture is written
// only through ItemStore.Upsert so it exercises the same normalized satellites
// and hierarchy closure that a caller uses in production.
func TestItemStoreQuerySQLiteFilterFamilies(t *testing.T) {
	fixture := newFilterQueryFixture(t)

	minPremiere := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	maxPremiere := time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC)
	minRating, maxRating := float32(8), float32(9)
	minRuntime, maxRuntime := int64(3_000), int64(5_000)
	trueValue, falseValue := true, false
	minLastSavedForUser := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	unairedReference := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
		total int
	}{
		{
			name:  "kinds and media types intersect",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, MediaTypes: []item.MediaType{item.MediaTypeVideo}},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID, fixture.virtual.ID}, total: 3,
		},
		{
			name:  "excluded kinds apply when no inclusion list overrides them",
			query: store.ItemQuery{ExcludeItemTypes: []item.Kind{item.KindFolder}},
			want:  []dto.UUID{fixture.audio.ID, fixture.movie.ID, fixture.foreign.ID, fixture.virtual.ID}, total: 4,
		},
		{
			name:  "included kinds override excluded kinds",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, ExcludeItemTypes: []item.Kind{item.KindMovie}},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID, fixture.virtual.ID}, total: 3,
		},
		{
			name:  "ancestor scope uses maintained closure",
			query: store.ItemQuery{AncestorIDs: []dto.UUID{fixture.branch.ID}, IncludeItemTypes: []item.Kind{item.KindMovie}},
			want:  []dto.UUID{fixture.movie.ID, fixture.virtual.ID}, total: 2,
		},
		{
			name:  "top parent scope excludes a foreign library",
			query: store.ItemQuery{TopParentIDs: []dto.UUID{fixture.root.ID}, IncludeItemTypes: []item.Kind{item.KindMovie}},
			want:  []dto.UUID{fixture.movie.ID, fixture.virtual.ID}, total: 2,
		},
		{
			name:  "include and exclude item IDs combine",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.movie.ID, fixture.virtual.ID, fixture.foreign.ID}, ExcludeItemIDs: []dto.UUID{fixture.virtual.ID}},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID}, total: 2,
		},
		{
			name:  "genres use normalized values",
			query: store.ItemQuery{Genres: []string{"Drama"}},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "studios use normalized values",
			query: store.ItemQuery{Studios: []string{"North Star"}},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "tags include matching normalized values",
			query: store.ItemQuery{Tags: []string{"Featured"}},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "excluded tags reject a mapped value",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, ExcludeTags: []string{"Blocked"}},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID}, total: 2,
		},
		{
			name:  "single provider identifier matches provider and value",
			query: store.ItemQuery{HasAnyProviderID: map[string]string{"Tmdb": "603"}},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "multi-value provider identifier matches any supplied value",
			query: store.ItemQuery{HasAnyProviderIDs: map[string][]string{"Tmdb": {"absent", "999"}}},
			want:  []dto.UUID{fixture.virtual.ID}, total: 1,
		},
		{
			name:  "multi-value provider identifiers treat asterisk as an exact value",
			query: store.ItemQuery{HasAnyProviderIDs: map[string][]string{"Tmdb": {"*"}}, Limit: filterQueryInt(1)},
			want:  []dto.UUID{}, total: 0,
		},
		{
			name:  "excluded provider identifier removes exact pair",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, ExcludeProviderIDs: map[string]string{"Tmdb": "999"}},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID}, total: 2,
		},
		{
			name:  "year matches stored production year",
			query: store.ItemQuery{Years: []int{2024}},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "premiere date window is inclusive",
			query: store.ItemQuery{MinPremiereDate: &minPremiere, MaxPremiereDate: &maxPremiere},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "community rating range is inclusive",
			query: store.ItemQuery{MinCommunityRating: &minRating, MaxCommunityRating: &maxRating},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "runtime range is inclusive",
			query: store.ItemQuery{MinRunTimeTicks: &minRuntime, MaxRunTimeTicks: &maxRuntime},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "last saved for user requires identity but filters global saved date",
			query: store.ItemQuery{UserID: &fixture.userID, MinDateLastSavedForUser: &minLastSavedForUser},
			want:  []dto.UUID{fixture.virtual.ID}, total: 1,
		},
		{
			name:  "search term uses the clean name column",
			query: store.ItemQuery{SearchTerm: "cafe"},
			want:  []dto.UUID{fixture.movie.ID}, total: 1,
		},
		{
			name:  "folder filter uses stored item fact",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindFolder}, IsFolder: &trueValue},
			want:  []dto.UUID{fixture.branch.ID, fixture.foreignRoot.ID, fixture.root.ID}, total: 3,
		},
		{
			name:  "folder false excludes folder kinds",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, IsFolder: &falseValue},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID, fixture.virtual.ID}, total: 3,
		},
		{
			name:  "virtual item filter uses stored item fact",
			query: store.ItemQuery{IsVirtualItem: &trueValue},
			want:  []dto.UUID{fixture.virtual.ID}, total: 1,
		},
		{
			name:  "missing aliases virtual item",
			query: store.ItemQuery{IsMissing: &trueValue},
			want:  []dto.UUID{fixture.virtual.ID}, total: 1,
		},
		{
			name:  "explicit virtual item filter overrides missing alias",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, IsVirtualItem: &falseValue, IsMissing: &trueValue},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID}, total: 2,
		},
		{
			name:  "unaired derives from future premiere relative to request reference",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, IsUnaired: &trueValue, ReferenceTime: &unairedReference},
			want:  []dto.UUID{fixture.virtual.ID}, total: 1,
		},
		{
			name:  "aired derives from historical premiere relative to request reference",
			query: store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie}, IsUnaired: &falseValue, ReferenceTime: &unairedReference},
			want:  []dto.UUID{fixture.movie.ID, fixture.foreign.ID}, total: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
			query.SortOrder = []store.SortOrder{store.SortOrderAscending}
			query.EnableTotalRecordCount = true
			got, err := fixture.items.Query(t.Context(), query)
			if err != nil {
				t.Fatalf("querying fixture: %v", err)
			}
			if ids := filterQueryIDs(got.Items); !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("item IDs = %v, want %v", ids, tc.want)
			}
			if got.TotalRecordCount != tc.total {
				t.Fatalf("total record count = %d, want %d before pagination", got.TotalRecordCount, tc.total)
			}
		})
	}
}

type filterQueryFixture struct {
	items       *store.ItemStore
	root        item.Item
	branch      item.Item
	foreignRoot item.Item
	movie       item.Item
	virtual     item.Item
	foreign     item.Item
	audio       item.Item
	userID      dto.UUID
}

func newFilterQueryFixture(t *testing.T) filterQueryFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite filter fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite filter fixture: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID := filterQueryUUID(t, 102)
	root := filterQueryItem(t, 1, item.KindFolder, nil, "root")
	root.IsFolder = true
	branch := filterQueryItem(t, 2, item.KindFolder, &root.ID, "branch")
	branch.IsFolder = true
	foreignRoot := filterQueryItem(t, 3, item.KindFolder, nil, "foreign root")
	foreignRoot.IsFolder = true

	movie := filterQueryItem(t, 10, item.KindMovie, &branch.ID, "cafe feature")
	movie.Name = filterQueryString("Café Feature")
	movie.MediaType = filterQueryMediaType(item.MediaTypeVideo)
	movie.ProductionYear = filterQueryIntValue(2024)
	movie.PremiereDate = filterQueryTime(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	movie.CommunityRating = filterQueryFloat32(8.5)
	movie.RunTimeTicks = filterQueryInt64(4_000)
	movie.Values = item.ItemValues{Genres: []string{"Drama"}, Studios: []string{"North Star"}, Tags: []string{"Featured"}}
	movie.ProviderIDs = filterQueryProviderIDs("Tmdb", "603", "Imdb", "tt0084787")
	movie.DateLastSaved = filterQueryTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	virtual := filterQueryItem(t, 11, item.KindMovie, &branch.ID, "future feature")
	virtual.MediaType = filterQueryMediaType(item.MediaTypeVideo)
	virtual.IsVirtualItem = true
	virtual.ProductionYear = filterQueryIntValue(2025)
	virtual.PremiereDate = filterQueryTime(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	virtual.CommunityRating = filterQueryFloat32(5)
	virtual.RunTimeTicks = filterQueryInt64(8_000)
	virtual.Values = item.ItemValues{Genres: []string{"Action"}, Studios: []string{"Other Studio"}, Tags: []string{"Blocked", "Drama", "North Star"}}
	virtual.ProviderIDs = filterQueryProviderIDs("Tmdb", "999")
	virtual.DateLastSaved = filterQueryTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	audio := filterQueryItem(t, 12, item.KindAudio, &branch.ID, "audio feature")
	audio.MediaType = filterQueryMediaType(item.MediaTypeAudio)
	audio.PremiereDate = filterQueryTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	foreign := filterQueryItem(t, 13, item.KindMovie, &foreignRoot.ID, "foreign feature")
	foreign.MediaType = filterQueryMediaType(item.MediaTypeVideo)
	foreign.PremiereDate = filterQueryTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))

	for _, it := range []item.Item{root, branch, foreignRoot, movie, virtual, audio, foreign} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting filter fixture item %s: %v", it.ID, err)
		}
	}
	return filterQueryFixture{items: items, root: root, branch: branch, foreignRoot: foreignRoot, movie: movie, virtual: virtual, foreign: foreign, audio: audio, userID: userID}
}

func filterQueryItem(t *testing.T, value int, kind item.Kind, parentID *dto.UUID, sortName string) item.Item {
	t.Helper()
	return item.Item{ID: filterQueryUUID(t, value), Kind: kind, ParentID: parentID, Name: filterQueryString(sortName), SortName: filterQueryString(sortName)}
}

func filterQueryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func filterQueryProviderIDs(entries ...string) item.ProviderIDs {
	var providers item.ProviderIDs
	for index := 0; index < len(entries); index += 2 {
		providers.Set(entries[index], entries[index+1])
	}
	return providers
}

func filterQueryIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}

func filterQueryString(value string) *string                    { return &value }
func filterQueryInt(value int) *int                             { return &value }
func filterQueryIntValue(value int) *int                        { return &value }
func filterQueryInt64(value int64) *int64                       { return &value }
func filterQueryFloat32(value float32) *float32                 { return &value }
func filterQueryTime(value time.Time) *time.Time                { return &value }
func filterQueryMediaType(value item.MediaType) *item.MediaType { return &value }
