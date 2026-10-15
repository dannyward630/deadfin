// SPDX-License-Identifier: GPL-2.0-only

// Ported active rows from pinned upstream/jellyfin/tests/Jellyfin.Naming.Tests/TV.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

type upstreamSeasonCase struct {
	path, parent string
	season       *int
	folder       bool
}

func assertUpstreamSeason(t *testing.T, c upstreamSeasonCase, aliases, numeric bool) {
	t.Helper()
	got := naming.ParseSeason(c.path, c.parent, aliases, numeric)
	if !equalIntPointer(got.SeasonNumber, c.season) || got.IsSeasonFolder != c.folder || got.Success != (c.season != nil) {
		t.Fatalf("ParseSeason(%q, %q, aliases=%t, numeric=%t) = %#v, want season=%v folder=%t success=%t", c.path, c.parent, aliases, numeric, got, c.season, c.folder, c.season != nil)
	}
}

// Jellyfin.Naming.Tests.TV.SeasonNumberTests.GetSeasonNumberFromEpisodeFileTest (44 active InlineData rows).
func TestUpstreamTVSeasonNumberTestsGetSeasonNumberFromEpisodeFile(t *testing.T) {
	cases := []struct {
		path   string
		season *int
	}{
		{"The Daily Show/The Daily Show 25x22 - [WEBDL-720p][AAC 2.0][x264] Noah Baumbach-TBS.mkv", intp(25)},
		{"/Show/Season 02/S02E03 blah.avi", intp(2)},
		{"Season 1/seriesname S01x02 blah.avi", intp(1)},
		{"Season 1/S01x02 blah.avi", intp(1)},
		{"Season 1/seriesname S01xE02 blah.avi", intp(1)},
		{"Season 1/01x02 blah.avi", intp(1)},
		{"Season 1/S01E02 blah.avi", intp(1)},
		{"Season 1/S01xE02 blah.avi", intp(1)},
		{"Season 1/seriesname 01x02 blah.avi", intp(1)},
		{"Season 1/seriesname S01E02 blah.avi", intp(1)},
		{"Season 2/Elementary - 02x03 - 02x04 - 02x15 - Ep Name.mp4", intp(2)},
		{"Season 2/02x03 - 02x04 - 02x15 - Ep Name.mp4", intp(2)},
		{"Season 2/02x03-04-15 - Ep Name.mp4", intp(2)},
		{"Season 2/Elementary - 02x03-04-15 - Ep Name.mp4", intp(2)},
		{"Season 02/02x03-E15 - Ep Name.mp4", intp(2)},
		{"Season 02/Elementary - 02x03-E15 - Ep Name.mp4", intp(2)},
		{"Season 02/02x03 - x04 - x15 - Ep Name.mp4", intp(2)},
		{"Season 02/Elementary - 02x03 - x04 - x15 - Ep Name.mp4", intp(2)},
		{"Season 02/02x03x04x15 - Ep Name.mp4", intp(2)},
		{"Season 02/Elementary - 02x03x04x15 - Ep Name.mp4", intp(2)},
		{"Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", intp(1)},
		{"Season 1/S01E23-E24-E26 - The Woman.mp4", intp(1)},
		{"Season 25/The Simpsons.S25E09.Steal this episode.mp4", intp(25)},
		{"The Simpsons/The Simpsons.S25E09.Steal this episode.mp4", intp(25)},
		{"2016/Season s2016e1.mp4", intp(2016)},
		{"2016/Season 2016x1.mp4", intp(2016)},
		{"Season 2009/2009x02 blah.avi", intp(2009)},
		{"Season 2009/S2009x02 blah.avi", intp(2009)},
		{"Season 2009/S2009E02 blah.avi", intp(2009)},
		{"Season 2009/S2009xE02 blah.avi", intp(2009)},
		{"Season 2009/seriesname 2009x02 blah.avi", intp(2009)},
		{"Season 2009/seriesname S2009x02 blah.avi", intp(2009)},
		{"Season 2009/seriesname S2009E02 blah.avi", intp(2009)},
		{"Season 2009/Elementary - 2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/2009x03-04-15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/Elementary - 2009x03 - x04 - x15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/2009x03x04x15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/Elementary - 2009x03x04x15 - Ep Name.mp4", intp(2009)},
		{"Season 2009/Elementary - S2009E23-E24-E26 - The Woman.mp4", intp(2009)},
		{"Season 2009/S2009E23-E24-E26 - The Woman.mp4", intp(2009)},
		{"Series/1-12 - The Woman.mp4", intp(1)},
		{"Running Man/Running Man S2017E368.mkv", intp(2017)},
		{"Case Closed (1996-2007)/Case Closed - 317.mkv", nil},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := mustParseUpstreamEpisode(t, c.path, false, naming.DefaultOptions())
			if c.season != nil && (got == nil || !equalIntPointer(got.SeasonNumber, c.season)) || c.season == nil && got != nil && !equalIntPointer(got.SeasonNumber, nil) {
				t.Fatalf("ParseEpisode(%q) = %#v, want season %v", c.path, got, c.season)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SeasonPathParserTests.GetSeasonNumberFromPathTest (68 active InlineData rows).
func TestUpstreamTVSeasonPathParserGetSeasonNumberFromPath(t *testing.T) {
	cases := []upstreamSeasonCase{
		{"/Drive/Season 1", "/Drive", intp(1), true}, {"/Drive/SEASON 1", "/Drive", intp(1), true}, {"/Drive/Staffel 1", "/Drive", intp(1), true}, {"/Drive/STAFFEL 1", "/Drive", intp(1), true}, {"/Drive/Stagione 1", "/Drive", intp(1), true}, {"/Drive/STAGIONE 1", "/Drive", intp(1), true}, {"/Drive/sæson 1", "/Drive", intp(1), true}, {"/Drive/SÆSON 1", "/Drive", intp(1), true}, {"/Drive/Temporada 1", "/Drive", intp(1), true}, {"/Drive/TEMPORADA 1", "/Drive", intp(1), true}, {"/Drive/series 1", "/Drive", intp(1), true}, {"/Drive/SERIES 1", "/Drive", intp(1), true}, {"/Drive/Kausi 1", "/Drive", intp(1), true}, {"/Drive/KAUSI 1", "/Drive", intp(1), true}, {"/Drive/Säsong 1", "/Drive", intp(1), true}, {"/Drive/SÄSONG 1", "/Drive", intp(1), true}, {"/Drive/Seizoen 1", "/Drive", intp(1), true}, {"/Drive/SEIZOEN 1", "/Drive", intp(1), true}, {"/Drive/Seasong 1", "/Drive", intp(1), true}, {"/Drive/SEASONG 1", "/Drive", intp(1), true}, {"/Drive/Sezon 1", "/Drive", intp(1), true}, {"/Drive/SEZON 1", "/Drive", intp(1), true}, {"/Drive/sezona 1", "/Drive", intp(1), true}, {"/Drive/SEZONA 1", "/Drive", intp(1), true}, {"/Drive/sezóna 1", "/Drive", intp(1), true}, {"/Drive/SEZÓNA 1", "/Drive", intp(1), true}, {"/Drive/Sezonul 1", "/Drive", intp(1), true}, {"/Drive/SEZONUL 1", "/Drive", intp(1), true}, {"/Drive/시즌 1", "/Drive", intp(1), true}, {"/Drive/シーズン 1", "/Drive", intp(1), true}, {"/Drive/сезон 1", "/Drive", intp(1), true}, {"/Drive/Сезон 1", "/Drive", intp(1), true}, {"/Drive/СЕЗОН 1", "/Drive", intp(1), true}, {"/Drive/Season 10", "/Drive", intp(10), true}, {"/Drive/Season 100", "/Drive", intp(100), true}, {"/Drive/s1", "/Drive", intp(1), true}, {"/Drive/S1", "/Drive", intp(1), true}, {"/Drive/Season 2", "/Drive", intp(2), true}, {"/Drive/Season 02", "/Drive", intp(2), true}, {"/Drive/Seinfeld/S02", "/Seinfeld", intp(2), true}, {"/Drive/Seinfeld/2", "/Seinfeld", intp(2), true}, {"/Drive/Seinfeld Season 2", "/Drive", nil, false}, {"/Drive/Season 2009", "/Drive", intp(2009), true}, {"/Drive/Season1", "/Drive", intp(1), true}, {"The Wonder Years/The.Wonder.Years.S04.PDTV.x264-JCH", "/The Wonder Years", intp(4), true}, {"/Drive/Season 7 (2016)", "/Drive", intp(7), true}, {"/Drive/Staffel 7 (2016)", "/Drive", intp(7), true}, {"/Drive/Stagione 7 (2016)", "/Drive", intp(7), true}, {"/Drive/Stargate SG-1/Season 1", "/Drive/Stargate SG-1", intp(1), true}, {"/Drive/Stargate SG-1/Stargate SG-1 Season 1", "/Drive/Stargate SG-1", intp(1), true}, {"/Drive/Season (8)", "/Drive", nil, false}, {"/Drive/3.Staffel", "/Drive", intp(3), true}, {"/Drive/s06e05", "/Drive", nil, false}, {"/Drive/The.Legend.of.Condor.Heroes.2017.V2.web-dl.1080p.h264.aac-hdctv", "/Drive", nil, false}, {"/Drive/extras", "/Drive", intp(0), true}, {"/Drive/EXTRAS", "/Drive", intp(0), true}, {"/Drive/specials", "/Drive", intp(0), true}, {"/Drive/SPECIALS", "/Drive", intp(0), true}, {"/Drive/Episode 1 Season 2", "/Drive", nil, false}, {"/Drive/Episode 1 SEASON 2", "/Drive", nil, false}, {"/media/YouTube/Devyn Johnston/2024-01-24 4070 Ti SUPER in under 7 minutes", "/media/YouTube/Devyn Johnston", nil, false}, {"/media/YouTube/Devyn Johnston/2025-01-28 5090 vs 2 SFF Cases", "/media/YouTube/Devyn Johnston", nil, false}, {"/Drive/202401244070", "/Drive", nil, false}, {"/Drive/Drive.S01.2160p.WEB-DL.DDP5.1.H.265-XXXX", "/Drive", intp(1), true}, {"The Wonder Years/The.Wonder.Years.S04.1080p.PDTV.x264-JCH", "/The Wonder Years", intp(4), true}, {"The Wonder Years/[The.Wonder.Years.S04.1080p.PDTV.x264-JCH]", "/The Wonder Years", intp(4), true}, {"The Wonder Years/The.Wonder.Years [S04][1080p.PDTV.x264-JCH]", "/The Wonder Years", intp(4), true}, {"The Wonder Years/The Wonder Years Season 01 1080p", "/The Wonder Years", intp(1), true},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) { assertUpstreamSeason(t, c, true, true) })
	}
}

// Jellyfin.Naming.Tests.TV.SeasonPathParserTests.GetSeasonNumberFromPathMixedLibraryTest (11 active InlineData rows).
func TestUpstreamTVSeasonPathParserGetSeasonNumberFromPathMixedLibrary(t *testing.T) {
	cases := []upstreamSeasonCase{{"/Drive/300 Collection/300 (2006)", "/Drive/300 Collection", nil, false}, {"/Drive/300 Collection/300 Rise of an Empire", "/Drive/300 Collection", nil, false}, {"/Drive/300 Collection/1", "/Drive/300 Collection", nil, false}, {"/Drive/300 Collection/300 Disc 1", "/Drive/300 Collection", nil, false}, {"/Drive/28 Years Later Collection/28 Days Later", "/Drive/28 Years Later Collection", nil, false}, {"/Drive/28 Years Later Collection/28 Weeks Later (2007)", "/Drive/28 Years Later Collection", nil, false}, {"/Drive/28 Years Later Collection/28 Years Later 2025", "/Drive/28 Years Later Collection", nil, false}, {"/Drive/300 Collection/Season 1", "/Drive/300 Collection", intp(1), true}, {"/Drive/28 Years Later Collection/Season 01", "/Drive/28 Years Later Collection", intp(1), true}, {"/Drive/300 Collection/S01", "/Drive/300 Collection", intp(1), true}, {"/Drive/300 Collection/S1", "/Drive/300 Collection", intp(1), true}}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) { assertUpstreamSeason(t, c, false, false) })
	}
}

// Jellyfin.Naming.Tests.TV.SeriesPathParserTest.SeriesPathParserParseTest (9 active InlineData rows).
func TestUpstreamTVSeriesPathParserParseTest(t *testing.T) {
	cases := []struct{ path, name string }{{"The.Show.S01", "The.Show"}, {"/The.Show.S01", "The.Show"}, {"/some/place/The.Show.S01", "The.Show"}, {"/something/The.Show.S01", "The.Show"}, {"The Show Season 10", "The Show"}, {"The Show S01E01", "The Show"}, {"The Show S01E01 Episode", "The Show"}, {"/something/The Show/Season 1", "The Show"}, {"/something/The Show/S01", "The Show"}}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got, err := naming.ParseSeriesPath(c.path, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ParseSeriesPath(%q) error = %v", c.path, err)
			}
			if !got.Success || got.SeriesName != c.name {
				t.Fatalf("ParseSeriesPath(%q) = %#v, want success %q", c.path, got, c.name)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SeriesResolverTests.SeriesResolverResolveTest (11 active InlineData rows).
func TestUpstreamTVSeriesResolverResolveTest(t *testing.T) {
	cases := []struct{ path, name string }{{"The.Show.S01", "The Show"}, {"The.Show.S01.COMPLETE", "The Show"}, {"S.H.O.W.S01", "S.H.O.W"}, {"The.Show.P.I.S01", "The Show P.I"}, {"The_Show_Season_1", "The Show"}, {"/something/The_Show/Season 10", "The Show"}, {"The Show", "The Show"}, {"/some/path/The Show", "The Show"}, {"/some/path/The Show s02e10 720p hdtv", "The Show"}, {"/some/path/The Show s02e10 the episode 720p hdtv", "The Show"}, {"/some/path/1923 (2022)", "1923"}}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got, err := naming.ResolveSeries(c.path, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ResolveSeries(%q) error = %v", c.path, err)
			}
			if got.Name != c.name {
				t.Fatalf("ResolveSeries(%q) = %#v, want name %q", c.path, got, c.name)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SeasonPathParserTests.GetSeasonNumberFromPathMixedLibraryTest.
// The last row is Deadfin's: it shows the guard that a mixed library adds.
// The punctuation-free name "Season2" matches ProcessPost, but the folder name
// holds no season keyword.
func TestUpstreamTVSeasonPathParserMixedLibrary(t *testing.T) {
	for _, c := range []upstreamSeasonCase{
		{"/Drive/300 Collection/300 (2006)", "/Drive/300 Collection", nil, false},
		{"/Drive/300 Collection/300 Rise of an Empire", "/Drive/300 Collection", nil, false},
		{"/Drive/300 Collection/1", "/Drive/300 Collection", nil, false},
		{"/Drive/300 Collection/300 Disc 1", "/Drive/300 Collection", nil, false},
		{"/Drive/28 Years Later Collection/28 Days Later", "/Drive/28 Years Later Collection", nil, false},
		{"/Drive/28 Years Later Collection/28 Weeks Later (2007)", "/Drive/28 Years Later Collection", nil, false},
		{"/Drive/28 Years Later Collection/28 Years Later 2025", "/Drive/28 Years Later Collection", nil, false},
		{"/Drive/300 Collection/Season 1", "/Drive/300 Collection", intp(1), true},
		{"/Drive/28 Years Later Collection/Season 01", "/Drive/28 Years Later Collection", intp(1), true},
		{"/Drive/300 Collection/S01", "/Drive/300 Collection", intp(1), true},
		{"/Drive/300 Collection/S1", "/Drive/300 Collection", intp(1), true},
		{"/Drive/Show/Sea-son 2", "/Drive/Show", nil, false},
	} {
		t.Run(c.path, func(t *testing.T) { assertUpstreamSeason(t, c, false, false) })
	}
	assertUpstreamSeason(t, upstreamSeasonCase{"/Drive/Show/Sea-son 2", "/Drive/Show", intp(2), true}, true, true)
}
