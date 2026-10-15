// SPDX-License-Identifier: GPL-2.0-only

// Exact source boundaries from StackResolver.cs:86-120, VideoListResolver.cs primary-override loop, and cleaner parsers.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoStackPartTypesHonorIgnoreCase(t *testing.T) {
	paths := []string{"Film-CD1.mkv", "Film-cd2.mkv"}
	got := mustResolveStacks(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 2 {
		t.Fatalf("ResolveStacks case-variant part types = %#v, want one two-file stack", got)
	}
}

func TestUpstreamVideoListUsesLastExactBasenamePrimary(t *testing.T) {
	paths := []string{
		"/movies/Movie/Movie.mkv",
		"/movies/Movie/Movie-720p.mkv",
		"/movies/Movie/Movie.mp4",
	}
	got := mustResolveVideoList(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Path != paths[2] {
		t.Fatalf("ResolveVideoList exact-basename primary = %#v, want final exact basename %q", got, paths[2])
	}
}

func TestUpstreamVideoDefaultCleanerStripsSampleSuffix(t *testing.T) {
	if got, ok := naming.CleanString("Movie-sample", naming.DefaultOptions()); !ok || got != "Movie" {
		t.Fatalf("CleanString Movie-sample = (%q, %t), want (Movie, true)", got, ok)
	}
}

func TestUpstreamVideoCleanDateTimeRequiresExactlyFiveCaptures(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanDateTimeRegexes = []string{`^(.*)ZZ(2024)()$`}
	got := naming.CleanDateTime("TitleZZ2024", opts)
	if got.Name != "TitleZZ2024" || got.Year != nil {
		t.Fatalf("CleanDateTime four-capture custom rule = %#v, want unchanged/no-year", got)
	}
}
