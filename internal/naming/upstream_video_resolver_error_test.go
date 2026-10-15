// SPDX-License-Identifier: GPL-2.0-only

// Extra-rule error propagation through video resolver APIs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoResolversSurfaceInvalidExtraRuleErrors(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`}}

	file, fileErr := naming.ResolveVideoWithError("Movie.mkv", opts)
	if fileErr == nil || file != nil {
		t.Fatalf("ResolveVideoWithError invalid extra rule = (%#v, %v), want (nil, error)", file, fileErr)
	}

	directory, directoryErr := naming.ResolveVideoDirectoryWithError("Movie", opts, true, "")
	if directoryErr != nil || directory == nil || !directory.IsDirectory || directory.Name != "Movie" {
		t.Fatalf("ResolveVideoDirectoryWithError bypasses extra rules = (%#v, %v), want Movie directory without error", directory, directoryErr)
	}
}
