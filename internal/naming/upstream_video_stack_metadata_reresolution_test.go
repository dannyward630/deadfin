// SPDX-License-Identifier: GPL-2.0-only

// Stack constituent re-resolution from upstream VideoListResolver.cs:78-89.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoListReResolvesStackConstituentMetadata(t *testing.T) {
	infos := []naming.VideoFileInfo{
		{Path: "/Movies/Movie (2000)/Movie (2000) part1.mkv", Name: "poison"},
		{Path: "/Movies/Movie (2000)/Movie (2000) part2.mkv", Name: "poison"},
	}
	got := mustResolveVideoInfoList(t, infos, naming.DefaultOptions(), "")
	if len(got) != 1 || len(got[0].Files) != 2 || got[0].Name != "Movie" || !reflect.DeepEqual(got[0].Year, intp(2000)) {
		t.Fatalf("ResolveVideoInfoList re-resolved stack = %#v, want Movie/2000 with two files", got)
	}
	for _, file := range got[0].Files {
		if file.Name != "Movie" || !reflect.DeepEqual(file.Year, intp(2000)) {
			t.Fatalf("stack constituent metadata = %#v, want path-resolved Movie/2000", file)
		}
	}
}
