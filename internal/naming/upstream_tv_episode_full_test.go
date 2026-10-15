// SPDX-License-Identifier: GPL-2.0-only

// Ported active rows from pinned upstream/jellyfin/tests/Jellyfin.Naming.Tests/TV.
package naming_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

type upstreamEpisodeCase struct {
	path      string
	directory bool
	series    string
	season    *int
	episode   *int
	ending    *int
	byDate    bool
	year      *int
	month     *int
	day       *int
}

func mustParseUpstreamEpisode(t *testing.T, path string, isDirectory bool, opts naming.NamingOptions) *naming.EpisodeInfo {
	t.Helper()
	got, err := naming.ParseEpisode(path, isDirectory, opts)
	if err != nil {
		t.Fatalf("ParseEpisode(%q, directory=%t) error = %v", path, isDirectory, err)
	}
	return got
}

func mustResolveUpstreamEpisode(t *testing.T, path string, isDirectory bool, opts naming.NamingOptions, allowAbsoluteNumbers bool) *naming.EpisodeInfo {
	t.Helper()
	got, err := naming.ResolveEpisode(path, isDirectory, opts, allowAbsoluteNumbers)
	if err != nil {
		t.Fatalf("ResolveEpisode(%q, directory=%t) error = %v", path, isDirectory, err)
	}
	return got
}

func assertUpstreamEpisode(t *testing.T, c upstreamEpisodeCase) {
	t.Helper()
	got := mustParseUpstreamEpisode(t, c.path, c.directory, naming.DefaultOptions())
	if got == nil {
		t.Fatalf("ParseEpisode(%q, directory=%t) = nil", c.path, c.directory)
	}
	if got.SeriesName != c.series || !equalIntPointer(got.SeasonNumber, c.season) || !equalIntPointer(got.EpisodeNumber, c.episode) || !equalIntPointer(got.EndingEpisodeNumber, c.ending) || got.ByDate != c.byDate || !equalIntPointer(got.Year, c.year) || !equalIntPointer(got.Month, c.month) || !equalIntPointer(got.Day, c.day) {
		t.Fatalf("ParseEpisode(%q) = %#v, want series=%q season=%v episode=%v ending=%v date=%t %v-%v-%v", c.path, got, c.series, c.season, c.episode, c.ending, c.byDate, c.year, c.month, c.day)
	}
}

func assertUpstreamResolvedEpisode(t *testing.T, c upstreamEpisodeCase) {
	t.Helper()
	got := mustResolveUpstreamEpisode(t, c.path, c.directory, naming.DefaultOptions(), false)
	if got == nil || !strings.EqualFold(got.SeriesName, c.series) || !equalIntPointer(got.SeasonNumber, c.season) || !equalIntPointer(got.EpisodeNumber, c.episode) || !equalIntPointer(got.EndingEpisodeNumber, c.ending) || got.Path != c.path || got.Container != strings.TrimPrefix(filepath.Ext(c.path), ".") || got.Format3D != "" || got.Is3D || got.IsStub || got.StubType != "" || got.ByDate != c.byDate {
		t.Fatalf("ResolveEpisode(%q) = %#v, want source episode metadata", c.path, got)
	}
}

func equalIntPointer(got, want *int) bool {
	return got == nil && want == nil || got != nil && want != nil && *got == *want
}

// Jellyfin.Naming.Tests.TV.DailyEpisodeTests.Test (5 active InlineData rows).
func TestUpstreamTVDailyEpisodeTestsTest(t *testing.T) {
	cases := []upstreamEpisodeCase{
		{"/server/anything_1996.11.14.mp4", false, "anything", nil, nil, nil, true, intp(1996), intp(11), intp(14)},
		{"/server/anything_1996-11-14.mp4", false, "anything", nil, nil, nil, true, intp(1996), intp(11), intp(14)},
		{"/server/james.corden.2017.04.20.anne.hathaway.720p.hdtv.x264-crooks.mkv", false, "james.corden", nil, nil, nil, true, intp(2017), intp(4), intp(20)},
		{"/server/ABC News 2018_03_24_19_00_00.mkv", false, "ABC News", nil, nil, nil, true, intp(2018), intp(3), intp(24)},
		{"/server/Jeopardy 2023 07 14 HDTV x264 AC3.mkv", false, "Jeopardy", nil, nil, nil, true, intp(2023), intp(7), intp(14)},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) { assertUpstreamEpisode(t, c) })
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.ParseEpisodesCorrectly (22 active InlineData rows).
func TestUpstreamTVEpisodePathParserParseEpisodesCorrectly(t *testing.T) {
	cases := []upstreamEpisodeCase{
		{"/media/Foo/Foo-S01E01", true, "Foo", intp(1), intp(1), nil, false, nil, nil, nil},
		{"/media/Foo - S04E011", true, "Foo", intp(4), intp(11), nil, false, nil, nil, nil},
		{"/media/Foo/Foo s01x01", true, "Foo", intp(1), intp(1), nil, false, nil, nil, nil},
		{"/media/Foo (2019)/Season 4/Foo (2019).S04E03", true, "Foo (2019)", intp(4), intp(3), nil, false, nil, nil, nil},
		{"D:\\media\\Foo\\Foo-S01E01", true, "Foo", intp(1), intp(1), nil, false, nil, nil, nil},
		{"D:\\media\\Foo - S04E011", true, "Foo", intp(4), intp(11), nil, false, nil, nil, nil},
		{"D:\\media\\Foo\\Foo s01x01", true, "Foo", intp(1), intp(1), nil, false, nil, nil, nil},
		{"D:\\media\\Foo (2019)\\Season 4\\Foo (2019).S04E03", true, "Foo (2019)", intp(4), intp(3), nil, false, nil, nil, nil},
		{"/Season 2/Elementary - 02x03-04-15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15), false, nil, nil, nil},
		{"/Season 1/seriesname S01E02 blah.avi", false, "seriesname", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/Running Man/Running Man S2017E368.mkv", false, "Running Man", intp(2017), intp(368), nil, false, nil, nil, nil},
		{"/Season 1/seriesname 01x02 blah.avi", false, "seriesname", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/Season 25/The Simpsons.S25E09.Steal this episode.mp4", false, "The Simpsons", intp(25), intp(9), nil, false, nil, nil, nil},
		{"/Season 1/seriesname S01x02 blah.avi", false, "seriesname", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/Season 2/Elementary - 02x03 - 02x04 - 02x15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15), false, nil, nil, nil},
		{"/Season 1/seriesname S01xE02 blah.avi", false, "seriesname", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/Season 02/Elementary - 02x03 - x04 - x15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15), false, nil, nil, nil},
		{"/Season 02/Elementary - 02x03x04x15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15), false, nil, nil, nil},
		{"/Season 02/Elementary - 02x03-E15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15), false, nil, nil, nil},
		{"/Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", false, "Elementary", intp(1), intp(23), intp(26), false, nil, nil, nil},
		{"/The Wonder Years/The.Wonder.Years.S04.PDTV.x264-JCH/The Wonder Years s04e07 Christmas Party NTSC PDTV.avi", false, "The Wonder Years", intp(4), intp(7), nil, false, nil, nil, nil},
		{"/The.Sopranos/Season 3/The Sopranos Season 3 Episode 09 - The Telltale Moozadell.avi", false, "The Sopranos", intp(3), intp(9), nil, false, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) { assertUpstreamEpisode(t, c) })
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.EpisodePathParserTest_DifferentExpressionsParameters (1 active InlineData row).
func TestUpstreamTVEpisodePathParserDifferentExpressionsParameters(t *testing.T) {
	isNamed, isOptimistic := true, true
	got, err := naming.ParseEpisodeWithFilters("/test/01-03.avi", false, naming.DefaultOptions(), naming.EpisodeParseFilters{IsNamed: &isNamed, IsOptimistic: &isOptimistic, FillExtendedInfo: true})
	if err != nil || got == nil || got.SeasonNumber != nil || !equalIntPointer(got.EpisodeNumber, intp(1)) || !equalIntPointer(got.EndingEpisodeNumber, intp(3)) {
		t.Fatalf("filtered ParseEpisodeWithFilters = %#v, %v; want season nil, episode 1, ending 3 without error", got, err)
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.EpisodePathParserTest_FalsePositivePixelRate (1 active Fact).
func TestUpstreamTVEpisodePathParserFalsePositivePixelRate(t *testing.T) {
	if got := mustParseUpstreamEpisode(t, "Series Special (1920x1080).mkv", false, naming.DefaultOptions()); got != nil {
		t.Fatalf("ParseEpisode pixel-rate text = %#v, want no episode", got)
	}
}

// Jellyfin.Naming.Tests.TV.EpisodeNumberTests.GetEpisodeNumberFromFileTest (73 active InlineData rows).
func TestUpstreamTVEpisodeNumberTestsGetEpisodeNumberFromFile(t *testing.T) {
	cases := []struct {
		path    string
		episode int
	}{
		{"Season 21/One Piece 1001", 1001},
		{"Watchmen (2019)/Watchmen 1x03 [WEBDL-720p][EAC3 5.1][h264][-TBS] - She Was Killed by Space Junk.mkv", 3},
		{"The Daily Show/The Daily Show 25x22 - [WEBDL-720p][AAC 2.0][x264] Noah Baumbach-TBS.mkv", 22},
		{"Castle Rock 2x01 Que el rio siga su curso [WEB-DL HULU 1080p h264 Dual DD5.1 Subs].mkv", 1},
		{"After Life 1x06 Episodio 6 [WEB-DL NF 1080p h264 Dual DD 5.1 Sub].mkv", 6},
		{"Season 02/S02E03 blah.avi", 3},
		{"Season 2/02x03 - 02x04 - 02x15 - Ep Name.mp4", 3},
		{"Season 02/02x03 - x04 - x15 - Ep Name.mp4", 3},
		{"Season 1/01x02 blah.avi", 2},
		{"Season 1/S01x02 blah.avi", 2},
		{"Season 1/S01E02 blah.avi", 2},
		{"Season 2/Elementary - 02x03-04-15 - Ep Name.mp4", 3},
		{"Season 1/S01xE02 blah.avi", 2},
		{"Season 1/seriesname S01E02 blah.avi", 2},
		{"Season 2/Episode - 16.avi", 16},
		{"Season 2/Episode 16.avi", 16},
		{"Season 2/Episode 16 - Some Title.avi", 16},
		{"Season 2/16 Some Title.avi", 16},
		{"Season 2/16 - 12 Some Title.avi", 16},
		{"Season 2/7 - 12 Angry Men.avi", 7},
		{"Season 1/seriesname 01x02 blah.avi", 2},
		{"Season 25/The Simpsons.S25E09.Steal this episode.mp4", 9},
		{"Season 1/seriesname S01x02 blah.avi", 2},
		{"Season 2/Elementary - 02x03 - 02x04 - 02x15 - Ep Name.mp4", 3},
		{"Season 1/seriesname S01xE02 blah.avi", 2},
		{"Season 02/Elementary - 02x03 - x04 - x15 - Ep Name.mp4", 3},
		{"Season 02/Elementary - 02x03x04x15 - Ep Name.mp4", 3},
		{"Season 2/02x03-04-15 - Ep Name.mp4", 3},
		{"Season 02/02x03-E15 - Ep Name.mp4", 3},
		{"Season 02/Elementary - 02x03-E15 - Ep Name.mp4", 3},
		{"Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", 23},
		{"Season 2009/S2009E23-E24-E26 - The Woman.mp4", 23},
		{"Season 2009/2009x02 blah.avi", 2},
		{"Season 2009/S2009x02 blah.avi", 2},
		{"Season 2009/S2009E02 blah.avi", 2},
		{"Season 2009/seriesname 2009x02 blah.avi", 2},
		{"Season 2009/Elementary - 2009x03x04x15 - Ep Name.mp4", 3},
		{"Season 2009/2009x03x04x15 - Ep Name.mp4", 3},
		{"Season 2009/Elementary - 2009x03-E15 - Ep Name.mp4", 3},
		{"Season 2009/S2009xE02 blah.avi", 2},
		{"Season 2009/Elementary - S2009E23-E24-E26 - The Woman.mp4", 23},
		{"Season 2009/seriesname S2009xE02 blah.avi", 2},
		{"Season 2009/2009x03-E15 - Ep Name.mp4", 3},
		{"Season 2009/seriesname S2009E02 blah.avi", 2},
		{"Season 2009/2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", 3},
		{"Season 2009/2009x03 - x04 - x15 - Ep Name.mp4", 3},
		{"Season 2009/seriesname S2009x02 blah.avi", 2},
		{"Season 2009/Elementary - 2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", 3},
		{"Season 2009/Elementary - 2009x03-04-15 - Ep Name.mp4", 3},
		{"Season 2009/2009x03-04-15 - Ep Name.mp4", 3},
		{"Season 2009/Elementary - 2009x03 - x04 - x15 - Ep Name.mp4", 3},
		{"Season 1/02 - blah-02 a.avi", 2},
		{"Season 1/02 - blah.avi", 2},
		{"Season 2/02 - blah 14 blah.avi", 2},
		{"Season 2/02.avi", 2},
		{"Season 2/2. Infestation.avi", 2},
		{"The Wonder Years/The.Wonder.Years.S04.PDTV.x264-JCH/The Wonder Years s04e07 Christmas Party NTSC PDTV.avi", 7},
		{"Running Man/Running Man S2017E368.mkv", 368},
		{"Season 2/[HorribleSubs] Hunter X Hunter - 136 [720p].mkv", 136},
		{"Log Horizon 2/[HorribleSubs] Log Horizon 2 - 03 [720p].mkv", 3},
		{"Season 1/seriesname 05.mkv", 5},
		{"[BBT-RMX] Ranma ½ - 154 [50AC421A].mkv", 154},
		{"Season 2/Episode 21 - 94 Meetings.mp4", 21},
		{"/The.Legend.of.Condor.Heroes.2017.V2.web-dl.1080p.h264.aac-hdctv/The.Legend.of.Condor.Heroes.2017.E07.V2.web-dl.1080p.h264.aac-hdctv.mkv", 7},
		{"Season 3/The Series Season 3 Episode 9 - The title.avi", 9},
		{"Season 3/The Series S3 E9 - The title.avi", 9},
		{"Season 3/S003 E009.avi", 9},
		{"Season 3/Season 3 Episode 9.avi", 9},
		{"[VCB-Studio] Re Zero kara Hajimeru Isekai Seikatsu [21][Ma10p_1080p][x265_flac].mkv", 21},
		{"[CASO&Sumisora][Oda_Nobuna_no_Yabou][04][BDRIP][1920x1080][x264_AAC][7620E503].mp4", 4},
		{"Case Closed (1996-2007)/Case Closed - 317.mkv", 317},
		{"Season 2/Hunter X Hunter - 101.mkv", 101},
		{"Season 1/Show Name - 1234 [720p].mkv", 1234},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := mustParseUpstreamEpisode(t, c.path, false, naming.DefaultOptions())
			if got == nil || !equalIntPointer(got.EpisodeNumber, intp(c.episode)) {
				t.Fatalf("ParseEpisode(%q) = %#v, want episode %d", c.path, got, c.episode)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.EpisodeNumberWithoutSeasonTests.GetEpisodeNumberFromFileTest (10 active InlineData rows).
func TestUpstreamTVEpisodeNumberWithoutSeasonTestsGetEpisodeNumberFromFile(t *testing.T) {
	cases := []struct {
		path    string
		episode int
	}{{"The Simpsons/The Simpsons.S25E08.Steal this episode.mp4", 8}, {"The Simpsons/The Simpsons - 02 - Ep Name.avi", 2}, {"The Simpsons/02.avi", 2}, {"The Simpsons/02 - Ep Name.avi", 2}, {"The Simpsons/02-Ep Name.avi", 2}, {"The Simpsons/02.EpName.avi", 2}, {"The Simpsons/The Simpsons - 02.avi", 2}, {"The Simpsons/The Simpsons - 02 Ep Name.avi", 2}, {"GJ Club (2013)/GJ Club - 07.mkv", 7}, {"Case Closed (1996-2007)/Case Closed - 317.mkv", 317}}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := mustResolveUpstreamEpisode(t, c.path, false, naming.DefaultOptions(), false)
			if got == nil || !equalIntPointer(got.EpisodeNumber, intp(c.episode)) {
				t.Fatalf("ResolveEpisode(%q) = %#v, want episode %d", c.path, got, c.episode)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SimpleEpisodeTests.TestSimple (16 active InlineData rows).
func TestUpstreamTVSimpleEpisodeTestsTestSimple(t *testing.T) {
	cases := []upstreamEpisodeCase{
		{"/server/anything_s01e02.mp4", false, "anything", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/server/anything_s1e2.mp4", false, "anything", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/server/anything_s01.e02.mp4", false, "anything", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/server/anything_102.mp4", false, "anything", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/server/anything_1x02.mp4", false, "anything", intp(1), intp(2), nil, false, nil, nil, nil},
		{"/server/The Walking Dead 4x01.mp4", false, "The Walking Dead", intp(4), intp(1), nil, false, nil, nil, nil},
		{"/server/the_simpsons-s02e01_18536.mp4", false, "the_simpsons", intp(2), intp(1), nil, false, nil, nil, nil},
		{"/server/Temp/S01E02 foo.mp4", false, "", intp(1), intp(2), nil, false, nil, nil, nil},
		{"Series/4x12 - The Woman.mp4", false, "", intp(4), intp(12), nil, false, nil, nil, nil},
		{"Series/LA X, Pt. 1_s06e32.mp4", false, "LA X, Pt. 1", intp(6), intp(32), nil, false, nil, nil, nil},
		{"[Baz-Bar]Foo - [1080p][Multiple Subtitle]/[Baz-Bar] Foo - 05 [1080p][Multiple Subtitle].mkv", false, "Foo", nil, intp(5), nil, false, nil, nil, nil},
		{"/Foo/The.Series.Name.S01E04.WEBRip.x264-Baz[Bar]/the.series.name.s01e04.webrip.x264-Baz[Bar].mkv", false, "The.Series.Name", intp(1), intp(4), nil, false, nil, nil, nil},
		{"Love.Death.and.Robots.S01.1080p.NF.WEB-DL.DDP5.1.x264-NTG/Love.Death.and.Robots.S01E01.Sonnies.Edge.1080p.NF.WEB-DL.DDP5.1.x264-NTG.mkv", false, "Love.Death.and.Robots", intp(1), intp(1), nil, false, nil, nil, nil},
		{"[YuiSubs] Tensura Nikki - Tensei Shitara Slime Datta Ken/[YuiSubs] Tensura Nikki - Tensei Shitara Slime Datta Ken - 12 (NVENC H.265 1080p).mkv", false, "Tensura Nikki - Tensei Shitara Slime Datta Ken", nil, intp(12), nil, false, nil, nil, nil},
		{"[Baz-Bar]Foo - 01 - 12[1080p][Multiple Subtitle]/[Baz-Bar] Foo - 05 [1080p][Multiple Subtitle].mkv", false, "Foo", nil, intp(5), nil, false, nil, nil, nil},
		{"Series/4-12 - The Woman.mp4", false, "", intp(4), intp(12), intp(12), false, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) { assertUpstreamResolvedEpisode(t, c) })
	}
}

// Jellyfin.Naming.Tests.TV.MultiEpisodeTests.TestGetEndingEpisodeNumberFromFile (61 active InlineData rows).
func TestUpstreamTVMultiEpisodeTestsTestGetEndingEpisodeNumberFromFile(t *testing.T) {
	cases := []struct {
		path   string
		ending *int
	}{
		{"Season 1/4x01 – 20 Hours in America (1).mkv", nil},
		{"Season 1/01x02 blah.avi", nil},
		{"Season 1/S01x02 blah.avi", nil},
		{"Season 1/S01E02 blah.avi", nil},
		{"Season 1/S01xE02 blah.avi", nil},
		{"Season 1/seriesname 01x02 blah.avi", nil},
		{"Season 1/seriesname S01x02 blah.avi", nil},
		{"Season 1/seriesname S01E02 blah.avi", nil},
		{"Season 1/seriesname S01xE02 blah.avi", nil},
		{"Season 2/02x03 - 04 Ep Name.mp4", nil},
		{"Season 2/My show name 02x03 - 04 Ep Name.mp4", nil},
		{"Season 2/Elementary - 02x03 - 02x04 - 02x15 - Ep Name.mp4", intp(15)},
		{"Season 2/02x03 - 02x04 - 02x15 - Ep Name.mp4", intp(15)},
		{"Season 2/02x03-04-15 - Ep Name.mp4", intp(15)},
		{"Season 2/Elementary - 02x03-04-15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03-E15 - Ep Name.mp4", intp(15)},
		{"Season 02/Elementary - 02x03-E15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03 - x04 - x15 - Ep Name.mp4", intp(15)},
		{"Season 02/Elementary - 02x03 - x04 - x15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03x04x15 - Ep Name.mp4", intp(15)},
		{"Season 02/Elementary - 02x03x04x15 - Ep Name.mp4", intp(15)},
		{"Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 1/S01E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 2009/2009x02 blah.avi", nil},
		{"Season 2009/S2009x02 blah.avi", nil},
		{"Season 2009/S2009E02 blah.avi", nil},
		{"Season 2009/S2009xE02 blah.avi", nil},
		{"Season 2009/seriesname 2009x02 blah.avi", nil},
		{"Season 2009/seriesname S2009x02 blah.avi", nil},
		{"Season 2009/seriesname S2009E02 blah.avi", nil},
		{"Season 2009/seriesname S2009xE02 blah.avi", nil},
		{"Season 2009/Elementary - 2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/2009x03 - 2009x04 - 2009x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/2009x03-04-15 - Ep Name.mp4", intp(15)},
		{"Season 2009/Elementary - 2009x03-04-15 - Ep Name.mp4", intp(15)},
		{"Season 2009/2009x03-E15 - Ep Name.mp4", intp(15)},
		{"Season 2009/Elementary - 2009x03-E15 - Ep Name.mp4", intp(15)},
		{"Season 2009/2009x03 - x04 - x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/Elementary - 2009x03 - x04 - x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/2009x03x04x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/Elementary - 2009x03x04x15 - Ep Name.mp4", intp(15)},
		{"Season 2009/Elementary - S2009E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 2009/S2009E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 1/02 - blah.avi", nil},
		{"Season 2/02 - blah 14 blah.avi", nil},
		{"Season 1/02 - blah-02 a.avi", nil},
		{"Season 2/02.avi", nil},
		{"Season 1/02-03 - blah.avi", intp(3)},
		{"Season 2/02-04 - blah 14 blah.avi", intp(4)},
		{"Season 1/02-05 - blah-02 a.avi", intp(5)},
		{"Season 2/02-04.avi", intp(4)},
		{"Season 2 /[HorribleSubs] Hunter X Hunter - 136[720p].mkv", nil},
		{"Season 1/series-s09e14-1080p.mkv", nil},
		{"Season 1/series-s09e14-720p.mkv", nil},
		{"Season 1/series-s09e14-720i.mkv", nil},
		{"Season 1/MOONLIGHTING_s01e01-e04.mkv", intp(4)},
		{"Season 1/MOONLIGHTING_s01e01-e04", intp(4)},
		{"Season 1/S01E01 The 6-10 to Lubbock [WEBRip-1080p][AV1 Opus].mkv", nil},
		{"Season 5/S05E23 11-59 [HDTV-1080p][x265 AC3].mkv", nil},
		{"Season 5/S05E23 11-59 [HDTV-1080p][HEVC AC3].mkv", nil},
		{"Season 1/S01E01 1-23-45 [Bluray-1080p][AV1 Opus].mkv", nil},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := mustParseUpstreamEpisode(t, c.path, false, naming.DefaultOptions())
			if got == nil || !equalIntPointer(got.EndingEpisodeNumber, c.ending) {
				t.Fatalf("ParseEpisode(%q) = %#v, want ending %v", c.path, got, c.ending)
			}
		})
	}
}

// Disabled upstream evidence (not active): DailyEpisodeTests lines 17-19 (3 TODO rows);
// EpisodeNumberTests lines 86-89 (4); EpisodeNumberWithoutSeasonTests lines 22-26 (4);
// EpisodePathParserTest lines 34-39 (6); SimpleEpisodeTests lines 29-30 (2); and
// SeasonNumberTests line 56 (1): 20 commented TODO InlineData rows are the only exclusions.

// Jellyfin.Naming.Tests.TV.AbsoluteEpisodeNumberTests.GetEpisodeNumberFromFileTest (7 active InlineData rows).
func TestUpstreamTVAbsoluteEpisodeNumberTestsGetEpisodeNumberFromFile(t *testing.T) {
	cases := []struct {
		path    string
		episode int
	}{{"The Simpsons/12.avi", 12}, {"The Simpsons/The Simpsons 12.avi", 12}, {"The Simpsons/The Simpsons 82.avi", 82}, {"The Simpsons/The Simpsons 112.avi", 112}, {"The Simpsons/Foo_ep_02.avi", 2}, {"The Simpsons/The Simpsons 889.avi", 889}, {"The Simpsons/The Simpsons 101.avi", 101}}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := mustResolveUpstreamEpisode(t, c.path, false, naming.DefaultOptions(), true)
			if got == nil || !equalIntPointer(got.EpisodeNumber, intp(c.episode)) {
				t.Fatalf("ResolveEpisode(%q, absolute=true) = %#v, want episode %d", c.path, got, c.episode)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.EpisodeResolverTest_WrongExtension (1 active Fact).
func TestUpstreamTVEpisodeResolverWrongExtension(t *testing.T) {
	if got := mustResolveUpstreamEpisode(t, "test.mp3", false, naming.DefaultOptions(), false); got != nil {
		t.Fatalf("ResolveEpisode(test.mp3) = %#v, want nil", got)
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.EpisodeResolverTest_WrongExtensionStub (1 active Fact).
func TestUpstreamTVEpisodeResolverWrongExtensionStub(t *testing.T) {
	got := mustResolveUpstreamEpisode(t, "dvd.disc", false, naming.DefaultOptions(), false)
	if got == nil || !got.IsStub {
		t.Fatalf("ResolveEpisode(dvd.disc) = %#v, want stub result", got)
	}
}

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.EpisodePathParserTest_EmptyDateParsers (1 active Fact).
func TestUpstreamTVEpisodePathParserEmptyDateParsers(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression("(([0-9]{4})-([0-9]{2})-([0-9]{2}) [0-9]{2}:[0-9]{2}:[0-9]{2})", true)}
	if got := mustParseUpstreamEpisode(t, "ABC_2019_10_21 11:00:00", false, opts); got == nil {
		t.Fatal("ParseEpisode with custom date expression = nil, want match")
	}
}

// Jellyfin.Naming.Tests.TV.TvParserHelpersTest.SeriesStatusParserTest_Valid (6 active InlineData rows).
func TestUpstreamTVSeriesStatusParserValid(t *testing.T) {
	cases := []struct {
		input string
		want  naming.SeriesStatus
	}{{"Ended", naming.SeriesStatusEnded}, {"Cancelled", naming.SeriesStatusEnded}, {"Continuing", naming.SeriesStatusContinuing}, {"Returning", naming.SeriesStatusContinuing}, {"Returning Series", naming.SeriesStatusContinuing}, {"Unreleased", naming.SeriesStatusUnreleased}}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got, ok := naming.TryParseSeriesStatus(c.input)
			if !ok || got != c.want {
				t.Fatalf("TryParseSeriesStatus(%q) = %q, %t; want %q, true", c.input, got, ok, c.want)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.TvParserHelpersTest.SeriesStatusParserTest_InValid (1 active InlineData row).
func TestUpstreamTVSeriesStatusParserInvalid(t *testing.T) {
	got, ok := naming.TryParseSeriesStatus("XXX")
	if ok || got != "" {
		t.Fatalf("TryParseSeriesStatus(XXX) = %q, %t; want empty, false", got, ok)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:55-59: assigning EpisodeExpressions replaces,
// rather than supplements, the default ordered expression list.
func TestConfiguredEpisodeExpressionsReplaceDefaults(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression(`^custom-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)}
	matched := mustParseUpstreamEpisode(t, "custom-1-2", false, opts)
	if matched == nil || !equalIntPointer(matched.SeasonNumber, intp(1)) || !equalIntPointer(matched.EpisodeNumber, intp(2)) {
		t.Fatalf("custom EpisodeExpression result = %#v, want season 1 episode 2", matched)
	}
	if got := mustParseUpstreamEpisode(t, "Show.S01E02.mkv", false, opts); got != nil {
		t.Fatalf("ParseEpisode fell through configured expressions to defaults: %#v", got)
	}
}

// Emby.Naming/Common/EpisodeExpression.cs:30-38: direct Expression mutation
// invalidates the cached regular expression while preserving other configured rules.
func TestEpisodeExpressionMutationRefreshesRegex(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions[0].Expression = `^custom-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`
	matched := mustParseUpstreamEpisode(t, "custom-3-4", false, opts)
	if matched == nil || !equalIntPointer(matched.SeasonNumber, intp(3)) || !equalIntPointer(matched.EpisodeNumber, intp(4)) {
		t.Fatalf("mutated EpisodeExpression result = %#v, want season 3 episode 4", matched)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:55-69: supplied named and optimistic
// filters require exact flag equality before an expression may parse.
func TestEpisodeParseFiltersRequireExactRuleFlags(t *testing.T) {
	named := naming.NewEpisodeExpression(`^named-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	named.IsNamed = true
	optimistic := naming.NewEpisodeExpression(`^optimistic-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	optimistic.IsNamed = true
	optimistic.IsOptimistic = true
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{named, optimistic}
	trueValue, falseValue := true, false
	got, err := naming.ParseEpisodeWithFilters("named-1-2", false, opts, naming.EpisodeParseFilters{IsNamed: &trueValue})
	if err != nil || got == nil || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("named filter result = %#v, %v; want episode 2 without error", got, err)
	}
	got, err = naming.ParseEpisodeWithFilters("named-1-2", false, opts, naming.EpisodeParseFilters{IsNamed: &falseValue})
	if err != nil || got != nil {
		t.Fatalf("named=false result = %#v, %v; want nil without error", got, err)
	}
	got, err = naming.ParseEpisodeWithFilters("optimistic-3-4", false, opts, naming.EpisodeParseFilters{IsOptimistic: &trueValue})
	if err != nil || got == nil || !equalIntPointer(got.EpisodeNumber, intp(4)) {
		t.Fatalf("optimistic filter result = %#v, %v; want episode 4 without error", got, err)
	}
	got, err = naming.ParseEpisodeWithFilters("optimistic-3-4", false, opts, naming.EpisodeParseFilters{IsOptimistic: &falseValue})
	if err != nil || got != nil {
		t.Fatalf("optimistic=false result = %#v, %v; want nil without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:55-69: absolute-number support is an
// equality filter for both caller-provided and default expression rules.
func TestEpisodeParseFiltersRequireExactAbsoluteSupport(t *testing.T) {
	unsupported := naming.NewEpisodeExpression(`^absolute-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	unsupported.IsNamed = true
	unsupported.SupportsAbsoluteEpisodeNumbers = false
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{unsupported}
	trueValue, falseValue := true, false
	if got, err := naming.ParseEpisodeWithFilters("absolute-1-2", false, opts, naming.EpisodeParseFilters{SupportsAbsoluteNumbers: &trueValue}); err != nil || got != nil {
		t.Fatalf("absolute=true result = %#v, %v; want nil without error", got, err)
	}
	if got, err := naming.ParseEpisodeWithFilters("absolute-1-2", false, opts, naming.EpisodeParseFilters{SupportsAbsoluteNumbers: &falseValue}); err != nil || got == nil || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("absolute=false result = %#v, %v; want episode 2 without error", got, err)
	}
	isNamed, isOptimistic := true, true
	if got, err := naming.ParseEpisodeWithFilters("/test/01-03.avi", false, naming.DefaultOptions(), naming.EpisodeParseFilters{IsNamed: &isNamed, IsOptimistic: &isOptimistic, SupportsAbsoluteNumbers: &trueValue, FillExtendedInfo: true}); err != nil || got == nil || !equalIntPointer(got.EpisodeNumber, intp(1)) {
		t.Fatalf("default absolute=true result = %#v, %v; want episode 1 without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:53-59 plus NamingOptions.cs rule 10:
// default rules obey SupportsAbsoluteNumbers and IsNamed filters without compiling
// their source-only lookaheads under RE2.
func TestDefaultEpisodeRulesRespectAbsoluteAndNamedFilters(t *testing.T) {
	trueValue := true
	if got, err := naming.ParseEpisodeWithFilters("/server/anything_102.mp4", false, naming.DefaultOptions(), naming.EpisodeParseFilters{SupportsAbsoluteNumbers: &trueValue}); err != nil || got != nil {
		t.Fatalf("default absolute=true result = %#v, %v; want nil without error", got, err)
	}
	if got, err := naming.ParseEpisodeWithFilters("/server/anything_102.mp4", false, naming.DefaultOptions(), naming.EpisodeParseFilters{IsNamed: &trueValue}); err != nil || got == nil || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("default named=true result = %#v, %v; want S1E2 without error", got, err)
	}
}

// Emby.Naming/Common/EpisodeExpression.cs:40-63: mutating a default rule flag
// changes the configured rule set even when its Expression text is unchanged.
func TestMutatedDefaultExpressionFlagDisablesProceduralFastPath(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions[10].IsNamed = false
	trueValue := true
	if got, err := naming.ParseEpisodeWithFilters("/server/anything_102.mp4", false, opts, naming.EpisodeParseFilters{IsNamed: &trueValue}); err != nil || got != nil {
		t.Fatalf("mutated named flag result = %#v, %v; want nil without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:79-90: FillExtendedInfo suppresses only
// secondary FillAdditional work; a primary expression ending capture remains.
func TestEpisodeParseFiltersFillExtendedInfoRetainsPrimaryEnding(t *testing.T) {
	isNamed, isOptimistic := true, true
	filters := naming.EpisodeParseFilters{IsNamed: &isNamed, IsOptimistic: &isOptimistic, FillExtendedInfo: false}
	withoutExtended, err := naming.ParseEpisodeWithFilters("/test/01-03.avi", false, naming.DefaultOptions(), filters)
	if err != nil || withoutExtended == nil || !equalIntPointer(withoutExtended.EpisodeNumber, intp(1)) || !equalIntPointer(withoutExtended.EndingEpisodeNumber, intp(3)) {
		t.Fatalf("FillExtendedInfo=false result = %#v, %v; want primary episode 1 ending 3", withoutExtended, err)
	}
	filters.FillExtendedInfo = true
	withExtended, err := naming.ParseEpisodeWithFilters("/test/01-03.avi", false, naming.DefaultOptions(), filters)
	if err != nil || withExtended == nil || !equalIntPointer(withExtended.EndingEpisodeNumber, intp(3)) {
		t.Fatalf("FillExtendedInfo=true result = %#v, %v; want ending 3", withExtended, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:139-183: IsNamed=false uses positional
// season/episode captures rather than named capture groups.
func TestEpisodeParseFiltersUsePositionalUnnamedCaptures(t *testing.T) {
	positional := naming.NewEpisodeExpression(`^positional-([0-9]+)-([0-9]+)$`, false)
	positional.IsNamed = false
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{positional}
	falseValue := false
	got, err := naming.ParseEpisodeWithFilters("positional-3-4", false, opts, naming.EpisodeParseFilters{IsNamed: &falseValue})
	if err != nil || got == nil || !equalIntPointer(got.SeasonNumber, intp(3)) || !equalIntPointer(got.EpisodeNumber, intp(4)) {
		t.Fatalf("unnamed positional result = %#v, %v; want season 3 episode 4 without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:79-90: configured MultipleEpisodeExpressions
// control extended ending detection and must not fall through to hard-coded defaults.
func TestConfiguredMultipleEpisodeExpressionsReplaceDefaultEndingRules(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.MultipleEpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression(`^never$`, false)}
	got := mustParseUpstreamEpisode(t, "Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", false, opts)
	if got == nil || !equalIntPointer(got.EpisodeNumber, intp(23)) || got.EndingEpisodeNumber != nil {
		t.Fatalf("custom multiple-expression result = %#v, want episode 23 without ending", got)
	}
}

// Emby.Naming/TV/SeriesPathParser.cs:24-58: configured named expressions replace
// procedural defaults; a nonmatching custom list must not fall through.
func TestConfiguredSeriesPathExpressionsReplaceDefaults(t *testing.T) {
	opts := naming.DefaultOptions()
	never := naming.NewEpisodeExpression(`^never$`, false)
	never.IsNamed = true
	opts.EpisodeExpressions = []*naming.EpisodeExpression{never}
	got, err := naming.ParseSeriesPath("The.Show.S01", opts)
	if err != nil {
		t.Fatalf("ParseSeriesPath configured expression error = %v", err)
	}
	if got.Success || got.SeriesName != "" {
		t.Fatalf("ParseSeriesPath fell through configured expressions: %#v", got)
	}
}
