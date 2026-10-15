// SPDX-License-Identifier: GPL-2.0-only

// Port of metadata-list case in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/VideoListResolverTests.cs:196-209.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.VideoListResolverTests.TestMultiDisc.
func TestUpstreamVideoMetadataListMultiDisc(t *testing.T) {
	opts := naming.DefaultOptions()
	paths := []string{
		"M:/Movies (DVD)/Movies (Musical)/Sound of Music (1965)/Sound of Music Disc 1",
		"M:/Movies (DVD)/Movies (Musical)/Sound of Music (1965)/Sound of Music Disc 2",
	}
	infos := make([]naming.VideoFileInfo, 0, len(paths))
	for _, path := range paths {
		info := mustResolveVideoDirectory(t, path, opts, true, "")
		if info == nil {
			t.Fatalf("ResolveVideoDirectory(%q) = nil", path)
		}
		infos = append(infos, *info)
	}
	got := mustResolveVideoInfoList(t, infos, opts, "")
	if len(got) != 1 || len(got[0].Files) != 2 || !got[0].Files[0].IsDirectory || !got[0].Files[1].IsDirectory {
		t.Fatalf("ResolveVideoInfoList(directory multi-disc) = %#v, want one two-directory item", got)
	}
}
