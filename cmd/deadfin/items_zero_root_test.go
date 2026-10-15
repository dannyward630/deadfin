// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Source: upstream/jellyfin/Jellyfin.Api/Controllers/UserLibraryController.cs:78-103
// (Guid.Empty item selects the persisted UserRootFolder) and
// Helpers/RequestHelpers.cs:67-85 (Guid.Empty user selects caller; cross-user needs admin).
func TestApplicationGetZeroItemResolvesPersistedUserRootWithAuthorizedUserOverlay(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())

	adminToken := loginComposedAdministrator(t, app.Router())
	admin, err := app.UserService().GetByName(t.Context(), "captain")
	if err != nil {
		t.Fatalf("load configured administrator: %v", err)
	}
	member, err := app.UserService().Create(t.Context(), "library-member", "member password")
	if err != nil {
		t.Fatalf("create ordinary member: %v", err)
	}
	memberToken := issueZeroRootMemberToken(t, app, member.ID)

	items := store.NewItemStore(app.stores)
	roots, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindUserRootFolder}})
	if err != nil {
		t.Fatalf("query bootstrapped persisted user root: %v", err)
	}
	if len(roots.Items) != 1 {
		t.Fatalf("bootstrapped UserRootFolder count = %d, want 1", len(roots.Items))
	}
	rootID := roots.Items[0].ID
	seedZeroRootUserData(t, items, admin.ID, rootID, false, 2, false)
	seedZeroRootUserData(t, items, member.ID, rootID, true, 7, true)

	zero := dto.NilUUID.Dashed()
	assertZeroRootItem(t, app, memberToken, "/Items/"+zero+"?enableUserData=true", rootID, true, 7, true)
	assertZeroRootItem(t, app, adminToken, "/Items/"+zero+"?userId="+member.ID.Dashed()+"&enableUserData=true", rootID, true, 7, true)
	assertZeroRootItem(t, app, adminToken, "/Users/"+member.ID.Dashed()+"/Items/"+zero+"?enableUserData=true", rootID, true, 7, true)

	for _, path := range []string{
		"/Items/" + zero + "?userId=" + admin.ID.Dashed(),
		"/Users/" + admin.ID.Dashed() + "/Items/" + zero,
	} {
		assertZeroRootStatus(t, app, memberToken, path, http.StatusForbidden)
	}

	missingUser := dto.UUID{0x94, 0x64, 0xa0, 0x13, 0x2f, 0x75, 0x4d, 0xfa, 0x83, 0x24, 0xdd, 0x3e, 0x7e, 0xb9, 0x71, 0x1a}
	for _, path := range []string{
		"/Items/" + zero + "?userId=" + missingUser.Dashed(),
		"/Users/" + missingUser.Dashed() + "/Items/" + zero,
	} {
		assertZeroRootStatus(t, app, adminToken, path, http.StatusNotFound)
	}

	missingItem := dto.UUID{0xf8, 0x0f, 0x0a, 0xc8, 0xcb, 0x05, 0x47, 0xb8, 0xa0, 0xc1, 0x26, 0x6c, 0x53, 0x95, 0x3c, 0x7a}
	assertZeroRootStatus(t, app, memberToken, "/Items/"+missingItem.Dashed()+"?enableUserData=true", http.StatusNotFound)
}

func issueZeroRootMemberToken(t *testing.T, app *application, userID dto.UUID) string {
	t.Helper()

	device, err := app.UserService().IssueToken(t.Context(), store.Device{
		UserID:     userID,
		AppName:    "zero-root-test",
		AppVersion: "1.0",
		DeviceName: "zero-root-member",
		DeviceID:   "zero-root-member-device",
	})
	if err != nil {
		t.Fatalf("issue ordinary-member token: %v", err)
	}
	if device.AccessToken == "" {
		t.Fatal("issued ordinary-member token is empty")
	}
	return device.AccessToken
}

func seedZeroRootUserData(t *testing.T, items *store.ItemStore, userID, rootID dto.UUID, played bool, count int, favorite bool) {
	t.Helper()

	if err := items.UpsertUserData(t.Context(), store.UserData{
		UserID:     userID,
		CustomKey:  rootID.Dashed(),
		ItemID:     rootID,
		Played:     played,
		PlayCount:  count,
		IsFavorite: favorite,
	}); err != nil {
		t.Fatalf("persist user overlay for root %s: %v", rootID, err)
	}
}

func assertZeroRootItem(t *testing.T, app *application, token, path string, wantID dto.UUID, played bool, count int, favorite bool) {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body: %s", path, record.Code, record.Body.String())
	}
	var response dto.BaseItemDto
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET %s item response: %v; body: %s", path, err, record.Body.String())
	}
	if response.ID != wantID || response.Type != dto.Kind(item.KindUserRootFolder.String()) {
		t.Fatalf("GET %s root projection = ID %s Type %q, want persisted root ID %s Type UserRootFolder", path, response.ID, response.Type, wantID)
	}
	if response.UserData == nil {
		t.Fatalf("GET %s UserData = nil, want current target's persisted overlay", path)
	}
	if response.UserData.Played != played || response.UserData.PlayCount != count || response.UserData.IsFavorite != favorite {
		t.Fatalf("GET %s UserData = %#v, want played=%t count=%d favorite=%t", path, response.UserData, played, count, favorite)
	}
}

func assertZeroRootStatus(t *testing.T, app *application, token, path string, want int) {
	t.Helper()

	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if record.Code != want {
		t.Fatalf("GET %s status = %d, want %d; body: %s", path, record.Code, want, record.Body.String())
	}
}
