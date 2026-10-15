// SPDX-License-Identifier: GPL-2.0-only

// Exact parser boundaries from upstream Format3DParser.cs:50-59 and ExtraRuleResolver.cs:13,37.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoFormatThreeDimensionalTerminalSliceRetainsPrecedingToken(t *testing.T) {
	got := naming.Parse3D("hsbsX", naming.DefaultOptions())
	if !got.Is3D || got.Format != "hsbs" {
		t.Fatalf("Parse3D hsbsX = %#v, want hsbs three-dimensional match", got)
	}
}

func TestUpstreamVideoExtraDoesNotTrimUnicodeDecimalSuffix(t *testing.T) {
	got, err := naming.ResolveExtraWithError("Movie-trailer٢.mp4", "", naming.DefaultOptions())
	if err != nil {
		t.Fatalf("ResolveExtraWithError Unicode decimal suffix error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveExtraWithError Unicode decimal suffix = %#v, want no extra", got)
	}
}
