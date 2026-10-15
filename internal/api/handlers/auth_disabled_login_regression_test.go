// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/session"
)

// Stock corpus: corpora/login.yaml's GoldenrecDisabled case. The anonymous
// password endpoint must not disclose that valid credentials belong to a
// disabled account: both failed attempts have the same opaque 401 response.
func TestAuthenticateByNameDisabledAccountDoesNotDiscloseAccountState(t *testing.T) {
	h := newP104Harness(t)
	disabled := h.createUser(t, "GoldenrecDisabled", "goldenrec-disabled-password", json.RawMessage(`{"IsDisabled":true}`), json.RawMessage(`{}`))
	enabled := h.createUser(t, "GoldenrecEnabled", "goldenrec-enabled-password", json.RawMessage(`{"IsDisabled":false}`), json.RawMessage(`{}`))

	login := func(username, password string) *httptest.ResponseRecorder {
		t.Helper()
		return p104Call(h.rt, http.MethodPost, "/Users/AuthenticateByName", `{"Username":"`+username+`","Pw":"`+password+`"}`, "application/json", "", "", p104MediaBrowserMetadata)
	}

	wrongPassword := login(disabled.Name, "goldenrec-wrong-password")
	validDisabledPassword := login(disabled.Name, "goldenrec-disabled-password")
	for _, response := range []*httptest.ResponseRecorder{wrongPassword, validDisabledPassword} {
		assertStatus(t, response, http.StatusUnauthorized)
		if got := response.Header().Get("Content-Type"); got != "text/plain" {
			t.Fatalf("Content-Type = %q, want stock text/plain", got)
		}
		if got := response.Body.String(); got != "Error processing request." {
			t.Fatalf("error body = %q, want exact stock body %q", got, "Error processing request.")
		}
	}
	if got, want := validDisabledPassword.Body.String(), wrongPassword.Body.String(); got != want {
		t.Fatalf("disabled valid-password body = %q, wrong-password body = %q; account state must not be disclosed", got, want)
	}
	if sessions := h.sessions.Sessions(session.Query{DeviceID: "browser-id"}); len(sessions) != 0 {
		t.Fatalf("sessions after rejected logins = %#v, want none", sessions)
	}

	enabledLogin := login(enabled.Name, "goldenrec-enabled-password")
	assertStatus(t, enabledLogin, http.StatusOK)

	// This is an authenticated request that route policy rejects. It must retain
	// 403; the anonymous login endpoint's non-disclosure rule is narrower.
	h.identities["forbidden"] = auth.Identity{Authenticated: true, Token: "deny", UserID: enabled.ID, DeviceID: "browser-id", Client: "web"}
	protected := p104Call(h.rt, http.MethodGet, "/Users/Me", "", "", "", "forbidden", "")
	assertStatus(t, protected, http.StatusForbidden)
}
