// SPDX-License-Identifier: GPL-2.0-only

package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestSessionActivityRecordsForPostCapabilitiesWithoutOrEmptyID verifies that
// POST /Sessions/Capabilities records authenticated activity when the id query
// parameter is absent or empty, and does not record it when id is explicit.
// This mirrors the PostFullCapabilities behavior (SessionController.cs:355-358).
func TestSessionActivityRecordsForPostCapabilitiesWithoutOrEmptyID(t *testing.T) {
	for _, test := range []struct {
		name         string
		query        string
		wantActivity bool
	}{
		{name: "no id records activity", query: "", wantActivity: true},
		{name: "empty id records activity", query: "?id=", wantActivity: true},
		{name: "explicit id does not record activity", query: "?id=abc", wantActivity: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeClock := clock.NewFake(time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC))
			sessions := session.New(fakeClock)
			t.Cleanup(func() {
				if err := sessions.Close(); err != nil {
					t.Errorf("close sessions: %v", err)
				}
			})

			original, err := sessions.LogActivity(t.Context(), session.Activity{
				Client: "Browser", Version: "1.0", DeviceID: "device-1", DeviceName: "Browser", UserID: middlewareTestUserID, RemoteEndPoint: "192.0.2.1",
			})
			if err != nil {
				t.Fatalf("seed session: %v", err)
			}

			resolver := &fakeResolver{identity: auth.Identity{
				Authenticated: true, Token: "token", UserID: middlewareTestUserID,
				Client: "Browser", DeviceID: "device-1", Version: "1.0",
			}}
			authorizer := middleware.NewAuthorizer(resolver, &fakeEvaluator{decision: auth.DecisionAllowed}, &fakeUsers{user: store.User{ID: middlewareTestUserID}})
			routes := router.New(authorizer.Wrap, middleware.NewSessionActivity(sessions).Wrap)
			routes.Handle(http.MethodPost, "/Sessions/Capabilities", router.Route{OperationID: "PostCapabilities", Policy: string(auth.PolicyDefaultAuthorization)}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			fakeClock.Advance(5 * time.Second)
			request := httptest.NewRequest(http.MethodPost, "/Sessions/Capabilities"+test.query, nil)
			request.RemoteAddr = "198.51.100.44:8096"
			recorder := httptest.NewRecorder()
			routes.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}

			snapshot, ok := sessionByID(sessions, original.ID)
			if !ok {
				t.Fatalf("session %q not found after request", original.ID)
			}
			activityRecorded := snapshot.LastActivity.Equal(fakeClock.Now())
			if activityRecorded != test.wantActivity {
				t.Fatalf("activity recorded = %t (last activity = %s, clock = %s), want %t",
					activityRecorded, snapshot.LastActivity, fakeClock.Now(), test.wantActivity)
			}
		})
	}
}

// TestSessionActivityRecordsForPostFullCapabilitiesWithoutOrEmptyID verifies the
// same behavior for the Full variant, proving that both capability endpoints
// share the same id-conditional activity rule.
func TestSessionActivityRecordsForPostFullCapabilitiesWithoutOrEmptyID(t *testing.T) {
	for _, test := range []struct {
		name         string
		query        string
		wantActivity bool
	}{
		{name: "no id records activity", query: "", wantActivity: true},
		{name: "empty id records activity", query: "?id=", wantActivity: true},
		{name: "explicit id does not record activity", query: "?id=abc", wantActivity: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeClock := clock.NewFake(time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC))
			sessions := session.New(fakeClock)
			t.Cleanup(func() {
				if err := sessions.Close(); err != nil {
					t.Errorf("close sessions: %v", err)
				}
			})

			original, err := sessions.LogActivity(t.Context(), session.Activity{
				Client: "Browser", Version: "1.0", DeviceID: "device-1", DeviceName: "Browser", UserID: middlewareTestUserID, RemoteEndPoint: "192.0.2.1",
			})
			if err != nil {
				t.Fatalf("seed session: %v", err)
			}

			resolver := &fakeResolver{identity: auth.Identity{
				Authenticated: true, Token: "token", UserID: middlewareTestUserID,
				Client: "Browser", DeviceID: "device-1", Version: "1.0",
			}}
			authorizer := middleware.NewAuthorizer(resolver, &fakeEvaluator{decision: auth.DecisionAllowed}, &fakeUsers{user: store.User{ID: middlewareTestUserID}})
			routes := router.New(authorizer.Wrap, middleware.NewSessionActivity(sessions).Wrap)
			routes.Handle(http.MethodPost, "/Sessions/Capabilities/Full", router.Route{OperationID: "PostFullCapabilities", Policy: string(auth.PolicyDefaultAuthorization)}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			fakeClock.Advance(5 * time.Second)
			request := httptest.NewRequest(http.MethodPost, "/Sessions/Capabilities/Full"+test.query, nil)
			request.RemoteAddr = "198.51.100.44:8096"
			recorder := httptest.NewRecorder()
			routes.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}

			snapshot, ok := sessionByID(sessions, original.ID)
			if !ok {
				t.Fatalf("session %q not found after request", original.ID)
			}
			activityRecorded := snapshot.LastActivity.Equal(fakeClock.Now())
			if activityRecorded != test.wantActivity {
				t.Fatalf("activity recorded = %t (last activity = %s, clock = %s), want %t",
					activityRecorded, snapshot.LastActivity, fakeClock.Now(), test.wantActivity)
			}
		})
	}
}

func sessionByID(sessions *session.Manager, id string) (session.Snapshot, bool) {
	for _, s := range sessions.Sessions(session.Query{}) {
		if s.ID == id {
			return s, true
		}
	}
	return session.Snapshot{}, false
}
