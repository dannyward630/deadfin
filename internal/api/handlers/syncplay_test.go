// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestSyncPlayRoutesMatchPinnedOpenAPI(t *testing.T) {
	rt := router.New()
	handlers.RegisterSyncPlay(rt)

	hasAccess := string(auth.PolicySyncPlayHasAccess)
	canCreate := string(auth.PolicySyncPlayCreateGroup)
	canJoin := string(auth.PolicySyncPlayJoinGroup)
	wantRoutes := []router.Route{
		{Method: http.MethodGet, Pattern: "/SyncPlay/{id}", OperationID: "SyncPlayGetGroup", Policy: canJoin},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Buffering", OperationID: "SyncPlayBuffering", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Join", OperationID: "SyncPlayJoinGroup", Policy: canJoin},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Leave", OperationID: "SyncPlayLeaveGroup", Policy: hasAccess},
		{Method: http.MethodGet, Pattern: "/SyncPlay/List", OperationID: "SyncPlayGetGroups", Policy: canJoin},
		{Method: http.MethodPost, Pattern: "/SyncPlay/MovePlaylistItem", OperationID: "SyncPlayMovePlaylistItem", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/New", OperationID: "SyncPlayCreateGroup", Policy: canCreate},
		{Method: http.MethodPost, Pattern: "/SyncPlay/NextItem", OperationID: "SyncPlayNextItem", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Pause", OperationID: "SyncPlayPause", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Ping", OperationID: "SyncPlayPing", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/PreviousItem", OperationID: "SyncPlayPreviousItem", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Queue", OperationID: "SyncPlayQueue", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Ready", OperationID: "SyncPlayReady", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/RemoveFromPlaylist", OperationID: "SyncPlayRemoveFromPlaylist", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Seek", OperationID: "SyncPlaySeek", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/SetIgnoreWait", OperationID: "SyncPlaySetIgnoreWait", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/SetNewQueue", OperationID: "SyncPlaySetNewQueue", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/SetPlaylistItem", OperationID: "SyncPlaySetPlaylistItem", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/SetRepeatMode", OperationID: "SyncPlaySetRepeatMode", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/SetShuffleMode", OperationID: "SyncPlaySetShuffleMode", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Stop", OperationID: "SyncPlayStop", Policy: hasAccess},
		{Method: http.MethodPost, Pattern: "/SyncPlay/Unpause", OperationID: "SyncPlayUnpause", Policy: hasAccess},
	}

	if got := len(rt.Routes()); got != len(wantRoutes) {
		t.Fatalf("SyncPlay route count = %d, want %d", got, len(wantRoutes))
	}
	for _, want := range wantRoutes {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found || got != want {
				t.Fatalf("route %q = %#v, found %t, want %#v", want.OperationID, got, found, want)
			}
		})
	}
}

func TestSyncPlayListReturnsEmptyArray(t *testing.T) {
	rt := router.New()
	handlers.RegisterSyncPlay(rt)
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/SyncPlay/List", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET SyncPlay List status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &groups); err != nil {
		t.Fatalf("decode SyncPlay List: %v; body=%s", err, record.Body.String())
	}
	if groups == nil || len(groups) != 0 {
		t.Fatalf("SyncPlay List = %#v, want non-nil empty array", groups)
	}
}

func TestSyncPlayGroupLookupReportsNotFound(t *testing.T) {
	rt := router.New()
	handlers.RegisterSyncPlay(rt)

	missing := httptest.NewRecorder()
	rt.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/SyncPlay/not-a-group", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("GET SyncPlay group status = %d, want 404; body=%s", missing.Code, missing.Body.String())
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(missing.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode SyncPlay group problem: %v; body=%s", err, missing.Body.String())
	}
	if problem.Status != http.StatusNotFound || problem.Detail != "sync play group not found" {
		t.Fatalf("SyncPlay group problem = %#v", problem)
	}
}

// TestSyncPlayDisabledPolicyRejectsMutations exercises the deferred SyncPlay
// surface through the production authorization middleware. The user policy
// explicitly grants CreateAndJoinGroups, so the evaluator allows the routes.
// The handlers enforce the disabled capability: List returns 200 with an
// empty array, and every mutation returns 403.
func TestSyncPlayDisabledPolicyRejectsMutations(t *testing.T) {
	userID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f9")
	identity := auth.Identity{Authenticated: true, UserID: userID, Token: "member-token"}
	resolver := syncPlayPolicyResolver{identity: identity}
	network := syncPlayLocalNetwork{address: netip.MustParseAddr("192.0.2.80")}
	evaluator := auth.NewEvaluator(
		syncPlayStartup{completed: true},
		network,
		syncPlaySchedules{},
		func() time.Time { return time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC) },
	)
	users := syncPlayUsers{user: store.User{
		ID:     userID,
		Policy: []byte(`{"EnableRemoteAccess":true,"SyncPlayAccess":"CreateAndJoinGroups"}`),
	}}
	authorizer := middleware.NewAuthorizer(resolver, evaluator, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterSyncPlay(rt)

	list := httptest.NewRecorder()
	rt.ServeHTTP(list, syncPlayRequest(http.MethodGet, "/SyncPlay/List"))
	if list.Code != http.StatusOK {
		t.Fatalf("GET SyncPlay List status = %d, want 200; body=%s", list.Code, list.Body.String())
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(list.Body.Bytes(), &groups); err != nil {
		t.Fatalf("decode SyncPlay List: %v; body=%s", err, list.Body.String())
	}
	if groups == nil || len(groups) != 0 {
		t.Fatalf("SyncPlay List = %#v, want non-nil empty array", groups)
	}

	for _, mutation := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/SyncPlay/New"},
		{http.MethodPost, "/SyncPlay/Join"},
		{http.MethodPost, "/SyncPlay/Buffering"},
		{http.MethodPost, "/SyncPlay/Leave"},
		{http.MethodPost, "/SyncPlay/MovePlaylistItem"},
		{http.MethodPost, "/SyncPlay/NextItem"},
		{http.MethodPost, "/SyncPlay/Pause"},
		{http.MethodPost, "/SyncPlay/Ping"},
		{http.MethodPost, "/SyncPlay/PreviousItem"},
		{http.MethodPost, "/SyncPlay/Queue"},
		{http.MethodPost, "/SyncPlay/Ready"},
		{http.MethodPost, "/SyncPlay/RemoveFromPlaylist"},
		{http.MethodPost, "/SyncPlay/Seek"},
		{http.MethodPost, "/SyncPlay/SetIgnoreWait"},
		{http.MethodPost, "/SyncPlay/SetNewQueue"},
		{http.MethodPost, "/SyncPlay/SetPlaylistItem"},
		{http.MethodPost, "/SyncPlay/SetRepeatMode"},
		{http.MethodPost, "/SyncPlay/SetShuffleMode"},
		{http.MethodPost, "/SyncPlay/Stop"},
		{http.MethodPost, "/SyncPlay/Unpause"},
	} {
		t.Run(mutation.method+" "+mutation.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, syncPlayRequest(mutation.method, mutation.path))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			var problem struct {
				Title  string `json:"title"`
				Status int    `json:"status"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v; body=%s", err, rec.Body.String())
			}
			if problem.Status != http.StatusForbidden || problem.Detail == "" {
				t.Fatalf("problem = %#v, want 403 with detail", problem)
			}
		})
	}
}

type syncPlayPolicyResolver struct{ identity auth.Identity }

func (r syncPlayPolicyResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return r.identity, nil
}

type syncPlayLocalNetwork struct{ address netip.Addr }

func (n syncPlayLocalNetwork) IsInLocalNetwork(addr netip.Addr) bool {
	return addr == n.address
}

type syncPlayStartup struct{ completed bool }

func (s syncPlayStartup) StartupWizardCompleted() bool { return s.completed }

type syncPlaySchedules struct{}

func (syncPlaySchedules) AccessSchedules(context.Context, dto.UUID) ([]store.AccessSchedule, error) {
	return nil, nil
}

type syncPlayUsers struct{ user store.User }

func (u syncPlayUsers) Get(_ context.Context, _ dto.UUID) (store.User, error) {
	return u.user, nil
}

func syncPlayRequest(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = "192.0.2.80:8096"
	request.Header.Set("Authorization", `MediaBrowser Token="member-token"`)
	return request
}
