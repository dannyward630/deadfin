// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
)

// A non-administrator can report capabilities only for its own sessions.
// Another user's session ID gets the unknown-session 404 and the target does
// not change; an administrator keeps the pinned behavior (divergence D-67).
func TestSessionCapabilitiesRejectAnotherUsersSessionForNonAdministrator(t *testing.T) {
	h := newP104Harness(t)
	admin := h.createUser(t, "Capability administrator", "admin password", json.RawMessage(`{"IsAdministrator":true,"EnableAllDevices":true}`), json.RawMessage(`{}`))
	alice := h.createUser(t, "Capability Alice", "alice password", json.RawMessage(`{"IsAdministrator":false,"EnableAllDevices":true}`), json.RawMessage(`{}`))
	bob := h.createUser(t, "Capability Bob", "bob password", json.RawMessage(`{"IsAdministrator":false,"EnableAllDevices":true}`), json.RawMessage(`{}`))
	h.identities["cap-admin"] = auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Web", DeviceID: "admin-browser", Version: "12"}
	h.identities["cap-alice"] = auth.Identity{Authenticated: true, UserID: alice.ID, Client: "Web", DeviceID: "alice-browser", Version: "12"}

	logSession := func(client, device string, userID dto.UUID) session.Snapshot {
		t.Helper()
		snapshot, err := h.sessions.LogActivity(t.Context(), session.Activity{Client: client, Version: "12", DeviceID: device, DeviceName: device, UserID: userID})
		if err != nil {
			t.Fatalf("create session %s: %v", device, err)
		}
		return snapshot
	}
	aliceCurrent := logSession("Web", "alice-browser", alice.ID)
	aliceOther := logSession("Swiftfin", "alice-tv", alice.ID)
	bobSession := logSession("Swiftfin", "bob-tv", bob.ID)
	bobID := bobSession.ID
	if err := h.sessions.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &bobID, Capabilities: session.Capabilities{SupportsMediaControl: true, PlayableMediaTypes: []string{"Video"}}}); err != nil {
		t.Fatalf("seed bob capabilities: %v", err)
	}
	capabilities := func(id string) session.Capabilities {
		t.Helper()
		return p104Session(t, h.sessions.Sessions(session.Query{}), id).Capabilities
	}

	// Own sessions: the implicit current session and an explicit ID.
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/Sessions/Capabilities/Full", `{"PlayableMediaTypes":["Audio"],"SupportsMediaControl":true}`},
		{http.MethodPost, "/Sessions/Capabilities/Full?Id=" + aliceOther.ID, `{"PlayableMediaTypes":["Audio"],"SupportsMediaControl":true}`},
		{http.MethodPost, "/Sessions/Capabilities?PlayableMediaTypes=Audio&SupportsMediaControl=true&Id=" + aliceOther.ID, ""},
	} {
		response := p104Call(h.rt, call.method, call.path, call.body, "application/json", "", "cap-alice", "")
		assertStatus(t, response, http.StatusNoContent)
	}
	for _, id := range []string{aliceCurrent.ID, aliceOther.ID} {
		if got := capabilities(id); !got.SupportsMediaControl || !sameP104Strings(got.PlayableMediaTypes, []string{"Audio"}) {
			t.Fatalf("own session %s capabilities = %#v, want Audio with media control", id, got)
		}
	}

	// Another user's session: unknown-session response, state unchanged.
	for _, call := range []struct{ path, body string }{
		{"/Sessions/Capabilities/Full?Id=" + bobSession.ID, `{"PlayableMediaTypes":["Photo"],"SupportsMediaControl":false}`},
		{"/Sessions/Capabilities?PlayableMediaTypes=Photo&SupportsMediaControl=false&Id=" + bobSession.ID, ""},
	} {
		response := p104Call(h.rt, http.MethodPost, call.path, call.body, "application/json", "", "cap-alice", "")
		assertStatus(t, response, http.StatusNotFound)
	}
	if got := capabilities(bobSession.ID); !got.SupportsMediaControl || !sameP104Strings(got.PlayableMediaTypes, []string{"Video"}) {
		t.Fatalf("bob capabilities after non-admin report = %#v, want unchanged Video with media control", got)
	}

	// Administrator: any session, as upstream.
	for _, call := range []struct{ path, body string }{
		{"/Sessions/Capabilities/Full?Id=" + bobSession.ID, `{"PlayableMediaTypes":["Photo"],"SupportsMediaControl":false}`},
		{"/Sessions/Capabilities?PlayableMediaTypes=Book&SupportsMediaControl=false&Id=" + aliceOther.ID, ""},
	} {
		response := p104Call(h.rt, http.MethodPost, call.path, call.body, "application/json", "", "cap-admin", "")
		assertStatus(t, response, http.StatusNoContent)
	}
	if got := capabilities(bobSession.ID); got.SupportsMediaControl || !sameP104Strings(got.PlayableMediaTypes, []string{"Photo"}) {
		t.Fatalf("bob capabilities after admin report = %#v, want Photo without media control", got)
	}
	if got := capabilities(aliceOther.ID); got.SupportsMediaControl || !sameP104Strings(got.PlayableMediaTypes, []string{"Book"}) {
		t.Fatalf("alice capabilities after admin report = %#v, want Book without media control", got)
	}
}

func sameP104Strings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
