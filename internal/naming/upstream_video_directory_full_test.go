// SPDX-License-Identifier: GPL-2.0-only

// Port of upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/VideoResolverTests.cs:172-191.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.VideoResolverTests.ResolveDirectoryTest (3 active paths).
func TestUpstreamVideoResolveDirectoriesFull(t *testing.T) {
	for _, tt := range []struct {
		path     string
		resolved bool
	}{{"/Server/Iron Man", true}, {"Batman", true}, {"", false}} {
		t.Run(tt.path, func(t *testing.T) {
			got := mustResolveVideoDirectory(t, tt.path, naming.DefaultOptions(), true, "")
			if (got != nil) != tt.resolved {
				t.Fatalf("ResolveVideoDirectory(%q) = %#v, want resolved=%t", tt.path, got, tt.resolved)
			}
			if got != nil && (got.Container != "" || !got.IsDirectory) {
				t.Fatalf("ResolveVideoDirectory(%q) = %#v, want directory with no container", tt.path, got)
			}
		})
	}
}
