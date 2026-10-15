// SPDX-License-Identifier: GPL-2.0-only

// Expectations are a filename-level port of tools/goldenrec/fixtures/expected-inventory.json.
// Folder, box-set, artist, album, series, and season records are scanner-owned and intentionally
// excluded: WP-P2-04 owns only independent Emby.Naming resolver/parser behavior.
// NamingOptions.cs:159 treats a space-separated terminal `sample` as a cleaner suffix.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestSyntheticFixtureFilenameInventory(t *testing.T) {
	opts := naming.DefaultOptions()

	for _, path := range []string{
		"/media/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/10 - Synthetic Track 10.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/11 - Synthetic Track 11.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/2 - Synthetic Track 2.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/3 - Synthetic Track 3.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/4 - Synthetic Track 4.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/5 - Synthetic Track 5.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/6 - Synthetic Track 6.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/7 - Synthetic Track 7.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/8 - Synthetic Track 8.mp3",
		"/media/music/Goldenrec Generator/Synthetic Album/9 - Synthetic Track 9.mp3",
	} {
		t.Run("audio/"+path, func(t *testing.T) {
			if !naming.IsAudioFile(path, opts) {
				t.Fatalf("IsAudioFile(%q) = false, want true", path)
			}
		})
	}

	for _, tt := range []struct {
		path, series    string
		season, episode int
	}{
		{"/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E01 - First Signal.mkv", "Synthetic Show", 1, 1},
		{"/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E02 - Second Signal.mkv", "Synthetic Show", 1, 2},
		{"/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E03 - Third Signal.mkv", "Synthetic Show", 1, 3},
		{"/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E01 - Return Signal.mkv", "Synthetic Show", 2, 1},
		{"/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E02 - Wide Signal.mkv", "Synthetic Show", 2, 2},
		{"/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E03 - Final Signal.mkv", "Synthetic Show", 2, 3},
		{"/media/tv/Synthetic Show (2024)/Specials/Synthetic Show S00E01 - Pilot Extra.mkv", "Synthetic Show", 0, 1},
		{"/media/tv/Synthetic Show (2024)/Specials/Synthetic Show S00E02 - Behind The Bars.mkv", "Synthetic Show", 0, 2},
	} {
		t.Run("episode/"+tt.path, func(t *testing.T) {
			got, err := naming.ParseEpisode(tt.path, false, opts)
			if err != nil {
				t.Fatalf("ParseEpisode(%q) error = %v", tt.path, err)
			}
			if got == nil || got.SeriesName != tt.series || !reflect.DeepEqual(got.SeasonNumber, intp(tt.season)) || !reflect.DeepEqual(got.EpisodeNumber, intp(tt.episode)) {
				t.Fatalf("ParseEpisode(%q) = %#v, want %q S%02dE%02d", tt.path, got, tt.series, tt.season, tt.episode)
			}
		})
	}

	for _, tt := range []struct {
		path, name string
		year       *int
		stub       bool
	}{
		{"/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv", "HLG", intp(2024), false},
		{"/media/movies/Odd Names/Café déjà-vu (2020).mkv", "Café déjà-vu", intp(2020), false},
		{"/media/movies/Odd Names/Film.Name.2022.1080p.BluRay.mkv", "Film.Name", intp(2022), false},
		{"/media/movies/Odd Names/The [Odd] Film (2021) {edition-Director's Cut}.mkv", "The [Odd] Film", intp(2021), false},
		{"/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv", "Standalone Movie", intp(2023), false},
		{"/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv", "Subtitle", intp(2024), false},
		{"/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv", "Collection Film One", intp(2020), false},
		{"/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv", "Collection Film Three", intp(2022), false},
		{"/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv", "Collection Film Two", intp(2021), false},
		{"/media/movies/Synthetic Disc.bluray.disc", "Synthetic Disc", nil, true},
		{"/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv", "Synthetic Feature", intp(2024), false},
	} {
		t.Run("video/"+tt.path, func(t *testing.T) {
			got := mustResolveVideo(t, tt.path, opts)
			if got == nil || got.Name != tt.name || !reflect.DeepEqual(got.Year, tt.year) || got.IsStub != tt.stub {
				t.Fatalf("ResolveVideo(%q) = %#v, want name/year/stub %q/%v/%t", tt.path, got, tt.name, tt.year, tt.stub)
			}
		})
	}
}
