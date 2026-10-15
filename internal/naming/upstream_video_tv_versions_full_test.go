// SPDX-License-Identifier: GPL-2.0-only

// Exhaustive port of collectionType: tvshows facts in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/MultiVersionTests.cs:468-977,1092-1132.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoTVVersionsFull(t *testing.T) {
	cases := []struct {
		method                          string
		paths                           []string
		count, alternateTotal, maxFiles int
	}{
		{"TestMultiVersionEpisodeInOwnFolder", []string{"/TV/Dexter/Dexter - S01E01/Dexter - S01E01 - 1080p.mkv", "/TV/Dexter/Dexter - S01E01/Dexter - S01E01 - 720p.mkv"}, 1, 1, 1},
		{"TestMultiVersionEpisodeMixedSeasonFolder", []string{"/TV/Dexter/Season 1/Dexter - S01E01 - 1080p.mkv", "/TV/Dexter/Season 1/Dexter - S01E01 - 720p.mkv", "/TV/Dexter/Season 1/Dexter - S01E02.mkv", "/TV/Dexter/Season 1/Dexter - S01E03 - 1080p.mkv", "/TV/Dexter/Season 1/Dexter - S01E03 - 720p.mkv"}, 3, 2, 1},
		{"TestMultiVersionEpisodeDontCollapse", []string{"/TV/Dexter/Season 1/Dexter - S01E01.mkv", "/TV/Dexter/Season 1/Dexter - S01E02.mkv", "/TV/Dexter/Season 1/Dexter - S01E03.mkv", "/TV/Dexter/Season 1/Dexter - S01E04.mkv", "/TV/Dexter/Season 1/Dexter - S01E05.mkv"}, 5, 0, 1},
		{"TestMultiVersionEpisodeWithVersionSuffix", []string{"/TV/Show/Season 1/Show - S01E01 - Aired.mkv", "/TV/Show/Season 1/Show - S01E01 - Uncensored.mkv", "/TV/Show/Season 1/Show - S01E02 - Aired.mkv", "/TV/Show/Season 1/Show - S01E02 - Uncensored.mkv"}, 2, 2, 1},
		{"TestMultiVersionEpisodeFourVersions", []string{"/TV/Show/Season 1/Show - S01E01 - VersionA.mkv", "/TV/Show/Season 1/Show - S01E01 - VersionB.mkv", "/TV/Show/Season 1/Show - S01E01 - VersionC.mkv", "/TV/Show/Season 1/Show - S01E01 - VersionD.mkv"}, 1, 3, 1},
		{"TestMultiVersionEpisodeWithResolutions", []string{"/TV/Show/Season 1/Show - S01E01 - 720p.mkv", "/TV/Show/Season 1/Show - S01E01 - 2160p.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p.mkv"}, 1, 2, 1},
		{"TestMultiVersionEpisodeDifferentSeasons", []string{"/TV/Show/Show - S01E01.mkv", "/TV/Show/Show - S02E01.mkv"}, 2, 0, 1},
		{"TestMultiVersionEpisodeSameNumberDifferentTitle", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot.mkv", "/TV/Show/Season 1/Show - S01E01 - Completely Different Title.mkv"}, 1, 1, 1},
		{"TestMultiVersionEpisodeWithTitle", []string{"/TV/Show/Show - S01E01/Show - S01E01 - Episode Title - 1080p.mkv", "/TV/Show/Show - S01E01/Show - S01E01 - Episode Title - 720p.mkv"}, 1, 1, 1},
		{"TestMultiVersionEpisodeWithTitleMixedFolder", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot - 1080p.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - 720p.mkv", "/TV/Show/Season 1/Show - S01E02 - Second Episode - 1080p.mkv", "/TV/Show/Season 1/Show - S01E02 - Second Episode - 720p.mkv", "/TV/Show/Season 1/Show - S01E03 - Third Episode.mkv"}, 3, 2, 1},
		{"TestMultiVersionEpisodeInSeasonSubfolder", []string{"/TV/Show/Season 1/Show - S01E01/Show - S01E01 - 1080p.mkv", "/TV/Show/Season 1/Show - S01E01/Show - S01E01 - 720p.mkv"}, 1, 1, 1},
		{"TestMultiVersionEpisodeWithTitleAndVersionSuffix", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot - Aired.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - Uncensored.mkv", "/TV/Show/Season 1/Show - S01E02 - The Getaway - Aired.mkv", "/TV/Show/Season 1/Show - S01E02 - The Getaway - Uncensored.mkv"}, 2, 2, 1},
		{"TestMultiVersionEpisodeWithAdditionalPartsCd", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p cd1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p cd2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodeWithAdditionalPartsDashPart", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p - part1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p - part2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodeWithAdditionalPartsPt", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p.pt1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p.pt2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodeWithAdditionalPartsAndTitle", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot - 1080p part1.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - 1080p part2.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - 720p.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodeWithAdditionalPartsAndTitleDashSeparator", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot - 1080p - part1.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - 1080p - part2.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot - 720p.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodeWithAdditionalPartsAndMultipleEpisodes", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p cd1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p cd2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv", "/TV/Show/Season 1/Show - S01E02 - Other.mkv"}, 2, 1, 2},
		{"TestMultiVersionEpisodePartStackAlongsideSingleFileResolutions", []string{"/TV/Show/Season 1/S01E01 - 720p.mkv", "/TV/Show/Season 1/S01E01 - 1080p.mkv", "/TV/Show/Season 1/S01E01 - Part 1.mkv", "/TV/Show/Season 1/S01E01 - Part 2.mkv", "/TV/Show/Season 1/S01E01 - Part 3.mkv"}, 1, 2, 3},
		{"TestMultiVersionEpisodeTwoPartStacks", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p - part1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p - part2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p - part1.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p - part2.mkv"}, 1, 1, 2},
		{"TestMultiVersionEpisodePartStackWithTrailer", []string{"/TV/Show/Season 1/Show - S01E01 - 1080p part1.mkv", "/TV/Show/Season 1/Show - S01E01 - 1080p part2.mkv", "/TV/Show/Season 1/Show - S01E01 - 720p.mkv", "/TV/Show/Season 1/Show - S01E01-trailer.mp4"}, 2, 1, 2},
		{"TestEpisodeStackingWithHyphenNoSpaces", []string{"/TV/Show/Season 1/Show - S01E01-1080p-cd1.mkv", "/TV/Show/Season 1/Show - S01E01-1080p-cd2.mkv", "/TV/Show/Season 1/Show - S01E01-720p.mkv"}, 1, 1, 2},
		{"TestEpisodeStackingWithHyphenNoSpacesAndTitle", []string{"/TV/Show/Season 1/Show - S01E01 - Pilot-1080p-part1.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot-1080p-part2.mkv", "/TV/Show/Season 1/Show - S01E01 - Pilot-720p.mkv"}, 1, 1, 2},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveVideoListForCollection(t, tt.paths, naming.DefaultOptions(), "tvshows")
			altTotal, maxFiles := 0, 0
			for _, item := range got {
				altTotal += len(item.AlternateVersions)
				if len(item.Files) > maxFiles {
					maxFiles = len(item.Files)
				}
			}
			if len(got) != tt.count || altTotal != tt.alternateTotal || maxFiles != tt.maxFiles {
				t.Fatalf("ResolveVideoListForCollection(%s) = %#v, want item/alternate/max-file counts %d/%d/%d", tt.method, got, tt.count, tt.alternateTotal, tt.maxFiles)
			}
		})
	}
}
