// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// The application layer, rather than the pure resolver, owns precedence between
// the request's typed profile and the session/device fallback profile.
func TestPlaybackInfoApplication_RequestProfilePrecedesStoredProfileAndStoredProfileFallsBack(t *testing.T) {
	itemID := playbackInfoItemID()
	sources := playbackServiceSources(itemID)
	storedProfile := playbackServiceDirectProfile("stored-mp4", "mp4")
	explicitProfile := playbackServiceDirectProfile("request-webm", "webm")
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:        playbackServiceSourceReader{records: sources[:1]},
		DeviceProfiles: &playbackServiceProfileReader{profile: storedProfile},
		Permissions:    playbackServicePermissionReader{permissions: playbackServicePermissions()},
		Network:        playbackServiceNetwork{isLAN: true},
		NewPlaySessionID: func() string {
			return "minted-profile-session"
		},
	})

	request := playbackServiceRequest(itemID)
	request.DeviceProfile = explicitProfile
	got, err := service.PlaybackInfo(t.Context(), request)
	if err != nil {
		t.Fatalf("PlaybackInfo with explicit profile: %v", err)
	}
	if got.ErrorCode != playback.PlaybackInfoNoCompatibleStream || len(got.MediaSources) != 0 {
		t.Fatalf("explicit incompatible profile result = %#v, want NoCompatibleStream with no sources", got)
	}

	request.DeviceProfile = nil
	got, err = service.PlaybackInfo(t.Context(), request)
	if err != nil {
		t.Fatalf("PlaybackInfo with stored fallback profile: %v", err)
	}
	if got.ErrorCode != "" || len(got.MediaSources) != 1 || !got.MediaSources[0].SupportsDirectPlay {
		t.Fatalf("stored fallback result = %#v, want one direct-play source", got)
	}
}

// Playback capability reports are scoped to the authenticated client/device
// pair. A second client on the same physical device must neither influence
// negotiation nor supply a fallback profile.
func TestPlaybackInfoApplication_SelectsStoredProfileByExactClientAndDevice(t *testing.T) {
	itemID := playbackInfoItemID()
	browserBitrate, televisionBitrate := 800_000, 4_000_000
	profiles := &playbackServiceProfileReader{profiles: map[playbackServiceProfileKey]*playback.DeviceProfile{
		{client: "Browser", deviceID: "shared-device"}: {
			ID:                  "browser-h264",
			MaxStreamingBitrate: &browserBitrate,
			TranscodingProfiles: []playback.TranscodingProfile{{Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP}},
		},
		{client: "Television", deviceID: "shared-device"}: {
			ID:                  "television-vp9",
			MaxStreamingBitrate: &televisionBitrate,
			TranscodingProfiles: []playback.TranscodingProfile{{Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "vp9", AudioCodec: "opus", Protocol: playback.StreamProtocolHTTP}},
		},
	}}
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:        playbackServiceSourceReader{records: playbackServiceSources(itemID)[:1]},
		DeviceProfiles: profiles,
		Permissions:    playbackServicePermissionReader{permissions: playbackServicePermissions()},
		Network:        playbackServiceNetwork{isLAN: true},
		NewPlaySessionID: func() string {
			return "exact-client-profile-session"
		},
	})

	for _, tc := range []struct {
		name          string
		client        string
		wantFragments []string
	}{
		{name: "browser selects its h264 low-bitrate profile", client: "Browser", wantFragments: []string{"DeviceProfileId=browserh264", "VideoCodec=h264", "VideoBitrate=672000"}},
		{name: "television selects its vp9 high-bitrate profile", client: "Television", wantFragments: []string{"DeviceProfileId=televisionvp9", "VideoCodec=vp9", "VideoBitrate=3808000"}},
		{name: "missing client device pair does not reuse another client profile", client: "Mobile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := playbackServiceRequest(itemID)
			request.Client = tc.client
			request.DeviceProfile = nil
			request.DeviceID = "shared-device"
			request.Session.DeviceID = request.DeviceID
			got, err := service.PlaybackInfo(t.Context(), request)
			if err != nil {
				t.Fatalf("PlaybackInfo: %v", err)
			}
			if profiles.lastClient != tc.client || profiles.lastDeviceID != request.DeviceID {
				t.Fatalf("stored profile lookup = client %q device %q, want exact client %q device %q", profiles.lastClient, profiles.lastDeviceID, tc.client, request.DeviceID)
			}
			if len(tc.wantFragments) != 0 {
				if got.ErrorCode != "" || len(got.MediaSources) != 1 {
					t.Fatalf("%s profile response = %#v, want one negotiated source", tc.client, got)
				}
				for _, fragment := range tc.wantFragments {
					if !strings.Contains(got.MediaSources[0].TranscodingURL, fragment) {
						t.Errorf("%s TranscodingURL = %q, want %q", tc.client, got.MediaSources[0].TranscodingURL, fragment)
					}
				}
				return
			}
			if got.ErrorCode != "" || len(got.MediaSources) != 1 || got.MediaSources[0].TranscodingURL != "" {
				t.Fatalf("missing exact-pair response = %#v, want raw source assembly without another client's profile", got)
			}
		})
	}
}

// A response has one play-session identity. The service mints it once and the
// resolver must carry it into every independently generated source URL.
func TestPlaybackInfoApplication_MintsOnePlaySessionIDSharedByAllURLs(t *testing.T) {
	itemID := playbackInfoItemID()
	mintCalls := 0
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:        playbackServiceSourceReader{records: playbackServiceSources(itemID)},
		DeviceProfiles: &playbackServiceProfileReader{profile: playbackServiceTranscodeProfile()},
		Permissions:    playbackServicePermissionReader{permissions: playbackServicePermissions()},
		Network:        playbackServiceNetwork{isLAN: true},
		NewPlaySessionID: func() string {
			mintCalls++
			return "minted-shared-session"
		},
	})

	request := playbackServiceRequest(itemID)
	request.DeviceProfile = nil
	request.Session.ID = "caller-supplied-session"
	got, err := service.PlaybackInfo(t.Context(), request)
	if err != nil {
		t.Fatalf("PlaybackInfo: %v", err)
	}
	if mintCalls != 1 {
		t.Fatalf("play-session IDs minted = %d, want one", mintCalls)
	}
	if got.PlaySessionID != "minted-shared-session" {
		t.Fatalf("PlaySessionID = %q, want service-minted identity", got.PlaySessionID)
	}
	if len(got.MediaSources) != 2 {
		t.Fatalf("MediaSources = %d, want two independently resolved sources", len(got.MediaSources))
	}
	for _, source := range got.MediaSources {
		if !strings.Contains(source.TranscodingURL, "PlaySessionId=minted-shared-session") {
			t.Errorf("source %q TranscodingURL = %q, want shared minted PlaySessionId", source.ID, source.TranscodingURL)
		}
		if strings.Contains(source.TranscodingURL, "caller-supplied-session") {
			t.Errorf("source %q TranscodingURL retained caller session identity: %q", source.ID, source.TranscodingURL)
		}
	}
}

// A resumable alternate must still lead an unscoped response after resolver
// ranking, but replaying stream selection after negotiation must not replace a
// client's explicit source-track request with remembered user tracks.
func TestPlaybackInfoApplication_OrdersResumableAlternateWithoutOverwritingExplicitNegotiatedStreams(t *testing.T) {
	itemID := playbackInfoItemID()
	alternateID := itemID
	alternateID[15]++
	container := "mp4"
	streams := []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackServiceString("eng"), IsDefault: true},
		{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackServiceString("fra")},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: playbackServiceString("eng"), IsDefault: true},
		{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: playbackServiceString("fra")},
	}
	storedAudio, storedSubtitle := 1, 2
	selection := playback.SourceSelection{
		Configuration: dto.UserConfiguration{
			AudioLanguagePreference:    playbackServiceString("eng"),
			SubtitleLanguagePreference: playbackServiceString("eng"),
			SubtitleMode:               dto.SubtitleModeAlways,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
		},
		MediaKind:   playback.SourceMediaVideo,
		PrimaryItem: true,
		VersionUserData: map[string]playback.SourceUserData{
			itemID.String():      {AudioStreamIndex: &storedAudio, SubtitleStreamIndex: &storedSubtitle},
			alternateID.String(): {PositionTicks: 1},
		},
	}
	profile := &playback.DeviceProfile{
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		SubtitleProfiles: []playback.SubtitleProfile{{Format: "srt", Method: playback.SubtitleExternal}},
	}
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackServiceSourceReader{records: []playback.MediaSourceRecord{
			{ID: itemID, Path: playbackServiceString("/media/primary.mp4"), Container: &container, MediaStreams: streams},
			{ID: alternateID, Path: playbackServiceString("/media/alternate.mp4"), Container: &container, MediaStreams: streams},
		}},
		Selection:        playbackServiceSelectionReader{selection: selection, found: true},
		NewPlaySessionID: func() string { return "selection-regression-session" },
	})

	control := playbackServiceRequest(itemID)
	control.DeviceProfile = profile
	ordered, err := service.PlaybackInfo(t.Context(), control)
	if err != nil {
		t.Fatalf("unscoped PlaybackInfo: %v", err)
	}
	if len(ordered.MediaSources) != 2 || ordered.MediaSources[0].ID != alternateID.String() {
		t.Fatalf("resumable alternate order = %#v, want %q first", ordered.MediaSources, alternateID.String())
	}

	explicitAudio, explicitSubtitle := 3, 4
	request := playbackServiceRequest(itemID)
	request.DeviceProfile = profile
	request.MediaSourceID = itemID.String()
	request.AudioStreamIndex = &explicitAudio
	request.SubtitleStreamIndex = &explicitSubtitle
	got, err := service.PlaybackInfo(t.Context(), request)
	if err != nil {
		t.Fatalf("explicit PlaybackInfo: %v", err)
	}
	if len(got.MediaSources) != 1 {
		t.Fatalf("explicit PlaybackInfo sources = %#v, want one requested source", got.MediaSources)
	}
	resolved := got.MediaSources[0]
	if resolved.DefaultAudioStreamIndex == nil {
		t.Errorf("explicit DefaultAudioStreamIndex omitted, want negotiated %d rather than remembered %d", explicitAudio, storedAudio)
	} else if got := *resolved.DefaultAudioStreamIndex; got != explicitAudio {
		t.Errorf("explicit DefaultAudioStreamIndex = %d, want negotiated %d rather than remembered %d", got, explicitAudio, storedAudio)
	}
	if resolved.DefaultSubtitleStreamIndex == nil {
		t.Errorf("explicit DefaultSubtitleStreamIndex omitted, want negotiated %d rather than remembered %d", explicitSubtitle, storedSubtitle)
	} else if got := *resolved.DefaultSubtitleStreamIndex; got != explicitSubtitle {
		t.Errorf("explicit DefaultSubtitleStreamIndex = %d, want negotiated %d rather than remembered %d", got, explicitSubtitle, storedSubtitle)
	}
}

// Source: MediaInfoController.cs PlaySessionId generation and
// MediaInfoHelper.cs:249-276. Session entropy is a response prerequisite: a
// response with an empty PlaySessionId produces unusable client URLs.
func TestPlaybackInfoApplication_RejectsEmptyMintedPlaySessionID(t *testing.T) {
	itemID := playbackInfoItemID()
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackServiceSourceReader{records: playbackServiceSources(itemID)},
		NewPlaySessionID: func() string {
			return ""
		},
	})

	got, err := service.PlaybackInfo(t.Context(), playbackServiceRequest(itemID))
	if err == nil {
		t.Fatal("PlaybackInfo with failed session-ID minting succeeded")
	}
	if got.PlaySessionID != "" || got.ErrorCode != "" || len(got.MediaSources) != 0 {
		t.Errorf("PlaybackInfo failure returned partial response %#v", got)
	}
}

func TestPlaybackInfoApplication_WithoutLiveProviderOpenIsUnavailableAndCloseIsIdempotent(t *testing.T) {
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{})

	opened, err := service.OpenLiveStream(t.Context(), playback.OpenLiveStreamRequest{OpenToken: "unavailable"})
	if err == nil {
		t.Fatal("OpenLiveStream without a provider succeeded")
	}
	if opened.MediaSource.ID != "" {
		t.Errorf("OpenLiveStream without a provider fabricated media source %#v", opened.MediaSource)
	}
	if err := service.CloseLiveStream(t.Context(), "missing-live-stream"); err != nil {
		t.Fatalf("CloseLiveStream without a provider = %v, want idempotent success", err)
	}
}

type playbackServiceSourceReader struct {
	records []playback.MediaSourceRecord
	err     error
}

func (reader playbackServiceSourceReader) MediaSourceRecords(_ context.Context, _ dto.UUID) ([]playback.MediaSourceRecord, error) {
	return reader.records, reader.err
}

type playbackServiceSelectionReader struct {
	selection playback.SourceSelection
	found     bool
	err       error
}

func (reader playbackServiceSelectionReader) SourceSelection(_ context.Context, _ dto.UUID, _ dto.UUID, _ []dto.UUID) (playback.SourceSelection, bool, error) {
	return reader.selection, reader.found, reader.err
}

type playbackServiceProfileKey struct {
	client   string
	deviceID string
}

type playbackServiceProfileReader struct {
	profiles     map[playbackServiceProfileKey]*playback.DeviceProfile
	profile      *playback.DeviceProfile
	err          error
	calls        int
	lastClient   string
	lastDeviceID string
}

func (reader *playbackServiceProfileReader) DeviceProfile(_ context.Context, client, deviceID string) (*playback.DeviceProfile, error) {
	reader.calls++
	reader.lastClient = client
	reader.lastDeviceID = deviceID
	if reader.err != nil {
		return nil, reader.err
	}
	if profile, ok := reader.profiles[playbackServiceProfileKey{client: client, deviceID: deviceID}]; ok {
		return profile, nil
	}
	return reader.profile, nil
}

type playbackServicePermissionReader struct {
	permissions playback.PlaybackPermissions
	err         error
}

func (reader playbackServicePermissionReader) PlaybackPermissions(_ context.Context, _ dto.UUID) (playback.PlaybackPermissions, error) {
	return reader.permissions, reader.err
}

type playbackServiceNetwork struct{ isLAN bool }

func (network playbackServiceNetwork) IsLAN(_ netip.Addr) bool { return network.isLAN }

func playbackServiceRequest(itemID dto.UUID) playback.PlaybackInfoRequest {
	request := playback.NewPlaybackInfoRequest()
	request.ItemID = itemID
	request.UserID = dto.UUID{0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x01, 0x02, 0x03}
	request.DeviceID = "device-profile-fallback"
	request.Session = playback.PlaybackSession{ID: "caller-default-session", DeviceID: request.DeviceID, AccessToken: "test-token"}
	return request
}

func playbackServiceSources(itemID dto.UUID) []playback.MediaSourceRecord {
	secondID := itemID
	secondID[15]++
	container := "mp4"
	streams := []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
	}
	return []playback.MediaSourceRecord{
		{ID: itemID, Path: playbackServiceString("/media/first.mp4"), Container: &container, MediaStreams: streams},
		{ID: secondID, Path: playbackServiceString("/media/second.mp4"), Container: &container, MediaStreams: streams},
	}
}

func playbackServiceDirectProfile(id, container string) *playback.DeviceProfile {
	return &playback.DeviceProfile{ID: id, DirectPlayProfiles: []playback.DirectPlayProfile{{
		Type: playback.DlnaVideo, Container: container, VideoCodec: "h264", AudioCodec: "aac",
	}}}
}

func playbackServiceTranscodeProfile() *playback.DeviceProfile {
	return &playback.DeviceProfile{ID: "transcode-profile", TranscodingProfiles: []playback.TranscodingProfile{{
		Type: playback.DlnaVideo, Context: playback.ContextStreaming, Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Protocol: playback.StreamProtocolHTTP,
	}}}
}

func playbackServicePermissions() playback.PlaybackPermissions {
	return playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true}
}

func playbackServiceString(value string) *string { return &value }
