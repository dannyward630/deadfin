// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// Stock DirectMedia PlaybackInfo labels every deliverable subtitle stream, not
// only the selected default.  In particular, an external SRT selection retains
// its server URL while a separate internal SubRip stream is delivered embedded.
func TestResolvePlaybackInfo_DirectMediaProfileAnnotatesInternalEmbedAlongsideExternalSelection(t *testing.T) {
	source := playback.MediaSourceInfo{
		ID:                   "subtitle-source",
		Container:            "mkv",
		Protocol:             playback.MediaProtocolFile,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams: []playback.MediaStream{
			{Index: 10, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, SupportsExternalStream: true, Path: "/media/Subtitle Sample.en.srt"},
			{Index: 20, Type: playback.StreamTypeVideo, Codec: "mpeg4"},
			{Index: 30, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
			{Index: 40, Type: playback.StreamTypeSubtitle, Codec: "subrip", SupportsExternalStream: true},
		},
	}
	selected := 10
	request := playback.NewPlaybackInfoRequest()
	request.ItemID = playbackInfoItemID()
	request.MediaType = playback.DlnaVideo
	request.MediaSourceID = source.ID
	request.SubtitleStreamIndex = &selected
	request.Session = playback.PlaybackSession{ID: "subtitle-session", AccessToken: "subtitle-token"}
	request.Permissions = playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true}
	request.Network = playback.PlaybackNetwork{IsLAN: true}
	request.DeviceProfile = &playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{Type: playback.DlnaVideo}},
		SubtitleProfiles: []playback.SubtitleProfile{
			{Format: "srt", Method: playback.SubtitleExternal},
			{Format: "srt", Method: playback.SubtitleEmbed},
		},
	}

	resolved, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(resolved.MediaSources) != 1 {
		t.Fatalf("ResolvePlaybackInfo() sources = %d, want one", len(resolved.MediaSources))
	}

	external, embedded := playbackSubtitleDeliveryStreams(t, resolved.MediaSources[0])
	if external.DeliveryMethod == nil || *external.DeliveryMethod != playback.SubtitleExternal {
		t.Errorf("external subtitle delivery = %#v, want External", external.DeliveryMethod)
	}
	if external.DeliveryURL == "" || external.IsExternalURL == nil || *external.IsExternalURL {
		t.Errorf("external subtitle URL delivery = url:%q external:%#v, want API URL and false", external.DeliveryURL, external.IsExternalURL)
	}
	if embedded.DeliveryMethod == nil || *embedded.DeliveryMethod != playback.SubtitleEmbed {
		t.Errorf("embedded subtitle delivery = %#v, want Embed", embedded.DeliveryMethod)
	}
	if embedded.DeliveryURL != "" || embedded.IsExternalURL != nil {
		t.Errorf("embedded subtitle URL delivery = url:%q external:%#v, want omitted", embedded.DeliveryURL, embedded.IsExternalURL)
	}
}

func playbackSubtitleDeliveryStreams(t *testing.T, source playback.MediaSourceInfo) (external, embedded playback.MediaStream) {
	t.Helper()
	for _, stream := range source.MediaStreams {
		if stream.Type != playback.StreamTypeSubtitle {
			continue
		}
		if stream.IsExternal {
			external = stream
		} else {
			embedded = stream
		}
	}
	if external.Type != playback.StreamTypeSubtitle || embedded.Type != playback.StreamTypeSubtitle {
		t.Fatalf("subtitle streams = %#v, want one external and one embedded subtitle", source.MediaStreams)
	}
	return external, embedded
}

// sharkPartyRemuxRequest models SharkParty's native AVPlayer profile: MKV
// does not direct play, the transcode is an HLS remux, and SRT, VTT, ASS,
// SSA and PGS are External (SharkParty Documentation/subtitle-overlay.md).
func sharkPartyRemuxRequest(source playback.MediaSourceInfo, selected int, canExtract func(string) bool) playback.PlaybackInfoRequest {
	request := playback.NewPlaybackInfoRequest()
	request.ItemID = playbackInfoItemID()
	request.MediaType = playback.DlnaVideo
	request.MediaSourceID = source.ID
	request.SubtitleStreamIndex = &selected
	request.Session = playback.PlaybackSession{ID: "remux-session", AccessToken: "remux-token"}
	request.Permissions = playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true}
	request.Network = playback.PlaybackNetwork{IsLAN: true}
	request.CanExtractSubtitles = canExtract
	request.DeviceProfile = &playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{Type: playback.DlnaVideo, Container: "mp4,m4v,mov", VideoCodec: "h264,hevc", AudioCodec: "aac,ac3,eac3,flac"}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264,hevc", AudioCodec: "aac,ac3,eac3,flac",
			Protocol: playback.StreamProtocolHLS, Context: playback.ContextStreaming,
		}},
		SubtitleProfiles: []playback.SubtitleProfile{
			{Format: "subrip", Method: playback.SubtitleExternal},
			{Format: "vtt", Method: playback.SubtitleExternal},
			{Format: "ass", Method: playback.SubtitleExternal},
			{Format: "ssa", Method: playback.SubtitleExternal},
			{Format: "pgssub", Method: playback.SubtitleExternal},
		},
	}
	return request
}

func remuxSubtitleSource() playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID:                   "0a1b2c3d4e5f60718293a4b5c6d7e8f9",
		Path:                 "/media/anime/Show S01E01.mkv",
		Container:            "mkv",
		Protocol:             playback.MediaProtocolFile,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", IsDefault: true},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "flac", IsDefault: true, Channels: intPointerForTest(2)},
			{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "ass", SupportsExternalStream: true},
			{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", SupportsExternalStream: true},
			{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "subrip", SupportsExternalStream: true},
		},
		MediaAttachments: []playback.MediaAttachment{{Index: 5, Codec: "ttf", FileName: "font.ttf", MimeType: "application/x-truetype-font"}},
	}
}

func intPointerForTest(value int) *int { return &value }

// StreamBuilder.GetExternalSubtitleProfile accepts an embedded stream during
// a transcode only when ITranscoderSupport.CanExtractSubtitles; then every
// subtitle stream carries its External URL (StreamInfo.GetSubtitleStreamInfo).
func TestResolvePlaybackInfo_RemuxDeliversEmbeddedASSAndPGSExternal(t *testing.T) {
	for _, selected := range []int{2, 3} {
		source := remuxSubtitleSource()
		request := sharkPartyRemuxRequest(source, selected, func(string) bool { return true })
		resolved, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(resolved.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(resolved.MediaSources))
		}
		got := resolved.MediaSources[0]
		if got.TranscodingURL == "" || strings.Contains(got.TranscodingURL, "SubtitleMethod=Encode") {
			t.Fatalf("selected %d: TranscodingURL = %q, want an HLS remux without SubtitleMethod=Encode", selected, got.TranscodingURL)
		}
		if strings.Contains(got.TranscodingURL, "SubtitleStreamIndex=") {
			t.Errorf("selected %d: TranscodingURL = %q, want no burned-in subtitle index", selected, got.TranscodingURL)
		}
		base := "/Videos/" + playbackInfoItemID().Dashed() + "/" + source.ID + "/Subtitles/"
		want := map[int]string{
			2: base + "2/0/Stream.ass?ApiKey=remux-token",
			3: base + "3/0/Stream.pgssub?ApiKey=remux-token",
			4: base + "4/0/Stream.subrip?ApiKey=remux-token",
		}
		for _, stream := range got.MediaStreams {
			if stream.Type != playback.StreamTypeSubtitle {
				continue
			}
			if stream.DeliveryMethod == nil || *stream.DeliveryMethod != playback.SubtitleExternal {
				t.Errorf("selected %d: stream %d DeliveryMethod = %v, want External", selected, stream.Index, stream.DeliveryMethod)
			}
			if stream.DeliveryURL != want[stream.Index] || stream.IsExternalURL == nil || *stream.IsExternalURL {
				t.Errorf("selected %d: stream %d DeliveryURL = %q (external %v), want %q", selected, stream.Index, stream.DeliveryURL, stream.IsExternalURL, want[stream.Index])
			}
		}
		if len(got.MediaAttachments) != 1 || got.MediaAttachments[0].DeliveryURL != "/Videos/"+playbackInfoItemID().Dashed()+"/"+source.ID+"/Attachments/5" {
			t.Errorf("selected %d: MediaAttachments = %#v, want the Attachments/5 URL", selected, got.MediaAttachments)
		}
	}
}

// With EnableSubtitleExtraction off, an embedded stream in a transcode falls
// back to Encode (burn-in), as before.
func TestResolvePlaybackInfo_RemuxWithoutExtractionEncodesEmbeddedSubtitles(t *testing.T) {
	source := remuxSubtitleSource()
	for name, canExtract := range map[string]func(string) bool{
		"nil seam":            nil,
		"extraction disabled": func(string) bool { return false },
	} {
		resolved, err := playback.ResolvePlaybackInfo(sharkPartyRemuxRequest(source, 2, canExtract), []playback.MediaSourceInfo{source})
		if err != nil {
			t.Fatalf("%s: ResolvePlaybackInfo() error = %v", name, err)
		}
		got := resolved.MediaSources[0]
		if !strings.Contains(got.TranscodingURL, "SubtitleMethod=Encode") {
			t.Errorf("%s: TranscodingURL = %q, want SubtitleMethod=Encode", name, got.TranscodingURL)
		}
		for _, stream := range got.MediaStreams {
			if stream.Type != playback.StreamTypeSubtitle {
				continue
			}
			if stream.DeliveryMethod == nil || *stream.DeliveryMethod != playback.SubtitleEncode || stream.DeliveryURL != "" {
				t.Errorf("%s: stream %d delivery = %v %q, want Encode without URL", name, stream.Index, stream.DeliveryMethod, stream.DeliveryURL)
			}
		}
	}
}
