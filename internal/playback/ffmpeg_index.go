// SPDX-License-Identifier: GPL-2.0-only

package playback

// ffmpegStreamIndex ports EncodingHelper.FindIndex
// (EncodingHelper.cs:7918-7937). MediaStreams lists external sidecars first,
// so a MediaStream index is not an ffmpeg input stream index. The ffmpeg
// index is the count of earlier streams with the same path. An embedded
// stream counts only the embedded streams before it; an external stream
// counts its position in its own file. The result is -1 when the stream is
// not in the list.
func ffmpegStreamIndex(streams []MediaStream, stream *MediaStream) int {
	if stream == nil {
		return -1
	}
	for position := range streams {
		if streams[position].Type == stream.Type && streams[position].Index == stream.Index {
			return embeddedInputIndex(streams, position)
		}
	}
	return -1
}

// ffmpegSubtitleIndex returns the position of a subtitle stream among the
// subtitle streams of its own file. The ffmpeg subtitles filter option si=
// counts subtitle streams only, not all streams of the input.
func ffmpegSubtitleIndex(streams []MediaStream, stream *MediaStream) int {
	if stream == nil {
		return -1
	}
	index := 0
	for position := range streams {
		candidate := &streams[position]
		if candidate.Type == stream.Type && candidate.Index == stream.Index {
			return index
		}
		if candidate.Type == StreamTypeSubtitle && candidate.Path == stream.Path {
			index++
		}
	}
	return -1
}
