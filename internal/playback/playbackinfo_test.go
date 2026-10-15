// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// Sources: upstream/jellyfin/Jellyfin.Api/Helpers/MediaInfoHelper.cs:87-145;
// MediaInfoController.cs:177-217. ResolvePlaybackInfo is the pure public service
// seam: callers supply assembled media sources, the resolved device profile, and
// the already-authenticated session/policy/network context.
func TestResolvePlaybackInfo_EmptyAndRequestedNoMatchAreNoCompatibleStream(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []playback.MediaSourceInfo
		request playback.PlaybackInfoRequest
	}{
		{
			name:    "empty source list",
			sources: []playback.MediaSourceInfo{},
			request: playbackInfoRequest(),
		},
		{
			name:    "requested source is absent",
			sources: []playback.MediaSourceInfo{playbackInfoVideoSource("available", false)},
			request: playbackInfoRequest(playback.WithMediaSourceID("missing")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playback.ResolvePlaybackInfo(tc.request, tc.sources)
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if got.ErrorCode != playback.PlaybackInfoNoCompatibleStream {
				t.Errorf("ErrorCode = %v, want NoCompatibleStream", got.ErrorCode)
			}
			if len(got.MediaSources) != 0 {
				t.Errorf("MediaSources = %#v, want empty", got.MediaSources)
			}
		})
	}
}

// Sources: MediaInfoHelper.cs:127-143,249-276; WP-P4-02:21-22.
// The caller owns session creation, so every resolved source and URL must share
// the injected ID rather than minting one source-specific session.
func TestResolvePlaybackInfo_UsesInjectedSessionAndDirectPlaysWithoutTranscodingProfiles(t *testing.T) {
	request := playbackInfoRequest(
		playback.WithSession(playback.PlaybackSession{ID: "shared-session", DeviceID: "device-1", AccessToken: "api-token"}),
		playback.WithDeviceProfile(&playback.DeviceProfile{
			ID: "direct-profile",
			DirectPlayProfiles: []playback.DirectPlayProfile{{
				Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
			}},
			TranscodingProfiles: []playback.TranscodingProfile{},
		}),
	)

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{playbackInfoVideoSource("direct", false)})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if got.PlaySessionID != "shared-session" {
		t.Errorf("PlaySessionID = %q, want injected session", got.PlaySessionID)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
	}
	if !got.MediaSources[0].SupportsDirectPlay {
		t.Error("direct-compatible source lost direct play")
	}
	if got.MediaSources[0].TranscodingURL != "" {
		t.Errorf("direct-play TranscodingURL = %q, want empty", got.MediaSources[0].TranscodingURL)
	}
}

// Sources: MediaInfoHelper.cs:220-228,249-276,287-328. Direct streaming is
// deliberately disabled for normal HTTP playback; it becomes selectable only
// when callers force it. Permission denial then removes the transcode fallback.
func TestResolvePlaybackInfo_GatesDirectStreamAndTranscodingPermissions(t *testing.T) {
	profile := playback.DeviceProfile{
		ID: "transcode-profile",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	source := playbackInfoVideoSource("stream-candidate", false)
	source.Container = "webm"

	for _, tc := range []struct {
		name          string
		request       playback.PlaybackInfoRequest
		wantStream    bool
		wantTranscode bool
	}{
		{
			name:          "ordinary request transcodes instead of direct streaming",
			request:       playbackInfoRequest(playback.WithDeviceProfile(&profile)),
			wantStream:    false,
			wantTranscode: true,
		},
		{
			name:          "forced direct stream exposes remux path",
			request:       playbackInfoRequest(playback.WithDeviceProfile(&profile), playback.WithForceDirectStream(true)),
			wantStream:    true,
			wantTranscode: true,
		},
		{
			name: "no audio video or remux permission removes transcoding",
			request: playbackInfoRequest(
				playback.WithDeviceProfile(&profile),
				playback.WithPermissions(playback.PlaybackPermissions{}),
			),
			wantStream:    false,
			wantTranscode: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playback.ResolvePlaybackInfo(tc.request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if resolved.SupportsDirectStream != tc.wantStream {
				t.Errorf("SupportsDirectStream = %t, want %t", resolved.SupportsDirectStream, tc.wantStream)
			}
			if resolved.SupportsTranscoding != tc.wantTranscode {
				t.Errorf("SupportsTranscoding = %t, want %t", resolved.SupportsTranscoding, tc.wantTranscode)
			}
		})
	}
}

// Source: MediaInfoHelper.cs:278-293. Video playback may transcode when any
// applicable audio/video/remux permission is granted; it does not require all.
func TestResolvePlaybackInfo_VideoTranscodingAcceptsAnyApplicablePermission(t *testing.T) {
	profile := &playback.DeviceProfile{TranscodingProfiles: []playback.TranscodingProfile{{
		Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
	}}}
	source := playbackInfoVideoSource("permission-source", false)
	source.Container = "webm"

	for _, tc := range []struct {
		name        string
		permissions playback.PlaybackPermissions
	}{
		{name: "audio transcode", permissions: playback.PlaybackPermissions{EnableAudioTranscoding: true}},
		{name: "video transcode", permissions: playback.PlaybackPermissions{EnableVideoTranscoding: true}},
		{name: "remux", permissions: playback.PlaybackPermissions{EnableRemuxing: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playback.ResolvePlaybackInfo(playbackInfoRequest(
				playback.WithDeviceProfile(profile),
				playback.WithPermissions(tc.permissions),
			), []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want one permitted transcode source", len(got.MediaSources))
			}
			if !got.MediaSources[0].SupportsTranscoding {
				t.Error("SupportsTranscoding = false, want true when one applicable permission is granted")
			}
		})
	}
}

// Source: upstream/jellyfin/Jellyfin.Api/Helpers/MediaInfoHelper.cs:504-525.
// The server remote limit is a WAN policy ceiling, not an override for the
// device's or request's client capability. LAN deliberately leaves the server
// ceiling unused.
func TestResolvePlaybackInfo_ServerRemoteLimitRespectsClientLimitsAndLAN(t *testing.T) {
	serverRemoteLimit := playbackInfoInt(6_000_000)
	deviceLimit := playbackInfoInt(2_000_000)
	requestLimit := playbackInfoInt(1_000_000)
	source := playbackInfoVideoSource("server-limit-source", false)
	source.Container = "webm"

	profile := &playback.DeviceProfile{MaxStreamingBitrate: deviceLimit, TranscodingProfiles: []playback.TranscodingProfile{{
		Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
	}}}
	base := playbackInfoRequest(
		playback.WithDeviceProfile(profile),
		playback.WithPermissions(playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true, RemoteBitrateLimit: serverRemoteLimit}),
	)

	for _, tc := range []struct {
		name    string
		request playback.PlaybackInfoRequest
		want    string
	}{
		{name: "LAN ignores server remote ceiling", request: base.WithNetwork(playback.PlaybackNetwork{IsLAN: true}), want: "VideoBitrate=1872000"},
		{name: "public WAN caps to device", request: base.WithNetwork(playback.PlaybackNetwork{IsLAN: false}), want: "VideoBitrate=1872000"},
		{name: "public WAN honors lower request", request: playbackInfoRequest(playback.WithDeviceProfile(profile), playback.WithMaxStreamingBitrate(requestLimit), playback.WithPermissions(playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true, RemoteBitrateLimit: serverRemoteLimit}), playback.WithNetwork(playback.PlaybackNetwork{IsLAN: false})), want: "VideoBitrate=872000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := playback.ResolvePlaybackInfo(tc.request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo: %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want one transcode candidate", len(got.MediaSources))
			}
			if url := got.MediaSources[0].TranscodingURL; !strings.Contains(url, tc.want) {
				t.Fatalf("TranscodingURL = %q, want client-constrained %q", url, tc.want)
			}
		})
	}
}

// Source: MediaInfoHelper.cs:504-525. The remote-policy clamp describes the
// requesting client network, not whether an individual source is remote.
func TestResolvePlaybackInfo_ClampsWANFileSourceByClientNetwork(t *testing.T) {
	profile := &playback.DeviceProfile{TranscodingProfiles: []playback.TranscodingProfile{{
		Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
	}}}
	source := playbackInfoVideoSource("local-file", false)
	source.Container = "webm"
	request := playbackInfoRequest(
		playback.WithDeviceProfile(profile),
		playback.WithMaxStreamingBitrate(playbackInfoInt(10_000_000)),
		playback.WithPermissions(playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true, RemoteBitrateLimit: playbackInfoInt(3_000_000)}),
		playback.WithNetwork(playback.PlaybackNetwork{IsLAN: false}),
	)

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("MediaSources = %d, want one source", len(got.MediaSources))
	}
	if !strings.Contains(got.MediaSources[0].TranscodingURL, "VideoBitrate=2872000") {
		t.Errorf("WAN local-file URL = %q, want remote-policy video budget 2872000", got.MediaSources[0].TranscodingURL)
	}
}

// Sources: MediaInfoHelper.cs:295-305,504-525; StreamInfo.cs ToUrl (ported by
// streaminfo.go:753-893). Remote limits apply only outside LAN, and force-remote
// playback must mint a URL with the shared PlaySessionId followed by copy flags.
func TestResolvePlaybackInfo_ClampsWANAndBuildsForcedRemoteURL(t *testing.T) {
	profile := playback.DeviceProfile{
		ID: "profile",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	remote := playbackInfoVideoSource("remote", true)
	remote.Bitrate = playbackInfoInt(5_000_000)
	base := playbackInfoRequest(
		playback.WithDeviceProfile(&profile),
		playback.WithSession(playback.PlaybackSession{ID: "shared-session", DeviceID: "device-1", AccessToken: "api-token"}),
		playback.WithMaxStreamingBitrate(playbackInfoInt(10_000_000)),
		playback.WithPermissions(playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true, ForceRemoteSourceTranscoding: true, RemoteBitrateLimit: playbackInfoInt(3_000_000)}),
	)
	base.AlwaysBurnInSubtitleWhenTranscoding = true

	lan, err := playback.ResolvePlaybackInfo(base.WithNetwork(playback.PlaybackNetwork{IsLAN: true}), []playback.MediaSourceInfo{remote})
	if err != nil {
		t.Fatalf("LAN ResolvePlaybackInfo() error = %v", err)
	}
	wan, err := playback.ResolvePlaybackInfo(base.WithNetwork(playback.PlaybackNetwork{IsLAN: false}), []playback.MediaSourceInfo{remote})
	if err != nil {
		t.Fatalf("WAN ResolvePlaybackInfo() error = %v", err)
	}
	if len(lan.MediaSources) != 1 || len(wan.MediaSources) != 1 {
		t.Fatalf("media source counts = LAN:%d WAN:%d, want 1", len(lan.MediaSources), len(wan.MediaSources))
	}
	if !strings.Contains(lan.MediaSources[0].TranscodingURL, "VideoBitrate=9872000") {
		t.Errorf("LAN URL = %q, want StreamBuilder video budget 9872000", lan.MediaSources[0].TranscodingURL)
	}
	if !strings.Contains(wan.MediaSources[0].TranscodingURL, "VideoBitrate=2872000") {
		t.Errorf("WAN URL = %q, want StreamBuilder video budget 2872000 after remote clamp", wan.MediaSources[0].TranscodingURL)
	}
	wantURLTail := "VideoBitrate=2872000&AudioBitrate=128000&PlaySessionId=shared-session&ApiKey=api-token"
	if !strings.Contains(wan.MediaSources[0].TranscodingURL, wantURLTail) {
		t.Errorf("forced-remote URL = %q, want ordered ToURL fields %q", wan.MediaSources[0].TranscodingURL, wantURLTail)
	}
	if !strings.HasSuffix(wan.MediaSources[0].TranscodingURL, "allowVideoStreamCopy=false&allowAudioStreamCopy=false&alwaysBurnInSubtitleWhenTranscoding=true") {
		t.Errorf("forced-remote URL = %q, want ordered copy and burn flags", wan.MediaSources[0].TranscodingURL)
	}
	if wan.MediaSources[0].SupportsDirectPlay || wan.MediaSources[0].SupportsDirectStream {
		t.Errorf("forced remote direct support = play:%t stream:%t, want both false", wan.MediaSources[0].SupportsDirectPlay, wan.MediaSources[0].SupportsDirectStream)
	}
}

// Sources: MediaInfoHelper.cs:334-345,479-501; StreamInfo.cs:1176-1281.
// Delivery fields and selected default stream indices are client-visible
// negotiation outputs. The API subtitle URL is required when the original
// sidecar cannot be handed to the client directly.
func TestResolvePlaybackInfo_AssignsSelectedDefaultsAndSubtitleDelivery(t *testing.T) {
	first := playbackInfoVideoSource("first", false)
	first.MediaAttachments = []playback.MediaAttachment{{Index: 4, FileName: "font.ttf"}}
	first.MediaStreams = append(first.MediaStreams,
		playback.MediaStream{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac"},
		playback.MediaStream{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: "https://cdn.example.invalid/feature.srt"},
	)
	second := playbackInfoVideoSource("second", false)
	second.Protocol = playback.MediaProtocolHTTP
	third := playbackInfoVideoSource("third", false)
	startTicks := int64(444)

	request := playbackInfoRequest(
		playback.WithMediaSourceID("first"),
		playback.WithSubtitleStreamIndex(playbackInfoInt(2)),
		playback.WithDeviceProfile(&playback.DeviceProfile{
			ID: "subtitle-profile",
			DirectPlayProfiles: []playback.DirectPlayProfile{{
				Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
			}},
			// The sidecar is deliberately not SupportsExternalStream, so the
			// response must retain the API URL selected by this profile.
			SubtitleProfiles: []playback.SubtitleProfile{{Format: "srt", Method: playback.SubtitleExternal}},
		}),
	)
	request.AudioStreamIndex = playbackInfoInt(3)
	request.StartTimeTicks = &startTicks
	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{second, first, third})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("selected delivery sources = %d, want 1", len(got.MediaSources))
	}
	resolved := got.MediaSources[0]
	if resolved.ID != "first" {
		t.Errorf("selected source = %q, want first", resolved.ID)
	}
	if resolved.DefaultAudioStreamIndex == nil || *resolved.DefaultAudioStreamIndex != 3 {
		t.Errorf("DefaultAudioStreamIndex = %#v, want 3", resolved.DefaultAudioStreamIndex)
	}
	if resolved.DefaultSubtitleStreamIndex == nil || *resolved.DefaultSubtitleStreamIndex != 2 {
		t.Errorf("DefaultSubtitleStreamIndex = %#v, want 2", resolved.DefaultSubtitleStreamIndex)
	}
	if resolved.MediaAttachments[0].DeliveryURL != "/Videos/123e4567-e89b-12d3-a456-426614174000/first/Attachments/4" {
		t.Errorf("attachment URL = %q", resolved.MediaAttachments[0].DeliveryURL)
	}
	subtitle := resolved.MediaStreams[3]
	if subtitle.DeliveryMethod == nil || *subtitle.DeliveryMethod != playback.SubtitleExternal {
		t.Errorf("subtitle delivery = %#v, want External", subtitle.DeliveryMethod)
	}
	// Direct playback must use zero ticks even when the item request carries a
	// resume position. The profile's output format owns the route extension;
	// an API delivery URL carries the access token.
	wantURL := "/Videos/123e4567-e89b-12d3-a456-426614174000/first/Subtitles/2/0/Stream.srt?ApiKey=token-default"
	if subtitle.DeliveryURL != wantURL {
		t.Errorf("external subtitle URL = %q, want %q", subtitle.DeliveryURL, wantURL)
	}
	if subtitle.IsExternalURL == nil || *subtitle.IsExternalURL {
		t.Errorf("subtitle IsExternalURL = %#v, want false for API delivery", subtitle.IsExternalURL)
	}
}

// Source: StreamInfo.cs:1263-1281. A public HTTP(S) sidecar may bypass the
// server only when every upstream safety condition is true. Its URL must not
// accidentally receive the API key and IsExternalUrl must make that distinction
// observable to the client.
func TestResolvePlaybackInfo_UsesDirectExternalSubtitleOnlyWhenProfileAllowsIt(t *testing.T) {
	source := playbackInfoVideoSource("remote-subtitle", false)
	source.MediaStreams = append(source.MediaStreams, playback.MediaStream{
		Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true,
		SupportsExternalStream: true, Path: "https://cdn.example.invalid/feature.srt",
	})
	request := playbackInfoRequest(
		playback.WithMediaSourceID("remote-subtitle"),
		playback.WithSubtitleStreamIndex(playbackInfoInt(2)),
		playback.WithDeviceProfile(&playback.DeviceProfile{
			DirectPlayProfiles: []playback.DirectPlayProfile{{Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}},
			SubtitleProfiles:   []playback.SubtitleProfile{{Format: "srt", Method: playback.SubtitleExternal}},
		}),
	)

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("MediaSources = %d, want one", len(got.MediaSources))
	}
	subtitle := got.MediaSources[0].MediaStreams[2]
	if subtitle.DeliveryURL != "https://cdn.example.invalid/feature.srt" {
		t.Errorf("direct external subtitle URL = %q, want original public URL", subtitle.DeliveryURL)
	}
	if subtitle.IsExternalURL == nil || !*subtitle.IsExternalURL {
		t.Errorf("direct external subtitle IsExternalURL = %#v, want true", subtitle.IsExternalURL)
	}
}

// Source: MediaInfoHelper.cs:193-211. Audio/subtitle selection belongs to the
// requested media source only. A request without MediaSourceId must negotiate
// each alternate source against that source's own default tracks.
func TestResolvePlaybackInfo_DoesNotApplyRequestedIndicesWithoutMediaSourceID(t *testing.T) {
	first := playbackInfoVideoSource("first", false)
	first.DefaultSubtitleStreamIndex = playbackInfoInt(2)
	first.MediaStreams = append(first.MediaStreams,
		playback.MediaStream{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac"},
		playback.MediaStream{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt"},
	)
	second := playbackInfoVideoSource("second", false)
	second.DefaultSubtitleStreamIndex = playbackInfoInt(7)
	second.MediaStreams = append(second.MediaStreams,
		playback.MediaStream{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac"},
		playback.MediaStream{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		playback.MediaStream{Index: 7, Type: playback.StreamTypeSubtitle, Codec: "srt"},
	)
	request := playbackInfoRequest(playback.WithDeviceProfile(&playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}},
		SubtitleProfiles:   []playback.SubtitleProfile{{Format: "srt", Method: playback.SubtitleExternal}},
	}))
	request.AudioStreamIndex = playbackInfoInt(3)
	request.SubtitleStreamIndex = playbackInfoInt(2)

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{first, second})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 2 {
		t.Fatalf("MediaSources = %d, want two alternatives", len(got.MediaSources))
	}
	for _, want := range []struct {
		id              string
		audio, subtitle int
	}{
		{"first", 1, 2},
		{"second", 1, 7},
	} {
		var source *playback.MediaSourceInfo
		for index := range got.MediaSources {
			if got.MediaSources[index].ID == want.id {
				source = &got.MediaSources[index]
				break
			}
		}
		if source == nil {
			t.Errorf("response omitted source %q", want.id)
			continue
		}
		if source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != want.audio || source.DefaultSubtitleStreamIndex == nil || *source.DefaultSubtitleStreamIndex != want.subtitle {
			t.Errorf("source %q defaults audio=%#v subtitle=%#v, want %d/%d", want.id, source.DefaultAudioStreamIndex, source.DefaultSubtitleStreamIndex, want.audio, want.subtitle)
		}
	}
}

// Source: MediaInfoHelper.cs:354-405. The queried item's own source preserves
// its user resume state, so it remains the first response candidate even when
// a sibling has a better method/protocol/bitrate rank.
func TestResolvePlaybackInfo_SortsRequestedItemSourceBeforeAllNegotiationRanks(t *testing.T) {
	request := playbackInfoRequest(playback.WithDeviceProfile(&playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}},
	}))
	requested := playbackInfoVideoSource(request.ItemID.Dashed(), false)
	requested.Protocol = playback.MediaProtocolHTTP
	sibling := playbackInfoVideoSource("file-direct-sibling", false)

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{sibling, requested})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 2 {
		t.Fatalf("MediaSources = %d, want two", len(got.MediaSources))
	}
	if got.MediaSources[0].ID != request.ItemID.Dashed() {
		t.Errorf("first source = %q, want requested item source %q before file-direct sibling", got.MediaSources[0].ID, request.ItemID.Dashed())
	}
}

// Source: MediaInfoHelper.cs:355-405. This locks every rank in the stable
// source chain: direct-play file, playable source, file, bitrate bucket, then
// original order for candidates tied on every preceding key.
func TestResolvePlaybackInfo_SortsMediaSourcesByFullStableUpstreamChain(t *testing.T) {
	profile := &playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	fileTranscode := playbackInfoVideoSource("file-transcode", false)
	fileTranscode.Container = "webm"
	fileTranscode.SupportsDirectPlay = false
	fileTranscode.SupportsDirectStream = false
	fileTranscode.MediaStreams[0].Codec = "hevc"

	remoteDirect := playbackInfoVideoSource("remote-direct", false)
	remoteDirect.Protocol = playback.MediaProtocolHTTP
	remoteDirect.Bitrate = playbackInfoInt(1_000_000)

	fileDirect := playbackInfoVideoSource("file-direct", false)
	fileDirect.Bitrate = playbackInfoInt(1_000_000)

	remoteLow := playbackInfoVideoSource("remote-low", false)
	remoteLow.Protocol = playback.MediaProtocolHTTP
	remoteLow.Container = "webm"
	remoteLow.SupportsDirectPlay = false
	remoteLow.SupportsDirectStream = false
	remoteLow.Bitrate = playbackInfoInt(1_000_000)

	remoteUnknown := remoteLow
	remoteUnknown.ID = "remote-unknown"
	remoteUnknown.Bitrate = nil

	remoteHigh := remoteLow
	remoteHigh.ID = "remote-high"
	remoteHigh.Bitrate = playbackInfoInt(6_000_000)

	got, err := playback.ResolvePlaybackInfo(playbackInfoRequest(
		playback.WithDeviceProfile(profile),
		playback.WithMaxStreamingBitrate(playbackInfoInt(3_000_000)),
	), []playback.MediaSourceInfo{fileTranscode, remoteDirect, fileDirect, remoteLow, remoteUnknown, remoteHigh})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	gotIDs := make([]string, len(got.MediaSources))
	for index := range got.MediaSources {
		gotIDs[index] = got.MediaSources[index].ID
	}
	wantIDs := []string{"file-direct", "remote-direct", "file-transcode", "remote-low", "remote-unknown", "remote-high"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("sorted source IDs = %v, want %v", gotIDs, wantIDs)
	}
}

// Source: upstream/jellyfin/MediaBrowser.Model/Dlna/StreamBuilder.cs
// GetOptimalAudioStream/buildAudioItem. Audio-only sources negotiate against
// audio profiles; they must never be evaluated by the video builder.
func TestResolvePlaybackInfo_AudioOnlySourcesUseAudioProfilesForDirectTranscodeAndEmptyProfiles(t *testing.T) {
	for _, tc := range []struct {
		name               string
		source             playback.MediaSourceInfo
		profile            *playback.DeviceProfile
		wantDirectPlay     bool
		wantTranscoding    bool
		wantTranscodingURL bool
	}{
		{
			name:   "source-compatible audio direct play",
			source: playbackInfoAudioSource("audio-direct", "mp3", "mp3"),
			profile: &playback.DeviceProfile{ID: "audio-direct", DirectPlayProfiles: []playback.DirectPlayProfile{{
				Type: playback.DlnaAudio, Container: "mp3", AudioCodec: "mp3",
			}}, TranscodingProfiles: []playback.TranscodingProfile{{
				Type: playback.DlnaAudio, Context: playback.ContextStreaming, Container: "mp3", AudioCodec: "mp3", Protocol: playback.StreamProtocolHTTP,
			}}},
			wantDirectPlay:  true,
			wantTranscoding: true,
		},
		{
			name:   "audio transcode",
			source: playbackInfoAudioSource("audio-transcode", "flac", "flac"),
			profile: &playback.DeviceProfile{ID: "audio-transcode", TranscodingProfiles: []playback.TranscodingProfile{{
				Type: playback.DlnaAudio, Context: playback.ContextStreaming, Container: "mp3", AudioCodec: "mp3", Protocol: playback.StreamProtocolHTTP,
			}}},
			wantTranscoding:    true,
			wantTranscodingURL: true,
		},
		{
			name:   "empty transcoding profiles still direct play compatible audio",
			source: playbackInfoAudioSource("audio-empty-profile", "m4a", "aac"),
			profile: &playback.DeviceProfile{ID: "audio-empty-profile", DirectPlayProfiles: []playback.DirectPlayProfile{{
				Type: playback.DlnaAudio, Container: "m4a", AudioCodec: "aac",
			}}, TranscodingProfiles: []playback.TranscodingProfile{}},
			wantDirectPlay: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := playbackInfoRequest(playback.WithDeviceProfile(tc.profile))
			request.MediaType = playback.DlnaAudio

			got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{tc.source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %#v, want one audio source", got.MediaSources)
			}
			resolved := got.MediaSources[0]
			if resolved.SupportsDirectPlay != tc.wantDirectPlay {
				t.Errorf("SupportsDirectPlay = %t, want %t", resolved.SupportsDirectPlay, tc.wantDirectPlay)
			}
			if resolved.SupportsTranscoding != tc.wantTranscoding {
				t.Errorf("SupportsTranscoding = %t, want %t", resolved.SupportsTranscoding, tc.wantTranscoding)
			}
			if tc.wantTranscodingURL {
				if !strings.HasPrefix(resolved.TranscodingURL, "/audio/"+request.ItemID.Dashed()+"/stream.mp3?") {
					t.Errorf("audio TranscodingURL = %q, want audio mp3 stream URL", resolved.TranscodingURL)
				}
			} else if resolved.TranscodingURL != "" {
				t.Errorf("direct audio TranscodingURL = %q, want empty", resolved.TranscodingURL)
			}
		})
	}
}

// Source: upstream/jellyfin/Jellyfin.Api/Helpers/MediaInfoHelper.cs:249-276;
// upstream/jellyfin/MediaBrowser.Model/Dlna/StreamInfo.cs ToUrl. The request
// seek must become StreamInfo.StartPositionTicks before ToUrl writes its fixed
// StartTimeTicks then PlaySessionId parameter sequence.
func TestResolvePlaybackInfo_PropagatesStartTimeTicksToTranscodingURLInUpstreamOrder(t *testing.T) {
	startTicks := int64(987654321)
	profile := &playback.DeviceProfile{ID: "seek-profile", TranscodingProfiles: []playback.TranscodingProfile{{
		Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP,
	}}}
	request := playbackInfoRequest(
		playback.WithDeviceProfile(profile),
		playback.WithSession(playback.PlaybackSession{ID: "seek-session", DeviceID: "seek-device", AccessToken: "seek-token"}),
	)
	request.StartTimeTicks = &startTicks
	source := playbackInfoVideoSource("seek-source", false)
	source.Container = "webm"

	got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
	if err != nil {
		t.Fatalf("ResolvePlaybackInfo() error = %v", err)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("MediaSources = %#v, want one transcoded video source", got.MediaSources)
	}
	url := got.MediaSources[0].TranscodingURL
	wantOrderedSeek := "StartTimeTicks=987654321&PlaySessionId=seek-session&ApiKey=seek-token"
	if !strings.Contains(url, wantOrderedSeek) {
		t.Errorf("TranscodingURL = %q, want ordered seek parameters %q", url, wantOrderedSeek)
	}
}

// Source: MediaSourceManager.cs:567-575 sets AudioIndexSource.Default from the
// preference bit alone, and StreamBuilder.cs:686-704 then empties the candidate
// set when no stream is default. Deadfin sets default provenance only for an
// actual IsDefault stream, so the profile scans the remaining candidates.
func TestResolvePlaybackInfo_NoDefaultAudioRejectsUnsupportedDirectPlay(t *testing.T) {
	profile := &playback.DeviceProfile{
		ID: "apple-tv-shaped",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mkv", VideoCodec: "h264", AudioCodec: "aac,ac3",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP,
		}},
	}
	request := playbackInfoRequest(playback.WithDeviceProfile(profile))

	for _, tc := range []struct {
		name               string
		streams            []playback.MediaStream
		playDefaultTrack   bool
		wantDirectPlay     bool
		wantTranscodingURL bool
		wantAudioIndex     int
	}{
		{
			name: "unsupported non-default audio rejects direct play and offers transcoding",
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "dts", IsDefault: false},
			},
			playDefaultTrack:   true,
			wantDirectPlay:     false,
			wantTranscodingURL: true,
			wantAudioIndex:     1,
		},
		{
			name: "supported non-default audio keeps direct play",
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: false},
			},
			playDefaultTrack:   true,
			wantDirectPlay:     true,
			wantTranscodingURL: false,
			wantAudioIndex:     1,
		},
		{
			name: "actual default audio keeps default-only behavior",
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "dts", IsDefault: true},
				{Index: 2, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: false},
			},
			playDefaultTrack:   true,
			wantDirectPlay:     false,
			wantTranscodingURL: true,
			wantAudioIndex:     1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := playback.MediaSourceInfo{
				ID:                   "no-default-audio",
				Protocol:             playback.MediaProtocolFile,
				Container:            "mkv",
				SupportsDirectPlay:   true,
				SupportsDirectStream: true,
				SupportsTranscoding:  true,
				MediaStreams:         tc.streams,
			}
			selection := playback.SourceSelection{
				MediaKind: playback.SourceMediaVideo,
				Configuration: dto.UserConfiguration{
					PlayDefaultAudioTrack: tc.playDefaultTrack,
					SubtitleMode:          dto.SubtitleModeNone,
				},
			}
			sources := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{source}, selection, nil)
			got, err := playback.ResolvePlaybackInfo(request, sources)
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if resolved.SupportsDirectPlay != tc.wantDirectPlay {
				t.Errorf("SupportsDirectPlay = %t, want %t", resolved.SupportsDirectPlay, tc.wantDirectPlay)
			}
			if tc.wantTranscodingURL && resolved.TranscodingURL == "" {
				t.Errorf("TranscodingURL = %q, want a non-empty transcoding URL", resolved.TranscodingURL)
			}
			if !tc.wantTranscodingURL && resolved.TranscodingURL != "" {
				t.Errorf("TranscodingURL = %q, want empty", resolved.TranscodingURL)
			}
			if resolved.DefaultAudioStreamIndex == nil || *resolved.DefaultAudioStreamIndex != tc.wantAudioIndex {
				t.Errorf("DefaultAudioStreamIndex = %v, want %d", resolved.DefaultAudioStreamIndex, tc.wantAudioIndex)
			}
		})
	}
}

// A multi-track source with no actual default applies language filtering first,
// then selects the first profile-supported stream in source order. An explicit
// audio index remains locked even when another stream is profile-compatible.
func TestResolvePlaybackInfo_MultiTrackNoDefaultSelectsFirstSupportedInSourceOrder(t *testing.T) {
	profile := &playback.DeviceProfile{
		ID: "multi-track-profile",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mkv", VideoCodec: "h264", AudioCodec: "aac,ac3",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP,
		}},
	}
	baseRequest := playbackInfoRequest(playback.WithDeviceProfile(profile))
	english := "eng"

	t.Run("multi-track no default filters by language then selects first supported", func(t *testing.T) {
		source := playback.MediaSourceInfo{
			ID:                   "multi-no-default",
			Protocol:             playback.MediaProtocolFile,
			Container:            "mkv",
			SupportsDirectPlay:   true,
			SupportsDirectStream: true,
			SupportsTranscoding:  true,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoString("fra"), IsDefault: false},
				{Index: 2, Type: playback.StreamTypeAudio, Codec: "dts", Language: playbackInfoString("eng"), IsDefault: false},
				{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoString("eng"), IsDefault: false},
				{Index: 4, Type: playback.StreamTypeAudio, Codec: "ac3", Language: playbackInfoString("eng"), IsDefault: false},
			},
		}
		selection := playback.SourceSelection{
			MediaKind: playback.SourceMediaVideo,
			Configuration: dto.UserConfiguration{
				AudioLanguagePreference: &english,
				PlayDefaultAudioTrack:   true,
				SubtitleMode:            dto.SubtitleModeNone,
			},
		}
		sources := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{source}, selection, nil)
		got, err := playback.ResolvePlaybackInfo(baseRequest, sources)
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
		}
		resolved := got.MediaSources[0]
		if !resolved.SupportsDirectPlay {
			t.Errorf("SupportsDirectPlay = false, want true (aac is supported)")
		}
		if resolved.DefaultAudioStreamIndex == nil || *resolved.DefaultAudioStreamIndex != 3 {
			t.Errorf("DefaultAudioStreamIndex = %v, want 3 (first supported preferred-language stream)", resolved.DefaultAudioStreamIndex)
		}
	})

	t.Run("explicit audio index stays locked when another stream is profile-compatible", func(t *testing.T) {
		source := playback.MediaSourceInfo{
			ID:                   "explicit-index",
			Protocol:             playback.MediaProtocolFile,
			Container:            "mkv",
			SupportsDirectPlay:   true,
			SupportsDirectStream: true,
			SupportsTranscoding:  true,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "dts", IsDefault: false},
				{Index: 2, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: false},
			},
		}
		request := baseRequest
		request.MediaSourceID = source.ID
		audioIndex := 1
		request.AudioStreamIndex = &audioIndex
		selection := playback.SourceSelection{
			MediaKind: playback.SourceMediaVideo,
			Configuration: dto.UserConfiguration{
				PlayDefaultAudioTrack: true,
				SubtitleMode:          dto.SubtitleModeNone,
			},
		}
		sources := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{source}, selection, nil)
		got, err := playback.ResolvePlaybackInfo(request, sources)
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
		}
		resolved := got.MediaSources[0]
		if resolved.SupportsDirectPlay {
			t.Errorf("SupportsDirectPlay = true, want false (explicit dts index is unsupported)")
		}
		if !resolved.SupportsTranscoding {
			t.Errorf("SupportsTranscoding = false, want true")
		}
		if resolved.DefaultAudioStreamIndex == nil || *resolved.DefaultAudioStreamIndex != 1 {
			t.Errorf("DefaultAudioStreamIndex = %v, want 1 (explicit index stays locked)", resolved.DefaultAudioStreamIndex)
		}
	})
}

func playbackInfoAudioSource(id, container, codec string) playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID:                   id,
		Protocol:             playback.MediaProtocolFile,
		Container:            container,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams: []playback.MediaStream{{
			Index: 0, Type: playback.StreamTypeAudio, Codec: codec, IsDefault: true,
		}},
	}
}

func playbackInfoRequest(options ...playback.PlaybackInfoOption) playback.PlaybackInfoRequest {
	request := playback.NewPlaybackInfoRequest()
	request.ItemID = playbackInfoItemID()
	request.MediaType = playback.DlnaVideo
	request.Session = playback.PlaybackSession{ID: "session-default", DeviceID: "device-default", AccessToken: "token-default"}
	request.Permissions = playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true}
	request.Network = playback.PlaybackNetwork{IsLAN: true}
	request.DeviceProfile = &playback.DeviceProfile{ID: "default-profile"}
	for _, option := range options {
		option(&request)
	}
	return request
}

func playbackInfoVideoSource(id string, remote bool) playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID:                   id,
		Protocol:             playback.MediaProtocolFile,
		Container:            "mp4",
		IsRemote:             remote,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
		},
	}
}

func playbackInfoInt(value int) *int {
	return &value
}

func playbackInfoString(value string) *string { return &value }

func playbackInfoItemID() dto.UUID {
	return dto.UUID{0x12, 0x3e, 0x45, 0x67, 0xe8, 0x9b, 0x12, 0xd3, 0xa4, 0x56, 0x42, 0x66, 0x14, 0x17, 0x40, 0x00}
}
