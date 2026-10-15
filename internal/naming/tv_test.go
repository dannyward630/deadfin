// SPDX-License-Identifier: GPL-2.0-only

// Ported from upstream/jellyfin/tests/Jellyfin.Naming.Tests/TV/*.cs.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.TV.EpisodePathParserTest.ParseEpisodesCorrectly and
// Jellyfin.Naming.Tests.TV.SimpleEpisodeTests.TestSimple.
func TestParseEpisode(t *testing.T) {
	tests := []struct {
		path                 string
		isDirectory          bool
		series               string
		season, episode, end *int
	}{
		{"/media/Foo/Foo-S01E01", true, "Foo", intp(1), intp(1), nil},
		{"/media/Foo - S04E011", true, "Foo", intp(4), intp(11), nil},
		{"/media/Foo (2019)/Season 4/Foo (2019).S04E03", true, "Foo (2019)", intp(4), intp(3), nil},
		{"/Season 2/Elementary - 02x03-04-15 - Ep Name.mp4", false, "Elementary", intp(2), intp(3), intp(15)},
		{"/Running Man/Running Man S2017E368.mkv", false, "Running Man", intp(2017), intp(368), nil},
		{"/The.Sopranos/Season 3/The Sopranos Season 3 Episode 09 - The Telltale Moozadell.avi", false, "The Sopranos", intp(3), intp(9), nil},
		{"/server/anything_s01e02.mp4", false, "anything", intp(1), intp(2), nil},
		{"/server/anything_102.mp4", false, "anything", intp(1), intp(2), nil},
		{"/server/The Walking Dead 4x01.mp4", false, "The Walking Dead", intp(4), intp(1), nil},
		{"Series/4-12 - The Woman.mp4", false, "", intp(4), intp(12), intp(12)},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := naming.ParseEpisode(tt.path, tt.isDirectory, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ParseEpisode(%q) error = %v", tt.path, err)
			}
			if got == nil || got.SeriesName != tt.series || !reflect.DeepEqual(got.SeasonNumber, tt.season) || !reflect.DeepEqual(got.EpisodeNumber, tt.episode) || !reflect.DeepEqual(got.EndingEpisodeNumber, tt.end) || got.ByDate {
				t.Fatalf("ParseEpisode(%q) = %#v, want series/season/episode/end %q/%v/%v/%v", tt.path, got, tt.series, tt.season, tt.episode, tt.end)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.MultiEpisodeTests.TestGetEndingEpisodeNumberFromFile.
func TestParseEpisodeRanges(t *testing.T) {
	tests := []struct {
		path   string
		ending *int
	}{
		{"Season 1/4x01 – 20 Hours in America (1).mkv", nil},
		{"Season 2/Elementary - 02x03 - 02x04 - 02x15 - Ep Name.mp4", intp(15)},
		{"Season 2/02x03-04-15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03-E15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03 - x04 - x15 - Ep Name.mp4", intp(15)},
		{"Season 02/02x03x04x15 - Ep Name.mp4", intp(15)},
		{"Season 1/Elementary - S01E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 2009/S2009E23-E24-E26 - The Woman.mp4", intp(26)},
		{"Season 1/02-03 - blah.avi", intp(3)},
		{"Season 1/series-s09e14-1080p.mkv", nil},
		{"Season 1/S01E01 The 6-10 to Lubbock [WEBRip-1080p][AV1 Opus].mkv", nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := naming.ParseEpisode(tt.path, false, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ParseEpisode(%q) error = %v", tt.path, err)
			}
			if got == nil || !reflect.DeepEqual(got.EndingEpisodeNumber, tt.ending) {
				t.Fatalf("ParseEpisode(%q).EndingEpisodeNumber = %v, want %v", tt.path, got.EndingEpisodeNumber, tt.ending)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SeasonPathParserTests.GetSeasonNumberFromPathTest.
func TestParseSeason(t *testing.T) {
	tests := []struct {
		path, parent string
		number       *int
		folder       bool
	}{
		{"/Drive/Season 1", "/Drive", intp(1), true},
		{"/Drive/Staffel 1", "/Drive", intp(1), true},
		{"/Drive/sæson 1", "/Drive", intp(1), true},
		{"/Drive/シーズン 1", "/Drive", intp(1), true},
		{"/Drive/СЕЗОН 1", "/Drive", intp(1), true},
		{"/Drive/Season 100", "/Drive", intp(100), true},
		{"/Drive/Season1", "/Drive", intp(1), true},
		{"/Drive/Season 7 (2016)", "/Drive", intp(7), true},
		{"/Drive/extras", "/Drive", intp(0), true},
		{"/Drive/specials", "/Drive", intp(0), true},
		{"/Drive/Season (8)", "/Drive", nil, false},
		{"/Drive/s06e05", "/Drive", nil, false},
		{"/Drive/202401244070", "/Drive", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := naming.ParseSeason(tt.path, tt.parent, true, true)
			if !reflect.DeepEqual(got.SeasonNumber, tt.number) || got.IsSeasonFolder != tt.folder {
				t.Fatalf("ParseSeason(%q) = %#v, want number/folder %v/%t", tt.path, got, tt.number, tt.folder)
			}
		})
	}
}

// Jellyfin.Naming.Tests.TV.SeriesResolverTests.SeriesResolverResolveTest and
// Jellyfin.Naming.Tests.TV.SeriesPathParserTest.SeriesPathParserParseTest.
func TestResolveAndParseSeries(t *testing.T) {
	tests := []struct{ path, resolveName, parseName string }{
		{"The.Show.S01", "The Show", "The.Show"},
		{"The.Show.S01.COMPLETE", "The Show", ""},
		{"S.H.O.W.S01", "S.H.O.W", ""},
		{"/something/The_Show/Season 10", "The Show", "The_Show"},
		{"/some/path/The Show s02e10 the episode 720p hdtv", "The Show", ""},
		{"/some/path/1923 (2022)", "1923", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resolved, err := naming.ResolveSeries(tt.path, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ResolveSeries(%q) error = %v", tt.path, err)
			}
			if resolved.Name != tt.resolveName {
				t.Fatalf("ResolveSeries(%q).Name = %q, want %q", tt.path, resolved.Name, tt.resolveName)
			}
			if tt.parseName != "" {
				parsed, err := naming.ParseSeriesPath(tt.path, naming.DefaultOptions())
				if err != nil {
					t.Fatalf("ParseSeriesPath(%q) error = %v", tt.path, err)
				}
				if !parsed.Success || parsed.SeriesName != tt.parseName {
					t.Fatalf("ParseSeriesPath(%q) = %#v, want success %q", tt.path, parsed, tt.parseName)
				}
			}
		})
	}
}
