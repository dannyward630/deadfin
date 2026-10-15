// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

const subtitleTicksPerSecond int64 = 10_000_000

var (
	// ErrSubtitlePlaylistUnsupported reports media with no positive runtime.
	ErrSubtitlePlaylistUnsupported = errors.New("hls: subtitles unsupported for this media")
	// ErrSubtitleSegmentLength reports a non-positive or unrepresentable segment length.
	ErrSubtitleSegmentLength = errors.New("hls: invalid subtitle segment length")
)

// SubtitlePlaylistRequest carries the resolved facts for an HLS subtitle
// rendition playlist. APIKey is URL-encoded as the opaque credential of each
// generated relative segment URI.
type SubtitlePlaylistRequest struct {
	RuntimeTicks         int64
	SegmentLengthSeconds int
	APIKey               string
}

// BuildSubtitlePlaylist renders Jellyfin's VOD subtitle playlist bytes. It is
// deliberately independent of media and request state: callers resolve those
// facts before constructing this value.
func BuildSubtitlePlaylist(request SubtitlePlaylistRequest) (string, error) {
	// Jellyfin validates runtime before segment length.
	if request.RuntimeTicks <= 0 {
		return "", ErrSubtitlePlaylistUnsupported
	}

	segmentTicks, err := subtitleSegmentTicks(request.SegmentLengthSeconds)
	if err != nil {
		return "", err
	}
	if err := validateLine(request.APIKey); err != nil {
		return "", fmt.Errorf("invalid subtitle API key: %w", err)
	}
	segmentCount := playlistSegmentCount(request.RuntimeTicks, segmentTicks)
	if err := checkPlaylistSegmentCount(segmentCount); err != nil {
		return "", err
	}
	playlistSize, err := subtitlePlaylistSize(request, segmentTicks, segmentCount)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.Grow(playlistSize)
	builder.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:")
	builder.WriteString(strconv.Itoa(request.SegmentLengthSeconds))
	builder.WriteString("\n#EXT-X-VERSION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")

	for positionTicks := int64(0); positionTicks < request.RuntimeTicks; {
		remaining := request.RuntimeTicks - positionTicks
		lengthTicks := min(remaining, segmentTicks)
		// Computing from remaining avoids overflow when the final segment is
		// adjacent to MaxInt64.
		endPositionTicks := positionTicks + lengthTicks

		builder.WriteString("#EXTINF:")
		builder.WriteString(dotnetDoubleString(float64(lengthTicks) / float64(subtitleTicksPerSecond)))
		builder.WriteString(",\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=")
		builder.WriteString(strconv.FormatInt(positionTicks, 10))
		builder.WriteString("&EndPositionTicks=")
		builder.WriteString(strconv.FormatInt(endPositionTicks, 10))
		builder.WriteString("&ApiKey=")
		builder.WriteString(url.QueryEscape(request.APIKey))
		builder.WriteByte('\n')

		positionTicks = endPositionTicks
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	return builder.String(), nil
}

func subtitlePlaylistSize(request SubtitlePlaylistRequest, segmentTicks, segmentCount int64) (int, error) {
	counter := playlistSizeCounter{}
	counter.addString("#EXTM3U\n#EXT-X-TARGETDURATION:")
	counter.add(decimalInt64Length(int64(request.SegmentLengthSeconds)))
	counter.addString("\n#EXT-X-VERSION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")

	for index, positionTicks := int64(0), int64(0); index < segmentCount; index++ {
		remaining := request.RuntimeTicks - positionTicks
		lengthTicks := min(remaining, segmentTicks)
		endPositionTicks := positionTicks + lengthTicks

		counter.addString("#EXTINF:")
		counter.addString(dotnetDoubleString(float64(lengthTicks) / float64(subtitleTicksPerSecond)))
		counter.addString(",\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=")
		counter.add(decimalInt64Length(positionTicks))
		counter.addString("&EndPositionTicks=")
		counter.add(decimalInt64Length(endPositionTicks))
		counter.addString("&ApiKey=")
		counter.addString(url.QueryEscape(request.APIKey))
		counter.add(1)

		positionTicks = endPositionTicks
	}
	counter.addString("#EXT-X-ENDLIST\n")
	return counter.result()
}

func subtitleSegmentTicks(seconds int) (int64, error) {
	if seconds <= 0 || int64(seconds) > math.MaxInt64/subtitleTicksPerSecond {
		return 0, ErrSubtitleSegmentLength
	}
	return int64(seconds) * subtitleTicksPerSecond, nil
}

// dotnetDoubleString follows the default .NET double general-format switch:
// fixed notation for decimal exponents -4 through 14, scientific notation
// elsewhere. This differs from Go's 'g' formatting at the upper boundary.
func dotnetDoubleString(value float64) string {
	if value == 0 {
		return "0"
	}

	scientific := strconv.FormatFloat(value, 'E', -1, 64)
	exponentStart := strings.LastIndexByte(scientific, 'E')
	if exponentStart < 0 {
		return scientific
	}
	exponent, err := strconv.Atoi(scientific[exponentStart+1:])
	if err != nil {
		return scientific
	}
	if exponent >= -4 && exponent <= 14 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return scientific
}
