// SPDX-License-Identifier: GPL-2.0-only

package hls_test

// Source vectors below are ported from Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// Jellyfin.MediaEncoding.Hls/Playlist/DynamicHlsPlaylistGenerator.cs:188-214 and
// DynamicHlsPlaylistGeneratorTests.cs:31-44,63-75.

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

const sourceTicksPerMillisecond int64 = 10_000

// TestSegmentLengthsMatchesPinnedEqualLengthVectors defends HLS VOD segment
// boundaries: whole segments retain the requested duration and the final
// segment exposes the exact source remainder to the playlist client.
func TestSegmentLengthsMatchesPinnedEqualLengthVectors(t *testing.T) {
	for _, tc := range []struct {
		name             string
		runtimeTicks     int64
		segmentLengthSec int
		want             []float64
	}{
		{
			name:             "13 seconds at 6 seconds retains one-second remainder",
			runtimeTicks:     sourceMillisecondsToTicks(13_000),
			segmentLengthSec: 6,
			want:             []float64{6, 6, 1},
		},
		{
			name:             "15 seconds at 3 seconds has only equal segments",
			runtimeTicks:     sourceMillisecondsToTicks(15_000),
			segmentLengthSec: 3,
			want:             []float64{3, 3, 3, 3, 3},
		},
		{
			name:             "25 seconds at 6 seconds retains one-second remainder",
			runtimeTicks:     sourceMillisecondsToTicks(25_000),
			segmentLengthSec: 6,
			want:             []float64{6, 6, 6, 6, 1},
		},
		{
			name:             "20.123 seconds preserves millisecond remainder",
			runtimeTicks:     sourceMillisecondsToTicks(20_123),
			segmentLengthSec: 6,
			want:             []float64{6, 6, 6, 2.123},
		},
		{
			name:             "runtime shorter than segment is one fractional segment",
			runtimeTicks:     sourceMillisecondsToTicks(1_234),
			segmentLengthSec: 6,
			want:             []float64{1.234},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hls.SegmentLengths(tc.runtimeTicks, tc.segmentLengthSec)
			if err != nil {
				t.Fatalf("SegmentLengths(%d, %d): %v", tc.runtimeTicks, tc.segmentLengthSec, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SegmentLengths(%d, %d) = %#v, want %#v", tc.runtimeTicks, tc.segmentLengthSec, got, tc.want)
			}
		})
	}
}

// TestSegmentLengthsRejectsNonPositiveInputs prevents invalid VOD requests
// from producing a malformed playlist or reaching division by zero.
func TestSegmentLengthsRejectsNonPositiveInputs(t *testing.T) {
	for _, tc := range []struct {
		name             string
		runtimeTicks     int64
		segmentLengthSec int
	}{
		{
			name:             "zero segment duration",
			runtimeTicks:     sourceMillisecondsToTicks(13_000),
			segmentLengthSec: 0,
		},
		{
			name:             "zero runtime",
			runtimeTicks:     0,
			segmentLengthSec: 6,
		},
		{
			name:             "negative segment duration",
			runtimeTicks:     sourceMillisecondsToTicks(13_000),
			segmentLengthSec: -6,
		},
		{
			name:             "negative runtime",
			runtimeTicks:     -sourceMillisecondsToTicks(13_000),
			segmentLengthSec: 6,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := hls.SegmentLengths(tc.runtimeTicks, tc.segmentLengthSec); err == nil {
				t.Errorf("SegmentLengths(%d, %d) accepted invalid input", tc.runtimeTicks, tc.segmentLengthSec)
			}
		})
	}
}

func sourceMillisecondsToTicks(milliseconds int64) int64 {
	return milliseconds * sourceTicksPerMillisecond
}
