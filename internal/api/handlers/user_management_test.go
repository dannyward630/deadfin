// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// User management ports UserController.CreateUserByName, DeleteUser, and
// UpdateUser. The harness evaluator admits every policy; the route
// test pins RequiresElevation, which the real evaluator enforces.
func TestUserManagementRoutesAreRegisteredWithUpstreamPolicies(t *testing.T) {
	h := newP104Harness(t)
	for _, want := range []router.Route{
		{Method: http.MethodPost, Pattern: "/Users/New", OperationID: "CreateUserByName", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodDelete, Pattern: "/Users/{userId}", OperationID: "DeleteUser", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Users", OperationID: "UpdateUser", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Users/{userId}", OperationID: "SHADOW:UpdateUserLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		got, found := routeByOperation(h.rt.Routes(), want.OperationID)
		if !found || got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy {
			t.Errorf("route %q = %#v (found %t), want %#v", want.OperationID, got, found, want)
		}
	}
}

func managementHarness(t *testing.T) (*p104Harness, store.User, store.User) {
	t.Helper()
	h := newP104Harness(t)
	admin := h.createUser(t, "Admin", "admin password", json.RawMessage(`{"IsAdministrator":true,"IsHidden":false}`), json.RawMessage(`{"GroupedFolders":[]}`))
	member := h.createUser(t, "Member", "member password", json.RawMessage(`{"IsHidden":false,"EnableUserPreferenceAccess":true}`), json.RawMessage(`{"GroupedFolders":[]}`))
	h.identities["admin"] = auth.Identity{Authenticated: true, Administrator: true, Token: "admin-token", UserID: admin.ID, DeviceID: "admin-device", Client: "web"}
	h.identities["member"] = auth.Identity{Authenticated: true, Token: "member-token", UserID: member.ID, DeviceID: "member-device", Client: "web"}
	return h, admin, member
}

func TestCreateUserByNameCreatesAUserWithPassword(t *testing.T) {
	h, _, _ := managementHarness(t)

	created := p104Call(h.rt, http.MethodPost, "/Users/New", `{"Name":"Sadie","Password":"secret"}`, "application/json", "", "admin", "")
	assertStatus(t, created, http.StatusOK)
	body := p104Object(t, created.Body.Bytes())
	if got := p104String(t, body["Name"]); got != "Sadie" {
		t.Fatalf("created Name = %q, want Sadie", got)
	}
	id, err := dto.ParseUUID(p104String(t, body["Id"]))
	if err != nil {
		t.Fatalf("created Id: %v", err)
	}
	if verified, err := h.users.VerifyPassword(t.Context(), id, "secret"); err != nil || !verified {
		t.Errorf("new user password verified=%t err=%v", verified, err)
	}

	// Without a password the user exists with none, as upstream does.
	open := p104Call(h.rt, http.MethodPost, "/Users/New", `{"Name":"Guest"}`, "application/json", "", "admin", "")
	assertStatus(t, open, http.StatusOK)

	for name, tc := range map[string]struct {
		body, identity string
		status         int
	}{
		"duplicate name (case-insensitive)": {`{"Name":"sadie"}`, "admin", http.StatusBadRequest},
		"invalid name":                      {`{"Name":""}`, "admin", http.StatusBadRequest},
		"missing name":                      {`{}`, "admin", http.StatusBadRequest},
	} {
		if got := p104Call(h.rt, http.MethodPost, "/Users/New", tc.body, "application/json", "", tc.identity, ""); got.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, got.Code, tc.status)
		}
	}
}

func TestDeleteUserRemovesUserAndKeepsTheLastAdministrator(t *testing.T) {
	h, admin, member := managementHarness(t)

	deleted := p104Call(h.rt, http.MethodDelete, "/Users/"+member.ID.Dashed(), "", "", "", "admin", "")
	assertStatus(t, deleted, http.StatusNoContent)
	if _, err := h.users.Get(t.Context(), member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted user lookup err = %v, want not found", err)
	}
	if got := p104Call(h.rt, http.MethodDelete, "/Users/"+member.ID.Dashed(), "", "", "", "admin", ""); got.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", got.Code)
	}

	// Another member makes Admin the last administrator but not the last user.
	h.createUser(t, "Second", "pw", json.RawMessage(`{"IsHidden":false}`), json.RawMessage(`{}`))
	if got := p104Call(h.rt, http.MethodDelete, "/Users/"+admin.ID.Dashed(), "", "", "", "admin", ""); got.Code != http.StatusBadRequest {
		t.Errorf("last administrator delete status = %d, want 400", got.Code)
	}
	if _, err := h.users.Get(t.Context(), admin.ID); err != nil {
		t.Errorf("last administrator was deleted: %v", err)
	}
}

func TestUpdateUserRenamesAndReplacesConfiguration(t *testing.T) {
	h, admin, member := managementHarness(t)

	renamed := p104Call(h.rt, http.MethodPost, "/Users?userId="+member.ID.Dashed(), `{"Name":"Nana","Configuration":{"AudioLanguagePreference":"ja","GroupedFolders":[]}}`, "application/json", "", "admin", "")
	assertStatus(t, renamed, http.StatusNoContent)
	got := p104Object(t, p104Call(h.rt, http.MethodGet, "/Users/"+member.ID.Dashed(), "", "", "", "member", "").Body.Bytes())
	if p104String(t, got["Name"]) != "Nana" || p104String(t, p104RawObject(t, got["Configuration"])["AudioLanguagePreference"]) != "ja" {
		t.Errorf("after update = %v", got)
	}

	// A user with preference access updates itself through the legacy route.
	self := p104Call(h.rt, http.MethodPost, "/Users/"+member.ID.Dashed(), `{"Name":"Nana","Configuration":{"AudioLanguagePreference":"de","GroupedFolders":[]}}`, "application/json", "", "member", "")
	assertStatus(t, self, http.StatusNoContent)

	for name, tc := range map[string]struct {
		path, body, identity string
		status               int
	}{
		"member updating another user": {"/Users?userId=" + admin.ID.Dashed(), `{"Name":"Admin","Configuration":{}}`, "member", http.StatusForbidden},
		"rename to an existing name":   {"/Users?userId=" + member.ID.Dashed(), `{"Name":"admin","Configuration":{}}`, "admin", http.StatusBadRequest},
		"missing name":                 {"/Users?userId=" + member.ID.Dashed(), `{"Configuration":{}}`, "admin", http.StatusBadRequest},
		"missing configuration":        {"/Users?userId=" + member.ID.Dashed(), `{"Name":"Nana"}`, "admin", http.StatusBadRequest},
		"unknown user":                 {"/Users?userId=" + dto.UUID{1}.Dashed(), `{"Name":"X","Configuration":{}}`, "admin", http.StatusNotFound},
		"unauthenticated":              {"/Users", `{"Name":"X","Configuration":{}}`, "", http.StatusUnauthorized},
	} {
		if got := p104Call(h.rt, http.MethodPost, tc.path, tc.body, "application/json", "", tc.identity, ""); got.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, got.Code, tc.status)
		}
	}
	if account, _ := h.users.Get(t.Context(), member.ID); account.Name != "Nana" {
		t.Errorf("a rejected update renamed the user to %q", account.Name)
	}
}
