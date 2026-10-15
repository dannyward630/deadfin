// SPDX-License-Identifier: GPL-2.0-only

// Port of remaining default-collection facts in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/MultiVersionTests.cs:628-644,979-1090.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoMovieStackVersionsFull(t *testing.T) {
	cases := []struct {
		method                          string
		paths                           []string
		count, primaryFiles, alternates int
	}{
		{"TestMultiVersionEpisodeDisabledByDefault", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv"}, 2, 1, 0},
		{"TestMovieStackingWithPartNaming", []string{"/movies/Movie/Movie part1.mkv", "/movies/Movie/Movie part2.mkv"}, 1, 2, 0},
		{"TestMovieStackingWithDashPartNaming", []string{"/movies/Movie/Movie - part1.mkv", "/movies/Movie/Movie - part2.mkv"}, 1, 2, 0},
		{"TestMovieStackingWithPtNaming", []string{"/movies/Movie/Movie.pt1.mkv", "/movies/Movie/Movie.pt2.mkv"}, 1, 2, 0},
		{"TestMovieStackingWithHyphenNoSpaces", []string{"/movies/Movie/Movie-part1.mkv", "/movies/Movie/Movie-part2.mkv"}, 1, 2, 0},
		{"TestMovieStackingWithHyphenNoSpacesAndVersion", []string{"/movies/Movie/Movie-1080p-part1.mkv", "/movies/Movie/Movie-1080p-part2.mkv", "/movies/Movie/Movie-720p.mkv"}, 1, 2, 1},
		{"TestMovieMultiVersionWithStackedAlternate", []string{"/movies/Inception (2010)/Inception (2010).mkv", "/movies/Inception (2010)/Inception (2010) - 4k part1.mkv", "/movies/Inception (2010)/Inception (2010) - 4k part2.mkv"}, 1, 1, 1},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveVideoList(t, tt.paths, naming.DefaultOptions())
			if len(got) != tt.count || len(got[0].Files) != tt.primaryFiles || len(got[0].AlternateVersions) != tt.alternates {
				t.Fatalf("ResolveVideoList(%s) = %#v, want item/file/alternate counts %d/%d/%d", tt.method, got, tt.count, tt.primaryFiles, tt.alternates)
			}
			if tt.method == "TestMovieMultiVersionWithStackedAlternate" {
				if got[0].Files[0].Path != "/movies/Inception (2010)/Inception (2010).mkv" || len(got[0].AlternateVersions[0].Files) != 2 {
					t.Fatalf("ResolveVideoList(%s) lost primary or constituent alternate files: %#v", tt.method, got)
				}
			}
		})
	}
}
