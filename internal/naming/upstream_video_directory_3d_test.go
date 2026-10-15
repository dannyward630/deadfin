// SPDX-License-Identifier: GPL-2.0-only

// Resolver metadata ordering from upstream Emby.Naming/Video/VideoResolver.cs:72,93-105.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoDirectoryPreservesThreeDimensionalMetadata(t *testing.T) {
	got := mustResolveVideoDirectory(t, "Movie.3d.hsbs.mkv", naming.DefaultOptions(), true, "")
	if got == nil || !got.IsDirectory || !got.Is3D || got.Format3D != "hsbs" {
		t.Fatalf("ResolveVideoDirectory 3D metadata = %#v, want directory Is3D hsbs", got)
	}
}
