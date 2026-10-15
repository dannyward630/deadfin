// SPDX-License-Identifier: GPL-2.0-only

// Package subtitles converts and windows text subtitle streams for request-scoped
// HLS subtitle delivery.
package subtitles

import "strings"

// NormalizeFormat maps ffmpeg codec names to the parser extensions used by
// Jellyfin's subtitle encoder. It deliberately preserves a leading dot: callers
// decide whether their input is a codec name or a filename extension.
func NormalizeFormat(codecOrExtension string) string {
	format := strings.ToLower(codecOrExtension)
	switch format {
	case "subrip":
		return "srt"
	case "webvtt":
		return "vtt"
	default:
		return format
	}
}
