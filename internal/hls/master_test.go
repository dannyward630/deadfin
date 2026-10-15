// SPDX-License-Identifier: GPL-2.0-only

package hls_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

// These cases transcribe the field and line order in Jellyfin's
// DynamicHlsHelper at b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// AppendPlaylist (lines 345-374), AddSubtitles (674-710), and AddTrickplay
// (719-745). They use the public formatter seam so production adapters may
// resolve source facts before this package serializes the playlist.
func TestBuildMasterPlaylistMinimalVideoPreservesRelativeURIAndAttributeOrder(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI:          "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9",
			Video:        hls.MasterVideo{Codec: "h264", Profile: "high", Level: 41},
			VideoBitrate: 1_000_000,
			Width:        1920,
			Height:       1080,
			FrameRate:    23.976,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1000000,AVERAGE-BANDWIDTH=1000000,CODECS=\"avc1.640029\",RESOLUTION=1920x1080,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistH264AACPinsLowBandwidthCompatibleCodecString guards
// the RFC 6381 H.264/AAC spelling clients use when selecting a normal SDR
// variant. The total bandwidth is the source audio/video sum, not a selected
// stream's bitrate.
func TestBuildMasterPlaylistH264AACPinsLowBandwidthCompatibleCodecString(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI:          "main.m3u8?PlaySessionId=session-7&AudioCodec=aac&MediaSourceId=source-9",
			Video:        hls.MasterVideo{Codec: "h264", Profile: "high", Level: 41, Range: hls.VideoRangeSDR},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 4_000_000,
			AudioBitrate: 128_000,
			Width:        1920,
			Height:       1080,
			FrameRate:    23.976,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4128000,AVERAGE-BANDWIDTH=4128000,VIDEO-RANGE=SDR,CODECS=\"avc1.640029,mp4a.40.2\",RESOLUTION=1920x1080,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&AudioCodec=aac&MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistHDRPreservesDynamicMetadataAndAddsSDRCompanion pins
// upstream's compatible-base-layer Dolby Vision signaling and the H.264 SDR
// entry it appends for clients that cannot choose the HDR stream. In particular,
// the fallback must retain the original query order before appending its two
// override parameters.
func TestBuildMasterPlaylistHDRPreservesDynamicMetadataAndAddsSDRCompanion(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9&DeviceId=tv-4",
			Video: hls.MasterVideo{
				Codec:    "hevc",
				Profile:  "main10",
				Level:    120,
				BitDepth: 10,
				Range:    hls.VideoRangeHDR,
				DolbyVision: &hls.DolbyVision{
					Profile:   8,
					Level:     6,
					BaseLayer: hls.VideoRangeHDR,
				},
			},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 6_000_000,
			AudioBitrate: 128_000,
			Width:        3840,
			Height:       2160,
			FrameRate:    24,
			SDRCompanion: &hls.MasterVideo{Codec: "h264", Profile: "high", Level: 41, BitDepth: 8, Range: hls.VideoRangeSDR},
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,VIDEO-RANGE=PQ,CODECS=\"hvc1.2.4.L120.B0,mp4a.40.2\",SUPPLEMENTAL-CODECS=\"dvh1.08.06/db1p\",RESOLUTION=3840x2160,FRAME-RATE=24\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9&DeviceId=tv-4\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,VIDEO-RANGE=SDR,CODECS=\"avc1.640029,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=24\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9&DeviceId=tv-4&VideoCodec=h264&AllowVideoStreamCopy=false\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

func TestBuildMasterPlaylistEmitsSubtitleRenditionBeforeItsGroupReference(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Subtitles: []hls.SubtitleRendition{{
			GroupID:  "subs",
			Name:     "English SDH",
			URI:      "source-9/Subtitles/7/subtitles.m3u8?SegmentLength=30&ApiKey=token-3",
			Language: "eng",
			Default:  true,
			Forced:   false,
		}},
		Variants: []hls.MasterVariant{{
			URI:           "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9",
			Video:         hls.MasterVideo{Codec: "h264", Profile: "high", Level: 41, Range: hls.VideoRangeSDR},
			Audio:         hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate:  1_000_000,
			AudioBitrate:  128_000,
			Width:         1280,
			Height:        720,
			FrameRate:     24,
			SubtitleGroup: "subs",
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"English SDH\",DEFAULT=YES,FORCED=NO,AUTOSELECT=YES,URI=\"source-9/Subtitles/7/subtitles.m3u8?SegmentLength=30&ApiKey=token-3\",LANGUAGE=\"eng\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1128000,AVERAGE-BANDWIDTH=1128000,VIDEO-RANGE=SDR,CODECS=\"avc1.640029,mp4a.40.2\",RESOLUTION=1280x720,FRAME-RATE=24,SUBTITLES=\"subs\"\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

func TestBuildMasterPlaylistEmitsExplicitTrickplayRenditionAfterMediaVariants(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI:          "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9",
			Video:        hls.MasterVideo{Codec: "h264", Profile: "high", Level: 41},
			VideoBitrate: 1_000_000,
			Width:        1280,
			Height:       720,
			FrameRate:    24,
		}},
		Trickplay: []hls.TrickplayRendition{{
			URI:       "Trickplay/320/tiles.m3u8?MediaSourceId=source-9&ApiKey=token-3",
			Bandwidth: 64_000,
			Width:     320,
			Height:    180,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1000000,AVERAGE-BANDWIDTH=1000000,CODECS=\"avc1.640029\",RESOLUTION=1280x720,FRAME-RATE=24\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n" +
		"#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=64000,RESOLUTION=320x180,CODECS=\"jpeg\",URI=\"Trickplay/320/tiles.m3u8?MediaSourceId=source-9&ApiKey=token-3\"\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}
