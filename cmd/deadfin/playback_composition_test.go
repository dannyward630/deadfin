// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Source: upstream/jellyfin/Jellyfin.Api/Helpers/MediaInfoHelper.cs:504-525.
// A non-positive user limit delegates to the configured server remote-client
// limit; zero must never become a literal zero-bitrate playback constraint.
func TestPlaybackPolicyReaderFallsBackToConfiguredRemoteBitrateWhenUserLimitIsZero(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")
	app := newCompositionApplication(t, root)

	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatalf("new configuration paths: %v", err)
	}
	changes := events.NewBus[config.Change](clock.NewReal())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		if err := changes.Close(ctx); err != nil {
			t.Errorf("close configuration change bus: %v", err)
		}
	})
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("open configuration: %v", err)
	}

	const serverRemoteBitrate = 6_000_000
	if err := manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
		system.RemoteClientBitrateLimit = serverRemoteBitrate
		return nil
	}); err != nil {
		t.Fatalf("configure server remote bitrate: %v", err)
	}

	member, err := app.UserService().Create(t.Context(), "server-bitrate-fallback", "password")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	member.Policy, err = json.Marshal(struct {
		RemoteClientBitrateLimit int `json:"RemoteClientBitrateLimit"`
	}{RemoteClientBitrateLimit: 0})
	if err != nil {
		t.Fatalf("marshal zero user bitrate policy: %v", err)
	}
	if err := app.UserService().Update(t.Context(), member); err != nil {
		t.Fatalf("persist zero user bitrate policy: %v", err)
	}

	permissions, err := (playbackPolicyReader{users: app.UserService(), manager: manager}).PlaybackPermissions(t.Context(), member.ID)
	if err != nil {
		t.Fatalf("read playback permissions: %v", err)
	}
	if permissions.RemoteBitrateLimit == nil || *permissions.RemoteBitrateLimit != serverRemoteBitrate {
		t.Fatalf("RemoteBitrateLimit = %v, want configured server limit %d for zero user policy", permissions.RemoteBitrateLimit, serverRemoteBitrate)
	}
}

// A stored JSON null is a present session capability describing no profile. It
// must take the assembly-only path, never device-profile negotiation. The
// authenticated user's normal source defaults still apply to that raw output.
func TestApplicationPostedPlaybackInfoTreatsStoredNullProfileAsAbsent(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())

	itemID := composedItemEnrichmentUUID(t, 0xd002)
	knownUserDefaultSubtitleStreamIndex := 2
	knownUserDefaultSubtitleStreamScore := 121_122
	items := store.NewItemStore(app.stores)
	seedFullComposedMedia(t, items, itemID)
	rawRecords, err := items.MediaSourceRecords(t.Context(), itemID)
	if err != nil {
		t.Fatalf("load persisted raw playback sources: %v", err)
	}
	wantSources, err := playback.AssembleMediaSources(t.Context(), items, itemID)
	if err != nil {
		t.Fatalf("assemble stored playback sources: %v", err)
	}
	rawRecordBySourceID := make(map[string]playback.MediaSourceRecord, len(rawRecords))
	for _, record := range rawRecords {
		rawRecordBySourceID[record.ID.String()] = record
	}
	for sourceIndex := range wantSources {
		source := &wantSources[sourceIndex]
		record, found := rawRecordBySourceID[source.ID]
		if !found {
			t.Fatalf("assembled source %q has no persisted record", source.ID)
		}
		if record.Name != nil {
			source.Name = *record.Name
		}
		if source.ID == itemID.String() {
			source.DefaultSubtitleStreamIndex = &knownUserDefaultSubtitleStreamIndex
		} else {
			source.DefaultSubtitleStreamIndex = nil
		}
		persistedStreamByIndex := make(map[int]playback.MediaStream, len(record.MediaStreams))
		for _, stream := range record.MediaStreams {
			persistedStreamByIndex[stream.Index] = stream
		}
		for streamIndex := range source.MediaStreams {
			stream := &source.MediaStreams[streamIndex]
			persisted, found := persistedStreamByIndex[stream.Index]
			if !found {
				t.Fatalf("assembled source %q stream %d has no persisted stream", source.ID, stream.Index)
			}
			stream.Score = persisted.Score
			if source.ID == itemID.String() && stream.Index == knownUserDefaultSubtitleStreamIndex {
				stream.Score = &knownUserDefaultSubtitleStreamScore
			}
			stream.SupportsExternalStream = persisted.SupportsExternalStream
		}
	}

	token := loginComposedAdministrator(t, app.Router())
	snapshots := app.sessions.Sessions(session.Query{DeviceID: "deadfin-test-device"})
	if len(snapshots) != 1 {
		t.Fatalf("device sessions = %#v, want one login session", snapshots)
	}
	sessionID := snapshots[0].ID
	if err := app.sessions.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID:           &sessionID,
		CurrentID:    sessionID,
		Capabilities: session.Capabilities{DeviceProfile: []byte(`null`)},
	}); err != nil {
		t.Fatalf("store literal null DeviceProfile: %v", err)
	}
	if raw, found := app.sessions.DeviceProfile("Deadfin Test", "deadfin-test-device"); !found || string(raw) != "null" {
		t.Fatalf("stored DeviceProfile = %q, found=%t; want present literal null", raw, found)
	}

	rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", []byte(`{"DeviceId":"deadfin-test-device"}`), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST PlaybackInfo status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		MediaSources  json.RawMessage            `json:"MediaSources"`
		PlaySessionID string                     `json:"PlaySessionId"`
		ErrorCode     playback.PlaybackInfoError `json:"ErrorCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode POST PlaybackInfo response: %v; body=%s", err, rec.Body.String())
	}
	if response.PlaySessionID == "" || response.ErrorCode != "" {
		t.Fatalf("POST PlaybackInfo response = %#v, want a raw-source session response", response)
	}
	wantSourceBytes, err := json.Marshal(wantSources)
	if err != nil {
		t.Fatalf("marshal raw persisted playback sources: %v", err)
	}
	if string(response.MediaSources) != string(wantSourceBytes) {
		t.Errorf("POST PlaybackInfo raw MediaSources = %s, want exact assembly-only sources with known-user defaults; stored null must not enable device-profile negotiation: %s", response.MediaSources, wantSourceBytes)
	}
}

// The composition adapter must retain the client identity that selected the
// authenticated session profile. Device-only lookup is ambiguous when one
// physical device hosts browser and television clients.
func TestPlaybackDeviceProfilesSelectExactClientDeviceSession(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	for _, activity := range []session.Activity{
		{Client: "Browser", Version: "1.0", DeviceID: "shared-device", DeviceName: "Browser Device"},
		{Client: "Television", Version: "1.0", DeviceID: "shared-device", DeviceName: "Television Device"},
	} {
		snapshot, err := app.sessions.LogActivity(t.Context(), activity)
		if err != nil {
			t.Fatalf("create %s session: %v", activity.Client, err)
		}
		profile := `{"Name":"browser-h264","MaxStreamingBitrate":800000,"TranscodingProfiles":[{"Type":"Video","Container":"ts","VideoCodec":"h264","AudioCodec":"aac","Protocol":"Http","Context":"Streaming"}]}`
		if activity.Client == "Television" {
			profile = `{"Name":"television-vp9","MaxStreamingBitrate":4000000,"TranscodingProfiles":[{"Type":"Video","Container":"ts","VideoCodec":"vp9","AudioCodec":"opus","Protocol":"Http","Context":"Streaming"}]}`
		}
		id := snapshot.ID
		if err := app.sessions.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{DeviceProfile: []byte(profile)}}); err != nil {
			t.Fatalf("store %s profile: %v", activity.Client, err)
		}
	}

	profiles := playbackDeviceProfiles{sessions: app.sessions}
	for _, tc := range []struct {
		client, name, codec string
		bitrate             int
	}{
		{client: "bRoWsEr", name: "browser-h264", codec: "h264", bitrate: 800000},
		{client: "TELEVISION", name: "television-vp9", codec: "vp9", bitrate: 4000000},
	} {
		t.Run(tc.client, func(t *testing.T) {
			profile, err := profiles.DeviceProfile(t.Context(), tc.client, "SHARED-DEVICE")
			if err != nil {
				t.Fatalf("read %s profile: %v", tc.client, err)
			}
			if profile == nil || profile.Name != tc.name || profile.MaxStreamingBitrate == nil || *profile.MaxStreamingBitrate != tc.bitrate || len(profile.TranscodingProfiles) != 1 || profile.TranscodingProfiles[0].VideoCodec != tc.codec {
				t.Fatalf("%s profile = %#v, want exact client profile %q (%s at %d)", tc.client, profile, tc.name, tc.codec, tc.bitrate)
			}
		})
	}

	missing, err := profiles.DeviceProfile(t.Context(), "Mobile", "shared-device")
	if err != nil || missing != nil {
		t.Fatalf("missing exact client/device profile = %#v, %v; want no device-only fallback", missing, err)
	}
}

// TestPlaybackSourceReaderMapsStoreNotFound proves the composition adapter
// translates the store sentinel to the playback domain. An empty-store
// lookup must return playback.ErrItemNotFound, not the raw store.ErrNotFound.
func TestPlaybackSourceReaderMapsStoreNotFound(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))

	absent, err := dto.ParseUUID("00000000-0000-0000-0000-000000009967")
	if err != nil {
		t.Fatalf("parse absent UUID: %v", err)
	}
	reader := playbackSourceReader{items: store.NewItemStore(app.stores)}
	if _, err := reader.MediaSourceRecords(t.Context(), absent); !errors.Is(err, playback.ErrItemNotFound) {
		t.Fatalf("playbackSourceReader absent item error = %v, want playback.ErrItemNotFound", err)
	}
}

// TestApplicationMissingStaticSourceReturnsNotFound proves a missing
// static-source request through the composed router still returns HTTP 404,
// not 500. The composition adapter maps store.ErrNotFound to
// playback.ErrItemNotFound, and the handler maps that to 404.
func TestApplicationMissingStaticSourceReturnsNotFound(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))

	absent, err := dto.ParseUUID("00000000-0000-0000-0000-000000009967")
	if err != nil {
		t.Fatalf("parse absent UUID: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Videos/"+absent.Dashed()+"/stream?Static=true", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing static-source GET status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
