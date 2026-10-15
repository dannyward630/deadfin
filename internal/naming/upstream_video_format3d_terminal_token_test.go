// SPDX-License-Identifier: GPL-2.0-only

// Terminal-token quirk from upstream Emby.Naming/Video/Format3DParser.cs:55-59.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoFormatThreeDimensionalIgnoresUnsuffixedTerminalToken(t *testing.T) {
	for _, input := range []string{"hsbs", "Movie hsbs"} {
		t.Run(input, func(t *testing.T) {
			got := naming.Parse3D(input, naming.DefaultOptions())
			if got.Is3D || got.Format != "" {
				t.Fatalf("Parse3D(%q) = %#v, want no terminal-token match", input, got)
			}
		})
	}
}
