// SPDX-License-Identifier: GPL-2.0-only

package trickplay_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/trickplay"
)

func TestPlaylistRenderMatchesJellyfinTilesM3U8(t *testing.T) {
	itemID := mustUUID(t, "00112233445566778899aabbccddeeff")
	for _, test := range []struct {
		name     string
		metadata trickplay.Metadata
		apiKey   string
		want     string
	}{
		{
			name: "full tile followed by partial final tile",
			metadata: trickplay.Metadata{
				ItemID:         itemID,
				Width:          320,
				Height:         180,
				TileWidth:      2,
				TileHeight:     2,
				ThumbnailCount: 5,
				Interval:       1_500,
				Bandwidth:      1_335,
			},
			apiKey: "opaque+key",
			want: "#EXTM3U\n" +
				"#EXT-X-TARGETDURATION:2\n" +
				"#EXT-X-VERSION:7\n" +
				"#EXT-X-MEDIA-SEQUENCE:1\n" +
				"#EXT-X-PLAYLIST-TYPE:VOD\n" +
				"#EXT-X-IMAGES-ONLY\n" +
				"#EXTINF:6,\n" +
				"#EXT-X-TILES:RESOLUTION=320x180,LAYOUT=2x2,DURATION=1.5\n" +
				"0.jpg?MediaSourceId=00112233445566778899aabbccddeeff&ApiKey=opaque%2Bkey\n" +
				"#EXTINF:1.5,\n" +
				"#EXT-X-TILES:RESOLUTION=320x180,LAYOUT=2x2,DURATION=1.5\n" +
				"1.jpg?MediaSourceId=00112233445566778899aabbccddeeff&ApiKey=opaque%2Bkey\n" +
				"#EXT-X-ENDLIST\n",
		},
		{
			name: "durations preserve three decimal places without trailing zeros",
			metadata: trickplay.Metadata{
				ItemID:         itemID,
				Width:          160,
				Height:         90,
				TileWidth:      1,
				TileHeight:     2,
				ThumbnailCount: 3,
				Interval:       333,
				Bandwidth:      1,
			},
			apiKey: "api-token",
			want: "#EXTM3U\n" +
				"#EXT-X-TARGETDURATION:2\n" +
				"#EXT-X-VERSION:7\n" +
				"#EXT-X-MEDIA-SEQUENCE:1\n" +
				"#EXT-X-PLAYLIST-TYPE:VOD\n" +
				"#EXT-X-IMAGES-ONLY\n" +
				"#EXTINF:0.666,\n" +
				"#EXT-X-TILES:RESOLUTION=160x90,LAYOUT=1x2,DURATION=0.333\n" +
				"0.jpg?MediaSourceId=00112233445566778899aabbccddeeff&ApiKey=api-token\n" +
				"#EXTINF:0.333,\n" +
				"#EXT-X-TILES:RESOLUTION=160x90,LAYOUT=1x2,DURATION=0.333\n" +
				"1.jpg?MediaSourceId=00112233445566778899aabbccddeeff&ApiKey=api-token\n" +
				"#EXT-X-ENDLIST\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := (trickplay.Playlist{Metadata: test.metadata, APIKey: test.apiKey}).Render()
			if !ok {
				t.Fatal("Render() reported no playlist for trickplay metadata with thumbnails")
			}
			if got != test.want {
				t.Fatalf("tiles.m3u8 bytes = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPlaylistRenderEscapesOpaqueAPIKey(t *testing.T) {
	const apiKey = "alpha+beta&role=admin% xy/雪"
	playlist := trickplay.Playlist{
		Metadata: trickplay.Metadata{
			ItemID:         mustUUID(t, "00112233445566778899aabbccddeeff"),
			Width:          320,
			Height:         180,
			TileWidth:      1,
			TileHeight:     1,
			ThumbnailCount: 1,
			Interval:       1_500,
		},
		APIKey: apiKey,
	}

	rendered, ok := playlist.Render()
	if !ok {
		t.Fatal("Render() reported no playlist for trickplay metadata with thumbnails")
	}
	const wantURI = "0.jpg?MediaSourceId=00112233445566778899aabbccddeeff&ApiKey=alpha%2Bbeta%26role%3Dadmin%25+xy%2F%E9%9B%AA"
	var gotURI string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "0.jpg?") {
			gotURI = line
			break
		}
	}
	if gotURI != wantURI {
		t.Fatalf("tile URI = %q, want canonical URI %q", gotURI, wantURI)
	}

	uri, err := url.Parse("https://example.invalid/" + gotURI)
	if err != nil {
		t.Fatalf("parse emitted URI: %v", err)
	}
	query := uri.Query()
	if got := query["ApiKey"]; len(got) != 1 || got[0] != apiKey {
		t.Fatalf("ApiKey values = %#v, want exactly %#v", got, []string{apiKey})
	}
	if got := query["MediaSourceId"]; len(got) != 1 || got[0] != "00112233445566778899aabbccddeeff" {
		t.Fatalf("MediaSourceId values = %#v, want unchanged item ID", got)
	}
	if len(query) != 2 {
		t.Fatalf("query fields = %#v, want only MediaSourceId and ApiKey", query)
	}
}

func TestPlaylistRenderHasNoPlaylistWithoutThumbnails(t *testing.T) {
	playlist := trickplay.Playlist{
		Metadata: trickplay.Metadata{
			ItemID:         mustUUID(t, "00112233445566778899aabbccddeeff"),
			Width:          320,
			Height:         180,
			TileWidth:      2,
			TileHeight:     2,
			ThumbnailCount: 0,
			Interval:       1_500,
		},
		APIKey: "opaque+key",
	}

	if got, ok := playlist.Render(); ok || got != "" {
		t.Fatalf("Render() = (%q, %t), want no playlist", got, ok)
	}
}
