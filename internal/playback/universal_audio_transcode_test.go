// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// UniversalAudioController.GetUniversalAudioStream @ b04614e
// (UniversalAudioController.cs:113-160): the source plays directly when a
// direct-play profile and every limit accept it; otherwise the transcoding
// profile built from TranscodingContainer, AudioCodec, and
// TranscodingProtocol selects a progressive or HLS transcode. Without a
// matching container, GetTranscodeReasonsFromDirectPlayProfile
// (StreamBuilder.cs:504-544) also reports VideoCodecNotSupported.
func TestDecideUniversalAudioSelectsStaticProgressiveOrHLS(t *testing.T) {
	hls := playback.StreamProtocolHLS
	limit := func(value int) *int { return &value }
	for _, tc := range []struct {
		name          string
		request       playback.UniversalAudioRequest
		wantStatic    bool
		wantProtocol  playback.MediaStreamProtocol
		wantContainer string
		wantReasons   playback.TranscodeReason
	}{
		{
			name:       "FLAC in the container list plays directly",
			request:    playback.UniversalAudioRequest{Containers: []string{"opus", "mp3|mp3", "flac"}},
			wantStatic: true,
		},
		{
			name:          "an mp3-only client transcodes over http",
			request:       playback.UniversalAudioRequest{Containers: []string{"mp3"}, TranscodingContainer: "mp3", AudioCodec: "mp3"},
			wantProtocol:  playback.StreamProtocolHTTP,
			wantContainer: "mp3",
			wantReasons:   playback.ReasonContainerNotSupported | playback.ReasonVideoCodecNotSupported | playback.ReasonAudioCodecNotSupported,
		},
		{
			name:          "omitted transcoding values default to mp3 over http",
			request:       playback.UniversalAudioRequest{Containers: []string{"aac"}},
			wantProtocol:  playback.StreamProtocolHTTP,
			wantContainer: "mp3",
			wantReasons:   playback.ReasonContainerNotSupported | playback.ReasonVideoCodecNotSupported | playback.ReasonAudioCodecNotSupported,
		},
		{
			name:          "a bitrate limit below the source transcodes",
			request:       playback.UniversalAudioRequest{Containers: []string{"flac"}, TranscodingContainer: "mp3", AudioCodec: "mp3", MaxStreamingBitrate: limit(128000)},
			wantProtocol:  playback.StreamProtocolHTTP,
			wantContainer: "mp3",
			wantReasons:   playback.ReasonContainerBitrateExceedsLimit,
		},
		{
			name:          "a sample-rate limit transcodes",
			request:       playback.UniversalAudioRequest{Containers: []string{"flac"}, TranscodingContainer: "aac", AudioCodec: "aac", MaxAudioSampleRate: limit(44100)},
			wantProtocol:  playback.StreamProtocolHTTP,
			wantContainer: "aac",
			wantReasons:   playback.ReasonAudioSampleRateNotSupported,
		},
		{
			name:          "hls protocol selects HLS segments",
			request:       playback.UniversalAudioRequest{Containers: []string{"mp3"}, TranscodingContainer: "ts", AudioCodec: "aac", TranscodingProtocol: &hls},
			wantProtocol:  playback.StreamProtocolHLS,
			wantContainer: "ts",
			wantReasons:   playback.ReasonContainerNotSupported | playback.ReasonVideoCodecNotSupported | playback.ReasonAudioCodecNotSupported,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := playback.DecideUniversalAudio(tc.request, universalFLACSource(), "item", nil)
			if err != nil {
				t.Fatalf("DecideUniversalAudio() error = %v", err)
			}
			if decision.Static != tc.wantStatic {
				t.Fatalf("DecideUniversalAudio().Static = %v, want %v; decision=%+v", decision.Static, tc.wantStatic, decision)
			}
			if tc.wantStatic {
				return
			}
			if decision.Protocol != tc.wantProtocol || decision.TranscodingContainer != tc.wantContainer {
				t.Errorf("DecideUniversalAudio() protocol=%v container=%q, want %v %q", decision.Protocol, decision.TranscodingContainer, tc.wantProtocol, tc.wantContainer)
			}
			if decision.TranscodeReasons != tc.wantReasons {
				t.Errorf("DecideUniversalAudio().TranscodeReasons = %q, want %q", decision.TranscodeReasonNames(), playback.UniversalAudioDecision{TranscodeReasons: tc.wantReasons}.TranscodeReasonNames())
			}
		})
	}
}

// MediaInfoHelper.GetMaxBitrate @ b04614e (MediaInfoHelper.cs:504-526) caps
// a remote client at the user's remote limit; a LAN client keeps the client
// limit, even above the 8 Mbps profile default.
func TestUniversalAudioAppliesRemoteBitrateLimit(t *testing.T) {
	itemID := universalAudioItemID
	remote := 192000
	for _, tc := range []struct {
		name       string
		lan        bool
		wantStatic bool
	}{
		{name: "LAN client plays the FLAC directly", lan: true, wantStatic: true},
		{name: "remote client transcodes under the remote limit", lan: false, wantStatic: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
				Sources:          playbackServiceSourceReader{records: []playback.MediaSourceRecord{universalFLACRecord(itemID)}},
				Permissions:      playbackServicePermissionReader{permissions: playback.PlaybackPermissions{EnableAudioTranscoding: true, RemoteBitrateLimit: &remote}},
				Network:          playbackServiceNetwork{isLAN: tc.lan},
				NewPlaySessionID: func() string { return "universal-session" },
			})
			clientLimit := 140_000_000
			decision, err := service.UniversalAudio(t.Context(), playback.UniversalAudioQuery{
				UserID:        dto.UUID{0xa1, 0x1c},
				ItemID:        itemID,
				ClientAddress: netip.MustParseAddr("203.0.113.7"),
				Request: playback.UniversalAudioRequest{
					Containers: []string{"flac"}, TranscodingContainer: "mp3", AudioCodec: "mp3", MaxStreamingBitrate: &clientLimit,
				},
			})
			if err != nil {
				t.Fatalf("UniversalAudio() error = %v", err)
			}
			if decision.Static != tc.wantStatic {
				t.Errorf("UniversalAudio().Static = %v, want %v", decision.Static, tc.wantStatic)
			}
			if decision.PlaySessionID != "universal-session" {
				t.Errorf("UniversalAudio().PlaySessionID = %q, want the minted session", decision.PlaySessionID)
			}
		})
	}
}

// StreamingHelpers.GetStreamingState and
// EncodingHelper.GetProgressiveAudioFullCommandLine @ b04614e: the first
// encodable codec, the lame two-channel limit, GetAudioBitrateParam, and the
// sample rate shape the ffmpeg job.
func TestOpenProgressiveAudioBuildsTheUpstreamTranscode(t *testing.T) {
	limit := func(value int) *int { return &value }
	caps := encoding.Capabilities{Encoders: []string{"aac", "libmp3lame", "libopus"}, AACEncoders: []string{"aac"}}
	for _, tc := range []struct {
		name            string
		request         playback.ProgressiveAudioRequest
		wantArgs        string
		wantContentType string
		wantExtension   string
	}{
		{
			name:            "mp3 from a 5.1 FLAC downmixes to the lame limit",
			request:         playback.ProgressiveAudioRequest{Container: "mp3", AudioCodec: "mp3", AudioBitrate: limit(140_000_000)},
			wantArgs:        "-i file:/media/song.flac -threads 0 -vn -ab 256000 -ac 2 -acodec libmp3lame -id3v2_version 3 -write_id3v1 1 -y",
			wantContentType: "audio/mpeg",
			wantExtension:   ".mp3",
		},
		{
			name:            "first encodable codec and the request limits",
			request:         playback.ProgressiveAudioRequest{AudioCodec: "wmapro,aac", AudioBitrate: limit(128000), MaxAudioChannels: limit(2), AudioSampleRate: limit(44100)},
			wantArgs:        "-i file:/media/song.flac -threads 0 -vn -ab 128000 -ac 2 -acodec aac -ar 44100 -id3v2_version 3 -write_id3v1 1 -y",
			wantContentType: "audio/aac",
			wantExtension:   ".aac",
		},
		{
			name:            "route extension infers the codec",
			request:         playback.ProgressiveAudioRequest{Container: "ogg", StartTimeTicks: 20_000_000},
			wantArgs:        "-ss 00:00:02.000 -i file:/media/song.flac -threads 0 -vn -ab 640000 -ac 6 -acodec libopus -id3v2_version 3 -write_id3v1 1 -y",
			wantContentType: "audio/ogg",
			wantExtension:   ".ogg",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opener := &progressiveFakeOpener{body: io.NopCloser(strings.NewReader("audio"))}
			service := playback.NewProgressiveService(playback.ProgressiveServiceConfig{
				Sources: &progressiveFakeSource{source: universalFLACSource()}, TranscodeDir: "/cache", Opener: opener,
				Capabilities: func() encoding.Capabilities { return caps },
			})
			request := tc.request
			request.ItemID = universalAudioItemID
			response, err := service.OpenProgressiveAudio(t.Context(), request)
			if err != nil {
				t.Fatalf("OpenProgressiveAudio() error = %v", err)
			}
			if response.ContentType != tc.wantContentType || !strings.HasSuffix(response.OutputPath, tc.wantExtension) {
				t.Errorf("OpenProgressiveAudio() content type %q output %q, want %q and extension %q", response.ContentType, response.OutputPath, tc.wantContentType, tc.wantExtension)
			}
			args := opener.request.Args
			if len(args) == 0 || args[len(args)-1] != response.OutputPath {
				t.Fatalf("ffmpeg argv = %q, want it to end with the output %q", args, response.OutputPath)
			}
			if got := strings.Join(args[:len(args)-1], " "); got != tc.wantArgs {
				t.Errorf("ffmpeg argv =\n  %s\nwant\n  %s", got, tc.wantArgs)
			}
		})
	}
}

func TestOpenProgressiveAudioHeadStartsNoProcess(t *testing.T) {
	opener := &progressiveFakeOpener{body: io.NopCloser(strings.NewReader("audio"))}
	service := playback.NewProgressiveService(playback.ProgressiveServiceConfig{
		Sources: &progressiveFakeSource{source: universalFLACSource()}, TranscodeDir: "/cache", Opener: opener,
		Capabilities: func() encoding.Capabilities { return encoding.Capabilities{Encoders: []string{"libmp3lame"}} },
	})
	response, err := service.OpenProgressiveAudio(t.Context(), playback.ProgressiveAudioRequest{
		ItemID: universalAudioItemID, Container: "mp3", AudioCodec: "mp3", Head: true,
	})
	if err != nil {
		t.Fatalf("OpenProgressiveAudio(HEAD) error = %v", err)
	}
	if opener.calls != 0 || response.Body != nil || response.ContentType != "audio/mpeg" {
		t.Errorf("OpenProgressiveAudio(HEAD) = %+v with %d opener calls, want audio/mpeg without a process", response, opener.calls)
	}
}

func universalFLACSource() playback.MediaSourceInfo {
	bitrate, channels, sampleRate, bitDepth, streamBitrate := 2_400_000, 6, 96000, 24, 2_400_000
	inode, size := int64(7), int64(1<<20)
	changed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	runtime := int64(600_000_000)
	source := playback.NewMediaSourceInfo()
	source.ID = "00000000000000000000000000004a7f"
	source.Protocol = playback.MediaProtocolFile
	source.Path = "/media/song.flac"
	source.Container = "flac"
	source.Bitrate = &bitrate
	source.RunTimeTicks = &runtime
	source.SourceRevision = "flac-revision"
	source.FileFacts = playback.SourceFileFacts{Size: &size, Inode: &inode, ChangedAt: &changed}
	source.MediaStreams = []playback.MediaStream{{
		Index: 0, Type: playback.StreamTypeAudio, Codec: "flac", IsDefault: true,
		Channels: &channels, SampleRate: &sampleRate, BitDepth: &bitDepth, BitRate: &streamBitrate,
	}}
	return source
}

func universalFLACRecord(itemID dto.UUID) playback.MediaSourceRecord {
	source := universalFLACSource()
	container := source.Container
	return playback.MediaSourceRecord{
		ID: itemID, Path: &source.Path, Container: &container, RunTimeTicks: source.RunTimeTicks,
		TotalBitrate: source.Bitrate, MediaStreams: source.MediaStreams,
	}
}

var universalAudioItemID = dto.UUID{0x04, 0xa7}
