// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestApplicationPlaybackInfoUsesPerSourceSelectionsAndAlternateResumeOrder
// defends the authenticated PlaybackInfo contract. A remembered stream choice
// applies only to sources that expose it; the remaining source derives its own
// language preference fallback. The most recently played in-progress alternate
// is the first playback candidate, while completed or zero-position history is
// not resumable.
func TestApplicationPlaybackInfoUsesPerSourceSelectionsAndAlternateResumeOrder(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())
	account := composedAdministrator(t, app)

	configuration := json.RawMessage(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true,"AudioLanguagePreference":"eng","PlayDefaultAudioTrack":false,"SubtitleLanguagePreference":"eng","SubtitleMode":"Default"}`)
	if err := app.UserService().UpdateConfiguration(t.Context(), user.Actor{UserID: account.ID, Administrator: true}, account.ID, configuration); err != nil {
		t.Fatalf("configure source-selection user: %v", err)
	}

	fixture := seedPlaybackInfoUserSources(t, app)
	fixture.upsertSelections(t, account.ID)
	token := loginComposedAdministrator(t, app.Router())

	t.Run("per source remembered selections and language fallbacks agree for GET and POST", func(t *testing.T) {
		want := []playbackInfoUserSourceExpectation{
			{id: fixture.primary.ID.String(), audio: 7, subtitle: -1},
			{id: fixture.alternate.ID.String(), audio: 13, subtitle: -1},
		}
		assertPlaybackInfoUserSourcesForMethods(t, app, token, fixture.primary.ID, want)
	})

	t.Run("most recently played resumable alternate sorts first for GET and POST", func(t *testing.T) {
		fixture.upsertResumeHistory(t, account.ID, 100, false, time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC), 200, false, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
		assertPlaybackInfoUserSourceOrderForMethods(t, app, token, fixture.primary.ID, fixture.alternate.ID.String(), fixture.primary.ID.String())
	})

	t.Run("zero position completed alternate does not displace primary for GET and POST", func(t *testing.T) {
		fixture.upsertResumeHistory(t, account.ID, 100, false, time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC), 0, true, time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC))
		assertPlaybackInfoUserSourceOrderForMethods(t, app, token, fixture.primary.ID, fixture.primary.ID.String(), fixture.alternate.ID.String())
	})
}

type playbackInfoUserSourcesFixture struct {
	items        *store.ItemStore
	primary      item.Item
	alternate    item.Item
	primaryKey   string
	alternateKey string
}

func seedPlaybackInfoUserSources(t *testing.T, app *application) playbackInfoUserSourcesFixture {
	t.Helper()

	items := store.NewItemStore(app.stores)
	primaryID := composedItemEnrichmentUUID(t, 0xd101)
	alternateID := composedItemEnrichmentUUID(t, 0xd102)
	primaryName, alternateName := "Playback selection feature", "Playback selection feature alternate"
	primaryPath, alternatePath, container := "/library/playback-selection.mkv", "/library/playback-selection-alternate.mkv", "mkv"
	runtime, size, bitrate := int64(20*60*10_000_000), int64(1_000_000), 8_500_000
	alternateOrder := 1
	primary := item.Item{
		ID: primaryID, Kind: item.KindMovie, IsMovie: true, Name: &primaryName, Path: &primaryPath, Container: &container,
		RunTimeTicks: &runtime, Size: &size, TotalBitrate: &bitrate,
		LinkedChildren: []item.LinkedChildRef{{ItemID: alternateID, Type: item.LinkedChildTypeLocalAlternateVersion, SortOrder: &alternateOrder}},
	}
	alternate := item.Item{
		ID: alternateID, Kind: item.KindMovie, IsMovie: true, Name: &alternateName, Path: &alternatePath, Container: &container,
		RunTimeTicks: &runtime, Size: &size, TotalBitrate: &bitrate,
	}
	for _, source := range []item.Item{alternate, primary} {
		if err := items.Upsert(t.Context(), source); err != nil {
			t.Fatalf("upsert PlaybackInfo source %s: %v", source.ID, err)
		}
	}
	for _, source := range []struct {
		id      dto.UUID
		streams []playback.MediaStream
	}{
		{
			id: primaryID,
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoUserSourceString("fre"), IsDefault: true},
				{Index: 7, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoUserSourceString("eng"), IsOriginal: true},
				{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", Language: playbackInfoUserSourceString("eng"), IsDefault: true},
			},
		},
		{
			id: alternateID,
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 11, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoUserSourceString("fre"), IsDefault: true},
				{Index: 13, Type: playback.StreamTypeAudio, Codec: "aac", Language: playbackInfoUserSourceString("eng"), IsOriginal: true},
				{Index: 12, Type: playback.StreamTypeSubtitle, Codec: "subrip", Language: playbackInfoUserSourceString("eng"), IsDefault: true},
			},
		},
	} {
		if err := items.ReplaceStreams(t.Context(), source.id, source.streams); err != nil {
			t.Fatalf("seed PlaybackInfo streams for %s: %v", source.id, err)
		}
	}

	persistedPrimary, err := items.Get(t.Context(), primaryID)
	if err != nil {
		t.Fatalf("load persisted primary PlaybackInfo source: %v", err)
	}
	persistedAlternate, err := items.Get(t.Context(), alternateID)
	if err != nil {
		t.Fatalf("load persisted alternate PlaybackInfo source: %v", err)
	}
	return playbackInfoUserSourcesFixture{
		items: items, primary: persistedPrimary, alternate: persistedAlternate,
		primaryKey: playbackInfoUserSourceKey(t, persistedPrimary), alternateKey: playbackInfoUserSourceKey(t, persistedAlternate),
	}
}

func (fixture playbackInfoUserSourcesFixture) upsertSelections(t *testing.T, userID dto.UUID) {
	t.Helper()

	audio, subtitle := 7, -1
	if err := fixture.items.UpsertUserData(t.Context(), store.UserData{
		UserID: userID, CustomKey: fixture.primaryKey, ItemID: fixture.primary.ID, AudioStreamIndex: &audio, SubtitleStreamIndex: &subtitle,
	}); err != nil {
		t.Fatalf("upsert remembered PlaybackInfo source selections: %v", err)
	}
}

func (fixture playbackInfoUserSourcesFixture) upsertResumeHistory(t *testing.T, userID dto.UUID, primaryPosition int64, primaryPlayed bool, primaryLastPlayed time.Time, alternatePosition int64, alternatePlayed bool, alternateLastPlayed time.Time) {
	t.Helper()

	for _, history := range []struct {
		item       item.Item
		key        string
		position   int64
		played     bool
		lastPlayed time.Time
	}{
		{item: fixture.primary, key: fixture.primaryKey, position: primaryPosition, played: primaryPlayed, lastPlayed: primaryLastPlayed},
		{item: fixture.alternate, key: fixture.alternateKey, position: alternatePosition, played: alternatePlayed, lastPlayed: alternateLastPlayed},
	} {
		if err := fixture.items.UpsertUserData(t.Context(), store.UserData{
			UserID: userID, CustomKey: history.key, ItemID: history.item.ID, PositionTicks: history.position, Played: history.played, LastPlayed: &history.lastPlayed,
		}); err != nil {
			t.Fatalf("upsert PlaybackInfo resume history for %s: %v", history.item.ID, err)
		}
	}
}

func playbackInfoUserSourceKey(t *testing.T, value item.Item) string {
	t.Helper()

	keys := item.UserDataKeys(value, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatalf("PlaybackInfo source %s has no user-data keys", value.ID)
	}
	return keys[0]
}

type playbackInfoUserSourceExpectation struct {
	id       string
	audio    int
	subtitle int
}

func assertPlaybackInfoUserSourcesForMethods(t *testing.T, app *application, token string, itemID dto.UUID, want []playbackInfoUserSourceExpectation) {
	t.Helper()

	for _, request := range playbackInfoUserSourceRequests(itemID) {
		t.Run(request.name, func(t *testing.T) {
			got := playbackInfoUserSourcesResponse(t, app, token, request.method, request.path, request.body)
			if len(got) != len(want) {
				t.Fatalf("%s PlaybackInfo source count = %d, want %d; IDs=%v", request.name, len(got), len(want), playbackInfoUserSourceIDs(got))
			}
			for index, expected := range want {
				actual := got[index]
				if actual.ID != expected.id || actual.DefaultAudioStreamIndex == nil || *actual.DefaultAudioStreamIndex != expected.audio || actual.DefaultSubtitleStreamIndex == nil || *actual.DefaultSubtitleStreamIndex != expected.subtitle {
					t.Errorf("%s source[%d] = ID=%q audio=%v subtitle=%v, want ID=%q audio=%d subtitle=%d", request.name, index, actual.ID, playbackInfoUserSourceIndex(actual.DefaultAudioStreamIndex), playbackInfoUserSourceIndex(actual.DefaultSubtitleStreamIndex), expected.id, expected.audio, expected.subtitle)
				}
			}
		})
	}
}

func assertPlaybackInfoUserSourceOrderForMethods(t *testing.T, app *application, token string, itemID dto.UUID, want ...string) {
	t.Helper()

	for _, request := range playbackInfoUserSourceRequests(itemID) {
		t.Run(request.name, func(t *testing.T) {
			got := playbackInfoUserSourcesResponse(t, app, token, request.method, request.path, request.body)
			if len(got) != len(want) {
				t.Fatalf("%s PlaybackInfo source count = %d, want %d; IDs=%v", request.name, len(got), len(want), playbackInfoUserSourceIDs(got))
			}
			for index, expected := range want {
				if got[index].ID != expected {
					t.Errorf("%s PlaybackInfo source[%d] = %q, want %q; full order=%v", request.name, index, got[index].ID, expected, playbackInfoUserSourceIDs(got))
				}
			}
		})
	}
}

type playbackInfoUserSourceRequest struct {
	name, method, path string
	body               []byte
}

func playbackInfoUserSourceRequests(itemID dto.UUID) []playbackInfoUserSourceRequest {
	path := "/Items/" + itemID.Dashed() + "/PlaybackInfo"
	return []playbackInfoUserSourceRequest{
		{name: "GET", method: http.MethodGet, path: path},
		{name: "POST", method: http.MethodPost, path: path, body: []byte(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mkv","VideoCodec":"h264","AudioCodec":"aac"}],"SubtitleProfiles":[{"Format":"srt","Method":"Embed"}]}}`)},
	}
}

func playbackInfoUserSourcesResponse(t *testing.T, app *application, token, method, path string, body []byte) []playback.MediaSourceInfo {
	t.Helper()

	response := scannedPlaybackInfoRequest(t, app, token, method, path, body, "")
	var sources []playback.MediaSourceInfo
	if err := json.Unmarshal(response["MediaSources"], &sources); err != nil {
		t.Fatalf("decode %s PlaybackInfo sources: %v; response=%#v", method, err, response)
	}
	return sources
}

func playbackInfoUserSourceIDs(sources []playback.MediaSourceInfo) []string {
	ids := make([]string, len(sources))
	for index := range sources {
		ids[index] = sources[index].ID
	}
	return ids
}

func playbackInfoUserSourceIndex(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func playbackInfoUserSourceString(value string) *string { return &value }
