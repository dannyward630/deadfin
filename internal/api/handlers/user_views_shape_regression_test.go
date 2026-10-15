// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestUserViewsRetainsStockViewFactsWithoutGenericDetailOnlyFields keeps the
// UserViews-specific projection distinct from the generic detailed-item shape.
func TestUserViewsRetainsStockViewFactsWithoutGenericDetailOnlyFields(t *testing.T) {
	h := newP104Harness(t)
	user := h.createUser(t, "View User", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{"OrderedViews":[]}`))
	h.identities["views"] = auth.Identity{Authenticated: true, UserID: user.ID}

	items := store.NewItemStore(h.stores)
	folderID := p104UUID(t, "00000000-0000-0000-0000-000000000701")
	name, path := "Golden Movies", "/state/root/default/Golden%20Movies"
	collection := item.CollectionTypeMovies
	created := time.Date(2026, time.July, 27, 18, 41, 53, 0, time.UTC)
	lastMediaAdded := time.Date(2026, time.July, 27, 18, 42, 7, 0, time.UTC)
	folder := item.Item{
		ID: folderID, Kind: item.KindCollectionFolder, IsFolder: true,
		Name: &name, SortName: &name, Path: &path, DateCreated: &created, DateLastMediaAdded: &lastMediaAdded,
		Values: item.ItemValues{ProductionLocations: []string{"must not leak from generic detail defaults"}},
		Extra:  item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &collection}},
	}
	for _, value := range []item.Item{
		folder,
		{ID: p104UUID(t, "00000000-0000-0000-0000-000000000702"), Kind: item.KindMovie, IsMovie: true, ParentID: &folderID},
		{ID: p104UUID(t, "00000000-0000-0000-0000-000000000703"), Kind: item.KindMovie, IsMovie: true, ParentID: &folderID},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert UserViews fixture %s: %v", value.ID, err)
		}
	}

	handlers.RegisterUserViews(h.rt, library.NewViews(items, h.users), dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, h.users, nil), h.identity.ID.String()))

	for _, profile := range []struct {
		name         string
		accept       string
		itemsKey     string
		pathKey      string
		createdKey   string
		lastMediaKey string
		etagKey      string
		childKey     string
		forbidden    []string
	}{
		{name: "Pascal", accept: "application/json", itemsKey: "Items", pathKey: "Path", createdKey: "DateCreated", lastMediaKey: "DateLastMediaAdded", etagKey: "Etag", childKey: "ChildCount", forbidden: []string{"ProductionLocations", "RecursiveItemCount", "Trickplay"}},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", pathKey: "path", createdKey: "dateCreated", lastMediaKey: "dateLastMediaAdded", etagKey: "etag", childKey: "childCount", forbidden: []string{"productionLocations", "recursiveItemCount", "trickplay"}},
	} {
		t.Run(profile.name, func(t *testing.T) {
			for _, endpoint := range []string{"/UserViews", "/Users/" + user.ID.Dashed() + "/Views"} {
				rec := p104Call(h.rt, http.MethodGet, endpoint, "", "", profile.accept, "views", p104MediaBrowserMetadata)
				assertStatus(t, rec, http.StatusOK)
				page := p104RawObject(t, rec.Body.Bytes())
				rows := p104RawArray(t, page[profile.itemsKey])
				if len(rows) != 1 {
					t.Fatalf("%s %s rows = %d, want exactly one persisted UserView", profile.name, endpoint, len(rows))
				}
				view := p104RawObject(t, rows[0])
				if got := p104String(t, view[profile.pathKey]); got != path {
					t.Fatalf("%s %s Path = %q, want persisted UserView path %q", profile.name, endpoint, got, path)
				}
				if got := p104String(t, view[profile.createdKey]); got != created.Format(time.RFC3339) {
					t.Fatalf("%s %s DateCreated = %q, want persisted lifecycle value %q", profile.name, endpoint, got, created.Format(time.RFC3339))
				}
				if got, want := p104String(t, view[profile.lastMediaKey]), "2026-07-27T18:42:07.0000000Z"; got != want {
					t.Fatalf("%s %s DateLastMediaAdded = %q, want System.Text.Json lifecycle value %q", profile.name, endpoint, got, want)
				}
				if got := p104String(t, view[profile.etagKey]); got == "" {
					t.Fatalf("%s %s Etag must be present and nonempty", profile.name, endpoint)
				}
				if got := string(view[profile.childKey]); len(got) != 1 || got[0] < '1' || got[0] > '9' {
					t.Fatalf("%s %s ChildCount = %s, want upstream-valid UserViews sentinel in [1,9]", profile.name, endpoint, got)
				}
				for _, field := range profile.forbidden {
					if value, found := view[field]; found {
						t.Fatalf("%s %s unexpectedly emitted generic detail-only %s=%s", profile.name, endpoint, field, value)
					}
				}
			}
		})
	}
}
