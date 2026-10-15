// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// LatestItems groups before it applies the limit, as
// BaseItemRepository.GetLatestItemList does. The fixture puts the newest 40
// episodes in 4 series, so a limit*2 candidate page grouped afterwards finds
// only 4 groups.

func TestItemLatestGroupedSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertLatestGrouped(t, stores)
}

func TestItemLatestGroupedPostgres(t *testing.T) {
	assertLatestGrouped(t, storetestpkg.OpenPostgres(t))
}

type latestGroupedFixture struct {
	shows, movies, music, plain dto.UUID
	series                      []dto.UUID // newest first
	movieChoices                []dto.UUID // newest first, one per key
	albums                      []dto.UUID // newest first
}

func newLatestGroupedFixture(t *testing.T, items *store.ItemStore) latestGroupedFixture {
	t.Helper()
	ctx := t.Context()
	next := 100_000
	id := func() dto.UUID { next++; return itemQueryPlanUUID(t, next) }
	name := itemQueryPlanString
	intp := func(n int) *int { return &n }
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) *time.Time { value := base.Add(time.Duration(minutes) * time.Minute); return &value }
	library := func(kind item.CollectionType) *item.CollectionType { return &kind }

	aggregate := id()
	f := latestGroupedFixture{shows: id(), movies: id(), music: id(), plain: id()}
	values := []item.Item{
		{ID: aggregate, Kind: item.KindAggregateFolder, Name: name("root")},
		{ID: f.shows, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Shows"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: library(item.CollectionTypeTVShows)}}},
		{ID: f.movies, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Movies"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: library(item.CollectionTypeMovies)}}},
		{ID: f.music, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Music"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: library(item.CollectionTypeMusic)}}},
		{ID: f.plain, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Mixed"), IsFolder: true},
	}

	// 20 series with one season each. Series 0-3 have 10 episodes each,
	// newer than every episode of series 4-19, which have 2 each. Series i
	// is newer than series i+1.
	for index := 0; index < 20; index++ {
		seriesID, seasonID := id(), id()
		seriesName := "Series " + string(rune('A'+index))
		key := "series-" + string(rune('a'+index))
		f.series = append(f.series, seriesID)
		values = append(values,
			item.Item{ID: seriesID, Kind: item.KindSeries, ParentID: &f.shows, Name: name(seriesName), IsFolder: true, PresentationUniqueKey: &key, DateCreated: at(0)},
			item.Item{ID: seasonID, Kind: item.KindSeason, ParentID: &seriesID, Name: name("Season 1"), IsFolder: true, IndexNumber: intp(1),
				SeriesID: &seriesID, SeriesName: name(seriesName), DateCreated: at(0)},
		)
		episodes, newest := 2, 10_000-index*100
		if index < 4 {
			episodes, newest = 10, 20_000-index*100
		}
		for episode := 0; episode < episodes; episode++ {
			values = append(values, item.Item{ID: id(), Kind: item.KindEpisode, ParentID: &seasonID, Name: name(seriesName + " episode"),
				IndexNumber: intp(episode + 1), ParentIndexNumber: intp(1), SeriesID: &seriesID, SeasonID: &seasonID,
				SeriesName: name(seriesName), SeriesPresentationUniqueKey: &key, DateCreated: at(newest - episode)})
		}
	}

	// 8 movies; the two newest share a key, so the newest of them stands for
	// both.
	movieKeys := []string{"m0", "m0", "m1", "m2", "m3", "m4", "m5", "m6"}
	for index, key := range movieKeys {
		movieID := id()
		values = append(values, item.Item{ID: movieID, Kind: item.KindMovie, ParentID: &f.movies, Name: name("Movie " + key),
			PresentationUniqueKey: &key, DateCreated: at(5_000 - index)})
		if index != 1 {
			f.movieChoices = append(f.movieChoices, movieID)
		}
	}

	// 4 albums with 3 tracks each; album i is newer than album i+1.
	for index := 0; index < 4; index++ {
		albumID := id()
		f.albums = append(f.albums, albumID)
		values = append(values, item.Item{ID: albumID, Kind: item.KindMusicAlbum, ParentID: &f.music, Name: name("Album"), IsFolder: true, DateCreated: at(3_000 - index)})
		for track := 0; track < 3; track++ {
			values = append(values, item.Item{ID: id(), Kind: item.KindAudio, ParentID: &albumID, Name: name("Track"), IndexNumber: intp(track + 1), DateCreated: at(3_000 - index)})
		}
	}
	values = append(values, item.Item{ID: id(), Kind: item.KindMovie, ParentID: &f.plain, Name: name("Mixed movie"), DateCreated: at(1)})

	for _, value := range values {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	return f
}

func assertLatestGrouped(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	f := newLatestGroupedFixture(t, items)
	notFolder, notVirtual := false, false
	query := func(parent dto.UUID, limit int) store.ItemQuery {
		return store.ItemQuery{ParentID: &parent, Recursive: true, IsFolder: &notFolder, IsVirtualItem: &notVirtual, Limit: &limit,
			OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByDateCreated, SortOrder: store.SortOrderDescending}}}
	}
	for _, test := range []struct {
		name   string
		parent dto.UUID
		limit  int
		want   []dto.UUID
	}{
		{name: "tvshows limit 16 over 20 series", parent: f.shows, limit: 16, want: f.series[:16]},
		{name: "tvshows limit above the series count", parent: f.shows, limit: 30, want: f.series},
		{name: "movies group by presentation key", parent: f.movies, limit: 5, want: f.movieChoices[:5]},
		{name: "music returns ancestor albums", parent: f.music, limit: 3, want: f.albums[:3]},
	} {
		t.Run(test.name, func(t *testing.T) {
			values, grouped, err := items.LatestItems(t.Context(), query(test.parent, test.limit))
			if err != nil {
				t.Fatalf("LatestItems: %v", err)
			}
			if !grouped {
				t.Fatal("LatestItems grouped = false, want true")
			}
			got := make([]dto.UUID, len(values))
			for index, value := range values {
				got[index] = value.ID
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("LatestItems ids = %v, want %v", got, test.want)
			}
		})
	}
	t.Run("other collection types fall back", func(t *testing.T) {
		values, grouped, err := items.LatestItems(t.Context(), query(f.plain, 16))
		if err != nil || grouped || values != nil {
			t.Fatalf("LatestItems = %v, %v, %v; want nil, false, nil", values, grouped, err)
		}
	})
}

// TestItemLatestGroupedReadsFilteredRowsOnce pins the statement shape of the
// tvshows path: one statement reads the analysis columns straight from the
// filtered relation. Two statements that each looked the filtered ids up in
// items doubled the cost of a large library (Latest Shows: ~6,400 episodes).
func TestItemLatestGroupedReadsFilteredRowsOnce(t *testing.T) {
	recorder := &nextUpStatementRecorder{}
	items := openNextUpStatementRecordingSQLite(t, recorder)
	f := newLatestGroupedFixture(t, items)
	notFolder, notVirtual := false, false
	limit := 16
	values, grouped, err := items.LatestItems(t.Context(), store.ItemQuery{ParentID: &f.shows, Recursive: true, IsFolder: &notFolder, IsVirtualItem: &notVirtual, Limit: &limit})
	if err != nil || !grouped || len(values) != 16 {
		t.Fatalf("LatestItems = %d items, grouped %v, %v; want 16, true, nil", len(values), grouped, err)
	}
	reads := 0
	for _, statement := range recorder.snapshot() {
		if strings.Contains(statement.query, "id IN (WITH") {
			t.Fatalf("LatestItems looks the filtered ids up again: %s", statement.query)
		}
		if strings.Contains(statement.query, "series_name IS NOT NULL") {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("LatestItems read the filtered series rows %d times, want 1", reads)
	}
}
