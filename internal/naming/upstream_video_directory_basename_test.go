// SPDX-License-Identifier: GPL-2.0-only

// Regression boundary from upstream Emby.Naming/Video/VideoResolver.cs:79-83.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoDirectoryUsesNameWithoutExtensionWhenParseDisabled(t *testing.T) {
	got := mustResolveVideoDirectory(t, "Movie.mkv", naming.DefaultOptions(), false, "")
	if got == nil || got.Name != "Movie" || !got.IsDirectory || got.Container != "" {
		t.Fatalf("ResolveVideoDirectory parse-disabled basename = %#v, want directory named Movie without container", got)
	}
}
