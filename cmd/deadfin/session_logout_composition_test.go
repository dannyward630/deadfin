// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

const logoutDeviceAuthorization = `MediaBrowser Client="Composition Test", Device="Composition Device", DeviceId="logout-device", Version="1.0"`

// TestSessionLogoutRevokesTokenEndsSessionAndClosesSockets ports
// SessionController.ReportSessionEnded and SessionManager.Logout: the caller's
// device token stops authenticating, its session leaves GET /Sessions, and its
// WebSocket closes. Other devices keep their tokens and sessions.
func TestSessionLogoutRevokesTokenEndsSessionAndClosesSockets(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())
	admin := loginComposedAdministrator(t, app.Router())
	login := composedLogin(t, app, "captain", "correct horse battery staple", "logout-device")
	assertCompositionLoginStatus(t, login, http.StatusOK)
	token := compositionAccessToken(t, login)
	var result struct {
		SessionInfo struct {
			ID string `json:"Id"`
		}
	}
	if err := json.Unmarshal(login.Body.Bytes(), &result); err != nil || result.SessionInfo.ID == "" {
		t.Fatalf("decode login session ID: %v; body: %s", err, login.Body.String())
	}
	if !compositionSessionIDs(t, app.Router(), admin)[result.SessionInfo.ID] {
		t.Fatalf("session %q is not listed before logout", result.SessionInfo.ID)
	}

	server := httptest.NewServer(app.Router())
	defer server.Close()
	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/socket", server.URL)
	if err != nil {
		t.Fatalf("new websocket config: %v", err)
	}
	config.Header.Set("Authorization", logoutDeviceAuthorization+`, Token="`+token+`"`)
	socket, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = socket.Close() }()
	var envelope map[string]json.RawMessage
	if err := websocket.JSON.Receive(socket, &envelope); err != nil {
		t.Fatalf("receive initial envelope: %v", err)
	}

	// Logout waits for the session's sockets to stop, so the next read fails
	// at once.
	if code := logoutDeviceRequest(app.Router(), http.MethodPost, "/Sessions/Logout", token); code != http.StatusNoContent {
		t.Fatalf("POST /Sessions/Logout = %d, want 204", code)
	}

	if err := websocket.JSON.Receive(socket, &envelope); err == nil {
		t.Fatalf("websocket stayed open after logout and sent %v", envelope)
	}
	if code := logoutDeviceRequest(app.Router(), http.MethodGet, "/Users/Me", token); code != http.StatusUnauthorized {
		t.Errorf("GET /Users/Me after logout = %d, want 401", code)
	}
	if code := logoutDeviceRequest(app.Router(), http.MethodPost, "/Sessions/Logout", token); code != http.StatusUnauthorized {
		t.Errorf("second POST /Sessions/Logout = %d, want 401", code)
	}
	if compositionSessionIDs(t, app.Router(), admin)[result.SessionInfo.ID] {
		t.Errorf("session %q is still listed after logout", result.SessionInfo.ID)
	}
	assertAuthenticatedStatus(t, app.Router(), http.MethodGet, "/Users/Me", nil, admin, http.StatusOK)
}

// TestSessionLogoutWithAPIKeyIsNoOp matches SessionManager.Logout(string): an
// API key has no device row, so logout leaves the key valid.
func TestSessionLogoutWithAPIKeyIsNoOp(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())
	admin := loginComposedAdministrator(t, app.Router())
	assertAuthenticatedStatus(t, app.Router(), http.MethodPost, "/Auth/Keys?app=Seerr", nil, admin, http.StatusNoContent)
	list := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Auth/Keys", nil, admin)
	var keys struct {
		Items []struct{ AccessToken string }
	}
	if err := json.Unmarshal(list.Body.Bytes(), &keys); err != nil || len(keys.Items) != 1 {
		t.Fatalf("decode keys: %v; body: %s", err, list.Body.String())
	}
	key := keys.Items[0].AccessToken

	withKey := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "10.0.0.5:51000"
		req.Header.Set("Authorization", `MediaBrowser Client="Seerr", Token="`+key+`"`)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := withKey(http.MethodPost, "/Sessions/Logout"); code != http.StatusNoContent {
		t.Fatalf("POST /Sessions/Logout with an API key = %d, want 204", code)
	}
	if code := withKey(http.MethodGet, "/Users"); code != http.StatusOK {
		t.Errorf("GET /Users with the key after logout = %d, want 200", code)
	}
	assertAuthenticatedStatus(t, app.Router(), http.MethodGet, "/Users/Me", nil, admin, http.StatusOK)
}

func logoutDeviceRequest(handler http.Handler, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:8096"
	req.Header.Set("Authorization", logoutDeviceAuthorization+`, Token="`+token+`"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

func compositionSessionIDs(t *testing.T, handler http.Handler, token string) map[string]bool {
	t.Helper()
	rec := authenticatedCompositionRequest(handler, http.MethodGet, "/Sessions", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Sessions = %d; body: %s", rec.Code, rec.Body.String())
	}
	var sessions []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	ids := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		ids[session.ID] = true
	}
	return ids
}
