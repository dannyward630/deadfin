// SPDX-License-Identifier: GPL-2.0-only

// Regression boundary from VideoListResolver.cs:288-295: Regex.Match selects the first resolution token.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoVersionsRankFirstResolutionToken(t *testing.T) {
	paths := []string{
		"/movies/Movie/Movie.mkv",
		"/movies/Movie/Movie - 720p source2160p.mkv",
		"/movies/Movie/Movie - 1080p.mkv",
	}
	got := mustResolveVideoList(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].AlternateVersions) != 2 {
		t.Fatalf("ResolveVideoList first-resolution group = %#v, want one primary and two alternates", got)
	}
	want := []string{paths[0], paths[2], paths[1]}
	actual := []string{got[0].Files[0].Path, got[0].AlternateVersions[0].Files[0].Path, got[0].AlternateVersions[1].Files[0].Path}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("first-resolution rank order = %q, want %q", actual, want)
	}
}
