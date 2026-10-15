// SPDX-License-Identifier: GPL-2.0-only

package anime_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/anime"
)

// The testdata files are trimmed copies of the real seed datasets (manami
// release 2026-27, AniDB titles of 2026-10-10, Anime-Lists master of
// 2026-10-10). They hold the Attack on Titan (TVDB 267440), Spy x Family
// (TVDB 405920), One Piece (TVDB 81797), and Frieren (TVDB 424536) entries.
func loadFixture(t *testing.T) *anime.Crosswalk {
	t.Helper()
	crosswalk, found, err := anime.Load(t.Context(), "testdata", anime.DefaultSources())
	if err != nil || !found {
		t.Fatalf("Load(testdata) = found %v, error %v", found, err)
	}
	return crosswalk
}

func TestCrosswalkIdentifiesByTVDB(t *testing.T) {
	crosswalk := loadFixture(t)
	identity, found := crosswalk.Identify(anime.Query{TVDB: 267440})
	if !found {
		t.Fatal("TVDB 267440 not identified")
	}
	if identity.Method != anime.MethodTVDB || identity.Anime.AniDB != 9541 || identity.Anime.AniList != 16498 || identity.Anime.MAL != 16498 || identity.Anime.Kitsu != 7442 {
		t.Fatalf("identity = %+v, want the season 1 entry (AniDB 9541, AniList 16498)", identity)
	}
	if identity.TVDB != 267440 || identity.TMDb != 1429 {
		t.Fatalf("identity series ids = TVDB %d TMDb %d, want 267440 and 1429", identity.TVDB, identity.TMDb)
	}
}

// TestCrosswalkSplitCourSeasons covers the multi-season mapping: Attack on
// Titan's TVDB season 3 is two AniDB entries (and two AniList entries), and
// season 4 is three.
func TestCrosswalkSplitCourSeasons(t *testing.T) {
	crosswalk := loadFixture(t)
	for _, tc := range []struct {
		tvdb, season int
		want         []anime.Segment
	}{
		{267440, 1, []anime.Segment{{AniDB: 9541, AniList: 16498, MAL: 16498, Name: "Shingeki no Kyojin"}}},
		{267440, 3, []anime.Segment{
			{AniDB: 13241, AniList: 99147, MAL: 35760, Name: "Shingeki no Kyojin Season 3"},
			{AniDB: 14444, AniList: 104578, MAL: 38524, EpisodeOffset: 12, Name: "Shingeki no Kyojin Season 3 (2019)"},
		}},
		{267440, 4, []anime.Segment{
			{AniDB: 14977, AniList: 110277, MAL: 40028, Name: "Shingeki no Kyojin: The Final Season"},
			{AniDB: 16177, AniList: 131681, MAL: 48583, EpisodeOffset: 16, Name: "Shingeki no Kyojin: The Final Season (2022)"},
			{AniDB: 17303, AniList: 146984, MAL: 51535, EpisodeOffset: 28, Name: "Shingeki no Kyojin: The Final Season (2023)"},
		}},
		{405920, 1, []anime.Segment{
			{AniDB: 16947, AniList: 140960, MAL: 50265, Name: "Spy x Family"},
			{AniDB: 17061, AniList: 142838, MAL: 50602, EpisodeOffset: 12, Name: "Spy x Family (2022)"},
		}},
		// One Piece maps with absolute numbering: no per-season segments.
		{81797, 1, nil},
	} {
		got := crosswalk.SeasonSegments(tc.tvdb, tc.season)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SeasonSegments(%d, %d) = %+v, want %+v", tc.tvdb, tc.season, got, tc.want)
		}
	}
	// TMDb numbers Frieren as one season; its second and third cours sit at
	// episode offsets 28 and 38 of TMDb season 1.
	tmdb := crosswalk.TMDbSeasonSegments(209867, 1)
	if len(tmdb) != 3 || tmdb[0].AniDB != 17617 || tmdb[1].EpisodeOffset != 28 || tmdb[2].EpisodeOffset != 38 {
		t.Fatalf("TMDbSeasonSegments(209867, 1) = %+v", tmdb)
	}
	if specials := crosswalk.SeasonSegments(267440, 0); len(specials) != 4 {
		t.Fatalf("season 0 segments = %+v, want the four specials and movies", specials)
	}
}

func TestCrosswalkAbsoluteSeriesAnchor(t *testing.T) {
	crosswalk := loadFixture(t)
	identity, found := crosswalk.Identify(anime.Query{TVDB: 81797})
	if !found || identity.Anime.AniDB != 69 {
		t.Fatalf("One Piece identity = %+v found %v, want the absolute TV entry AniDB 69", identity, found)
	}
}

func TestCrosswalkIdentifiesByTitle(t *testing.T) {
	crosswalk := loadFixture(t)
	for _, name := range []string{"Attack on Titan", "Shingeki no Kyojin (2013)", "進撃の巨人", "SHINGEKI NO KYOJIN", "Shingeki no Kyojin TV"} {
		identity, found := crosswalk.Identify(anime.Query{Names: []string{name}, AllowTitle: true})
		if !found || identity.Anime.AniDB != 9541 || identity.Method != anime.MethodTitle || identity.TVDB != 267440 {
			t.Errorf("Identify(%q) = %+v found %v, want AniDB 9541 by title with TVDB 267440", name, identity, found)
		}
	}
	// An abbreviation is too ambiguous to identify a series.
	if identity, found := crosswalk.Identify(anime.Query{Names: []string{"SnK"}, AllowTitle: true}); found {
		t.Errorf("Identify(SnK) = %+v, want no match", identity)
	}
	// The title fallback is opt-in.
	if identity, found := crosswalk.Identify(anime.Query{Names: []string{"Attack on Titan"}}); found {
		t.Errorf("Identify without AllowTitle = %+v, want no match", identity)
	}
	// A year selects among same-titled entries.
	identity, found := crosswalk.Identify(anime.Query{Names: []string{"Sousou no Frieren"}, Year: 2023, AllowTitle: true})
	if !found || identity.Anime.AniDB != 17617 {
		t.Errorf("Identify(Sousou no Frieren, 2023) = %+v, want AniDB 17617", identity)
	}
}

func TestCrosswalkIdentifiesByDirectIDs(t *testing.T) {
	crosswalk := loadFixture(t)
	for _, tc := range []struct {
		query  anime.Query
		method anime.Method
	}{
		{anime.Query{AniList: 99147}, anime.MethodAniList},
		{anime.Query{AniDB: 13241}, anime.MethodAniDB},
		{anime.Query{MAL: 35760}, anime.MethodMAL},
	} {
		identity, found := crosswalk.Identify(tc.query)
		if !found || identity.Method != tc.method || identity.Anime.AniDB != 13241 || identity.Anime.AniList != 99147 || identity.TVDB != 267440 {
			t.Errorf("Identify(%+v) = %+v, want AniDB 13241 by %s", tc.query, identity, tc.method)
		}
	}
	if titles := crosswalk.AniDBTitles(9541); len(titles) < 10 {
		t.Fatalf("AniDBTitles(9541) = %d titles, want the full dump set", len(titles))
	}
}

func TestFoldTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Shingeki no Kyojin", "shingekinokyojin"},
		{"Shōnen", "shonen"},
		{"Shounen", "shonen"},
		{"Kyoukai no Kanata", "kyokainokanata"},
		{"Ｓｈｉｎｇｅｋｉ", "shingeki"},
		{"シンゲキ", "しんげき"},
		{"ｼﾝｹﾞｷ", "しんげき"},
		{"進撃の巨人", "進撃の巨人"},
		{"Re:Zero − Starting Life", "rezerostartinglife"},
		{"   ", ""},
	} {
		if got := anime.FoldTitle(tc.in); got != tc.want {
			t.Errorf("FoldTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCrosswalkReplacesSeriesIDsThatMapToNoAnime covers a series that another
// provider matched to a same-name show: its TVDB and TMDb ids map to no anime,
// and Anime-Lists lists the identified anime with other ids. The anime ids win.
// An id that maps to another anime, or that Anime-Lists does not contradict,
// stays.
func TestCrosswalkReplacesSeriesIDsThatMapToNoAnime(t *testing.T) {
	crosswalk := loadFixture(t)
	identity, found := crosswalk.Identify(anime.Query{TVDB: 71663, TMDb: 456, Names: []string{"Shingeki no Kyojin"}, AllowTitle: true})
	if !found || identity.Method != anime.MethodTitle || identity.Anime.AniDB != 9541 {
		t.Fatalf("Identify = %+v, %v; want AniDB 9541 by title", identity, found)
	}
	if identity.TVDB != 267440 || identity.TMDb != 1429 || identity.WrongTVDB != 71663 || identity.WrongTMDb != 456 {
		t.Fatalf("series ids = %+v, want TVDB 267440 / TMDb 1429 replacing 71663 / 456", identity)
	}

	// One Piece's TVDB id maps to an anime: it is not a same-name match.
	identity, found = crosswalk.Identify(anime.Query{AniList: 16498, TVDB: 81797})
	if !found || identity.TVDB != 81797 || identity.WrongTVDB != 0 {
		t.Fatalf("Identify with an anime TVDB id = %+v, %v; want the id kept", identity, found)
	}
}
