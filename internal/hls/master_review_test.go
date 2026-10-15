// SPDX-License-Identifier: GPL-2.0-only

package hls_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

// TestBuildMasterPlaylistSDRCompanionCanonicalizesCodecOverridesWithoutReorderingRawQuery
// guards the fallback URL a client follows after selecting the SDR rendition:
// it removes every old override and appends canonical replacement keys, while
// retaining the byte spelling and ordering of unrelated query parameters.
func TestBuildMasterPlaylistSDRCompanionCanonicalizesCodecOverridesWithoutReorderingRawQuery(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?DeviceId=tv%2B4&VideoCodec=hevc&Raw=one%2Ftwo&AllowVideoStreamCopy=true&MediaSourceId=source-9",
			Video: hls.MasterVideo{
				Codec: "hevc", Profile: "main10", Level: 120, BitDepth: 10, Range: hls.VideoRangeHDR,
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
		"#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,VIDEO-RANGE=PQ,CODECS=\"hvc1.2.4.L120.B0,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=24\n" +
		"main.m3u8?DeviceId=tv%2B4&VideoCodec=hevc&Raw=one%2Ftwo&AllowVideoStreamCopy=true&MediaSourceId=source-9\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,VIDEO-RANGE=SDR,CODECS=\"avc1.640029,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=24\n" +
		"main.m3u8?DeviceId=tv%2B4&Raw=one%2Ftwo&MediaSourceId=source-9&VideoCodec=h264&AllowVideoStreamCopy=false\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistSDRCompanionCanonicalizesEncodedOverrideKeys ensures
// that an SDR fallback cannot carry legacy overrides hidden behind percent-
// encoded (or duplicate) query-key spellings. Query bytes unrelated to the two
// overrides remain verbatim and in their original order, including malformed
// escapes which url.Parse deliberately leaves in RawQuery.
func TestBuildMasterPlaylistSDRCompanionCanonicalizesEncodedOverrideKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		uri               string
		unrelatedRawQuery string
	}{
		{
			name:              "mixed case encoded override keys",
			uri:               "main.m3u8?DeviceId=tv%2B4&%56ideoCodec=hevc&Raw=one%2Ftwo&%41llowVideoStreamCopy=true&MediaSourceId=source-9",
			unrelatedRawQuery: "DeviceId=tv%2B4&Raw=one%2Ftwo&MediaSourceId=source-9",
		},
		{
			name:              "plain and encoded duplicate aliases",
			uri:               "main.m3u8?Before=first&VideoCodec=hevc&%56ideoCodec=vp9&%76IDEOcODEC=av1&Raw=one%2Ftwo&AllowVideoStreamCopy=true&%41llowVideoStreamCopy=1&After=last",
			unrelatedRawQuery: "Before=first&Raw=one%2Ftwo&After=last",
		},
		{
			name:              "malformed unrelated escapes",
			uri:               "main.m3u8?Broken=%ZZ&%56ideoCodec=hevc&Truncated=%2&%41llowVideoStreamCopy=true&Reserved=%26%3D",
			unrelatedRawQuery: "Broken=%ZZ&Truncated=%2&Reserved=%26%3D",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
				Variants: []hls.MasterVariant{{
					URI: tc.uri,
					Video: hls.MasterVideo{
						Codec: "hevc", Profile: "main10", Level: 120, BitDepth: 10, Range: hls.VideoRangeHDR,
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

			companion := sdrCompanionURI(t, got)
			parsed, err := url.Parse(companion)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", companion, err)
			}
			wantRawQuery := tc.unrelatedRawQuery + "&VideoCodec=h264&AllowVideoStreamCopy=false"
			if parsed.RawQuery != wantRawQuery {
				t.Errorf("SDR companion RawQuery = %q, want %q", parsed.RawQuery, wantRawQuery)
			}

			values := parsed.Query()
			if got := values["VideoCodec"]; len(got) != 1 || got[0] != "h264" {
				t.Errorf("SDR companion VideoCodec values = %q, want one h264 override", got)
			}
			if got := values["AllowVideoStreamCopy"]; len(got) != 1 || got[0] != "false" {
				t.Errorf("SDR companion AllowVideoStreamCopy values = %q, want one false override", got)
			}

			videoCodecKeys, copyKeys := 0, 0
			for rawPart := range strings.SplitSeq(parsed.RawQuery, "&") {
				rawKey, _, _ := strings.Cut(rawPart, "=")
				key, err := url.QueryUnescape(rawKey)
				if err != nil {
					continue
				}
				switch {
				case strings.EqualFold(key, "VideoCodec"):
					videoCodecKeys++
				case strings.EqualFold(key, "AllowVideoStreamCopy"):
					copyKeys++
				}
			}
			if videoCodecKeys != 1 || copyKeys != 1 {
				t.Errorf("SDR companion decoded override keys = VideoCodec:%d AllowVideoStreamCopy:%d, want exactly one of each", videoCodecKeys, copyKeys)
			}
		})
	}
}

func sdrCompanionURI(t *testing.T, playlist string) string {
	t.Helper()
	lines := strings.Split(playlist, "\n")
	for index, line := range lines[:len(lines)-1] {
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") && strings.Contains(line, "VIDEO-RANGE=SDR") {
			return lines[index+1]
		}
	}
	t.Fatalf("SDR companion URI not found in playlist %q", playlist)
	return ""
}

// TestBuildMasterPlaylistPlacesDirectDolbyVisionP5BeforeHEVCFallback pins the
// two same-URL rendition entries required for profile 5: clients which understand
// Dolby Vision must select the dvh1 entry before legacy HEVC clients reach hvc1.
func TestBuildMasterPlaylistPlacesDirectDolbyVisionP5BeforeHEVCFallback(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9",
			Video: hls.MasterVideo{
				Codec: "hevc", Profile: "main10", Level: 153, BitDepth: 10, Range: hls.VideoRangeHDR,
				DolbyVision: &hls.DolbyVision{Profile: 5, Level: 9, Direct: true},
			},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 28_000_000,
			AudioBitrate: 768_000,
			Width:        3840,
			Height:       2160,
			FrameRate:    23.976,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=28768000,AVERAGE-BANDWIDTH=28768000,VIDEO-RANGE=PQ,CODECS=\"dvh1.05.09,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=28768000,AVERAGE-BANDWIDTH=28768000,VIDEO-RANGE=PQ,CODECS=\"hvc1.2.4.L153.B0,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistPlacesDirectDolbyVisionP10BeforeAV1Fallback pins the
// AV1 equivalent of the profile-5 contract: dav1 precedes the ordinary AV1
// fallback because profile 10 has no compatible base layer to supplement.
func TestBuildMasterPlaylistPlacesDirectDolbyVisionP10BeforeAV1Fallback(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9",
			Video: hls.MasterVideo{
				Codec: "av1", Profile: "main", Level: 13, BitDepth: 10, Range: hls.VideoRangeHDR,
				DolbyVision: &hls.DolbyVision{Profile: 10, Level: 13, Direct: true},
			},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 28_000_000,
			AudioBitrate: 768_000,
			Width:        3840,
			Height:       2160,
			FrameRate:    23.976,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=28768000,AVERAGE-BANDWIDTH=28768000,VIDEO-RANGE=PQ,CODECS=\"dav1.10.13,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=28768000,AVERAGE-BANDWIDTH=28768000,VIDEO-RANGE=PQ,CODECS=\"av01.0.13M.10,mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=23.976\n" +
		"main.m3u8?PlaySessionId=session-7&MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistSuppressesDirectDolbyVisionWithoutClientSupport
// prevents a dvh1/dav1 entry from reaching clients that did not explicitly
// request Dolby Vision, while retaining their ordinary HEVC or AV1 fallback.
func TestBuildMasterPlaylistSuppressesDirectDolbyVisionWithoutClientSupport(t *testing.T) {
	cases := []struct {
		name     string
		video    hls.MasterVideo
		fallback string
	}{
		{
			name: "P5 HEVC",
			video: hls.MasterVideo{
				Codec: "hevc", Profile: "main10", Level: 153, BitDepth: 10, Range: hls.VideoRangeHDR,
				DolbyVision: &hls.DolbyVision{Profile: 5, Level: 9, Direct: false},
			},
			fallback: "hvc1.2.4.L153.B0",
		},
		{
			name: "P10 AV1",
			video: hls.MasterVideo{
				Codec: "av1", Profile: "main", Level: 13, BitDepth: 10, Range: hls.VideoRangeHDR,
				DolbyVision: &hls.DolbyVision{Profile: 10, Level: 13, Direct: false},
			},
			fallback: "av01.0.13M.10",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
				Variants: []hls.MasterVariant{{
					URI:          "main.m3u8?MediaSourceId=source-9",
					Video:        tc.video,
					Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
					VideoBitrate: 28_000_000,
					AudioBitrate: 768_000,
					Width:        3840,
					Height:       2160,
					FrameRate:    23.976,
				}},
			})
			if err != nil {
				t.Fatalf("BuildMasterPlaylist: %v", err)
			}

			want := "#EXTM3U\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=28768000,AVERAGE-BANDWIDTH=28768000,VIDEO-RANGE=PQ,CODECS=\"" + tc.fallback + ",mp4a.40.2\",RESOLUTION=3840x2160,FRAME-RATE=23.976\n" +
				"main.m3u8?MediaSourceId=source-9\n"
			if got != want {
				t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
			}
		})
	}
}

// TestBuildMasterPlaylistOmitsZeroLevelVideoCodecButRetainsAudio ensures that
// an unknown source level never turns into a syntactically plausible but false
// RFC 6381 video token; audio remains advertised so audio-capable clients retain
// the stream description.
func TestBuildMasterPlaylistOmitsZeroLevelVideoCodecButRetainsAudio(t *testing.T) {
	cases := []struct {
		name            string
		video           hls.MasterVideo
		videoRangeField string
	}{
		{name: "H264", video: hls.MasterVideo{Codec: "h264", Profile: "high", Level: 0, BitDepth: 8, Range: hls.VideoRangeSDR}, videoRangeField: ",VIDEO-RANGE=SDR"},
		{name: "HEVC", video: hls.MasterVideo{Codec: "hevc", Profile: "main10", Level: 0, BitDepth: 10, Range: hls.VideoRangeHDR}, videoRangeField: ",VIDEO-RANGE=PQ"},
		{name: "AV1", video: hls.MasterVideo{Codec: "av1", Profile: "main", Level: 0, BitDepth: 10, Range: hls.VideoRangeHDR}, videoRangeField: ",VIDEO-RANGE=PQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
				Variants: []hls.MasterVariant{{
					URI:          "main.m3u8?MediaSourceId=source-9",
					Video:        tc.video,
					Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
					VideoBitrate: 4_000_000,
					AudioBitrate: 128_000,
					Width:        1920,
					Height:       1080,
					FrameRate:    24,
				}},
			})
			if err != nil {
				t.Fatalf("BuildMasterPlaylist: %v", err)
			}

			const prefix = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=4128000,AVERAGE-BANDWIDTH=4128000"
			want := prefix + tc.videoRangeField + ",CODECS=\"mp4a.40.2\",RESOLUTION=1920x1080,FRAME-RATE=24\n" +
				"main.m3u8?MediaSourceId=source-9\n"
			if got != want {
				t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
			}
		})
	}
}

// TestBuildMasterPlaylistUsesVP9SourceFactsForLevel40 pins the VP9 RFC 6381
// level derivation from the remuxed source geometry, frame rate, pixel format,
// and bit depth rather than emitting the level-00 token for missing facts.
func TestBuildMasterPlaylistUsesVP9SourceFactsForLevel40(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?MediaSourceId=source-9",
			Video: hls.MasterVideo{
				Codec: "vp9", Level: 1, BitDepth: 8, PixelFormat: "yuv420p", Range: hls.VideoRangeSDR,
			},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 4_000_000,
			AudioBitrate: 128_000,
			Width:        1920,
			Height:       1080,
			FrameRate:    30,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4128000,AVERAGE-BANDWIDTH=4128000,VIDEO-RANGE=SDR,CODECS=\"vp09.00.40.08,mp4a.40.2\",RESOLUTION=1920x1080,FRAME-RATE=30\n" +
		"main.m3u8?MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}

// TestBuildMasterPlaylistAdvertisesHDR10PlusBaseCodecSupplementalMetadata
// guards the cdm4 marker a capable HLS client needs to identify HDR10+ carried
// with an otherwise ordinary HEVC base codec.
func TestBuildMasterPlaylistAdvertisesHDR10PlusBaseCodecSupplementalMetadata(t *testing.T) {
	got, err := hls.BuildMasterPlaylist(hls.MasterRequest{
		Variants: []hls.MasterVariant{{
			URI: "main.m3u8?MediaSourceId=source-9",
			Video: hls.MasterVideo{
				Codec: "hevc", Profile: "main10", Level: 120, BitDepth: 10, Range: hls.VideoRangeHDR, HDR10Plus: true,
			},
			Audio:        hls.MasterAudio{Codec: "aac", Profile: "LC"},
			VideoBitrate: 6_000_000,
			AudioBitrate: 128_000,
			Width:        3840,
			Height:       2160,
			FrameRate:    24,
		}},
	})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist: %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,VIDEO-RANGE=PQ,CODECS=\"hvc1.2.4.L120.B0,mp4a.40.2\",SUPPLEMENTAL-CODECS=\"hvc1.2.4.L120.B0/cdm4\",RESOLUTION=3840x2160,FRAME-RATE=24\n" +
		"main.m3u8?MediaSourceId=source-9\n"
	if got != want {
		t.Errorf("BuildMasterPlaylist() = %q, want %q", got, want)
	}
}
