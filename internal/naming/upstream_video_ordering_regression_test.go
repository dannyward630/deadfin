// SPDX-License-Identifier: GPL-2.0-only

// Regression boundaries from upstream VideoListResolver.cs:286-300 and StackResolver.cs:67-69,124-130.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoVersionsSortResolutionsNumericallyDescending(t *testing.T) {
	paths := []string{
		"/TV/Show/Season 1/Show - S01E01 - 720p.mkv",
		"/TV/Show/Season 1/Show - S01E01 - 2160p.mkv",
		"/TV/Show/Season 1/Show - S01E01 - 1080p.mkv",
	}
	got := mustResolveVideoListForCollection(t, paths, naming.DefaultOptions(), "tvshows")
	if len(got) != 1 || len(got[0].AlternateVersions) != 2 {
		t.Fatalf("ResolveVideoListForCollection resolution group = %#v, want one primary and two alternates", got)
	}
	want := []string{paths[1], paths[2], paths[0]}
	actual := []string{got[0].Files[0].Path, got[0].AlternateVersions[0].Files[0].Path, got[0].AlternateVersions[1].Files[0].Path}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("resolution order = %q, want %q", actual, want)
	}
}

func TestUpstreamVideoStackKeepsLexicalPartInsertionOrder(t *testing.T) {
	paths := []string{"Movie part10.mkv", "Movie part2.mkv"}
	got := mustResolveStacks(t, paths, naming.DefaultOptions())
	if len(got) != 1 || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveStacks(%q) = %#v, want lexical insertion order %q", paths, got, paths)
	}
}
