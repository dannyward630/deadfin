// SPDX-License-Identifier: GPL-2.0-only

// Invalid custom regex propagation from upstream ExtraRuleResolver.cs:43-56.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoExtraInvalidCustomRegexSurfacesError(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`}}
	got, err := naming.ResolveExtraWithError("Movie-extra.mp4", "", opts)
	if err == nil {
		t.Fatalf("ResolveExtraWithError invalid regex = (%#v, nil), want regex error", got)
	}
	if got != nil {
		t.Fatalf("ResolveExtraWithError invalid regex result = %#v, want nil", got)
	}
}
