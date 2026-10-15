// SPDX-License-Identifier: GPL-2.0-only

// Iterative configured-regex behavior from upstream CleanStringParser.cs:27-48.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoCleanStringIteratesAndTrimsCustomRules(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes = []string{
		`^(?<cleaned>.+?)ZZ$`,
		`^(?<cleaned>.+?)YY$`,
	}
	if got, ok := naming.CleanString("TitleYYZZ", opts); !ok || got != "Title" {
		t.Fatalf("CleanString iterative custom rules = (%q, %t), want (Title, true)", got, ok)
	}
	opts.CleanStringRegexes = []string{`^(?<cleaned>\s*.+?\s*)ZZ$`}
	if got, ok := naming.CleanString("  Title  ZZ", opts); !ok || got != "Title" {
		t.Fatalf("CleanString custom capture trim = (%q, %t), want (Title, true)", got, ok)
	}
}
