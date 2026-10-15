// SPDX-License-Identifier: GPL-2.0-only

// Exact grouping boundaries from upstream StackResolver.cs:86-120 and VideoListResolver.cs:34-35,275-295.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoStackRulesContinueAfterDuplicateConflict(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoFileStackingRules = []naming.FileStackRule{
		{Token: `^(?<filename>Movie) [ab](?<number>1)\.mkv$`, IsNumerical: true},
		{Token: `^(?<filename>Movie) (?<number>b)1\.mkv$`, IsNumerical: false},
	}
	paths := []string{"Movie a1.mkv", "Movie b1.mkv"}
	got := mustResolveStacks(t, paths, opts)
	if len(got) != 1 || got[0].Name != "Movie" || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveStacks later rule after duplicate = %#v, want Movie stack %q", got, paths)
	}
}

func TestUpstreamVideoVersionsOrderArbitraryLengthResolutionNumerically(t *testing.T) {
	paths := []string{
		"/movies/Movie/Movie-720p.mkv",
		"/movies/Movie/Movie-999999999999999999999p.mkv",
	}
	got := mustResolveVideoList(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Path != paths[1] {
		t.Fatalf("ResolveVideoList arbitrary-length resolution primary = %#v, want %q", got, paths[1])
	}
}
