// SPDX-License-Identifier: GPL-2.0-only

// Port of metadata/custom-rule cases in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/{StackTests,ExtraTests}.cs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.StackTests.TestDirectories, TestMixed, and TestMultiDiscs
// through the resolved-metadata public seam.
func TestUpstreamVideoMetadataStacksFull(t *testing.T) {
	cases := []struct {
		method    string
		infos     []naming.VideoFileInfo
		count     int
		names     map[string]int
		directory map[string]bool
	}{
		{"TestDirectories", []naming.VideoFileInfo{{Path: "blah blah - cd 1", IsDirectory: true}, {Path: "blah blah - cd 2", IsDirectory: true}}, 1, map[string]int{"blah blah": 2}, map[string]bool{"blah blah": true}},
		{"TestMixed", []naming.VideoFileInfo{{Path: "Bad Boys (2006) part1.mkv"}, {Path: "Bad Boys (2006) part2.mkv"}, {Path: "300 (2006) part2", IsDirectory: true}, {Path: "300 (2006) part3", IsDirectory: true}, {Path: "300 (2006) part1", IsDirectory: true}}, 2, map[string]int{"300 (2006)": 3, "Bad Boys (2006)": 2}, map[string]bool{"300 (2006)": true, "Bad Boys (2006)": false}},
		{"TestMultiDiscs", []naming.VideoFileInfo{{Path: "M:/Movies (DVD)/Movies (Musical)/The Sound of Music/The Sound of Music (1965) (Disc 01)", IsDirectory: true}, {Path: "M:/Movies (DVD)/Movies (Musical)/The Sound of Music/The Sound of Music (1965) (Disc 02)", IsDirectory: true}}, 1, map[string]int{"The Sound of Music (1965)": 2}, map[string]bool{"The Sound of Music (1965)": true}},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveVideoInfoStacks(t, tt.infos, naming.DefaultOptions())
			if len(got) != tt.count {
				t.Fatalf("ResolveVideoInfoStacks(%s) = %#v, want %d stacks", tt.method, got, tt.count)
			}
			for name, count := range tt.names {
				found := false
				for _, stack := range got {
					if stack.Name == name && len(stack.Files) == count && stack.IsDirectoryStack == tt.directory[name] {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("ResolveVideoInfoStacks(%s) missing stack %q/%d/directory=%t: %#v", tt.method, name, count, tt.directory[name], got)
				}
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.VideoListResolverTests.TestDirectoryStack.
func TestUpstreamVideoDirectoryStackMembership(t *testing.T) {
	stack := naming.FileStack{Files: []string{"XX"}, IsDirectoryStack: false}
	if stack.ContainsFile("XX", true) {
		t.Fatal("FileStack.ContainsFile(XX, directory=true) = true, want false for a file stack")
	}
}

// Jellyfin.Naming.Tests.Video.ExtraTests.TestExtraInfo_InvalidRuleType.
func TestUpstreamVideoCustomRegexExtraRule(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `([eE]x(tra)?\.\w+)`}}
	got, err := naming.ResolveExtraWithError("extra.mp4", "", opts)
	if err != nil {
		t.Fatalf("ResolveExtraWithError custom regex error = %v", err)
	}
	if got == nil || got.ExtraType != naming.ExtraUnknown {
		t.Fatalf("ResolveExtraWithError custom regex = %#v, want Unknown extra", got)
	}
}
