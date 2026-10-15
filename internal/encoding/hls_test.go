// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strings"
	"testing"
)

// TestHLSMeasuredConstraintsEncodingLayer verifies the measured encoding-layer
// constraints: the video-only HLS path emits -map -0:a and omits audio encoder
// arguments; encoded audio emits -ab unchanged; bufsize is 2x maxrate; the
// bt2390 tone-map operator covers both HLG and PQ transfers; and the
// segment-time value passes through.
//
// The playback-level integration tests in hls_output_constraints_test.go
// exercise these constraints through the HLS service. These tests exercise
// the encoding builder directly.

func defaultHLSCaps() Capabilities {
	return Capabilities{
		H264Encoder:      "libx264",
		AACEncoders:      []string{"aac"},
		SupportsTonemapX: true,
	}
}

func TestHLSMeasuredConstraintsVideoOnlyOmitsAudioArgs(t *testing.T) {
	job := HLSJob{
		EncodingJob: EncodingJob{
			InputPath:          "/media/video-only.mkv",
			InputContainer:     "mkv",
			OutputPath:         "/cache/playlist.m3u8",
			OutputContainer:    "fmp4",
			OutputVideoCodec:   "h264",
			OutputVideoBitrate: 64_000,
			OutputWidth:        416,
			OutputHeight:       234,
			VideoStream: VideoStream{
				Index:         0,
				Codec:         "hevc",
				BitDepth:      10,
				ColorTransfer: "arib-std-b67",
			},
			VideoQuality: VideoQuality{
				Preset:                "veryfast",
				CRF:                   23,
				ForcedKeyFrameSeconds: 3,
				X264Options:           "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0",
			},
		},
		SegmentLengthSeconds: 3,
	}
	args, err := BuildHLSArgs(job, defaultHLSCaps())
	if err != nil {
		t.Fatalf("BuildHLSArgs(): %v", err)
	}

	if !mcContainsPair(args, "-maxrate", "64000") {
		t.Errorf("video-only: args = %v, want -maxrate 64000", args)
	}
	if !mcContainsPair(args, "-bufsize", "128000") {
		t.Errorf("video-only: args = %v, want -bufsize 128000", args)
	}
	if mcContainsPair(args, "-codec:a:0", "aac") || mcContainsAny(args, "-ac", "-ab", "-ar") {
		t.Errorf("video-only: args = %v, must not emit audio encoder arguments", args)
	}
	if !mcContainsSequence(args, "-map", "-0:a") {
		t.Errorf("video-only: args = %v, want -map -0:a to exclude audio", args)
	}
}

func TestHLSMeasuredConstraintsEncodedAudioEmitsBitrateUnchanged(t *testing.T) {
	job := HLSJob{
		EncodingJob: EncodingJob{
			InputPath:           "/media/feature.mp4",
			InputContainer:      "mov,mp4,m4a,3gp,3g2,mj2",
			OutputPath:          "/cache/playlist.m3u8",
			OutputContainer:     "fmp4",
			OutputVideoCodec:    "h264",
			OutputVideoBitrate:  292_000,
			OutputAudioCodec:    "aac",
			OutputAudioBitrate:  128_000,
			OutputAudioChannels: 6,
			OutputWidth:         416,
			OutputHeight:        234,
			VideoStream:         VideoStream{Index: 0, Codec: "h264"},
			AudioStream:         AudioStream{Index: 1, Codec: "ac3", Channels: 6},
			VideoQuality: VideoQuality{
				Preset:                "veryfast",
				CRF:                   23,
				ForcedKeyFrameSeconds: 3,
				X264Options:           "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0",
			},
		},
		SegmentLengthSeconds: 3,
	}
	args, err := BuildHLSArgs(job, defaultHLSCaps())
	if err != nil {
		t.Fatalf("BuildHLSArgs(): %v", err)
	}

	if !mcContainsPair(args, "-ab", "128000") {
		t.Errorf("encoded audio: args = %v, want -ab 128000", args)
	}
	if !mcContainsPair(args, "-ac", "6") {
		t.Errorf("encoded audio: args = %v, want -ac 6", args)
	}
	if !mcContainsPair(args, "-bufsize", "584000") {
		t.Errorf("encoded audio: args = %v, want -bufsize 584000", args)
	}
}

func TestHLSMeasuredConstraintsHDRToneMapping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		transfer string
		wantSet  string
	}{
		{name: "HLG", transfer: "arib-std-b67", wantSet: "color_trc=arib-std-b67"},
		{name: "PQ", transfer: "smpte2084", wantSet: "color_trc=smpte2084"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := HLSJob{
				EncodingJob: EncodingJob{
					InputPath:          "/media/hdr.mkv",
					InputContainer:     "mkv",
					OutputPath:         "/cache/playlist.m3u8",
					OutputContainer:    "fmp4",
					OutputVideoCodec:   "h264",
					OutputVideoBitrate: 64_000,
					OutputWidth:        416,
					OutputHeight:       234,
					VideoStream: VideoStream{
						Index:         0,
						Codec:         "hevc",
						BitDepth:      10,
						ColorTransfer: tc.transfer,
					},
					AudioStream:         AudioStream{Index: 1, Codec: "aac", Channels: 2},
					OutputAudioCodec:    "aac",
					OutputAudioBitrate:  128_000,
					OutputAudioChannels: 2,
					VideoQuality: VideoQuality{
						Preset:                "veryfast",
						CRF:                   23,
						ForcedKeyFrameSeconds: 3,
						X264Options:           "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0",
					},
				},
				SegmentLengthSeconds: 3,
			}
			args, err := BuildHLSArgs(job, defaultHLSCaps())
			if err != nil {
				t.Fatalf("BuildHLSArgs(): %v", err)
			}

			filter := mcArgumentValue(args, "-vf")
			wantTonemap := "tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p"
			if !strings.Contains(filter, wantTonemap) {
				t.Errorf("%s: -vf = %q, want bt2390 tone-map operator", tc.name, filter)
			}
			if !strings.Contains(filter, tc.wantSet) {
				t.Errorf("%s: -vf = %q, want %s in setparams", tc.name, filter, tc.wantSet)
			}
		})
	}
}

func TestHLSMeasuredConstraintsSegmentTimePasses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		copyVideo bool
		wantHLS   string
	}{
		{name: "encoded video hls_time 3", copyVideo: false, wantHLS: "3"},
		{name: "video copy hls_time 6", copyVideo: true, wantHLS: "6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := HLSJob{
				EncodingJob: EncodingJob{
					InputPath:           "/media/feature.mp4",
					InputContainer:      "mov,mp4,m4a,3gp,3g2,mj2",
					OutputPath:          "/cache/playlist.m3u8",
					OutputContainer:     "fmp4",
					OutputVideoCodec:    "h264",
					OutputVideoBitrate:  292_000,
					OutputWidth:         416,
					OutputHeight:        234,
					VideoStream:         VideoStream{Index: 0, Codec: "h264"},
					AudioStream:         AudioStream{Index: 1, Codec: "aac", Channels: 2},
					OutputAudioCodec:    "aac",
					OutputAudioBitrate:  128_000,
					OutputAudioChannels: 2,
					VideoQuality: VideoQuality{
						Preset:                "veryfast",
						CRF:                   23,
						ForcedKeyFrameSeconds: 3,
						X264Options:           "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0",
					},
				},
				SegmentLengthSeconds: 0,
			}
			if tc.copyVideo {
				job.OutputVideoCodec = "copy"
				job.SegmentLengthSeconds = 6
			} else {
				job.SegmentLengthSeconds = 3
			}
			args, err := BuildHLSArgs(job, defaultHLSCaps())
			if err != nil {
				t.Fatalf("BuildHLSArgs(): %v", err)
			}
			if !mcContainsPair(args, "-hls_time", tc.wantHLS) {
				t.Errorf("%s: args = %v, want -hls_time %s", tc.name, args, tc.wantHLS)
			}
		})
	}
}

func mcContainsPair(args []string, option, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == option && args[i+1] == value {
			return true
		}
	}
	return false
}

func mcContainsAny(args []string, options ...string) bool {
	for _, opt := range options {
		for _, arg := range args {
			if arg == opt {
				return true
			}
		}
	}
	return false
}

func mcContainsSequence(args []string, want ...string) bool {
	if len(want) == 0 || len(args) < len(want) {
		return false
	}
	for start := 0; start+len(want) <= len(args); start++ {
		matched := true
		for i := range want {
			if args[start+i] != want[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func mcArgumentValue(args []string, option string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == option {
			return args[i+1]
		}
	}
	return ""
}
