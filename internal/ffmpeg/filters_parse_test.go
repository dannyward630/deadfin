// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"reflect"
	"testing"
)

// FFmpeg 7.1 and later print two filter flag columns; earlier releases print
// three. EncoderValidator.FilterRegex accepts both (\S{2,3}).
func TestParseListedFiltersAcceptsTwoAndThreeFlagColumns(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
	}{
		{
			name: "FFmpeg 8 two columns",
			output: "Filters:\n  T.. = Timeline support\n  .S. = Slice threading\n  A = Audio input/output\n  | = Source or sink filter\n" +
				" .. alphasrc          |->V       Generate a canvas of transparent pixels.\n" +
				" .. overlay_qsv       VV->V      Quick Sync Video overlay.\n" +
				" .S tonemapx          V->V       SIMD optimized HDR to SDR conversion with tonemapping.\r\n",
		},
		{
			name: "FFmpeg 6 three columns",
			output: "Filters:\n  T.. = Timeline support\n  .S. = Slice threading\n  ..C = Command support\n" +
				" ... alphasrc          |->V       Generate a canvas of transparent pixels.\n" +
				" ... overlay_qsv       VV->V      Quick Sync Video overlay.\n" +
				" .S. tonemapx          V->V       SIMD optimized HDR to SDR conversion with tonemapping.\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := parseListedCapabilities(test.output, "-filters")
			want := []string{"alphasrc", "overlay_qsv", "tonemapx"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("filters = %q, want %q", got, want)
			}
		})
	}
}
