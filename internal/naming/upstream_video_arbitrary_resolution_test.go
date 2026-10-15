// SPDX-License-Identifier: GPL-2.0-only

// Regression boundary from upstream VideoListResolver.cs ResolutionRegex: [0-9]{2}[0-9]+[ip].
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoVersionsSortArbitraryNumericResolutions(t *testing.T) {
	paths := []string{
		"/TV/Show/Season 1/Show - S01E01 - 1080p.mkv",
		"/TV/Show/Season 1/Show - S01E01 - 1440p.mkv",
		"/TV/Show/Season 1/Show - S01E01 - 720p.mkv",
	}
	got := mustResolveVideoListForCollection(t, paths, naming.DefaultOptions(), "tvshows")
	if len(got) != 1 || len(got[0].AlternateVersions) != 2 {
		t.Fatalf("ResolveVideoListForCollection arbitrary resolutions = %#v, want one primary and two alternates", got)
	}
	want := []string{paths[1], paths[0], paths[2]}
	actual := []string{got[0].Files[0].Path, got[0].AlternateVersions[0].Files[0].Path, got[0].AlternateVersions[1].Files[0].Path}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("arbitrary resolution order = %q, want %q", actual, want)
	}
}
