// SPDX-License-Identifier: GPL-2.0-only

// Ported from upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/{MultiVersionTests,StackTests}.cs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.MultiVersionTests.TestMultiEdition2,
// TestMultiVersionLimit, TestMultiVersion4, TestMultiVersion5, and TestLetterFolders.
func TestResolveVideoListGroupsAlternateVersions(t *testing.T) {
	tests := []struct {
		name              string
		files             []string
		groups, alternate int
	}{
		{
			"editions with trailer", []string{
				"/movies/X-Men Days of Future Past/X-Men Days of Future Past - apple.mkv",
				"/movies/X-Men Days of Future Past/X-Men Days of Future Past-trailer.mp4",
				"/movies/X-Men Days of Future Past/X-Men Days of Future Past - banana.mkv",
				"/movies/X-Men Days of Future Past/X-Men Days of Future Past [banana].mp4",
			}, 2, 2,
		},
		{
			"maximum seven alternate versions", []string{
				"/movies/Movie/Movie.mkv", "/movies/Movie/Movie-2.mkv", "/movies/Movie/Movie-3.mkv", "/movies/Movie/Movie-4.mkv",
				"/movies/Movie/Movie-5.mkv", "/movies/Movie/Movie-6.mkv", "/movies/Movie/Movie-7.mkv", "/movies/Movie/Movie-8.mkv",
			}, 1, 7,
		},
		{
			"different release years are not versions", []string{
				"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man (2008).mkv", "/movies/Iron Man/Iron Man (2009).mkv",
				"/movies/Iron Man/Iron Man (2010).mkv", "/movies/Iron Man/Iron Man (2011).mkv",
			}, 5, 0,
		},
		{
			"letter folders do not create versions", []string{
				"/movies/M/Movie 1.mkv", "/movies/M/Movie 2.mkv", "/movies/M/Movie 3.mkv", "/movies/M/Movie 4.mkv",
				"/movies/M/Movie 5.mkv", "/movies/M/Movie 6.mkv", "/movies/M/Movie 7.mkv",
			}, 7, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustResolveVideoList(t, tt.files, naming.DefaultOptions())
			if len(got) != tt.groups {
				t.Fatalf("ResolveVideoList() produced %d groups, want %d: %#v", len(got), tt.groups, got)
			}
			alternates := 0
			for _, group := range got {
				alternates += len(group.AlternateVersions)
			}
			if alternates != tt.alternate {
				t.Fatalf("ResolveVideoList() produced %d alternate versions, want %d: %#v", alternates, tt.alternate, got)
			}
		})
	}

	files := []string{
		"/movies/Iron Man/Iron Man.mkv", "/movies/Iron Man/Iron Man-720p.mkv", "/movies/Iron Man/Iron Man-test.mkv",
		"/movies/Iron Man/Iron Man-bluray.mkv", "/movies/Iron Man/Iron Man-3d.mkv", "/movies/Iron Man/Iron Man-3d-hsbs.mkv",
		"/movies/Iron Man/Iron Man[test].mkv",
	}
	got := mustResolveVideoList(t, files, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Path != files[0] || len(got[0].AlternateVersions) != 6 {
		t.Fatalf("ResolveVideoList(TestMultiVersion5) = %#v, want one primary and six alternates", got)
	}
	for index, path := range []string{files[1], files[4], files[5], files[3], files[2], files[6]} {
		alternate := got[0].AlternateVersions[index]
		if len(alternate.Files) != 1 || alternate.Files[0].Path != path {
			t.Fatalf("alternate %d = %#v, want constituent path %q", index, alternate, path)
		}
	}
}

// Jellyfin.Naming.Tests.Video.StackTests.TestSimpleStack, TestStackName,
// TestFalsePositives, TestFalsePositives3, and ResolveFiles_GivenPartInMiddleOfName_ReturnsNoStack.
func TestResolveStacks(t *testing.T) {
	for _, tt := range []struct {
		name      string
		files     []string
		wantName  string
		wantCount int
	}{
		{"four part movie", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv", "Bad Boys (2006)-trailer.mkv"}, "Bad Boys (2006)", 4},
		{"numeric title", []string{"d:/movies/300 2006 part1.mkv", "d:/movies/300 2006 part2.mkv"}, "300 2006", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := mustResolveStacks(t, tt.files, naming.DefaultOptions())
			if len(got) != 1 || got[0].Name != tt.wantName || len(got[0].Files) != tt.wantCount {
				t.Fatalf("ResolveStacks(%v) = %#v, want one %q stack with %d parts", tt.files, got, tt.wantName, tt.wantCount)
			}
		})
	}
	for _, files := range [][]string{
		{"Bad Boys (2006).mkv", "Bad Boys (2007).mkv"},
		{"300 (2006).mkv", "300 (2007).mkv"},
		{"Bad Boys (2006).part1.stv.unrated.multi.1080p.bluray.x264-rough.mkv", "Bad Boys (2006).part2.stv.unrated.multi.1080p.bluray.x264-rough.mkv"},
		{"Bad Boys (2006).mkv", "Bad Boys (2006) 1.mkv", "Bad Boys (2006) 2.mkv"},
	} {
		t.Run(files[0], func(t *testing.T) {
			if got := mustResolveStacks(t, files, naming.DefaultOptions()); len(got) != 0 {
				t.Fatalf("ResolveStacks(%v) = %#v, want no stacks", files, got)
			}
		})
	}
}
