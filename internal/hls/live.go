// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"path"
	"strings"
)

// Fmp4InitFileName returns the init fragment produced beside playlistPath. On
// Windows ffmpeg is given an absolute init URI; on Unix only its basename is
// exposed in the playlist.
func Fmp4InitFileName(playlistPath, segmentContainer string, windows bool) string {
	if !strings.EqualFold(segmentContainer, "fmp4") {
		return ""
	}
	initPath := strings.TrimSuffix(playlistPath, ".m3u8") + "-1.mp4"
	if windows {
		return initPath
	}
	return path.Base(initPath)
}

// RewriteLivePlaylist rewrites only fMP4 init-map URI values to handler URIs;
// media fragment URIs are already relative to the playlist and are retained
// byte-for-byte.
func RewriteLivePlaylist(playlist, playlistID, segmentContainer string) string {
	if !strings.EqualFold(segmentContainer, "fmp4") && !strings.EqualFold(segmentContainer, "mp4") {
		return playlist
	}

	var rewritten strings.Builder
	lastWritten := 0
	for lineStart := 0; lineStart < len(playlist); {
		lineEnd := strings.IndexByte(playlist[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(playlist)
		} else {
			lineEnd += lineStart
		}

		uriStart, uriEnd, ok := liveMapURIValue(playlist[lineStart:lineEnd])
		if ok {
			initName := initMapBaseName(playlist[lineStart+uriStart : lineStart+uriEnd])
			if initName != "" {
				if lastWritten == 0 {
					rewritten.Grow(len(playlist) + len("hls/") + len(playlistID) + 1)
				}
				rewritten.WriteString(playlist[lastWritten : lineStart+uriStart])
				rewritten.WriteString("hls/")
				rewritten.WriteString(playlistID)
				rewritten.WriteByte('/')
				rewritten.WriteString(initName)
				lastWritten = lineStart + uriEnd
			}
		}

		if lineEnd == len(playlist) {
			break
		}
		lineStart = lineEnd + 1
	}

	if lastWritten == 0 {
		return playlist
	}
	rewritten.WriteString(playlist[lastWritten:])
	return rewritten.String()
}

func liveMapURIValue(line string) (int, int, bool) {
	const mapPrefix = "#EXT-X-MAP:"
	if !strings.HasPrefix(line, mapPrefix) {
		return 0, 0, false
	}

	lineEnd := len(line)
	if lineEnd > len(mapPrefix) && line[lineEnd-1] == '\r' {
		lineEnd--
	}

	uriStart, uriEnd := -1, -1
	for position := len(mapPrefix); position < lineEnd; {
		nameStart := position
		equals := strings.IndexByte(line[position:lineEnd], '=')
		if equals < 1 {
			return 0, 0, false
		}
		equals += position
		if !isLiveMapAttributeName(line[nameStart:equals]) {
			return 0, 0, false
		}
		position = equals + 1
		if position == lineEnd {
			return 0, 0, false
		}

		quoted := line[position] == '"'
		valueStart := position
		valueEnd := lineEnd
		if quoted {
			valueStart++
			quote := strings.IndexByte(line[valueStart:lineEnd], '"')
			if quote < 0 {
				return 0, 0, false
			}
			valueEnd = valueStart + quote
			position = valueEnd + 1
		} else {
			comma := strings.IndexByte(line[position:lineEnd], ',')
			if comma >= 0 {
				valueEnd = position + comma
			}
			if valueEnd == valueStart || strings.IndexByte(line[valueStart:valueEnd], '"') >= 0 {
				return 0, 0, false
			}
			position = valueEnd
		}

		if line[nameStart:equals] == "URI" {
			if !quoted || uriStart >= 0 {
				return 0, 0, false
			}
			uriStart, uriEnd = valueStart, valueEnd
		}

		if position == lineEnd {
			break
		}
		if line[position] != ',' {
			return 0, 0, false
		}
		position++
		if position == lineEnd {
			return 0, 0, false
		}
	}

	if uriStart < 0 {
		return 0, 0, false
	}
	return uriStart, uriEnd, true
}

func isLiveMapAttributeName(name string) bool {
	for _, character := range name {
		if character != '-' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

func initMapBaseName(uri string) string {
	if separator := strings.LastIndexAny(uri, `/\\`); separator >= 0 {
		return uri[separator+1:]
	}
	return uri
}
