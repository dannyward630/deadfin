// SPDX-License-Identifier: GPL-2.0-only

// Resolution-versus-nonresolution ordering from upstream VideoListResolver.cs:286-300.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoVersionsPlaceResolutionBeforeNonresolutionAlternates(t *testing.T) {
	paths := []string{
		"/movies/Movie/Movie.mkv",
		"/movies/Movie/Movie-3d.mkv",
		"/movies/Movie/Movie-720p.mkv",
	}
	got := mustResolveVideoList(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].AlternateVersions) != 2 {
		t.Fatalf("ResolveVideoList resolution class group = %#v, want one primary and two alternates", got)
	}
	want := []string{paths[0], paths[2], paths[1]}
	actual := []string{got[0].Files[0].Path, got[0].AlternateVersions[0].Files[0].Path, got[0].AlternateVersions[1].Files[0].Path}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("resolution class order = %q, want %q", actual, want)
	}
}
