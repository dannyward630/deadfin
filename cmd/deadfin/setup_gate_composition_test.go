// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
)

const setupTestToken = "0000000000000000000000000000000000000000000000000000000000000000"

func setupGateRequest(handler http.Handler, method, path, body, remote string, configure func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="setup-gate-device", Version="1.0"`)
	if configure != nil {
		configure(request)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestFreshApplicationConfinesFirstRunSetupToLocalPeersOrToken covers D-53:
// a remote peer of a fresh instance cannot read or set the first user, list
// the hidden administrator, or log in with its blank password.
func TestFreshApplicationConfinesFirstRunSetupToLocalPeersOrToken(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"), withSetupRandom(bytes.NewReader(make([]byte, 32))))
	handler := app.Router()
	const remote = "203.0.113.9:40000"
	userBody := `{"Name":"captain","Password":"correct horse battery staple"}`

	for _, step := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/Startup/User", ""},
		{http.MethodGet, "/Startup/Configuration", ""},
		{http.MethodPost, "/Startup/User", userBody},
		{http.MethodPost, "/Startup/Complete", ""},
		{http.MethodGet, "/Users/Public", ""},
		{http.MethodPost, "/Users/AuthenticateByName", `{"Username":"root","Pw":""}`},
		{http.MethodPost, "/QuickConnect/Initiate", ""},
	} {
		if got := setupGateRequest(handler, step.method, step.path, step.body, remote, nil).Code; got != http.StatusForbidden {
			t.Fatalf("remote %s %s status = %d, want 403", step.method, step.path, got)
		}
	}
	for _, path := range []string{"/System/Info/Public", "/System/Ping", "/Branding/Configuration", "/deadfin/v1/build"} {
		if got := setupGateRequest(handler, http.MethodGet, path, "", remote, nil).Code; got != http.StatusOK {
			t.Fatalf("remote GET %s status = %d, want 200", path, got)
		}
	}

	relayed := func(request *http.Request) { request.Header.Set("X-Forwarded-For", "192.168.1.20") }
	if got := setupGateRequest(handler, http.MethodGet, "/Startup/User", "", "127.0.0.1:8096", relayed).Code; got != http.StatusForbidden {
		t.Fatalf("proxied loopback GET /Startup/User status = %d, want 403", got)
	}
	if got := setupGateRequest(handler, http.MethodGet, "/Startup/User", "", "192.168.1.20:5000", nil).Code; got != http.StatusOK {
		t.Fatalf("direct LAN GET /Startup/User status = %d, want 200", got)
	}

	if got := setupGateRequest(handler, http.MethodGet, "/deadfin/v1/setup?token=wrong", "", remote, nil).Code; got != http.StatusForbidden {
		t.Fatalf("unlock with a wrong token status = %d, want 403", got)
	}
	unlock := setupGateRequest(handler, http.MethodGet, "/deadfin/v1/setup?token="+setupTestToken, "", remote, nil)
	if unlock.Code != http.StatusSeeOther || unlock.Header().Get("Location") != "../../web/" {
		t.Fatalf("unlock status = %d Location = %q, want 303 ../../web/", unlock.Code, unlock.Header().Get("Location"))
	}
	cookies := unlock.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != middleware.SetupCookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unlock cookies = %+v, want one HttpOnly SameSite=Strict %s cookie", cookies, middleware.SetupCookieName)
	}
	withCookie := func(request *http.Request) {
		relayed(request)
		request.AddCookie(cookies[0])
	}
	for _, step := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/Startup/User", "", http.StatusOK},
		{http.MethodPost, "/Startup/User", userBody, http.StatusNoContent},
		{http.MethodPost, "/Startup/Complete", "", http.StatusNoContent},
	} {
		if got := setupGateRequest(handler, step.method, step.path, step.body, remote, withCookie).Code; got != step.want {
			t.Fatalf("unlocked remote %s %s status = %d, want %d", step.method, step.path, got, step.want)
		}
	}

	// After setup the gate is inert: a remote peer gets ordinary authorization.
	if got := setupGateRequest(handler, http.MethodGet, "/Startup/User", "", remote, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("completed remote GET /Startup/User status = %d, want 401", got)
	}
	if got := setupGateRequest(handler, http.MethodGet, "/Users/Public", "", remote, nil).Code; got != http.StatusOK {
		t.Fatalf("completed remote GET /Users/Public status = %d, want 200", got)
	}
	if got := setupGateRequest(handler, http.MethodGet, "/deadfin/v1/setup?token="+setupTestToken, "", remote, nil).Code; got != http.StatusNotFound {
		t.Fatalf("completed unlock status = %d, want 404", got)
	}
}
