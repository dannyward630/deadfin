// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// swiftfinVLCProfile is the HLS transcoding profile shape of the Swiftfin and
// SharkParty VLC player (VideoPlayerType+VLC.swift). It has no direct-play
// profile, so an AV1 source must transcode.
const swiftfinVLCProfile = `{"TranscodingProfiles":[{"Type":"Video","Context":"Streaming","Protocol":"hls","Container":"mp4","BreakOnNonKeyFrames":true,"MinSegments":2,"MaxAudioChannels":"8",` +
	`"VideoCodec":"h263,h264,hevc,mjpeg,mpeg1video,mpeg2video,mpeg4,vp9","AudioCodec":"aac,ac3,alac,dts,eac3,flac,mp1,mp2,mp3,opus,vorbis"}]}`

// TestPlaybackInfoOpusHLSURLServesMasterAndSegment is the Opus HLS
// regression. PlaybackInfo keeps the copyable Opus stream as the only HLS
// audio codec (StreamBuilder.cs:974-1030), and the HLS handler must serve
// that URL by copying Opus into fMP4 segments, as DynamicHlsController does.
func TestPlaybackInfoOpusHLSURLServesMasterAndSegment(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007d6")
	container, path := "mkv", "/media/av1-opus-episode.mkv"
	videoBitrate, audioBitrate, totalBitrate := 3_000_000, 128_000, 3_128_000
	width, height, channels, sampleRate := 1920, 1080, 2, 48_000
	playbackInfo := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{hlsCanonicalTestRecord(playback.MediaSourceRecord{
			ID: itemID, Path: &path, Container: &container, TotalBitrate: &totalBitrate,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "av1", BitRate: &videoBitrate, Width: &width, Height: &height},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "opus", BitRate: &audioBitrate, Channels: &channels, SampleRate: &sampleRate, IsDefault: true},
			},
		})}},
		Permissions:      playbackConcretePermissionReader{},
		Network:          playbackConcreteNetwork{},
		NewPlaySessionID: func() string { return "opus-session" },
	})
	manager := &codecBurnHLSManager{}
	hlsService := playback.NewHLSService(playback.HLSServiceConfig{
		Sources:      playbackInfo,
		TranscodeDir: "/cache",
		Capabilities: encoding.Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}, EncoderMajorVersion: 7},
		Manager:      manager,
	})
	routes := router.New()
	handlers.RegisterPlaybackInfo(routes, playbackInfo, nil)
	handlers.RegisterHLSStreams(routes, hlsService, slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{"DeviceId":"opus-device","MediaSourceId":"` + strings.ReplaceAll(itemID.Dashed(), "-", "") + `","MaxStreamingBitrate":20000000,"DeviceProfile":` + swiftfinVLCProfile + `}`
	playbackRecord := request(routes, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
	assertStatus(t, playbackRecord, http.StatusOK)
	var resolved playback.PlaybackInfoResponse
	if err := json.Unmarshal(playbackRecord.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, playbackRecord.Body.Bytes())
	}
	if len(resolved.MediaSources) != 1 || resolved.MediaSources[0].TranscodingURL == "" {
		t.Fatalf("PlaybackInfo sources = %#v, want exactly one generated HLS URL", resolved.MediaSources)
	}
	generated, err := url.Parse(resolved.MediaSources[0].TranscodingURL)
	if err != nil {
		t.Fatalf("parse generated HLS URL: %v", err)
	}
	query := generated.Query()
	if got := query.Get("AudioCodec"); got != "opus" {
		t.Fatalf("generated HLS URL AudioCodec = %q, want opus (pinned StreamBuilder keeps the copyable source codec); URL=%q", got, generated.RawQuery)
	}
	if got := query.Get("SegmentContainer"); got != "mp4" {
		t.Fatalf("generated HLS URL SegmentContainer = %q, want mp4", got)
	}

	masterRecord := request(routes, http.MethodGet, generated.RequestURI(), "", "", "")
	assertStatus(t, masterRecord, http.StatusOK)
	if playlist := masterRecord.Body.String(); !strings.Contains(playlist, `,Opus"`) {
		t.Fatalf("master playlist = %q, want an Opus audio codec", playlist)
	}

	query.Set("runtimeTicks", "0")
	query.Set("actualSegmentLengthTicks", "30000000")
	segmentURL := "/Videos/" + itemID.Dashed() + "/hls1/main/0.mp4?" + query.Encode()
	segmentRecord := request(routes, http.MethodGet, segmentURL, "", "", "")
	assertStatus(t, segmentRecord, http.StatusOK)
	args := manager.lastRestart.Args
	if !hlsArgsContainSequence(args, "-codec:a:0", "copy", "-strict", "-2") {
		t.Fatalf("first segment argv = %q, want Opus stream copy with mp4 muxer strictness", args)
	}
	if !hlsArgsContainSequence(args, "-hls_segment_type", "fmp4") {
		t.Fatalf("first segment argv = %q, want fMP4 segments", args)
	}
}

func hlsArgsContainSequence(args []string, want ...string) bool {
	for start := 0; start+len(want) <= len(args); start++ {
		match := true
		for offset, value := range want {
			if args[start+offset] != value {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
