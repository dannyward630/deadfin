// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"math"
	"strings"
)

// ResolutionNormalizerV1 is the pinned Go port of
// MediaBrowser.Model/Dlna/ResolutionNormalizer.cs at Jellyfin commit b04614e.
// The breakpoint table, frame-rate scaling, HDR factor, and dimension rules
// are reproduced verbatim. The normalizer selects the first breakpoint whose
// inclusive maximum bitrate covers the normalized bitrate.

// resolutionBreakpoint is one entry in the pinned resolution table. MaxBitrate
// is the inclusive upper bound. MaxWidth is the width cap that applies when
// the normalized bitrate does not exceed that bound.
type resolutionBreakpoint struct {
	MaxWidth   int
	MaxBitrate int
}

// resolutionBreakpointsV1 lists all eight pinned breakpoints in ascending
// bitrate order, matching ResolutionNormalizer.cs:12-20 at b04614e. It
// returns a fresh slice on each call to avoid package-level mutable state,
// per CONVENTIONS.md 1.1.
func resolutionBreakpointsV1() []resolutionBreakpoint {
	return []resolutionBreakpoint{
		{MaxWidth: 416, MaxBitrate: 365_000},
		{MaxWidth: 640, MaxBitrate: 730_000},
		{MaxWidth: 768, MaxBitrate: 1_100_000},
		{MaxWidth: 960, MaxBitrate: 3_000_000},
		{MaxWidth: 1280, MaxBitrate: 6_000_000},
		{MaxWidth: 1920, MaxBitrate: 13_500_000},
		{MaxWidth: 2560, MaxBitrate: 28_000_000},
		{MaxWidth: 3840, MaxBitrate: 50_000_000},
	}
}

// ResolutionOptionsV1 carries the width and height limits that the normalizer
// returns. A nil dimension means the caller has no constraint from this source.
type ResolutionOptionsV1 struct {
	MaxWidth  *int
	MaxHeight *int
}

// NormalizeResolutionV1 reproduces ResolutionNormalizer.Normalize at
// b04614e. It derives a width cap from the h264-equivalent output bitrate,
// adjusted for frame rate and HDR, then intersects it with any explicit
// caller-supplied maximum.
//
// inputBitrate is the source video bitrate; when the output bitrate meets or
// exceeds it the normalizer skips the downscale and returns the caller limits.
// outputBitrate is the requested video bitrate as the client sent it.
// h264EquivalentOutputBitrate is the requested video bitrate converted to its
// H.264-equivalent value by the caller, matching the pinned
// EncodingHelper.ScaleBitrate path. For the H.264 output path the caller
// applies the low-bitrate bands and passes the result here; the normalizer
// does not perform that conversion. maxWidth and maxHeight are explicit caller
// limits (absent in every measured HLS request). targetFps is the effective
// target frame rate; 30 fps applies when it is absent. isHdr scales the
// reference bitrate by 0.8 for HDR sources.
func NormalizeResolutionV1(
	inputBitrate *int,
	outputBitrate int,
	h264EquivalentOutputBitrate int,
	maxWidth *int,
	maxHeight *int,
	targetFps *float64,
	isHdr bool,
) ResolutionOptionsV1 {
	// If the bitrate is not increasing, do not downscale the resolution.
	if inputBitrate != nil && outputBitrate >= *inputBitrate {
		if maxWidth != nil || maxHeight != nil {
			return ResolutionOptionsV1{MaxWidth: maxWidth, MaxHeight: maxHeight}
		}
	}

	// The reference bitrate is based on SDR h264 at 30 fps. The pinned
	// C# source uses float (single precision) for the scale, the
	// reference bitrate, and the HDR factor. Reproduce that arithmetic
	// here so the port matches the pinned output bit-for-bit.
	referenceFps := float32(30)
	if targetFps != nil && *targetFps > 0 {
		referenceFps = float32(*targetFps)
	}

	var referenceScale float32
	if referenceFps <= 30 {
		referenceScale = 30 / referenceFps
	} else {
		referenceScale = 1 / float32(math.Sqrt(float64(referenceFps/30)))
	}

	referenceBitrate := float32(h264EquivalentOutputBitrate) * referenceScale

	if isHdr {
		referenceBitrate *= 0.8
	}

	// Convert.ToInt32 rounds to the nearest even integer (banker's
	// rounding). The pinned source widens the float to double before the
	// round, so pass the float32 through float64 to match that path.
	normalizedBitrate := convertToInt32(float64(referenceBitrate))

	breakpoint := resolutionConfigurationV1(normalizedBitrate)
	if breakpoint == nil {
		return ResolutionOptionsV1{MaxWidth: maxWidth, MaxHeight: maxHeight}
	}

	originWidth := maxWidth
	cap := breakpoint.MaxWidth
	if maxWidth != nil {
		if cap < *maxWidth {
			maxWidth = &cap
		}
	} else {
		maxWidth = &cap
	}
	// Clear MaxHeight when the normalizer changes MaxWidth, matching the
	// .NET rule at ResolutionNormalizer.cs:73-77.
	if originWidth == nil || *originWidth != *maxWidth {
		maxHeight = nil
	}

	return ResolutionOptionsV1{MaxWidth: maxWidth, MaxHeight: maxHeight}
}

func resolutionConfigurationV1(outputBitrate int) *resolutionBreakpoint {
	breakpoints := resolutionBreakpointsV1()
	for index := range breakpoints {
		if outputBitrate <= breakpoints[index].MaxBitrate {
			return &breakpoints[index]
		}
	}
	return nil
}

// convertToInt32 reproduces .NET Convert.ToInt32(double), which rounds to the
// nearest integer with banker's rounding (round-half-to-even) and clamps the
// result to the int32 range.
func convertToInt32(value float64) int {
	// Convert.ToInt32 rounds the double to the nearest integer using
	// MidpointRounding.ToEven, then checks the int32 range. For the bitrates
	// this normalizer sees, the value is always in range, but the clamp keeps
	// the contract exact.
	if math.IsNaN(value) {
		return 0
	}
	if value >= float64(math.MaxInt32) {
		return math.MaxInt32
	}
	if value <= float64(math.MinInt32) {
		return math.MinInt32
	}
	return int(math.RoundToEven(value))
}

// h264EquivalentResolutionBitrateV1 reproduces the H.264-to-H.264 branch of
// EncodingHelper.ScaleBitrate at MediaBrowser.Controller/MediaEncoding/
// EncodingHelper.cs:2774-2802 (Jellyfin commit b04614e). The pinned caller
// scales the requested bitrate before it passes it to the resolution
// normalizer. The same-codec base ratio is 1, but the function raises that
// ratio at low bitrates: 4 through 500000, 3 through 1000000, 2.5 through
// 2000000, and 2 through 3000000. The result uses Convert.ToInt32, which
// rounds to the nearest even integer.
//
// This helper supplies the missing mechanism that makes the pinned breakpoint
// table reproduce the measured RC2 output at 420 kbps and 292 kbps. Without
// it, the normalizer receives the raw client bitrate and selects a narrower
// breakpoint than the pinned server emits. The HLS caller passes this value
// as h264EquivalentOutputBitrate so the normalizer derives the correct
// reference bitrate, while outputBitrate stays unchanged for ffmpeg rate
// control.
func h264EquivalentResolutionBitrateV1(bitrate int) int {
	return h264EquivalentBitrate(bitrate, "h264")
}

// h264EquivalentBitrate ports EncodingHelper.ScaleBitrate(bitrate, codec,
// "h264") for an H.264, HEVC, or AV1 output (EncodingHelper.cs:2747-2802).
func h264EquivalentBitrate(bitrate int, codec string) int {
	return scaleVideoBitrate(bitrate, codec, "h264")
}

// scaleVideoBitrate ports EncodingHelper.ScaleBitrate (EncodingHelper.cs:
// 2747-2802). The ratio of the codec efficiency factors does not go below 1.
// Low bitrates get a larger ratio, and a bitrate of 30 Mbit/s or more is
// not scaled.
func scaleVideoBitrate(bitrate int, inputCodec, outputCodec string) int {
	if bitrate <= 0 {
		return 0
	}
	scaleFactor := math.Max(videoBitrateScaleFactor(outputCodec)/videoBitrateScaleFactor(inputCodec), 1)
	if bitrate <= 500_000 {
		scaleFactor = math.Max(scaleFactor, 4)
	} else if bitrate <= 1_000_000 {
		scaleFactor = math.Max(scaleFactor, 3)
	} else if bitrate <= 2_000_000 {
		scaleFactor = math.Max(scaleFactor, 2.5)
	} else if bitrate <= 3_000_000 {
		scaleFactor = math.Max(scaleFactor, 2)
	} else if bitrate >= 30_000_000 {
		scaleFactor = 1
	}
	return convertToInt32(scaleFactor * float64(bitrate))
}

// videoBitrateScaleFactor ports EncodingHelper.GetVideoBitrateScaleFactor.
func videoBitrateScaleFactor(codec string) float64 {
	switch strings.ToLower(codec) {
	case "hevc", "h265", "vp9":
		return .6
	case "av1":
		return .5
	}
	return 1
}
