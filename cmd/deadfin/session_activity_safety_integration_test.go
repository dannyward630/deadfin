// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// A durable device token authenticates independently from the in-memory
// session registry. Its first playback report after a process restart must
// establish a live session for its token owner, so the normal start/progress/
// stop lifecycle needs no password login to recover.
func TestApplicationPersistentDeviceTokenRestoresSessionForPlaybackAfterRestart(t *testing.T) {
	const runtimeTicks int64 = 20 * 60 * 10_000_000

	programData := filepath.Join(t.TempDir(), "program-data")
	first := newCompositionApplication(t, programData)
	owner, err := first.UserService().Create(t.Context(), "persistent-device-owner", "correct password")
	if err != nil {
		t.Fatalf("create token owner: %v", err)
	}
	token := issueSessionActivitySafetyToken(t, first, owner.ID)
	movieID := seedPlaystateTracerMovie(t, first, runtimeTicks)

	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("close first application: %v", err)
	}
	app := newCompositionApplication(t, programData)
	if sessions := app.sessions.Sessions(session.Query{}); len(sessions) != 0 {
		t.Fatalf("reopened session registry = %#v, want no in-memory sessions", sessions)
	}

	positionTicks := runtimeTicks / 2
	for _, report := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "start",
			path: "/Sessions/Playing",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"PlayMethod":"DirectPlay","PlaySessionId":"persistent-token-session"}`, movieID.Dashed()),
		},
		{
			name: "progress",
			path: "/Sessions/Playing/Progress",
			body: fmt.Sprintf(`{"ItemId":%q,"CanSeek":true,"PositionTicks":%d,"PlayMethod":"DirectPlay","PlaySessionId":"persistent-token-session"}`, movieID.Dashed(), positionTicks),
		},
		{
			name: "stop",
			path: "/Sessions/Playing/Stopped",
			body: fmt.Sprintf(`{"ItemId":%q,"PositionTicks":%d,"PlaySessionId":"persistent-token-session"}`, movieID.Dashed(), positionTicks),
		},
	} {
		t.Run(report.name, func(t *testing.T) {
			record := authenticatedCompositionRequest(app.Router(), http.MethodPost, report.path, []byte(report.body), token)
			if record.Code != http.StatusNoContent {
				t.Fatalf("%s with persisted token status = %d, want %d; body: %s", report.name, record.Code, http.StatusNoContent, record.Body.String())
			}

			snapshot, found := app.sessions.Session("Deadfin Test", "deadfin-test-device")
			if !found {
				t.Fatal("persisted token request did not recreate its live session")
			}
			if snapshot.UserID != owner.ID {
				t.Fatalf("recreated session owner = %s, want token owner %s", snapshot.UserID, owner.ID)
			}
			if snapshot.Target.Generation == 0 {
				t.Fatal("recreated session has zero target generation")
			}

			switch report.name {
			case "start", "progress":
				if snapshot.NowPlaying == nil || snapshot.NowPlaying.ItemID != movieID {
					t.Fatalf("%s session playback = %#v, want active item %s", report.name, snapshot.NowPlaying, movieID.Dashed())
				}
				if report.name == "progress" && (snapshot.NowPlaying.PositionTicks == nil || *snapshot.NowPlaying.PositionTicks != positionTicks) {
					t.Fatalf("progress session position = %v, want %d", snapshot.NowPlaying.PositionTicks, positionTicks)
				}
			case "stop":
				if snapshot.NowPlaying != nil {
					t.Fatalf("stopped session still has active playback %#v", snapshot.NowPlaying)
				}
			}
		})
	}
}

// A device token is an identity credential, not permission to mutate a live
// session whose Client+DeviceID now belongs to another user. Progress is the
// documented no-op for an unavailable target; an omitted capability Id must
// reject rather than select that other user's current session.
func TestApplicationCrossUserDeviceTokenCannotTouchOrReportCapabilitiesToDisplacedSession(t *testing.T) {
	t.Run("progress leaves displaced session untouched", func(t *testing.T) {
		app, tokenA, before := newDisplacedSessionSafetyFixture(t)
		progress := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Sessions/Playing/Progress", []byte(`{"ItemId":"00000000-0000-0000-0000-000000b00001","PositionTicks":12345}`), tokenA)
		if progress.Code != http.StatusNoContent {
			t.Fatalf("cross-user progress status = %d, want %d; body: %s", progress.Code, http.StatusNoContent, progress.Body.String())
		}
		assertDisplacedSessionUntouched(t, app, before)
	})

	t.Run("implicit capability report rejects displaced session", func(t *testing.T) {
		app, tokenA, before := newDisplacedSessionSafetyFixture(t)
		capabilities := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Sessions/Capabilities/Full", []byte(`{"PlayableMediaTypes":["Video"],"SupportedCommands":["Play"],"SupportsMediaControl":true}`), tokenA)
		if capabilities.Code != http.StatusNotFound {
			t.Fatalf("cross-user implicit capability report status = %d, want %d; body: %s", capabilities.Code, http.StatusNotFound, capabilities.Body.String())
		}
		assertDisplacedSessionUntouched(t, app, before)
	})
}

func newDisplacedSessionSafetyFixture(t *testing.T) (*application, string, session.Snapshot) {
	t.Helper()

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	userA, err := app.UserService().Create(t.Context(), "session-owner-a", "correct password")
	if err != nil {
		t.Fatalf("create user A: %v", err)
	}
	userB, err := app.UserService().Create(t.Context(), "session-owner-b", "correct password")
	if err != nil {
		t.Fatalf("create user B: %v", err)
	}
	tokenA := issueSessionActivitySafetyToken(t, app, userA.ID)
	before, err := app.sessions.LogActivity(t.Context(), session.Activity{
		Client:         "Deadfin Test",
		Version:        "1.0",
		DeviceID:       "deadfin-test-device",
		DeviceName:     "User B Device",
		UserID:         userB.ID,
		RemoteEndPoint: "203.0.113.10",
	})
	if err != nil {
		t.Fatalf("seed user B session: %v", err)
	}
	return app, tokenA, before
}

func issueSessionActivitySafetyToken(t *testing.T, app *application, userID dto.UUID) string {
	t.Helper()

	device, err := app.UserService().IssueToken(t.Context(), store.Device{
		UserID:     userID,
		AppName:    "Deadfin Test",
		AppVersion: "1.0",
		DeviceName: "Deadfin Browser",
		DeviceID:   "deadfin-test-device",
	})
	if err != nil {
		t.Fatalf("issue persistent device token: %v", err)
	}
	if device.AccessToken == "" {
		t.Fatal("issued persistent device token is empty")
	}
	return device.AccessToken
}

func assertDisplacedSessionUntouched(t *testing.T, app *application, want session.Snapshot) {
	t.Helper()

	got, found := app.sessions.Session("Deadfin Test", "deadfin-test-device")
	if !found {
		t.Fatal("displaced user B session was removed")
	}
	if got.UserID != want.UserID || got.Target != want.Target {
		t.Fatalf("displaced session identity = owner %s target %#v, want owner %s target %#v", got.UserID, got.Target, want.UserID, want.Target)
	}
	if got.DeviceName != want.DeviceName || got.Version != want.Version || got.RemoteEndPoint != want.RemoteEndPoint || !got.LastActivity.Equal(want.LastActivity) {
		t.Fatalf("displaced session activity = device %q version %q endpoint %q at %s, want device %q version %q endpoint %q at %s", got.DeviceName, got.Version, got.RemoteEndPoint, got.LastActivity, want.DeviceName, want.Version, want.RemoteEndPoint, want.LastActivity)
	}
	if !reflect.DeepEqual(got.Capabilities, want.Capabilities) {
		t.Fatalf("displaced session capabilities = %#v, want %#v", got.Capabilities, want.Capabilities)
	}
}
