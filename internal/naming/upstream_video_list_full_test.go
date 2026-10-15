// SPDX-License-Identifier: GPL-2.0-only

// Port of active file-based cases in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/VideoListResolverTests.cs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.VideoListResolverTests.TestStackAndExtras through TestSubfolders.
func TestUpstreamVideoListResolverFull(t *testing.T) {
	cases := []struct {
		method string
		paths  []string
		count  int
		stacks map[string]int
	}{
		{"TestStackAndExtras", []string{"Harry Potter and the Deathly Hallows-trailer.mkv", "Harry Potter and the Deathly Hallows.trailer.mkv", "Harry Potter and the Deathly Hallows part1.mkv", "Harry Potter and the Deathly Hallows part2.mkv", "Harry Potter and the Deathly Hallows part3.mkv", "Harry Potter and the Deathly Hallows part4.mkv", "Batman-deleted.mkv", "Batman-sample.mkv", "Batman-trailer.mkv", "Batman part1.mkv", "Batman part2.mkv", "Batman part3.mkv", "Avengers.mkv", "Avengers-trailer.mkv", "trailer.mkv", "WillyWonka-trailer.mkv"}, 11, map[string]int{"Batman": 3, "Harry Potter and the Deathly Hallows": 4}},
		{"TestWithMetadata", []string{"300.mkv", "300.nfo"}, 1, nil},
		{"TestWithExtra", []string{"300.mkv", "300 - trailer.mkv"}, 2, nil},
		{"TestVariationWithFolderName", []string{"X-Men Days of Future Past - 1080p.mkv", "X-Men Days of Future Past-trailer.mp4"}, 2, nil},
		{"TestTrailer2", []string{"X-Men Days of Future Past - 1080p.mkv", "X-Men Days of Future Past-trailer.mp4", "X-Men Days of Future Past-trailer2.mp4"}, 3, nil},
		{"Resolve_SameNameAndYear_ReturnsSingleItem", []string{"Looper (2012)-trailer.mkv", "Looper 2012-trailer.mkv", "Looper.2012.bluray.720p.x264.mkv"}, 3, nil},
		{"Resolve_TrailerMatchesFolderName_ReturnsSingleItem", []string{"/movies/Looper (2012)/Looper (2012)-trailer.mkv", "/movies/Looper (2012)/Looper.bluray.720p.x264.mkv"}, 2, nil},
		{"TestSeparateFiles", []string{"My video 1.mkv", "My video 2.mkv", "My video 3.mkv", "My video 4.mkv", "My video 5.mkv"}, 5, nil},
		{"TestPoundSign", []string{"My movie #1.mp4", "My movie #2.mp4"}, 2, nil},
		{"TestStackedWithTrailer", []string{"No (2012) part1.mp4", "No (2012) part2.mp4", "No (2012) part1-trailer.mp4", "No (2012)-trailer.mp4"}, 3, nil},
		{"TestExtrasByFolderName", []string{"/Movies/Top Gun (1984)/movie.mp4", "/Movies/Top Gun (1984)/Top Gun (1984)-trailer.mp4", "/Movies/Top Gun (1984)/Top Gun (1984)-trailer2.mp4", "/Movies/trailer.mp4"}, 4, nil},
		{"TestDoubleTags", []string{"/MCFAMILY-PC/Private3$/Heterosexual/Breast In Class 2 Counterfeit Racks (2011)/Breast In Class 2 Counterfeit Racks (2011) Disc 1 cd1.avi", "/MCFAMILY-PC/Private3$/Heterosexual/Breast In Class 2 Counterfeit Racks (2011)/Breast In Class 2 Counterfeit Racks (2011) Disc 1 cd2.avi", "/MCFAMILY-PC/Private3$/Heterosexual/Breast In Class 2 Counterfeit Racks (2011)/Breast In Class 2 Disc 2 cd1.avi", "/MCFAMILY-PC/Private3$/Heterosexual/Breast In Class 2 Counterfeit Racks (2011)/Breast In Class 2 Disc 2 cd2.avi"}, 2, nil},
		{"TestArgumentOutOfRangeException", []string{"/nas-markrobbo78/Videos/INDEX HTPC/Movies/Watched/3 - ACTION/Argo (2012)/movie.mkv"}, 1, nil},
		{"TestColony", []string{"The Colony.mkv"}, 1, nil},
		{"TestFourSisters", []string{"Four Sisters and a Wedding - A.avi", "Four Sisters and a Wedding - B.avi"}, 2, nil},
		{"TestFourRooms", []string{"Four Rooms - A.avi", "Four Rooms - A.mp4"}, 2, nil},
		{"TestMovieTrailer", []string{"/Server/Despicable Me/Despicable Me (2010).mkv", "/Server/Despicable Me/trailer.mkv"}, 2, nil},
		{"Resolve_TrailerInTrailersFolder_ReturnsCorrectExtraType", []string{"/Server/Despicable Me/Despicable Me (2010).mkv", "/Server/Despicable Me/trailers/some title.mkv"}, 2, nil},
		{"TestSubfolders", []string{"/Movies/Despicable Me/Despicable Me.mkv", "/Movies/Despicable Me/trailers/trailer.mkv"}, 2, nil},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveVideoList(t, tt.paths, naming.DefaultOptions())
			if len(got) != tt.count {
				t.Fatalf("ResolveVideoList(%s) yielded %#v, want %d items", tt.method, got, tt.count)
			}
			for name, files := range tt.stacks {
				found := false
				for _, item := range got {
					if item.Name == name && len(item.Files) == files {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("ResolveVideoList(%s) missing %q stack with %d files: %#v", tt.method, name, files, got)
				}
			}
			var extras map[int]naming.ExtraType
			switch tt.method {
			case "TestStackAndExtras":
				extras = map[int]naming.ExtraType{2: "", 3: naming.ExtraTrailer, 4: naming.ExtraTrailer, 5: naming.ExtraDeletedScene, 6: naming.ExtraSample, 7: naming.ExtraTrailer, 8: naming.ExtraTrailer, 9: naming.ExtraTrailer, 10: naming.ExtraTrailer}
			case "TestWithExtra":
				extras = map[int]naming.ExtraType{0: "", 1: naming.ExtraTrailer}
			case "TestTrailer2", "Resolve_SameNameAndYear_ReturnsSingleItem":
				extras = map[int]naming.ExtraType{0: "", 1: naming.ExtraTrailer, 2: naming.ExtraTrailer}
			case "Resolve_TrailerMatchesFolderName_ReturnsSingleItem":
				extras = map[int]naming.ExtraType{0: "", 1: naming.ExtraTrailer}
			}
			for index, want := range extras {
				if index >= len(got) || got[index].ExtraType != want {
					t.Fatalf("ResolveVideoList(%s)[%d] = %#v, want ExtraType %q", tt.method, index, got, want)
				}
			}
		})
	}
}
