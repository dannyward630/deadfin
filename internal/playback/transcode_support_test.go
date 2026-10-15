// SPDX-License-Identifier: GPL-2.0-only

package playback

import "testing"

// TestEncodingVideoStreamCarriesReferenceFrameRate checks that the HLS GOP
// builder gets the average rate beside RealFrameRate (D-62).
func TestEncodingVideoStreamCarriesReferenceFrameRate(t *testing.T) {
	real, average, broken := float32(47.952), float32(23.976025), float32(90000)
	for _, test := range []struct {
		name          string
		real, average *float32
		want          float64
	}{
		{"average", &real, &average, float64(average)},
		{"broken average falls back to RealFrameRate", &real, &broken, float64(real)},
		{"no average falls back to RealFrameRate", &real, nil, float64(real)},
		{"no rate", nil, nil, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := encodingVideoStream(&MediaStream{Type: StreamTypeVideo, RealFrameRate: test.real, AverageFrameRate: test.average})
			if stream.ReferenceFrameRate != test.want {
				t.Errorf("ReferenceFrameRate = %v, want %v", stream.ReferenceFrameRate, test.want)
			}
		})
	}
}
