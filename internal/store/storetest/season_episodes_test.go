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
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreSeasonEpisodesSQLite proves the EpisodesOfSeasonID scope, the
// Season ParentId rewrite, and the season counts against SQLite.
func TestItemStoreSeasonEpisodesSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runItemStoreSeasonEpisodes(t, stores)
}

// TestItemStoreSeasonEpisodesPostgres runs the same cases against PostgreSQL
// when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreSeasonEpisodesPostgres(t *testing.T) {
	runItemStoreSeasonEpisodes(t, storetestpkg.OpenPostgres(t))
}

// runItemStoreSeasonEpisodes ports the cases of Series.FilterEpisodesBySeason
// (jellyfin@b04614e Series.cs). Season 2 is a folder that holds one episode;
// the other season 2 episodes are files in the series root.
func runItemStoreSeasonEpisodes(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	id := func(value int) dto.UUID {
		t.Helper()
		parsed, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
		if err != nil {
			t.Fatalf("parse ID %d: %v", value, err)
		}
		return parsed
	}
	intp := func(value int) *int { return &value }
	named := func(value int, kind item.Kind, parent dto.UUID, name string) item.Item {
		return item.Item{ID: id(value), Kind: kind, ParentID: &parent, Name: &name, SortName: &name, IsFolder: kind != item.KindEpisode}
	}
	library := id(1)
	libraryItem := item.Item{ID: library, Kind: item.KindCollectionFolder, IsFolder: true}
	series, otherSeries := named(2, item.KindSeries, library, "series"), named(3, item.KindSeries, library, "other series")
	season := func(value int, path *string, number *int, name string) item.Item {
		s := named(value, item.KindSeason, series.ID, name)
		s.SeriesID, s.Path, s.IndexNumber = &series.ID, path, number
		return s
	}
	seasonTwoPath, seasonTwoAgainPath := "/tv/series/Season 2", "/tv/series/Season 02"
	seasonTwo := season(10, &seasonTwoPath, intp(2), "Season 2")
	seasonTwoAgain := season(20, &seasonTwoAgainPath, intp(2), "Season 02")
	unnumbered := season(30, nil, nil, "Season Unknown")
	episode := func(value int, parent dto.UUID, seriesID dto.UUID, seasonID *dto.UUID, seasonNumber *int, name string) item.Item {
		e := named(value, item.KindEpisode, parent, name)
		e.SeriesID, e.SeasonID, e.ParentIndexNumber = &seriesID, seasonID, seasonNumber
		return e
	}
	inFolder := episode(11, seasonTwo.ID, series.ID, &seasonTwo.ID, intp(2), "002 - 0025")
	rootLinked := episode(12, series.ID, series.ID, &seasonTwo.ID, intp(2), "002 - 0001")
	rootNumbered := episode(13, series.ID, series.ID, nil, intp(2), "002 - 0002")
	special := episode(14, series.ID, series.ID, nil, intp(0), "000 - 0001")
	special.AirsBeforeSeasonNumber = intp(2)
	seasonThree := episode(15, series.ID, series.ID, nil, intp(3), "003 - 0001")
	sameKey := episode(22, seasonTwoAgain.ID, series.ID, &seasonTwoAgain.ID, nil, "002 - 0003")
	loose := episode(31, series.ID, series.ID, nil, nil, "loose")
	otherSeriesEpisode := episode(41, otherSeries.ID, otherSeries.ID, nil, intp(2), "002 - 0001 other")
	for _, value := range []item.Item{libraryItem, series, otherSeries, seasonTwo, seasonTwoAgain, unnumbered, inFolder, rootLinked, rootNumbered, special, seasonThree, sameKey, loose, otherSeriesEpisode} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	user := id(99)
	sorted := func(query store.ItemQuery) store.ItemQuery {
		query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
		query.SortOrder = []store.SortOrder{store.SortOrderAscending}
		query.EnableTotalRecordCount = true
		return query
	}
	seasonTwoEpisodes := []dto.UUID{special.ID, rootLinked.ID, rootNumbered.ID, sameKey.ID, inFolder.ID}
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{name: "season scope takes linked, numbered, same-key and aired-before episodes", query: store.ItemQuery{EpisodesOfSeasonID: &seasonTwo.ID}, want: seasonTwoEpisodes},
		{name: "season scope without specials within seasons takes linked and same-key episodes", query: store.ItemQuery{EpisodesOfSeasonID: &seasonTwo.ID, ExcludeSpecialsFromSeasons: true}, want: []dto.UUID{rootLinked.ID, sameKey.ID, inFolder.ID}},
		{name: "unnumbered virtual season takes unnumbered episodes without a season", query: store.ItemQuery{EpisodesOfSeasonID: &unnumbered.ID}, want: []dto.UUID{loose.ID}},
		{name: "user parent query of a season lists its episodes", query: store.ItemQuery{ParentID: &seasonTwo.ID, UserID: &user}, want: seasonTwoEpisodes},
		{name: "recursive user parent query of a season lists its episodes", query: store.ItemQuery{ParentID: &seasonTwo.ID, UserID: &user, Recursive: true, IncludeItemTypes: []item.Kind{item.KindEpisode}}, want: seasonTwoEpisodes},
		{name: "parent query without a user keeps the folder children", query: store.ItemQuery{ParentID: &seasonTwo.ID}, want: []dto.UUID{inFolder.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := items.TranslateQuery(t.Context(), sorted(tc.query))
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			// The season scope reads the season by key and its series'
			// episodes through ix_items_series, never the item table.
			assertScopedPlanAvoidsFullScans(t, stores, translated.SQL, translated.Args, []string{"items"})
			got, err := items.Query(t.Context(), sorted(tc.query))
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			ids := make([]dto.UUID, len(got.Items))
			for index, value := range got.Items {
				ids[index] = value.ID
			}
			if !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != len(tc.want) {
				t.Fatalf("IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, len(tc.want))
			}
		})
	}

	projections, err := items.DTOProjections(t.Context(), []dto.UUID{seasonTwo.ID, seasonTwoAgain.ID, unnumbered.ID, series.ID}, nil, false, false, true, true, nil)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	for _, tc := range []struct {
		id         dto.UUID
		child, all int
	}{
		{id: seasonTwo.ID, child: 5, all: 5},
		{id: seasonTwoAgain.ID, child: 5, all: 5},
		{id: unnumbered.ID, child: 1, all: 1},
	} {
		if got := projections.ChildCounts[tc.id]; got != tc.child {
			t.Fatalf("ChildCounts[%s] = %d, want %d", tc.id, got, tc.child)
		}
		if got := projections.RecursiveCounts[tc.id]; got != tc.all {
			t.Fatalf("RecursiveCounts[%s] = %d, want %d", tc.id, got, tc.all)
		}
	}
	// A series keeps its folder counts.
	if projections.ChildCounts[series.ID] == 0 {
		t.Fatalf("series ChildCount = 0, want its folder children")
	}
}
