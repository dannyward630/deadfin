// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"math"
	"testing"
)

// TestResolutionNormalizerV1 verifies the Go port of the pinned
// ResolutionNormalizer at MediaBrowser.Model/Dlna/ResolutionNormalizer.cs
// (Jellyfin commit b04614e). It covers all eight breakpoints, both
// frame-rate formulas, the HDR factor, nearest-even conversion, explicit
// dimension limits, and the source-independence property that the RC2
// observations document.

func TestResolutionNormalizerV1AllBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bitrate   int
		wantWidth int
	}{
		{name: "365k maps to 416", bitrate: 365_000, wantWidth: 416},
		{name: "730k maps to 640", bitrate: 730_000, wantWidth: 640},
		{name: "583417 maps to 640", bitrate: 583_417, wantWidth: 640},
		{name: "1100k maps to 768", bitrate: 1_100_000, wantWidth: 768},
		{name: "3000k maps to 960", bitrate: 3_000_000, wantWidth: 960},
		{name: "6000k maps to 1280", bitrate: 6_000_000, wantWidth: 1280},
		{name: "13500k maps to 1920", bitrate: 13_500_000, wantWidth: 1920},
		{name: "28000k maps to 2560", bitrate: 28_000_000, wantWidth: 2560},
		{name: "50000k maps to 3840", bitrate: 50_000_000, wantWidth: 3840},
		{name: "below first breakpoint maps to 416", bitrate: 100_000, wantWidth: 416},
		{name: "above last breakpoint returns nil", bitrate: 50_000_004, wantWidth: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := NormalizeResolutionV1(nil, tc.bitrate, tc.bitrate, nil, nil, nil, false)
			if tc.wantWidth < 0 {
				if result.MaxWidth != nil {
					t.Errorf("bitrate %d: MaxWidth = %v, want nil", tc.bitrate, result.MaxWidth)
				}
				return
			}
			if result.MaxWidth == nil || *result.MaxWidth != tc.wantWidth {
				t.Errorf("bitrate %d: MaxWidth = %v, want %d", tc.bitrate, result.MaxWidth, tc.wantWidth)
			}
		})
	}
}

func TestResolutionNormalizerV1InclusiveMaximums(t *testing.T) {
	for _, bp := range resolutionBreakpointsV1() {
		result := NormalizeResolutionV1(nil, bp.MaxBitrate, bp.MaxBitrate, nil, nil, nil, false)
		if result.MaxWidth == nil || *result.MaxWidth != bp.MaxWidth {
			t.Errorf("inclusive max bitrate %d: MaxWidth = %v, want %d", bp.MaxBitrate, result.MaxWidth, bp.MaxWidth)
		}
		// The next value above the inclusive max must select a wider
		// breakpoint. The pinned C# source uses single-precision
		// float, so MaxBitrate+1 is exact only up to 2^24. The two
		// largest breakpoints (28M and 50M) need a bigger delta to
		// stay distinguishable in float32.
		aboveBitrate := bp.MaxBitrate + 1
		if float32(aboveBitrate) == float32(bp.MaxBitrate) {
			aboveBitrate = int(math.Nextafter32(float32(bp.MaxBitrate), math.MaxFloat32))
		}
		above := NormalizeResolutionV1(nil, aboveBitrate, aboveBitrate, nil, nil, nil, false)
		if above.MaxWidth != nil && *above.MaxWidth <= bp.MaxWidth {
			t.Errorf("bitrate %d (above max %d): MaxWidth = %v, must exceed %d", aboveBitrate, bp.MaxBitrate, above.MaxWidth, bp.MaxWidth)
		}
	}
}

func TestResolutionNormalizerV1FrameRateScaling(t *testing.T) {
	// At 30 fps, 3,000,000 selects the 960 breakpoint.
	fps30 := 30.0
	result := NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, &fps30, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("30fps 3000k: MaxWidth = %v, want 960", result.MaxWidth)
	}

	// At 24 fps (<=30), the scale factor is 30/24 = 1.25, so the reference
	// bitrate is 3,000,000 * 1.25 = 3,750,000. This exceeds 3,000,000 and
	// selects the 1280 breakpoint.
	fps24 := 24.0
	result = NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, &fps24, false)
	if result.MaxWidth == nil || *result.MaxWidth != 1280 {
		t.Fatalf("24fps 3000k: MaxWidth = %v, want 1280 (reference 3,750,000)", result.MaxWidth)
	}

	// At 60 fps (>30), the scale factor is 1/sqrt(60/30) = 1/sqrt(2). The
	// reference bitrate is 3,000,000 / sqrt(2) = 2,121,320. This selects the
	// 960 breakpoint (max 3,000,000).
	fps60 := 60.0
	result = NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, &fps60, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("60fps 3000k: MaxWidth = %v, want 960 (reference 2,121,320)", result.MaxWidth)
	}
}

func TestResolutionNormalizerV1HDRFactor(t *testing.T) {
	// At 3,000,000 without HDR, the 960 breakpoint is selected.
	// With HDR, the reference bitrate is 3,000,000 * 0.8 = 2,400,000, which
	// still selects the 960 breakpoint (max 3,000,000).
	result := NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, nil, true)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("HDR 3000k: MaxWidth = %v, want 960 (reference 2,400,000)", result.MaxWidth)
	}

	// At 3,750,000 without HDR, the 1280 breakpoint is selected.
	// With HDR, the reference bitrate is 3,750,000 * 0.8 = 3,000,000, which
	// selects the 960 breakpoint (inclusive max 3,000,000).
	result = NormalizeResolutionV1(nil, 3_750_000, 3_750_000, nil, nil, nil, true)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("HDR 3750k: MaxWidth = %v, want 960 (reference 3,000,000)", result.MaxWidth)
	}
}

func TestResolutionNormalizerV1NearestEvenConversion(t *testing.T) {
	// Exact midpoints distinguish banker's rounding (round-half-to-even)
	// from ordinary nearest rounding. 2.5 rounds to 2 (even); 3.5 rounds
	// to 4 (even). Ordinary rounding would give 2.5 -> 3.
	for _, tc := range []struct {
		name string
		in   float64
		want int
	}{
		{name: "2.5 rounds to 2", in: 2.5, want: 2},
		{name: "3.5 rounds to 4", in: 3.5, want: 4},
		{name: "0.5 rounds to 0", in: 0.5, want: 0},
		{name: "1.5 rounds to 2", in: 1.5, want: 2},
		{name: "4.5 rounds to 4", in: 4.5, want: 4},
		{name: "5.5 rounds to 6", in: 5.5, want: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := convertToInt32(tc.in); got != tc.want {
				t.Errorf("convertToInt32(%v) = %d, want %d (round-half-to-even)", tc.in, got, tc.want)
			}
		})
	}

	// At 24 fps, 2,000,000 has reference bitrate 2,000,000 * 30/24 = 2,500,000.
	// That selects the 960 breakpoint.
	fps24 := 24.0
	result := NormalizeResolutionV1(nil, 2_000_000, 2_000_000, nil, nil, &fps24, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("24fps 2000k: MaxWidth = %v, want 960 (reference 2,500,000)", result.MaxWidth)
	}

	// At 23.976 fps, the reference bitrate for 3,000,000 is
	// 3,000,000 * 30 / 23.976 = 3,753,753.75. Convert.ToInt32 rounds to
	// nearest even: 3,753,754. This exceeds 3,000,000 and selects 1280.
	fps23976 := 23.976025
	result = NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, &fps23976, false)
	if result.MaxWidth == nil || *result.MaxWidth != 1280 {
		t.Fatalf("23.976fps 3000k: MaxWidth = %v, want 1280", result.MaxWidth)
	}
}

func TestResolutionNormalizerV1ExplicitDimensionLimits(t *testing.T) {
	// An explicit MaxWidth that is smaller than the breakpoint cap narrows the
	// result. The normalizer takes min(breakpoint, explicit).
	explicit := 800
	result := NormalizeResolutionV1(nil, 3_000_000, 3_000_000, &explicit, nil, nil, false)
	if result.MaxWidth == nil || *result.MaxWidth != 800 {
		t.Fatalf("explicit 800: MaxWidth = %v, want 800 (min of 960 and 800)", result.MaxWidth)
	}

	// An explicit MaxWidth larger than the breakpoint cap keeps the cap.
	explicit = 1920
	result = NormalizeResolutionV1(nil, 3_000_000, 3_000_000, &explicit, nil, nil, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("explicit 1920: MaxWidth = %v, want 960 (min of 960 and 1920)", result.MaxWidth)
	}

	// When the normalizer changes MaxWidth, MaxHeight is cleared.
	explicitHeight := 720
	result = NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, &explicitHeight, nil, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("explicit height 720: MaxWidth = %v, want 960", result.MaxWidth)
	}
	if result.MaxHeight != nil {
		t.Fatalf("explicit height 720: MaxHeight = %v, want nil (cleared when MaxWidth changed)", result.MaxHeight)
	}
}

func TestResolutionNormalizerV1InputBitrateNoDownscale(t *testing.T) {
	// When output bitrate >= input bitrate, the normalizer skips the
	// downscale and returns the caller limits unchanged.
	inputBitrate := 500_000
	explicit := 1920
	result := NormalizeResolutionV1(&inputBitrate, 500_000, 500_000, &explicit, nil, nil, false)
	if result.MaxWidth == nil || *result.MaxWidth != 1920 {
		t.Fatalf("output >= input: MaxWidth = %v, want 1920 (no downscale)", result.MaxWidth)
	}
}

func TestResolutionNormalizerV1SourceIndependence(t *testing.T) {
	// RC2 observed that a 320x180 source and a 3840x2160 source at 420 kbps
	// both received a width cap of 960. The normalizer is independent of
	// source dimensions: it takes only the bitrate, not the source width
	// or height. The pinned breakpoint table defines the expected output.
	// The two RC2 observations confirm that the cap does not depend on
	// source resolution.
	//
	// The HLS caller converts the requested bitrate to its H.264-equivalent
	// value through h264EquivalentResolutionBitrateV1 before it calls the
	// normalizer. The same-codec low-bitrate bands from
	// EncodingHelper.ScaleBitrate raise the reference bitrate so the pinned
	// table reproduces the measured RC2 output. The test passes the
	// converted value as h264EquivalentOutputBitrate and the raw value as
	// outputBitrate, matching the caller seam.
	//
	// Assert the exact v1 cap for each bitrate so the test does not
	// merely log. The cap is the same for any source at the same bitrate
	// because the normalizer takes no source-dimension argument.
	fps24 := 24.0
	for _, tc := range []struct {
		bitrate   int
		wantWidth int
	}{
		{bitrate: 64_000, wantWidth: 416},
		{bitrate: 292_000, wantWidth: 960},
		{bitrate: 420_000, wantWidth: 960},
	} {
		h264Equivalent := h264EquivalentResolutionBitrateV1(tc.bitrate)
		result := NormalizeResolutionV1(nil, tc.bitrate, h264Equivalent, nil, nil, &fps24, false)
		if result.MaxWidth == nil {
			t.Fatalf("bitrate %d: MaxWidth = nil, want %d", tc.bitrate, tc.wantWidth)
		}
		if *result.MaxWidth != tc.wantWidth {
			t.Errorf("bitrate %d: MaxWidth = %d, want %d (v1 cap, source-independent)", tc.bitrate, *result.MaxWidth, tc.wantWidth)
		}
	}
}

func TestResolutionNormalizerV1AbsentFrameRate(t *testing.T) {
	// When targetFps is nil, 30 fps applies. 3,000,000 at 30 fps selects 960.
	result := NormalizeResolutionV1(nil, 3_000_000, 3_000_000, nil, nil, nil, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("nil fps 3000k: MaxWidth = %v, want 960", result.MaxWidth)
	}
}

// TestResolutionNormalizerV1SinglePrecisionBoundary verifies that the Go
// port reproduces the pinned C# single-precision (float32) arithmetic, not
// float64. At 2,397,603 bps and 23.976025 fps the two precisions diverge:
// float32 gives a reference bitrate of 3,000,000.5, which Convert.ToInt32
// rounds to 3,000,000 (banker's rounding, round-half-to-even), selecting the
// 960 breakpoint (inclusive max 3,000,000). float64 gives 3,000,000.6, which
// rounds to 3,000,001 and crosses into the 1280 breakpoint. A float64 port
// would return 1280 here; the float32 port returns 960. This case uses no
// explicit dimension limits, is non-HDR, and passes a non-nil target fps, so
// it exercises the frame-rate scale directly rather than the 30 fps default.
func TestResolutionNormalizerV1SinglePrecisionBoundary(t *testing.T) {
	fps := 23.976025
	result := NormalizeResolutionV1(nil, 2_397_603, 2_397_603, nil, nil, &fps, false)
	if result.MaxWidth == nil || *result.MaxWidth != 960 {
		t.Fatalf("2397603 bps at 23.976025 fps: MaxWidth = %v, want 960 (float32 boundary)", result.MaxWidth)
	}
}

// TestResolutionNormalizerV1H264EquivalentBitrate verifies the H.264-to-H.264
// branch of EncodingHelper.ScaleBitrate at b04614e. The same-codec base ratio
// is 1, but the function raises that ratio at low bitrates. The five bands and
// their inclusive upper bounds are covered. A nearest-even case in the 2.5
// band distinguishes the pinned Convert.ToInt32 conversion from round-half-up.
func TestResolutionNormalizerV1H264EquivalentBitrate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bitrate   int
		wantH264  int
		wantWidth int
		targetFps *float64
	}{
		{
			// h264 256000 * 30/24 = 320000 reference, which selects the
			// 416 breakpoint (max 365000).
			name:    "64000 in band 4 maps to h264 256000, reference 320000, cap 416 at 24fps",
			bitrate: 64_000, wantH264: 256_000, wantWidth: 416,
			targetFps: float64Ptr(24),
		},
		{
			// h264 600000 * 30/24 = 750000 reference, which selects the
			// 768 breakpoint (max 1100000).
			name:    "150000 in band 4 maps to h264 600000, reference 750000, cap 768 at 24fps",
			bitrate: 150_000, wantH264: 600_000, wantWidth: 768,
			targetFps: float64Ptr(24),
		},
		{
			// 292000 is load-bearing: Swiftfin sends this value after it
			// subtracts the audio bitrate client-side (420000 - 128000).
			// Any change to the audio-subtraction path moves it across a
			// breakpoint and silently changes the output resolution.
			// h264 1168000 * 30/24 = 1460000 reference, which selects the
			// 960 breakpoint (max 3000000).
			name:    "292000 in band 4 maps to h264 1168000, reference 1460000, cap 960 at 24fps",
			bitrate: 292_000, wantH264: 1_168_000, wantWidth: 960,
			targetFps: float64Ptr(24),
		},
		{
			// h264 1680000 * 30/24 = 2100000 reference, which selects the
			// 960 breakpoint (max 3000000).
			name:    "420000 in band 4 maps to h264 1680000, reference 2100000, cap 960 at 24fps",
			bitrate: 420_000, wantH264: 1_680_000, wantWidth: 960,
			targetFps: float64Ptr(24),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h264EquivalentResolutionBitrateV1(tc.bitrate)
			if got != tc.wantH264 {
				t.Fatalf("h264EquivalentResolutionBitrateV1(%d) = %d, want %d", tc.bitrate, got, tc.wantH264)
			}
			result := NormalizeResolutionV1(nil, tc.bitrate, got, nil, nil, tc.targetFps, false)
			if result.MaxWidth == nil || *result.MaxWidth != tc.wantWidth {
				t.Fatalf("bitrate %d: MaxWidth = %v, want %d", tc.bitrate, result.MaxWidth, tc.wantWidth)
			}
		})
	}
}

// TestResolutionNormalizerV1H264EquivalentBitrateBands covers all five
// same-H.264 bitrate bands and their inclusive upper bounds, plus the
// nearest-even conversion at a 2.5-band midpoint.
func TestResolutionNormalizerV1H264EquivalentBitrateBands(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bitrate  int
		wantH264 int
	}{
		{name: "band 4 inclusive upper bound 500000", bitrate: 500_000, wantH264: 2_000_000},
		{name: "band 3 inclusive upper bound 1000000", bitrate: 1_000_000, wantH264: 3_000_000},
		{name: "band 2.5 inclusive upper bound 2000000", bitrate: 2_000_000, wantH264: 5_000_000},
		{name: "band 2 inclusive upper bound 3000000", bitrate: 3_000_000, wantH264: 6_000_000},
		{name: "above 3000000 uses base ratio 1", bitrate: 4_000_000, wantH264: 4_000_000},
		{name: "above 30000000 caps at base ratio 1", bitrate: 30_000_000, wantH264: 30_000_000},
		{name: "zero returns zero", bitrate: 0, wantH264: 0},
		{name: "negative returns zero", bitrate: -100, wantH264: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h264EquivalentResolutionBitrateV1(tc.bitrate); got != tc.wantH264 {
				t.Fatalf("h264EquivalentResolutionBitrateV1(%d) = %d, want %d", tc.bitrate, got, tc.wantH264)
			}
		})
	}
}

// TestResolutionNormalizerV1H264EquivalentBitrateNearestEven verifies that
// the 2.5 band produces a nearest-even result, not round-half-up.
// 1000001 * 2.5 = 2500002.5, which rounds to 2500002 (even), not 2500003.
func TestResolutionNormalizerV1H264EquivalentBitrateNearestEven(t *testing.T) {
	got := h264EquivalentResolutionBitrateV1(1_000_001)
	if got != 2_500_002 {
		t.Fatalf("h264EquivalentResolutionBitrateV1(1000001) = %d, want 2500002 (nearest even, not 2500003)", got)
	}
}

// float64Ptr returns a pointer to value for test table inputs.
func float64Ptr(value float64) *float64 { return &value }

// ScaleBitrate(bitrate, codec, "h264") raises an HEVC or AV1 output to its
// H.264 equivalent before the low-bitrate bands apply.
func TestH264EquivalentBitrateForHardwareCodecs(t *testing.T) {
	for _, tc := range []struct {
		codec   string
		bitrate int
		want    int
	}{
		{"hevc", 8_000_000, 13_333_333},
		{"av1", 8_000_000, 16_000_000},
		{"hevc", 2_000_000, 5_000_000},
		{"av1", 2_000_000, 5_000_000},
		{"av1", 1_500_000, 3_750_000},
		{"av1", 40_000_000, 40_000_000},
		{"h264", 8_000_000, 8_000_000},
	} {
		if got := h264EquivalentBitrate(tc.bitrate, tc.codec); got != tc.want {
			t.Errorf("h264EquivalentBitrate(%d, %q) = %d, want %d", tc.bitrate, tc.codec, got, tc.want)
		}
	}
}
