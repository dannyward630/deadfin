// SPDX-License-Identifier: GPL-2.0-only

package hls_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

func TestFmp4InitFileNameMatchesUnixAndWindowsBranches(t *testing.T) {
	for _, tc := range []struct {
		name, playlistPath, want string
		windows                  bool
	}{
		{
			name:         "Unix uses the init file basename",
			playlistPath: "/var/cache/transcodes/main.m3u8",
			windows:      false,
			want:         "main-1.mp4",
		},
		{
			name:         "Windows keeps the absolute init file path",
			playlistPath: `C:\transcodes\main.m3u8`,
			windows:      true,
			want:         `C:\transcodes\main-1.mp4`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hls.Fmp4InitFileName(tc.playlistPath, "fmp4", tc.windows); got != tc.want {
				t.Errorf("Fmp4InitFileName(%q, fmp4, windows=%t) = %q, want %q", tc.playlistPath, tc.windows, got, tc.want)
			}
		})
	}
}

func TestRewriteLivePlaylistRewritesFMP4InitURIForContainerAliases(t *testing.T) {
	const input = "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-MAP:URI=\"main-1.mp4\"\n" +
		"#EXTINF:3.000000,\n" +
		"main0.mp4\n"
	const want = "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-MAP:URI=\"hls/main/main-1.mp4\"\n" +
		"#EXTINF:3.000000,\n" +
		"main0.mp4\n"

	for _, segmentContainer := range []string{"fmp4", "mp4"} {
		t.Run(segmentContainer, func(t *testing.T) {
			if got := hls.RewriteLivePlaylist(input, "main", segmentContainer); got != want {
				t.Errorf("RewriteLivePlaylist() %s bytes differ:\n got: %q\nwant: %q", segmentContainer, got, want)
			}
		})
	}

	const tsInput = "#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXTINF:3.000000,\n" +
		"main0.ts\n"
	if got := hls.RewriteLivePlaylist(tsInput, "main", "ts"); got != tsInput {
		t.Errorf("RewriteLivePlaylist() TS bytes differ:\n got: %q\nwant unchanged: %q", got, tsInput)
	}
}

func TestRewriteLivePlaylistRewritesWindowsInitMapAndPreservesOtherPlaylistBytes(t *testing.T) {
	const input = "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"main-1.mp4\"\n" +
		"#EXT-X-MAP:URI=\"C:\\transcodes\\main-1.mp4\"\n" +
		"#EXTINF:3.000000,\n" +
		"main0.mp4\n"
	const want = "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"main-1.mp4\"\n" +
		"#EXT-X-MAP:URI=\"hls/main/main-1.mp4\"\n" +
		"#EXTINF:3.000000,\n" +
		"main0.mp4\n"

	if got := hls.RewriteLivePlaylist(input, "main", "fmp4"); got != want {
		t.Errorf("RewriteLivePlaylist() Windows init map bytes differ:\n got: %q\nwant: %q", got, want)
	}
}
