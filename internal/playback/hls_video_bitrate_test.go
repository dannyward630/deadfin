// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"strconv"
	"strings"
	"testing"
)

// TestHLSServiceTranscodeVideoBitrateFollowsSource asserts the port of
// EncodingHelper.GetVideoBitrateParamValue: the source bitrate
// caps the requested bitrate unless the request upscales, and the result is
// scaled from the source codec to the output codec.
func TestHLSServiceTranscodeVideoBitrateFollowsSource(t *testing.T) {
	for _, test := range []struct {
		name          string
		sourceCodec   string
		sourceBitrate int
		requested     int
		width, height *int
		want          int
	}{
		// The ATV-S04 item: HEVC 18.05 Mbit/s, Apple TV requests 119.36 Mbit/s.
		{name: "hevc source caps a large request", sourceCodec: "hevc", sourceBitrate: 18_051_734, requested: 119_360_000, want: 30_086_223},
		{name: "h264 source caps a large request", sourceCodec: "h264", sourceBitrate: 8_000_000, requested: 120_000_000, want: 8_000_000},
		{name: "scaled bitrate stays under the request", sourceCodec: "hevc", sourceBitrate: 18_000_000, requested: 20_000_000, want: 20_000_000},
		{name: "request below source wins", sourceCodec: "h264", sourceBitrate: 8_000_000, requested: 4_000_000, want: 4_000_000},
		{name: "low source bitrate is raised", sourceCodec: "h264", sourceBitrate: 1_500_000, requested: 10_000_000, want: 3_750_000},
		{name: "source at 3 Mbit/s is doubled", sourceCodec: "h264", sourceBitrate: 2_600_000, requested: 10_000_000, want: 5_200_000},
		{name: "upscaling keeps the request", sourceCodec: "h264", sourceBitrate: 2_600_000, requested: 10_000_000, width: hlsInt(1920), height: hlsInt(1080), want: 10_000_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := progressiveCopySource("/media/bitrate-519.mp4")
			source.MediaStreams[0].Codec = test.sourceCodec
			source.MediaStreams[0].BitRate = hlsInt(test.sourceBitrate)
			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.VideoBitrate = hlsInt(test.requested)
			request.Width, request.Height = test.width, test.height
			request.MaxWidth, request.MaxHeight = nil, nil

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(): %v", err)
			}
			if want := strconv.Itoa(test.want); !hlsArgPair(manager.lastRestart.Args, "-maxrate", want) {
				t.Fatalf("argv = %q, want -maxrate %s", manager.lastRestart.Args, want)
			}
		})
	}
}

// TestHLSServiceTranscodeKeepsSourceSizeWhenRequestCoversSource asserts the
// StreamingHelpers.cs:213-226 gate: with no requested size and a requested
// bitrate at or above the source, the resolution normalizer does not run.
// The source-capped bitrate alone would select a narrower breakpoint.
func TestHLSServiceTranscodeKeepsSourceSizeWhenRequestCoversSource(t *testing.T) {
	source := progressiveCopySource("/media/uhd-519.mkv")
	width, height := 3840, 2160
	source.MediaStreams[0].Codec = "hevc"
	source.MediaStreams[0].Width, source.MediaStreams[0].Height = &width, &height
	source.MediaStreams[0].BitRate = hlsInt(6_000_000)
	service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(120_000_000)
	request.MaxWidth, request.MaxHeight = nil, nil

	response, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master(): %v", err)
	}
	playlist := readHLSResponse(t, response)
	if !strings.Contains(playlist, "RESOLUTION=3840x2160") {
		t.Fatalf("Master() playlist = %q, want the source size 3840x2160", playlist)
	}
	if !strings.Contains(playlist, "BANDWIDTH=10128000,") {
		t.Fatalf("Master() playlist = %q, want BANDWIDTH 10000000 video + 128000 audio", playlist)
	}
}

// TestHLSServiceTranscodeNormalizesWhenRequestIsBelowSource asserts that the
// gate does not apply when the request is below the source bitrate.
func TestHLSServiceTranscodeNormalizesWhenRequestIsBelowSource(t *testing.T) {
	source := progressiveCopySource("/media/uhd-519.mkv")
	width, height := 3840, 2160
	source.MediaStreams[0].Width, source.MediaStreams[0].Height = &width, &height
	source.MediaStreams[0].BitRate = hlsInt(40_000_000)
	service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})
	request := hlsTranscodeRequest()
	request.VideoBitrate = hlsInt(4_000_000)
	request.MaxWidth, request.MaxHeight = nil, nil

	response, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master(): %v", err)
	}
	if playlist := readHLSResponse(t, response); strings.Contains(playlist, "RESOLUTION=3840x2160") {
		t.Fatalf("Master() playlist = %q, want a normalized size below the source", playlist)
	}
}
