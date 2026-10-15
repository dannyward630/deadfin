// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestAnimeTitleSearch defends anime title search: a series carries an
// AniList id, its stored AniList titles make it searchable by every title,
// and the kana and romaji folding of anime.FoldTitle applies to stored
// titles and terms. A series without anime titles keeps the ordinary name
// search only.
func TestAnimeTitleSearch(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	animeStore := store.NewAnimeStore(stores)

	root := filterQueryItem(t, 1, item.KindFolder, nil, "root")
	root.IsFolder = true
	titan := filterQueryItem(t, 10, item.KindSeries, &root.ID, "Attack on Titan")
	titan.IsFolder = true
	titan.ProviderIDs = filterQueryProviderIDs("AniList", "16498", "Tvdb", "267440")
	other := filterQueryItem(t, 11, item.KindSeries, &root.ID, "Titan Games")
	other.IsFolder = true
	other.ProviderIDs = filterQueryProviderIDs("Tvdb", "1")
	for _, it := range []item.Item{root, titan, other} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert %s: %v", it.ID, err)
		}
	}
	if err := animeStore.SaveMedia(t.Context(), store.AnimeMedia{
		AniListID: 16498, AniDBID: 9541, MALID: 16498, Format: "TV", Season: "SPRING", SeasonYear: 2013, Episodes: 25,
		Title: "Attack on Titan", DateFetched: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		TitleSets: []store.AnimeTitleSet{{Provider: "AniList", ExternalID: "16498", Titles: []store.AnimeTitle{
			{Title: "Attack on Titan", Language: "en", Kind: "english"},
			{Title: "Shingeki no Kyojin", Language: "x-jat", Kind: "romaji"},
			{Title: "進撃の巨人", Language: "ja", Kind: "native"},
			{Title: "シンゲキノキョジン", Language: "ja", Kind: "kana"},
			{Title: "Shingeki no Kyojin", Language: "x-jat", Kind: "duplicate"},
		}}},
	}); err != nil {
		t.Fatalf("SaveMedia: %v", err)
	}

	for _, tc := range []struct {
		term string
		want []dto.UUID
	}{
		{"Attack on Titan", []dto.UUID{titan.ID}},
		{"Shingeki", []dto.UUID{titan.ID}},
		{"shingeki no", []dto.UUID{titan.ID}},
		{"ShingekiNoKyojin", []dto.UUID{titan.ID}},
		{"進撃", []dto.UUID{titan.ID}},
		{"しんげき", []dto.UUID{titan.ID}},
		{"Kyoujin", []dto.UUID{titan.ID}},
		{"Ｓｈｉｎｇｅｋｉ", []dto.UUID{titan.ID}},
		{"Titan", []dto.UUID{other.ID, titan.ID}}, // search relevance: a name prefix ranks first,
		{"Games", []dto.UUID{other.ID}},
		{"Gundam", []dto.UUID{}},
	} {
		t.Run(tc.term, func(t *testing.T) {
			got, err := items.Query(t.Context(), store.ItemQuery{
				SearchTerm:       tc.term,
				IncludeItemTypes: []item.Kind{item.KindSeries},
				SortBy:           []store.ItemSortBy{store.ItemSortBySortName},
				SortOrder:        []store.SortOrder{store.SortOrderAscending},
			})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if ids := filterQueryIDs(got.Items); !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("search %q = %v, want %v", tc.term, ids, tc.want)
			}
		})
	}

	titles, err := animeStore.Titles(t.Context(), "AniList", "16498")
	if err != nil {
		t.Fatalf("Titles: %v", err)
	}
	if len(titles) != 4 || titles[0].Title != "Attack on Titan" || titles[2].Language != "ja" {
		t.Fatalf("stored titles = %+v, want four distinct titles in order", titles)
	}
}

// TestAnimeMediaAndSeasonMapRoundTrip stores the relation graph and a
// split-cour season map and reads them back.
func TestAnimeMediaAndSeasonMapRoundTrip(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	animeStore := store.NewAnimeStore(stores)
	fetched := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	media := store.AnimeMedia{
		AniListID: 99147, AniDBID: 13241, Format: "TV", Status: "FINISHED", Season: "SUMMER", SeasonYear: 2018, Episodes: 12,
		Title: "Attack on Titan Season 3", DateFetched: fetched,
		Relations: []store.AnimeRelation{
			{RelationType: "SEQUEL", TargetAniListID: 104578, TargetType: "ANIME", TargetFormat: "TV", TargetTitle: "Shingeki no Kyojin 3 Part 2"},
			{RelationType: "PREQUEL", TargetAniListID: 20958, TargetType: "ANIME", TargetFormat: "TV", TargetTitle: "Shingeki no Kyojin 2"},
			{RelationType: "PREQUEL", TargetAniListID: 20958, TargetType: "ANIME", TargetFormat: "TV", TargetTitle: "duplicate edge"},
		},
	}
	if err := animeStore.SaveMedia(t.Context(), media); err != nil {
		t.Fatalf("SaveMedia: %v", err)
	}
	// A second save replaces the edges instead of adding to them.
	media.Relations = media.Relations[:2]
	if err := animeStore.SaveMedia(t.Context(), media); err != nil {
		t.Fatalf("SaveMedia again: %v", err)
	}
	got, err := animeStore.Media(t.Context(), 99147)
	if err != nil {
		t.Fatalf("Media: %v", err)
	}
	if got.AniDBID != 13241 || got.MALID != 0 || got.SeasonYear != 2018 || !got.DateFetched.Equal(fetched) || len(got.Relations) != 2 ||
		got.Relations[0].RelationType != "PREQUEL" || got.Relations[1].TargetAniListID != 104578 {
		t.Fatalf("Media = %+v", got)
	}

	seasons := map[int][]store.AnimeSegment{
		1: {{AniDBID: 9541, AniListID: 16498, MALID: 16498, Name: "Shingeki no Kyojin"}},
		3: {
			{AniDBID: 13241, AniListID: 99147, MALID: 35760, Name: "Shingeki no Kyojin Season 3"},
			{AniDBID: 14444, AniListID: 104578, MALID: 38524, EpisodeOffset: 12, Name: "Shingeki no Kyojin Season 3 (2019)"},
		},
	}
	if err := animeStore.ReplaceSeasonMap(t.Context(), store.AnimeNumberingTVDB, 267440, seasons); err != nil {
		t.Fatalf("ReplaceSeasonMap: %v", err)
	}
	// The same series under TMDb numbering is a separate map.
	tmdbSeasons := map[int][]store.AnimeSegment{3: {{AniDBID: 13241, AniListID: 99147, Name: "Shingeki no Kyojin Season 3"}}}
	if err := animeStore.ReplaceSeasonMap(t.Context(), store.AnimeNumberingTMDb, 1429, tmdbSeasons); err != nil {
		t.Fatalf("ReplaceSeasonMap tmdb: %v", err)
	}
	stored, err := animeStore.SeasonMap(t.Context(), store.AnimeNumberingTVDB, 267440)
	if err != nil {
		t.Fatalf("SeasonMap: %v", err)
	}
	if !reflect.DeepEqual(stored, seasons) {
		t.Fatalf("SeasonMap = %+v, want %+v", stored, seasons)
	}
	if stored, err := animeStore.SeasonMap(t.Context(), store.AnimeNumberingTMDb, 1429); err != nil || !reflect.DeepEqual(stored, tmdbSeasons) {
		t.Fatalf("TMDb SeasonMap = %+v, %v", stored, err)
	}
}
