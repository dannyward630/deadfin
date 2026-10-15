// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationPlaystatePersistsEligibleResumeAcrossRestart exercises the
// client-facing playstate tracer: an authenticated client reports start,
// progress, and stop for an eligible movie, then sees the resumable, unplayed
// state through the existing item DTO after reopening persistent program data.
//
// The 20-minute runtime and halfway position deliberately exercise the normal
// resumable range: the movie is well above the configured minimum duration and
// the reported position is not close to either completion boundary.
func TestApplicationPlaystatePersistsEligibleResumeAcrossRestart(t *testing.T) {
	const (
		runtimeTicks  int64 = 20 * 60 * 10_000_000
		positionTicks int64 = runtimeTicks / 2
	)

	programData := filepath.Join(t.TempDir(), "program-data")
	var app *application
	t.Cleanup(func() {
		if app != nil {
			if err := app.Close(context.Background()); err != nil {
				t.Errorf("close playstate tracer application: %v", err)
			}
		}
	})

	app = openPlaystateTracerApplication(t, programData)
	configureComposedAdministrator(t, app.Router())
	token, _ := loginPlaystateTracerAdministrator(t, app)
	movieID := seedPlaystateTracerMovie(t, app, runtimeTicks)
	playSessionID := mintPlaystateTracerSession(t, app, token, movieID)
	for _, report := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "start",
			path: "/Sessions/Playing",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"PlayMethod":"DirectPlay","SessionId":%q,"PlaySessionId":%q}`, movieID.Dashed(), playSessionID, playSessionID),
		},
		{
			name: "progress",
			path: "/Sessions/Playing/Progress",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"IsPaused":false,"PositionTicks":%d,"PlayMethod":"DirectPlay","SessionId":%q,"PlaySessionId":%q}`, movieID.Dashed(), positionTicks, playSessionID, playSessionID),
		},
		{
			name: "stop",
			path: "/Sessions/Playing/Stopped",
			body: fmt.Sprintf(`{"ItemId":%q,"PositionTicks":%d,"SessionId":%q,"PlaySessionId":%q}`, movieID.Dashed(), positionTicks, playSessionID, playSessionID),
		},
	} {
		record := authenticatedCompositionRequest(app.Router(), http.MethodPost, report.path, []byte(report.body), token)
		if record.Code != http.StatusNoContent {
			t.Fatalf("%s playstate report %s status = %d, want %d; body: %s", report.name, report.path, record.Code, http.StatusNoContent, record.Body.String())
		}
	}

	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("close playstate tracer application before restart: %v", err)
	}
	app = nil
	app = openPlaystateTracerApplication(t, programData)
	token, userID := loginPlaystateTracerAdministrator(t, app)

	itemPath := fmt.Sprintf("/Items/%s?userId=%s&EnableUserData=true", movieID.Dashed(), userID.Dashed())
	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, itemPath, nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET resumed item status = %d, want %d; body: %s", record.Code, http.StatusOK, record.Body.String())
	}
	assertPlaystateTracerUserData(t, record.Body.Bytes(), positionTicks)
}

func openPlaystateTracerApplication(t *testing.T, programData string) *application {
	t.Helper()

	app, err := newApplication(t.Context(), programData)
	if err != nil {
		t.Fatalf("open playstate tracer application: %v", err)
	}
	return app
}

func loginPlaystateTracerAdministrator(t *testing.T, app *application) (string, dto.UUID) {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"captain","Pw":"correct horse battery staple"}`), "")
	if record.Code != http.StatusOK {
		t.Fatalf("playstate tracer login status = %d, want %d; body: %s", record.Code, http.StatusOK, record.Body.String())
	}
	var result struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode playstate tracer login: %v; body: %s", err, record.Body.String())
	}
	if result.AccessToken == "" || result.User.ID == "" {
		t.Fatalf("playstate tracer login response = %s, want access token and user ID", record.Body.String())
	}
	userID, err := dto.ParseUUID(result.User.ID)
	if err != nil {
		t.Fatalf("parse authenticated user ID %q: %v", result.User.ID, err)
	}
	return result.AccessToken, userID
}

func mintPlaystateTracerSession(t *testing.T, app *application, token string, movieID dto.UUID) string {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Items/"+movieID.Dashed()+"/PlaybackInfo", []byte(`{"DeviceId":"deadfin-test-device"}`), token)
	if record.Code != http.StatusOK {
		t.Fatalf("POST PlaybackInfo status = %d, want %d; body: %s", record.Code, http.StatusOK, record.Body.String())
	}
	var response struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v; body: %s", err, record.Body.String())
	}
	if response.PlaySessionID == "" {
		t.Fatalf("POST PlaybackInfo response = %s, want PlaySessionId", record.Body.String())
	}
	return response.PlaySessionID
}

func seedPlaystateTracerMovie(t *testing.T, app *application, runtimeTicks int64) dto.UUID {
	t.Helper()

	movieID, err := dto.ParseUUID("00000000000000000000000000a50002")
	if err != nil {
		t.Fatalf("parse playstate tracer movie ID: %v", err)
	}
	name, path, container := "Playstate tracer movie", "/library/playstate-tracer.mkv", "mkv"
	size, bitrate := int64(1_000_000), 8_500_000
	items := store.NewItemStore(app.stores)
	if err := items.Upsert(t.Context(), item.Item{
		ID: movieID, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path, Container: &container, Size: &size, RunTimeTicks: &runtimeTicks, TotalBitrate: &bitrate,
	}); err != nil {
		t.Fatalf("upsert eligible playstate tracer movie: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), movieID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}}); err != nil {
		t.Fatalf("store playstate tracer media stream: %v", err)
	}
	return movieID
}

func assertPlaystateTracerUserData(t *testing.T, body []byte, wantPositionTicks int64) {
	t.Helper()

	var itemDTO map[string]json.RawMessage
	if err := json.Unmarshal(body, &itemDTO); err != nil {
		t.Fatalf("decode resumed item DTO: %v; body: %s", err, body)
	}
	userData, found := itemDTO["UserData"]
	if !found {
		t.Fatalf("resumed item DTO has no Pascal-case UserData field: %s", body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(userData, &fields); err != nil {
		t.Fatalf("decode resumed item UserData: %v; UserData: %s", err, userData)
	}
	position, found := fields["PlaybackPositionTicks"]
	if !found {
		t.Fatalf("resumed UserData has no Pascal-case PlaybackPositionTicks field: %s", userData)
	}
	played, found := fields["Played"]
	if !found {
		t.Fatalf("resumed UserData has no Pascal-case Played field: %s", userData)
	}
	var gotPosition int64
	if err := json.Unmarshal(position, &gotPosition); err != nil {
		t.Fatalf("decode PlaybackPositionTicks %s: %v", position, err)
	}
	var gotPlayed bool
	if err := json.Unmarshal(played, &gotPlayed); err != nil {
		t.Fatalf("decode Played %s: %v", played, err)
	}
	if gotPosition != wantPositionTicks || gotPlayed {
		t.Fatalf("resumed UserData = PlaybackPositionTicks=%d Played=%t, want PlaybackPositionTicks=%d Played=false", gotPosition, gotPlayed, wantPositionTicks)
	}
}
