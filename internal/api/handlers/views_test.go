// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestUserViewsRoutesUseDetailedPersistedProjection protects the real-store
// modern and legacy endpoints from regressing to the former sparse route DTO.
func TestUserViewsRoutesUseDetailedPersistedProjection(t *testing.T) {
	h := newP104Harness(t)
	user := h.createUser(t, "View User", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{"OrderedViews":[]}`))
	h.identities["views"] = auth.Identity{Authenticated: true, UserID: user.ID}
	items := store.NewItemStore(h.stores)
	folderID := p104UUID(t, "00000000-0000-0000-0000-000000000201")
	name, path := "Movies", "/library/movies"
	collection := item.CollectionTypeMovies
	folder := item.Item{
		ID: folderID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &name, SortName: &name, Path: &path,
		Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &collection}},
	}
	if err := items.Upsert(t.Context(), folder); err != nil {
		t.Fatalf("upsert persisted view folder: %v", err)
	}
	views := library.NewViews(items, h.users)
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, h.users, nil), h.identity.ID.String())
	handlers.RegisterUserViews(h.rt, views, builder)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/UserViews", OperationID: "GetUserViews", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Views", OperationID: "SHADOW:GetUserViewsLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/UserViews/GroupingOptions", OperationID: "GetGroupingOptions", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/GroupingOptions", OperationID: "SHADOW:GetGroupingOptionsLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		got, found := routeByOperation(h.rt.Routes(), want.OperationID)
		if !found || got != want {
			t.Fatalf("route %q = %#v, found %t, want %#v", want.OperationID, got, found, want)
		}
	}

	var modern map[string]json.RawMessage
	for _, endpoint := range []string{"/UserViews", "/Users/" + user.ID.Dashed() + "/Views"} {
		rec := p104Call(h.rt, http.MethodGet, endpoint, "", "", `application/json; profile="CamelCase"`, "views", p104MediaBrowserMetadata)
		assertStatus(t, rec, http.StatusOK)
		body := p104RawObject(t, rec.Body.Bytes())
		if endpoint == "/UserViews" {
			modern = body
		} else if !reflect.DeepEqual(body, modern) {
			t.Fatalf("modern and legacy UserViews bodies differ:\nmodern=%s\nlegacy=%s", modern, body)
		}
		rows := p104RawArray(t, body["items"])
		if len(rows) != 1 {
			t.Fatalf("%s item count = %d, want 1", endpoint, len(rows))
		}
		view := p104RawObject(t, rows[0])
		if got := p104String(t, view["id"]); got != folderID.String() {
			t.Fatalf("%s item ID = %q, want %s", endpoint, got, folderID)
		}
		if got := p104String(t, view["displayPreferencesId"]); got != folderID.String() {
			t.Fatalf("%s DisplayPreferencesId = %q, want concrete row ID %s", endpoint, got, folderID)
		}
		for field, want := range map[string]string{"name": "Movies", "type": "CollectionFolder", "collectionType": "movies", "path": path, "locationType": "FileSystem", "mediaType": "Unknown"} {
			if got := p104String(t, view[field]); got != want {
				t.Fatalf("%s %s = %q, want %q", endpoint, field, got, want)
			}
		}
		for _, field := range []string{"canDelete", "canDownload", "enableMediaSourceDisplay", "isFolder", "lockData", "userData"} {
			if _, exists := view[field]; !exists {
				t.Fatalf("%s omitted detailed field %q: %s", endpoint, field, rec.Body.String())
			}
		}
		data := p104RawObject(t, view["userData"])
		if got := p104String(t, data["itemId"]); got != folderID.String() {
			t.Fatalf("%s UserData.ItemId = %q, want %s", endpoint, got, folderID)
		}
	}

	for _, endpoint := range []string{"/UserViews/GroupingOptions", "/Users/" + user.ID.Dashed() + "/GroupingOptions"} {
		rec := p104Call(h.rt, http.MethodGet, endpoint, "", "", `application/json; profile="CamelCase"`, "views", p104MediaBrowserMetadata)
		assertStatus(t, rec, http.StatusOK)
		options := p104RawArray(t, rec.Body.Bytes())
		if len(options) != 1 || p104String(t, p104RawObject(t, options[0])["id"]) != folderID.String() {
			t.Fatalf("%s grouping response = %s, want concrete CollectionFolder ID %s", endpoint, rec.Body.String(), folderID)
		}
	}
}

func TestUserViewsHandlersResolveModernAndLegacyUsersByPolicy(t *testing.T) {
	h := newP104Harness(t)
	user := h.createUser(t, "View User", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	otherUser := h.createUser(t, "Other User", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	h.identities["views"] = auth.Identity{Authenticated: true, UserID: user.ID}
	h.identities["admin"] = auth.Identity{Authenticated: true, Administrator: true, UserID: user.ID}
	items := store.NewItemStore(h.stores)
	handlers.RegisterUserViews(h.rt, library.NewViews(items, h.users), dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, h.users, nil), h.identity.ID.String()))
	missing := p104UUID(t, "00000000-0000-0000-0000-000000000202")
	for _, endpoint := range []string{
		"/UserViews?userId=" + otherUser.ID.Dashed(),
		"/Users/" + otherUser.ID.Dashed() + "/Views",
		"/UserViews/GroupingOptions?userId=" + otherUser.ID.Dashed(),
		"/Users/" + otherUser.ID.Dashed() + "/GroupingOptions",
	} {
		assertStatus(t, p104Call(h.rt, http.MethodGet, endpoint, "", "", "", "views", p104MediaBrowserMetadata), http.StatusForbidden)
		assertStatus(t, p104Call(h.rt, http.MethodGet, endpoint, "", "", "", "admin", p104MediaBrowserMetadata), http.StatusOK)
	}
	for _, endpoint := range []string{
		"/UserViews?userId=" + missing.Dashed(),
		"/Users/" + missing.Dashed() + "/Views",
		"/UserViews/GroupingOptions?userId=" + missing.Dashed(),
		"/Users/" + missing.Dashed() + "/GroupingOptions",
	} {
		assertStatus(t, p104Call(h.rt, http.MethodGet, endpoint, "", "", "", "admin", p104MediaBrowserMetadata), http.StatusNotFound)
	}
	assertStatus(t, p104Call(h.rt, http.MethodGet, "/UserViews?userId=00000000-0000-0000-0000-000000000000", "", "", "", "views", p104MediaBrowserMetadata), http.StatusOK)
	assertStatus(t, p104Call(h.rt, http.MethodGet, "/UserViews?includeHidden=not-bool", "", "", "", "views", p104MediaBrowserMetadata), http.StatusBadRequest)
}
