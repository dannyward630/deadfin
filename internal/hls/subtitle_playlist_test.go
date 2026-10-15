// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildSubtitlePlaylistPreservesStockFractionalTailBytes(t *testing.T) {
	got, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
		RuntimeTicks:         1_385_240_000,
		SegmentLengthSeconds: 30,
		APIKey:               "token-3",
	})
	if err != nil {
		t.Fatalf("BuildSubtitlePlaylist() error = %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:30\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=300000000&ApiKey=token-3\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=300000000&EndPositionTicks=600000000&ApiKey=token-3\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=600000000&EndPositionTicks=900000000&ApiKey=token-3\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=900000000&EndPositionTicks=1200000000&ApiKey=token-3\n" +
		"#EXTINF:18.524,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=1200000000&EndPositionTicks=1385240000&ApiKey=token-3\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("subtitle playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildSubtitlePlaylistOmitsPartialSegmentForExactDivision(t *testing.T) {
	got, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
		RuntimeTicks:         600_000_000,
		SegmentLengthSeconds: 30,
		APIKey:               "token-3",
	})
	if err != nil {
		t.Fatalf("BuildSubtitlePlaylist() error = %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:30\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=300000000&ApiKey=token-3\n" +
		"#EXTINF:30,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=300000000&EndPositionTicks=600000000&ApiKey=token-3\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("subtitle playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildSubtitlePlaylistFormatsOneTickAsScientificNotation(t *testing.T) {
	got, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
		RuntimeTicks:         1,
		SegmentLengthSeconds: 30,
		APIKey:               "token-3",
	})
	if err != nil {
		t.Fatalf("BuildSubtitlePlaylist() error = %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:30\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:1E-07,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=1&ApiKey=token-3\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("subtitle playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildSubtitlePlaylistPreservesEmptyAccessToken(t *testing.T) {
	got, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
		RuntimeTicks:         10_000_000,
		SegmentLengthSeconds: 30,
	})
	if err != nil {
		t.Fatalf("BuildSubtitlePlaylist() error = %v", err)
	}

	const want = "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:30\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:1,\n" +
		"stream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=10000000&ApiKey=\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("subtitle playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildSubtitlePlaylistRejectsOutputLimitsBeforeRendering bounds both
// request-controlled subtitle segment count and final playlist bytes.
func TestBuildSubtitlePlaylistRejectsOutputLimitsBeforeRendering(t *testing.T) {
	for _, tc := range []struct {
		name         string
		runtimeTicks int64
		apiKey       string
	}{
		{
			name:         "more than ten thousand segments",
			runtimeTicks: 100_010_000_000,
		},
		{
			name:         "ten thousand segments with one kibibyte API key exceeds eight mebibytes",
			runtimeTicks: 100_000_000_000,
			apiKey:       strings.Repeat("k", 1<<10),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			playlist, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
				RuntimeTicks:         tc.runtimeTicks,
				SegmentLengthSeconds: 1,
				APIKey:               tc.apiKey,
			})
			if err == nil || err.Error() != wantPlaylistOutputLimitError {
				t.Errorf("BuildSubtitlePlaylist() error = %v, want %q", err, wantPlaylistOutputLimitError)
			}
			if playlist != "" {
				t.Errorf("BuildSubtitlePlaylist() rendered %d bytes, want empty output-limit rejection", len(playlist))
			}
		})
	}
}

func TestBuildSubtitlePlaylistRejectsUnsupportedRuntimeAndInvalidSegmentLength(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request SubtitlePlaylistRequest
		want    error
	}{
		{
			name: "zero runtime",
			request: SubtitlePlaylistRequest{
				RuntimeTicks:         0,
				SegmentLengthSeconds: 30,
			},
			want: ErrSubtitlePlaylistUnsupported,
		},
		{
			name: "negative runtime",
			request: SubtitlePlaylistRequest{
				RuntimeTicks:         -1,
				SegmentLengthSeconds: 30,
			},
			want: ErrSubtitlePlaylistUnsupported,
		},
		{
			name: "zero segment length",
			request: SubtitlePlaylistRequest{
				RuntimeTicks:         10_000_000,
				SegmentLengthSeconds: 0,
			},
			want: ErrSubtitleSegmentLength,
		},
		{
			name: "negative segment length",
			request: SubtitlePlaylistRequest{
				RuntimeTicks:         10_000_000,
				SegmentLengthSeconds: -1,
			},
			want: ErrSubtitleSegmentLength,
		},
		{
			name: "runtime validation precedes segment validation",
			request: SubtitlePlaylistRequest{
				RuntimeTicks:         0,
				SegmentLengthSeconds: 0,
			},
			want: ErrSubtitlePlaylistUnsupported,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildSubtitlePlaylist(tc.request)
			if !errors.Is(err, tc.want) {
				t.Errorf("BuildSubtitlePlaylist() error = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

func TestBuildSubtitlePlaylistRejectsLineBreakInAccessToken(t *testing.T) {
	for _, token := range []string{"bad\nvalue", "bad\rvalue"} {
		t.Run("token="+token[:3], func(t *testing.T) {
			_, err := BuildSubtitlePlaylist(SubtitlePlaylistRequest{
				RuntimeTicks:         10_000_000,
				SegmentLengthSeconds: 30,
				APIKey:               token,
			})
			if err == nil {
				t.Error("BuildSubtitlePlaylist() error = nil, want CR/LF access token rejection")
			}
		})
	}
}

func TestDotnetDoubleStringMatchesDefaultFormatterThresholds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value float64
		want  string
	}{
		{name: "integer", value: 30, want: "30"},
		{name: "fraction", value: 18.524, want: "18.524"},
		{name: "subsecond", value: 0.5, want: "0.5"},
		{name: "large fixed notation", value: 1_000_000, want: "1000000"},
		{name: "small scientific notation", value: 1e-7, want: "1E-07"},
		{name: "large scientific notation", value: 1e15, want: "1E+15"},
		{name: "runtime scale fraction", value: 922337203685.4775, want: "922337203685.4775"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dotnetDoubleString(tc.value); got != tc.want {
				t.Errorf("dotnetDoubleString(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
