// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// An authenticated playback client sees its complete live player state through
// /Sessions, then retains terminal queue, playlist, and timing history after a
// stop removes the active item. This crosses the real authorization, playstate,
// session, item-projection, and router seams.
func TestApplicationSessionsProjectsActiveAndTerminalPlayback(t *testing.T) {
	const runtimeTicks int64 = 20 * 60 * 10_000_000

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())
	token, _ := loginPlaystateTracerAdministrator(t, app)
	movieID := seedPlaystateTracerMovie(t, app, runtimeTicks)

	for _, report := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "start",
			path: "/Sessions/Playing",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"PositionTicks":12345,"MediaSourceId":"source-start","PlayMethod":"DirectPlay","RepeatMode":"RepeatAll","PlaybackOrder":"Shuffle","LiveStreamId":"live-start","PlaySessionId":"projection-session"}`, movieID.Dashed()),
		},
		{
			name: "progress",
			path: "/Sessions/Playing/Progress",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"IsPaused":true,"IsMuted":true,"PositionTicks":98765,"MediaSourceId":"source-progress","AudioStreamIndex":3,"SubtitleStreamIndex":4,"VolumeLevel":80,"PlayMethod":"DirectStream","RepeatMode":"RepeatOne","PlaybackOrder":"Shuffle","LiveStreamId":"live-progress","PlaySessionId":"projection-session","PlaylistItemId":"active-playlist"}`, movieID.Dashed()),
		},
	} {
		record := authenticatedCompositionRequest(app.Router(), http.MethodPost, report.path, []byte(report.body), token)
		if record.Code != http.StatusNoContent {
			t.Fatalf("%s playback report status = %d, want %d; body: %s", report.name, record.Code, http.StatusNoContent, record.Body.String())
		}
	}

	active := sessionProjection(t, app, token)
	assertActiveSessionProjection(t, active, movieID)

	stopBody := fmt.Sprintf(`{"ItemId":%q,"PositionTicks":98765,"PlaySessionId":"projection-session","PlaylistItemId":"queue-current","NowPlayingQueue":[{"Id":%q,"PlaylistItemId":"queue-current"},{"Id":"00000000-0000-0000-0000-000000a50003","PlaylistItemId":"queue-next"}]}`, movieID.Dashed(), movieID.Dashed())
	record := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Sessions/Playing/Stopped", []byte(stopBody), token)
	if record.Code != http.StatusNoContent {
		t.Fatalf("stop playback report status = %d, want %d; body: %s", record.Code, http.StatusNoContent, record.Body.String())
	}

	stopped := sessionProjection(t, app, token)
	if len(stopped.NowPlayingItem) != 0 && string(stopped.NowPlayingItem) != "null" {
		t.Fatalf("stopped session NowPlayingItem = %s, want omitted or null", stopped.NowPlayingItem)
	}
	if stopped.LastPlaybackCheckIn.IsZero() || !stopped.LastPlaybackCheckIn.Equal(active.LastPlaybackCheckIn.Time) {
		t.Fatalf("stopped LastPlaybackCheckIn = %s, want active check-in %s retained", stopped.LastPlaybackCheckIn, active.LastPlaybackCheckIn)
	}
	if stopped.LastPausedDate == nil || active.LastPausedDate == nil || !stopped.LastPausedDate.Equal(active.LastPausedDate.Time) {
		t.Fatalf("stopped LastPausedDate = %v, want active pause date %v retained", stopped.LastPausedDate, active.LastPausedDate)
	}
	if stopped.PlaylistItemID == nil || *stopped.PlaylistItemID != "queue-current" || len(stopped.NowPlayingQueue) != 2 || stopped.NowPlayingQueue[0].ID != movieID || stopped.NowPlayingQueue[0].PlaylistItemID == nil || *stopped.NowPlayingQueue[0].PlaylistItemID != "queue-current" || stopped.NowPlayingQueue[1].ID.Dashed() != "00000000-0000-0000-0000-000000a50003" || stopped.NowPlayingQueue[1].PlaylistItemID == nil || *stopped.NowPlayingQueue[1].PlaylistItemID != "queue-next" {
		t.Fatalf("stopped terminal queue = %#v with playlist item %v, want current and next queue entries with queue-current playlist item", stopped.NowPlayingQueue, stopped.PlaylistItemID)
	}
}

type sessionProjectionDTO struct {
	PlayState           map[string]json.RawMessage `json:"PlayState"`
	NowPlayingItem      json.RawMessage            `json:"NowPlayingItem"`
	LastPlaybackCheckIn dto.Time                   `json:"LastPlaybackCheckIn"`
	LastPausedDate      *dto.Time                  `json:"LastPausedDate"`
	NowPlayingQueue     []sessionProjectionQueue   `json:"NowPlayingQueue"`
	PlaylistItemID      *string                    `json:"PlaylistItemId"`
}

type sessionProjectionQueue struct {
	ID             dto.UUID `json:"Id"`
	PlaylistItemID *string  `json:"PlaylistItemId"`
}

func sessionProjection(t *testing.T, app *application, token string) sessionProjectionDTO {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Sessions", nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Sessions status = %d, want %d; body: %s", record.Code, http.StatusOK, record.Body.String())
	}
	var sessions []sessionProjectionDTO
	if err := json.Unmarshal(record.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode GET /Sessions: %v; body: %s", err, record.Body.String())
	}
	if len(sessions) != 1 {
		t.Fatalf("GET /Sessions returned %d sessions, want exactly authenticated session; body: %s", len(sessions), record.Body.String())
	}
	return sessions[0]
}

func assertActiveSessionProjection(t *testing.T, got sessionProjectionDTO, movieID dto.UUID) {
	t.Helper()

	if got.LastPlaybackCheckIn.IsZero() {
		t.Fatal("active session LastPlaybackCheckIn is zero after start and progress")
	}
	if got.LastPausedDate == nil || got.LastPausedDate.IsZero() {
		t.Fatalf("active session LastPausedDate = %v, want progress pause timestamp", got.LastPausedDate)
	}
	var item struct {
		ID dto.UUID `json:"Id"`
	}
	if len(got.NowPlayingItem) == 0 || string(got.NowPlayingItem) == "null" || json.Unmarshal(got.NowPlayingItem, &item) != nil || item.ID != movieID {
		t.Fatalf("active session NowPlayingItem = %s, want nonempty projected item ID %s", got.NowPlayingItem, movieID.Dashed())
	}
	for key, want := range map[string]any{
		"CanSeek":             true,
		"IsPaused":            true,
		"IsMuted":             true,
		"PositionTicks":       int64(98_765),
		"MediaSourceId":       "source-progress",
		"AudioStreamIndex":    3,
		"SubtitleStreamIndex": 4,
		"VolumeLevel":         80,
		"PlayMethod":          "DirectStream",
		"RepeatMode":          "RepeatOne",
		"PlaybackOrder":       "Shuffle",
		"LiveStreamId":        "live-progress",
	} {
		raw, found := got.PlayState[key]
		if !found {
			t.Errorf("active session PlayState omits %q; state: %#v", key, got.PlayState)
			continue
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal expected PlayState %q: %v", key, err)
		}
		if string(raw) != string(wantJSON) {
			t.Errorf("active session PlayState.%s = %s, want %s", key, raw, wantJSON)
		}
	}
}
