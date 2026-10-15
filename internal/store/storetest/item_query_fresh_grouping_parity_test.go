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

// TestItemStoreQueryFreshGroupingParitySQLite executes the captured and
// relation-backed grouping contracts at the public ItemStore seam.
func TestItemStoreQueryFreshGroupingParitySQLite(t *testing.T) {
	runItemStoreQueryFreshGroupingParity(t, openItemStore)
}

// TestItemStoreQueryFreshGroupingParityPostgres executes the same contracts
// against a real PostgreSQL store when DEADFIN_TEST_POSTGRES_DSN is available.
func TestItemStoreQueryFreshGroupingParityPostgres(t *testing.T) {
	runItemStoreQueryFreshGroupingParity(t, openPostgresItemStore)
}

func runItemStoreQueryFreshGroupingParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	ids := seedFreshGroupingCapturedFixture(t, items)

	t.Run("public CollectionFolder recursively applies media filters before count and page", func(t *testing.T) {
		fixture := newFreshCollectionFolderFixture(t, items)
		page := 0
		limit := 1
		fixture.assertQuery(t, store.ItemQuery{
			ParentID:               &fixture.root.ID,
			Recursive:              true,
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			MediaTypes:             []item.MediaType{item.MediaTypeVideo},
			SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending},
			StartIndex:             &page,
			Limit:                  &limit,
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.video.ID}, 1)
		fixture.assertQuery(t, store.ItemQuery{
			ParentID:               &fixture.root.ID,
			Recursive:              true,
			IncludeItemTypes:       []item.Kind{item.KindPhoto},
			MediaTypes:             []item.MediaType{item.MediaTypePhoto},
			SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.photo.ID}, 1)
	})

	t.Run("Series and MusicAlbum child pages retain captured ordering and total before offset", func(t *testing.T) {
		series := freshGroupingFixtureID(t, ids, "Series:/media/tv/Synthetic Show (2024)")
		seasonOne := freshGroupingFixtureID(t, ids, "Season:/media/tv/Synthetic Show (2024)/Season 01")
		seasonTwo := freshGroupingFixtureID(t, ids, "Season:/media/tv/Synthetic Show (2024)/Season 02")
		album := freshGroupingFixtureID(t, ids, "MusicAlbum:/media/music/Goldenrec Generator/Synthetic Album")
		track := func(number int) dto.UUID {
			return freshGroupingFixtureID(t, ids, fmt.Sprintf("Audio:/media/music/Goldenrec Generator/Synthetic Album/%d - Synthetic Track %d.mp3", number, number))
		}
		start, limit := 1, 2
		freshGroupingAssertQuery(t, items, store.ItemQuery{
			ParentID:               &series,
			IncludeItemTypes:       []item.Kind{item.KindSeason},
			SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending},
			StartIndex:             &start,
			Limit:                  &limit,
			EnableTotalRecordCount: true,
		}, []dto.UUID{seasonOne, seasonTwo}, 3)

		start, limit = 3, 3
		freshGroupingAssertQuery(t, items, store.ItemQuery{
			ParentID:               &album,
			IncludeItemTypes:       []item.Kind{item.KindAudio},
			SortBy:                 []store.ItemSortBy{store.ItemSortByIndexNumber, store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending},
			StartIndex:             &start,
			Limit:                  &limit,
			EnableTotalRecordCount: true,
		}, []dto.UUID{track(4), track(5), track(6)}, 11)
	})

	t.Run("AdjacentTo captured episode target selects neighbors before count and offset", func(t *testing.T) {
		season := freshGroupingFixtureID(t, ids, "Season:/media/tv/Synthetic Show (2024)/Season 01")
		first := freshGroupingFixtureID(t, ids, "Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E01 - First Signal.mkv")
		second := freshGroupingFixtureID(t, ids, "Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E02 - Second Signal.mkv")
		third := freshGroupingFixtureID(t, ids, "Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E03 - Third Signal.mkv")
		freshGroupingAssertQuery(t, items, store.ItemQuery{
			ParentID:               &season,
			Recursive:              true,
			AdjacentTo:             &second,
			IncludeItemTypes:       []item.Kind{item.KindEpisode},
			SortBy:                 []store.ItemSortBy{store.ItemSortByParentIndexNumber, store.ItemSortByIndexNumber, store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending, store.SortOrderAscending},
			EnableTotalRecordCount: true,
		}, []dto.UUID{first, second, third}, 3)
		start, limit := 1, 1
		freshGroupingAssertQuery(t, items, store.ItemQuery{
			ParentID:               &season,
			Recursive:              true,
			AdjacentTo:             &second,
			IncludeItemTypes:       []item.Kind{item.KindEpisode},
			SortBy:                 []store.ItemSortBy{store.ItemSortByParentIndexNumber, store.ItemSortByIndexNumber, store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending, store.SortOrderAscending},
			StartIndex:             &start,
			Limit:                  &limit,
			EnableTotalRecordCount: true,
		}, []dto.UUID{second}, 3)
	})

	t.Run("captured scanner BoxSet relation collapses eleven movies to nine before each page", func(t *testing.T) {
		movies := freshGroupingFixtureID(t, ids, "Folder:/media/movies")
		disc := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Synthetic Disc.bluray.disc")
		cafe := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Odd Names/Café déjà-vu (2020).mkv")
		boxSet := freshGroupingFixtureID(t, ids, "BoxSet:/media/movies/Synthetic Collection [boxset]")
		odd := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Odd Names/The [Odd] Film (2021) {edition-Director's Cut}.mkv")
		film := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Odd Names/Film.Name.2022.1080p.BluRay.mkv")
		standalone := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv")
		hlg := freshGroupingFixtureID(t, ids, "Movie:/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv")
		subtitle := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv")
		feature := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv")
		collectionOne := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv")
		collectionTwo := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv")
		collectionThree := freshGroupingFixtureID(t, ids, "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv")

		page := func(start int, want []dto.UUID) {
			t.Helper()
			limit := 3
			freshGroupingAssertQuery(t, items, freshGroupingMovieCollapseQuery(movies, &start, &limit), want, 9)
		}
		page(0, []dto.UUID{disc, cafe, boxSet})
		page(3, []dto.UUID{odd, film, standalone})
		page(6, []dto.UUID{hlg, subtitle, feature})

		// Filters select child records first. The captured BoxSet itself has 2020,
		// so the year relation still resolves to the single collapsed collection.
		freshGroupingAssertQuery(t, items, freshGroupingMovieCollapseQuery(movies, nil, nil, func(query *store.ItemQuery) {
			query.Years = []int{2020}
		}), []dto.UUID{cafe, boxSet}, 2)

		// Name ranges are deferred until after child-to-BoxSet replacement; the
		// child names match this prefix but the collection's sort name does not.
		freshGroupingAssertQuery(t, items, freshGroupingMovieCollapseQuery(movies, nil, nil, func(query *store.ItemQuery) {
			query.NameStartsWith = "collection"
		}), []dto.UUID{}, 0)

		// The public Items route disables collapse for search. Matching captured
		// BoxSet children therefore remain individually visible rather than being
		// post-page filtered or replaced by a non-matching parent.
		freshGroupingAssertQuery(t, items, freshGroupingMovieCollapseQuery(movies, nil, nil, func(query *store.ItemQuery) {
			collapsed := false
			query.CollapseBoxSetItems = &collapsed
			query.SearchTerm = "collection"
		}), []dto.UUID{collectionOne, collectionTwo, collectionThree}, 3)
	})
}

func seedFreshGroupingCapturedFixture(t *testing.T, items *store.ItemStore) map[string]dto.UUID {
	t.Helper()
	_, root := loadItemQueryStockCorpus(t)
	var fixture itemQueryStockFixture
	decodeItemQueryStockJSON(t, root+"/itemquery-fixtures.json", &fixture)
	return seedItemQueryStockFixture(t, items, fixture)
}

type freshCollectionFolderFixture struct {
	items              *store.ItemStore
	root, video, photo item.Item
}

func newFreshCollectionFolderFixture(t *testing.T, items *store.ItemStore) freshCollectionFolderFixture {
	t.Helper()
	aggregate := freshGroupingItem(t, 900000, item.KindAggregateFolder, nil, "aggregate root")
	aggregate.IsFolder = true
	root := freshGroupingItem(t, 900001, item.KindCollectionFolder, &aggregate.ID, "public collection")
	root.IsFolder = true
	folder := freshGroupingItem(t, 900002, item.KindFolder, &root.ID, "nested folder")
	folder.IsFolder = true
	video := freshGroupingItem(t, 900003, item.KindMovie, &folder.ID, "video movie")
	video.MediaType = freshGroupingMediaType(item.MediaTypeVideo)
	photo := freshGroupingItem(t, 900004, item.KindPhoto, &folder.ID, "photo image")
	photo.MediaType = freshGroupingMediaType(item.MediaTypePhoto)
	audio := freshGroupingItem(t, 900005, item.KindAudio, &folder.ID, "audio track")
	audio.MediaType = freshGroupingMediaType(item.MediaTypeAudio)
	for _, value := range []item.Item{aggregate, root, folder, video, photo, audio} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting CollectionFolder fixture %s: %v", value.ID, err)
		}
	}
	return freshCollectionFolderFixture{items: items, root: root, video: video, photo: photo}
}

func (fixture freshCollectionFolderFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	freshGroupingAssertQuery(t, fixture.items, query, want, total)
}

func freshGroupingMovieCollapseQuery(parent dto.UUID, start, limit *int, mutate ...func(*store.ItemQuery)) store.ItemQuery {
	collapse := true
	query := store.ItemQuery{
		ParentID:               &parent,
		Recursive:              true,
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		CollapseBoxSetItems:    &collapse,
		SortBy:                 []store.ItemSortBy{store.ItemSortByProductionYear, store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending},
		StartIndex:             start,
		Limit:                  limit,
		EnableTotalRecordCount: true,
	}
	for _, apply := range mutate {
		apply(&query)
	}
	return query
}

func freshGroupingAssertQuery(t *testing.T, items *store.ItemStore, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	got, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("ItemStore.Query(%#v): %v", query, err)
	}
	actual := make([]dto.UUID, len(got.Items))
	for index, value := range got.Items {
		actual[index] = value.ID
	}
	if !reflect.DeepEqual(actual, want) || got.TotalRecordCount != total {
		t.Fatalf("query IDs/count = %v/%d, want %v/%d", actual, got.TotalRecordCount, want, total)
	}
}

func freshGroupingFixtureID(t *testing.T, ids map[string]dto.UUID, identity string) dto.UUID {
	t.Helper()
	id, ok := ids[identity]
	if !ok {
		t.Fatalf("captured fixture omitted %q", identity)
	}
	return id
}

func freshGroupingItem(t *testing.T, value int, kind item.Kind, parent *dto.UUID, name string) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return item.Item{ID: id, Kind: kind, ParentID: parent, Name: &name, SortName: &name}
}

func freshGroupingMediaType(value item.MediaType) *item.MediaType { return &value }
