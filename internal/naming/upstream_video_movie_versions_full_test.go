// SPDX-License-Identifier: GPL-2.0-only

// Port of the movie/empty-list active methods in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/MultiVersionTests.cs:23-464.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Each table row names one active upstream fact and exercises its complete file provider through the public resolver.
func TestUpstreamVideoMovieVersionsFull(t *testing.T) {
	cases := []struct {
		method            string
		paths             []string
		count, alternates int
	}{
		{"TestMultiEdition1", []string{"/movies/X-Men Days of Future Past/X-Men Days of Future Past - 1080p.mkv", "/movies/X-Men Days of Future Past/X-Men Days of Future Past-trailer.mp4", "/movies/X-Men Days of Future Past/X-Men Days of Future Past - [hsbs].mkv", "/movies/X-Men Days of Future Past/X-Men Days of Future Past [hsbs].mkv"}, 2, 2},
		{"TestMultiEdition2", []string{"/movies/X-Men Days of Future Past/X-Men Days of Future Past - apple.mkv", "/movies/X-Men Days of Future Past/X-Men Days of Future Past-trailer.mp4", "/movies/X-Men Days of Future Past/X-Men Days of Future Past - banana.mkv", "/movies/X-Men Days of Future Past/X-Men Days of Future Past [banana].mp4"}, 2, 2},
		{"TestMultiEdition3", []string{"/movies/The Phantom of the Opera (1925)/The Phantom of the Opera (1925) - 1925 version.mkv", "/movies/The Phantom of the Opera (1925)/The Phantom of the Opera (1925) - 1929 version.mkv"}, 1, 1},
		{"TestLetterFolders", []string{"/movies/M/Movie 1.mkv", "/movies/M/Movie 2.mkv", "/movies/M/Movie 3.mkv", "/movies/M/Movie 4.mkv", "/movies/M/Movie 5.mkv", "/movies/M/Movie 6.mkv", "/movies/M/Movie 7.mkv"}, 7, 0},
		{"TestMultiVersionLimit", []string{"/movies/Movie/Movie.mkv", "/movies/Movie/Movie-2.mkv", "/movies/Movie/Movie-3.mkv", "/movies/Movie/Movie-4.mkv", "/movies/Movie/Movie-5.mkv", "/movies/Movie/Movie-6.mkv", "/movies/Movie/Movie-7.mkv", "/movies/Movie/Movie-8.mkv"}, 1, 7},
		{"TestMultiVersionLimit2", []string{"/movies/Mo/Movie 1.mkv", "/movies/Mo/Movie 2.mkv", "/movies/Mo/Movie 3.mkv", "/movies/Mo/Movie 4.mkv", "/movies/Mo/Movie 5.mkv", "/movies/Mo/Movie 6.mkv", "/movies/Mo/Movie 7.mkv", "/movies/Mo/Movie 8.mkv", "/movies/Mo/Movie 9.mkv"}, 9, 0},
		{"TestMultiVersion3", []string{"/movies/Movie/Movie 1.mkv", "/movies/Movie/Movie 2.mkv", "/movies/Movie/Movie 3.mkv", "/movies/Movie/Movie 4.mkv", "/movies/Movie/Movie 5.mkv"}, 5, 0},
		{"TestMultiVersion4", []string{"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man (2008).mkv", "/movies/Iron Man/Iron Man (2009).mkv", "/movies/Iron Man/Iron Man (2010).mkv", "/movies/Iron Man/Iron Man (2011).mkv"}, 5, 0},
		{"TestMultiVersion5", []string{"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man-720p.mkv", "/movies/Iron Man/Iron Man-test.mkv", "/movies/Iron Man/Iron Man-bluray.mkv", "/movies/Iron Man/Iron Man-3d.mkv", "/movies/Iron Man/Iron Man-3d-hsbs.mkv", "/movies/Iron Man/Iron Man[test].mkv"}, 1, 6},
		{"TestMultiVersion6", []string{"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man - 720p.mkv", "/movies/Iron Man/Iron Man - test.mkv", "/movies/Iron Man/Iron Man - bluray.mkv", "/movies/Iron Man/Iron Man - 3d.mkv", "/movies/Iron Man/Iron Man - 3d-hsbs.mkv", "/movies/Iron Man/Iron Man [test].mkv"}, 1, 6},
		{"TestMultiVersion7", []string{"/movies/Iron Man/Iron Man - B (2006).mkv", "/movies/Iron Man/Iron Man - C (2007).mkv"}, 2, 0},
		{"TestMultiVersion8", []string{"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man_720p.mkv", "/movies/Iron Man/Iron Man_test.mkv", "/movies/Iron Man/Iron Man_bluray.mkv", "/movies/Iron Man/Iron Man_3d.mkv", "/movies/Iron Man/Iron Man_3d-hsbs.mkv", "/movies/Iron Man/Iron Man_3d.hsbs.mkv"}, 1, 6},
		{"TestMultiVersion9", []string{"/movies/Iron Man/Iron Man (2007).mkv", "/movies/Iron Man/Iron Man (2008).mkv", "/movies/Iron Man/Iron Man (2009).mkv", "/movies/Iron Man/Iron Man (2010).mkv", "/movies/Iron Man/Iron Man (2011).mkv"}, 5, 0},
		{"TestMultiVersion10", []string{"/movies/Blade Runner (1982)/Blade Runner (1982) [Final Cut] [1080p HEVC AAC].mkv", "/movies/Blade Runner (1982)/Blade Runner (1982) [EE by ADM] [480p HEVC AAC,AAC,AAC].mkv"}, 1, 1},
		{"TestMultiVersion11", []string{"/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) [1080p] Blu-ray.x264.DTS.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) [2160p] Blu-ray.x265.AAC.mkv"}, 1, 1},
		{"TestMultiVersion12", []string{"/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - Theatrical Release.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - Directors Cut.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 2160p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 720p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016).mkv"}, 1, 5},
		{"TestMultiVersion13", []string{"/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - Theatrical Release.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - Directors Cut.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 2160p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p Directors Cut.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 2160p Remux.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p Theatrical Release.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 720p.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p Remux.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 720p Directors Cut.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016) - 1080p High Bitrate.mkv", "/movies/X-Men Apocalypse (2016)/X-Men Apocalypse (2016).mkv"}, 1, 11},
		{"Resolve_GivenFolderNameWithBracketsAndHyphens_GroupsBasedOnFolderName", []string{"/movies/John Wick - Kapitel 3 (2019) [imdbid=tt6146586]/John Wick - Kapitel 3 (2019) [imdbid=tt6146586] - Version 1.mkv", "/movies/John Wick - Kapitel 3 (2019) [imdbid=tt6146586]/John Wick - Kapitel 3 (2019) [imdbid=tt6146586] - Version 2.mkv"}, 1, 1},
		{"Resolve_GivenUnclosedBrackets_DoesNotGroup", []string{"/movies/John Wick - Chapter 3 (2019)/John Wick - Chapter 3 (2019) [Version 1].mkv", "/movies/John Wick - Chapter 3 (2019)/John Wick - Chapter 3 (2019) [Version 2.mkv"}, 2, 0},
		{"TestEmptyList", nil, 0, 0},
		{"Resolve_GivenUnderscoreSeparator_GroupsVersions", []string{"/movies/Movie (2020)/Movie (2020)_4K.mkv", "/movies/Movie (2020)/Movie (2020)_1080p.mkv"}, 1, 1},
		{"Resolve_GivenDotSeparator_GroupsVersions", []string{"/movies/Movie (2020)/Movie (2020).UHD.mkv", "/movies/Movie (2020)/Movie (2020).1080p.mkv"}, 1, 1},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveVideoList(t, tt.paths, naming.DefaultOptions())
			if len(got) != tt.count {
				t.Fatalf("ResolveVideoList(%s) has %d items, want %d: %#v", tt.method, len(got), tt.count, got)
			}
			if tt.count > 0 && len(got[0].AlternateVersions) != tt.alternates {
				t.Fatalf("ResolveVideoList(%s) primary alternates = %#v, want %d", tt.method, got[0].AlternateVersions, tt.alternates)
			}
		})
	}
}
