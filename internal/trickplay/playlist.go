// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"net/url"
	"strconv"
	"strings"
)

// Playlist renders the Jellyfin tiles.m3u8 representation of Metadata.
type Playlist struct {
	Metadata Metadata
	APIKey   string
}

// Render returns a tiles.m3u8 playlist when this metadata contains thumbnails.
func (playlist Playlist) Render() (string, bool) {
	metadata := playlist.Metadata
	if metadata.ThumbnailCount <= 0 || metadata.Interval < 0 {
		return "", false
	}
	capacity, err := tileCapacity(metadata.TileWidth, metadata.TileHeight)
	if err != nil {
		return "", false
	}
	maxInt64 := int64(^uint64(0) >> 1)
	if metadata.Interval > 0 && metadata.Interval > maxInt64/int64(capacity) {
		return "", false
	}

	tileCount := ceilingDivide(metadata.ThumbnailCount, capacity)
	var builder strings.Builder
	builder.WriteString("#EXTM3U\n")
	builder.WriteString("#EXT-X-TARGETDURATION:")
	builder.WriteString(strconv.Itoa(tileCount))
	builder.WriteString("\n#EXT-X-VERSION:7\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-IMAGES-ONLY\n")

	for index := range tileCount {
		thumbnailCount := capacity
		if index == tileCount-1 {
			thumbnailCount = metadata.ThumbnailCount - index*capacity
		}
		durationMilliseconds := metadata.Interval * int64(thumbnailCount)

		builder.WriteString("#EXTINF:")
		builder.WriteString(formatMilliseconds(durationMilliseconds))
		builder.WriteString(",\n#EXT-X-TILES:RESOLUTION=")
		builder.WriteString(strconv.Itoa(metadata.Width))
		builder.WriteByte('x')
		builder.WriteString(strconv.Itoa(metadata.Height))
		builder.WriteString(",LAYOUT=")
		builder.WriteString(strconv.Itoa(metadata.TileWidth))
		builder.WriteByte('x')
		builder.WriteString(strconv.Itoa(metadata.TileHeight))
		builder.WriteString(",DURATION=")
		builder.WriteString(formatMilliseconds(metadata.Interval))
		builder.WriteByte('\n')
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString(".jpg?MediaSourceId=")
		builder.WriteString(metadata.ItemID.String())
		builder.WriteString("&ApiKey=")
		builder.WriteString(url.QueryEscape(playlist.APIKey))
		builder.WriteByte('\n')
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	return builder.String(), true
}

func formatMilliseconds(milliseconds int64) string {
	wholeSeconds := milliseconds / 1_000
	fraction := milliseconds % 1_000
	if fraction == 0 {
		return strconv.FormatInt(wholeSeconds, 10)
	}
	fractionText := strconv.FormatInt(fraction+1_000, 10)[1:]
	fractionText = strings.TrimRight(fractionText, "0")
	return strconv.FormatInt(wholeSeconds, 10) + "." + fractionText
}
