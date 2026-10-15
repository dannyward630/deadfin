// SPDX-License-Identifier: GPL-2.0-only

// Configured non-default rule content is consumed by VideoResolver.cs:146.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoCleanStringHonorsCustomRuleContent(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes = []string{`^(?<cleaned>.+?)ZZ$`}
	if got, ok := naming.CleanString("TitleZZ", opts); !ok || got != "Title" {
		t.Fatalf("CleanString custom rule = (%q, %t), want (Title, true)", got, ok)
	}
	if got, ok := naming.CleanString("Movie 1080p", opts); ok || got != "" {
		t.Fatalf("CleanString without matching custom rule = (%q, %t), want (empty, false)", got, ok)
	}
}
