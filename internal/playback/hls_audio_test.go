// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestHLSServiceAudioOnlyMasterUsesExactAudioCodecWithoutVideoAttributes(t *testing.T) {
	source := hlsAudioOnlySource("/media/album.m4a", "ac3")
	service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})
	request := hlsAudioOnlyRequest()

	response, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master(audio-only): %v", err)
	}
	playlist := readHLSResponse(t, response)
	const want = "#EXT-X-STREAM-INF:BANDWIDTH=128000,AVERAGE-BANDWIDTH=128000,CODECS=\"mp4a.40.2\"\nmain.m3u8\n"
	if playlist != "#EXTM3U\n"+want {
		t.Errorf("Master(audio-only) = %q, want %q", playlist, "#EXTM3U\n"+want)
	}
	for _, forbidden := range []string{"RESOLUTION=", "FRAME-RATE=", "VIDEO-RANGE=", "avc", "vp0", "hvc"} {
		if strings.Contains(playlist, forbidden) {
			t.Errorf("Master(audio-only) leaked video attribute %q: %q", forbidden, playlist)
		}
	}
}

func TestHLSServiceAudioOnlyMainAndSegmentUseAudioOnlyArtifacts(t *testing.T) {
	source := hlsAudioOnlySource("/media/album.m4a", "ac3")
	runtime := int64(120_000_000)
	source.RunTimeTicks = &runtime
	manager := &hlsFakeManager{segment: "audio-segment"}
	service := newHLSService(&progressiveFakeSource{source: source}, manager)
	request := hlsAudioOnlyRequest()
	request.RawQuery = "api_key=audio-token"

	main, err := service.Main(t.Context(), request)
	if err != nil {
		t.Fatalf("Main(audio-only): %v", err)
	}
	const wantMain = "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:6.000000, nodesc\nhls1/main/0.ts?api_key=audio-token&runtimeTicks=0&actualSegmentLengthTicks=60000000\n#EXTINF:6.000000, nodesc\nhls1/main/1.ts?api_key=audio-token&runtimeTicks=60000000&actualSegmentLengthTicks=60000000\n#EXT-X-ENDLIST\n"
	if got := readHLSResponse(t, main); got != wantMain {
		t.Errorf("Main(audio-only) = %q, want exact audio segment playlist %q", got, wantMain)
	}

	request.SegmentIndex = 1
	request.RuntimeTicks = 60_000_000
	segment, err := service.Segment(t.Context(), request)
	if err != nil {
		t.Fatalf("Segment(audio-only): %v", err)
	}
	if got := readHLSResponse(t, segment); got != "audio-segment" {
		t.Errorf("Segment(audio-only) body = %q, want manager artifact", got)
	}
	if manager.restartCalls != 1 || manager.lastRestart.PlaylistPath != segment.OutputPath || manager.lastWaitIndex != 1 {
		t.Errorf("Segment(audio-only) manager lifecycle = restart:%d request:%#v wait-index:%d", manager.restartCalls, manager.lastRestart, manager.lastWaitIndex)
	}
	// The audio stream is the only stream of the file, so its ffmpeg index
	// is 0 (EncodingHelper.FindIndex), whatever its MediaStream index.
	if got := hlsAudioOnlyMapValues(manager.lastRestart.Args); len(got) != 1 || got[0] != "0:0" {
		t.Errorf("Segment(audio-only) manager argv maps = %q, want only selected audio; argv=%q", got, manager.lastRestart.Args)
	}
	for _, argument := range manager.lastRestart.Args {
		if strings.HasPrefix(argument, "-codec:v") || argument == "-vf" || argument == "-filter_complex" {
			t.Errorf("Segment(audio-only) manager argv leaked video token %q: %q", argument, manager.lastRestart.Args)
		}
	}
}

func TestHLSServiceAudioOnlyRejectsSourceWithoutAudio(t *testing.T) {
	source := progressiveCopySource("/media/silent-video.mp4")
	source.MediaStreams = source.MediaStreams[:1]
	service := newHLSService(&progressiveFakeSource{source: source}, &hlsFakeManager{})

	_, err := service.Master(t.Context(), hlsAudioOnlyRequest())
	if !errors.Is(err, playback.ErrProgressiveUnsupported) {
		t.Fatalf("Master(audio-only source without audio) error = %v, want ErrProgressiveUnsupported", err)
	}
}

func hlsAudioOnlyRequest() playback.HLSRequest {
	request := hlsTranscodeRequest()
	request.AudioOnly = true
	request.VideoCodec = ""
	request.VideoCodecs = nil
	request.AllowVideoStreamCopy = false
	request.AudioCodec = "aac"
	request.AudioCodecs = []string{"aac"}
	request.AllowAudioStreamCopy = false
	request.EnableAutoStreamCopy = false
	request.EndpointPrefix = "hls1/main/"
	return request
}

func hlsAudioOnlySource(path, codec string) playback.MediaSourceInfo {
	source := progressiveCopySource(path)
	source.MediaStreams = []playback.MediaStream{source.MediaStreams[1]}
	source.MediaStreams[0].Codec = codec
	return source
}

func hlsAudioOnlyMapValues(args []string) []string {
	maps := make([]string, 0, 1)
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-map" {
			maps = append(maps, args[index+1])
		}
	}
	return maps
}

// StreamingHelpers.GetStreamingState @ b04614e (StreamingHelpers.cs:186)
// sets an audio-only OutputAudioBitrate from GetAudioBitrateParam: the
// codec and channel bitrate, capped by the requested AudioBitRate.
func TestHLSServiceAudioOnlyBitrateUsesGetAudioBitrateParam(t *testing.T) {
	limit := func(value int) *int { return &value }
	for _, tc := range []struct {
		name      string
		requested *int
		want      string
	}{
		{name: "no request uses the stereo aac bitrate", want: "256000"},
		{name: "request above the codec bitrate", requested: limit(140_000_000), want: "256000"},
		{name: "request below the codec bitrate", requested: limit(128000), want: "128000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := hlsAudioOnlySource("/media/album.m4a", "ac3")
			runtime := int64(120_000_000)
			source.RunTimeTicks = &runtime
			manager := &hlsFakeManager{segment: "audio-segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsAudioOnlyRequest()
			request.AudioBitrate = tc.requested
			request.MaxAudioChannels = limit(2)
			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(audio-only): %v", err)
			}
			args := manager.lastRestart.Args
			for index := 0; index+1 < len(args); index++ {
				if args[index] == "-b:a" || args[index] == "-ab" {
					if args[index+1] != tc.want {
						t.Errorf("audio bitrate %s %s, want %s; argv=%q", args[index], args[index+1], tc.want, args)
					}
					return
				}
			}
			t.Errorf("argv has no audio bitrate, want %s: %q", tc.want, args)
		})
	}
}
