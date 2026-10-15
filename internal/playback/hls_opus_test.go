// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// hlsOpusSource is an MKV source with AV1 video and stereo Opus audio.
func hlsOpusSource(channels int) playback.MediaSourceInfo {
	source := progressiveCopySource("/media/av1-opus.mkv")
	source.Container = "mkv"
	video := &source.MediaStreams[0]
	video.Codec = "av1"
	video.Profile = nil
	video.Level = nil
	audio := &source.MediaStreams[1]
	audio.Codec = "opus"
	bitrate := 128_000
	audio.BitRate, audio.Channels, audio.BitDepth = &bitrate, &channels, nil
	return source
}

// hlsOpusRequest is the TranscodingUrl that PlaybackInfo returns to Swiftfin
// for that source: H.264/HEVC/VP9 video, Opus audio only, fMP4 segments.
func hlsOpusRequest() playback.HLSRequest {
	request := hlsTranscodeRequest()
	request.VideoCodec, request.VideoCodecs = "h264,hevc,vp9", []string{"h264", "hevc", "vp9"}
	request.AudioCodec, request.AudioCodecs = "opus", []string{"opus"}
	request.AllowAudioStreamCopy, request.EnableAutoStreamCopy = true, true
	request.SegmentContainer = "mp4"
	return request
}

func TestHLSServiceCopiesOpusAudioIntoFMP4Segments(t *testing.T) {
	manager := &hlsFakeManager{segment: "opus segment"}
	service := newHLSService(&progressiveFakeSource{source: hlsOpusSource(2)}, manager)
	request := hlsOpusRequest()

	master, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master(AV1/Opus, AudioCodec=opus, fMP4): %v", err)
	}
	if playlist := readHLSResponse(t, master); !strings.Contains(playlist, `CODECS="avc1.640029,Opus"`) {
		t.Errorf("Master(AV1/Opus) = %q, want H.264 video with copied Opus audio", playlist)
	}

	request.SegmentIndex = 0
	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(AV1/Opus, 0): %v", err)
	}
	args := manager.lastRestart.Args
	if !hlsArgSequence(args, "-codec:a:0", "copy", "-strict", "-2") {
		t.Errorf("AV1/Opus HLS argv = %q, want Opus stream copy with the mp4 muxer strictness flag", args)
	}
	if !hlsArgPair(args, "-hls_segment_type", "fmp4") {
		t.Errorf("AV1/Opus HLS argv = %q, want fMP4 segments", args)
	}
	if hlsHasArg(args, "-ab") || hlsHasArg(args, "-ac") {
		t.Errorf("AV1/Opus HLS argv = %q, must not project reencode-only audio options", args)
	}
}

func TestHLSServiceOpusCopyHonorsCodecQualifiedChannelLimit(t *testing.T) {
	request := hlsOpusRequest()
	request.MaxAudioChannels = nil
	request.StreamOptions.Set("opus-audiochannels", "2")
	request.AudioCodec, request.AudioCodecs = "opus,aac", []string{"opus", "aac"}
	manager := &hlsFakeManager{segment: "segment"}
	service := newHLSService(&progressiveFakeSource{source: hlsOpusSource(6)}, manager)

	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment(6ch Opus, opus-audiochannels=2): %v", err)
	}
	if !hlsArgPair(manager.lastRestart.Args, "-codec:a:0", "aac") {
		t.Errorf("6ch Opus argv = %q, want an AAC encode when the Opus channel limit forbids copy", manager.lastRestart.Args)
	}
}

func TestHLSServiceRejectsOpusOnlyRequestWhenOpusCannotBeCopied(t *testing.T) {
	request := hlsOpusRequest()
	request.AllowAudioStreamCopy = false
	service := newHLSService(&progressiveFakeSource{source: hlsOpusSource(2)}, &hlsFakeManager{})

	if _, err := service.Master(t.Context(), request); !errors.Is(err, playback.ErrProgressiveUnsupported) {
		t.Fatalf("Master(Opus only, copy disallowed) error = %v, want ErrProgressiveUnsupported (D-50: HLS encodes audio only to AAC)", err)
	}
}
