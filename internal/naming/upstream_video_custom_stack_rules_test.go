// SPDX-License-Identifier: GPL-2.0-only

// Configurable FileStackRule behavior from upstream Emby.Naming/Video/FileStackRule.cs:18-46.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoDefaultStackRulesResolveStandardParts(t *testing.T) {
	paths := []string{"Movie part1.mkv", "Movie part2.mkv"}
	got := mustResolveStacks(t, paths, naming.DefaultOptions())
	if len(got) != 1 || got[0].Name != "Movie" || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveStacks default stack rules = %#v, want Movie with %q", got, paths)
	}
}

func TestUpstreamVideoCustomStackRulesReplaceDefaultRules(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoFileStackingRules = []naming.FileStackRule{{Token: `^(?<filename>.+) volume(?<number>[0-9]+)\.mkv$`, IsNumerical: true}}
	paths := []string{"Movie volume1.mkv", "Movie volume2.mkv"}
	got := mustResolveStacks(t, paths, opts)
	if len(got) != 1 || got[0].Name != "Movie" || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveStacks custom stack rule = %#v, want Movie with %q", got, paths)
	}
	if got := mustResolveStacks(t, []string{"Movie part1.mkv", "Movie part2.mkv"}, opts); len(got) != 0 {
		t.Fatalf("ResolveStacks custom stack rule retained default matching: %#v", got)
	}
}
