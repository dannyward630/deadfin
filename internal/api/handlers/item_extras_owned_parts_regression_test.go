// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestAdditionalPartsRealHandlerIncludesOwnedStackRows proves the public
// Video.GetAdditionalParts route traverses scanner-owned stack rows through the
// same access-filtered store reader as every other item lookup. The rows are
// intentionally ordinary Movies with OwnerID rather than alternate versions.
func TestAdditionalPartsRealHandlerIncludesOwnedStackRows(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open owned additional-parts store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close owned additional-parts store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, rootID, primaryID := itemsHandlerUUID(t, 960), itemsHandlerUUID(t, 961), itemsHandlerUUID(t, 962)
	allowedPathA, allowedPathZ, blockedPath := "/media/part-a.mkv", "/media/part-z.mkv", "/media/part-blocked.mkv"
	policy := `{"EnableAllFolders":false,"EnabledFolders":["` + rootID.String() + `"],"AllowedTags":["allowed"],"BlockedTags":["blocked"]}`
	user := store.User{ID: userID, Policy: []byte(policy)}
	root := item.Item{ID: rootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("visible root"), SortName: itemsHandlerString("visible root")}
	primary := item.Item{
		ID:              primaryID,
		Kind:            item.KindMovie,
		ParentID:        &rootID,
		Name:            itemsHandlerString("stack primary"),
		SortName:        itemsHandlerString("stack primary"),
		Values:          item.ItemValues{Tags: []string{"allowed"}},
		AdditionalParts: []string{allowedPathZ, blockedPath, allowedPathA},
	}
	partAID, partZID, blockedID := itemsHandlerUUID(t, 963), itemsHandlerUUID(t, 964), itemsHandlerUUID(t, 965)
	parts := []item.Item{
		{ID: partAID, Kind: item.KindMovie, ParentID: &rootID, OwnerID: &primaryID, Path: &allowedPathA, Name: itemsHandlerString("part a"), SortName: itemsHandlerString("a part"), Values: item.ItemValues{Tags: []string{"allowed"}}},
		{ID: partZID, Kind: item.KindMovie, ParentID: &rootID, OwnerID: &primaryID, Path: &allowedPathZ, Name: itemsHandlerString("part z"), SortName: itemsHandlerString("z part"), Values: item.ItemValues{Tags: []string{"allowed"}}},
		{ID: blockedID, Kind: item.KindMovie, ParentID: &rootID, OwnerID: &primaryID, Path: &blockedPath, Name: itemsHandlerString("blocked part"), SortName: itemsHandlerString("b part"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}},
	}
	for _, value := range append([]item.Item{root, primary}, parts...) {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed owned additional-part %s: %v", value.ID, err)
		}
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItemExtras(routes, reader, itemsAccessBuilder{}, users)
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Videos/"+primaryID.Dashed()+"/AdditionalParts?userId="+userID.Dashed(), nil)
	request.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET AdditionalParts = %d, want 200: %s", record.Code, record.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode AdditionalParts response: %v; body=%s", err, record.Body.String())
	}
	if got, want := itemExtrasDTOIDs(page.Items), []dto.UUID{partAID, partZID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("access-filtered owned parts = %v, want stable SortName order %v", got, want)
	}

	pathReads := make(map[string]store.ItemQuery)
	for _, query := range reader.queries {
		if query.Path != "" {
			pathReads[query.Path] = query
		}
	}
	for _, path := range []string{allowedPathA, allowedPathZ, blockedPath} {
		query, found := pathReads[path]
		if !found || !query.IncludeOwnedItems || query.UserID == nil || *query.UserID != userID || !reflect.DeepEqual(query.User, &user) {
			t.Fatalf("owned additional-part lookup %q = %#v, want IncludeOwnedItems and resolved access user", path, query)
		}
	}
	for _, part := range parts {
		stored, err := items.Get(t.Context(), part.ID)
		if err != nil {
			t.Fatalf("load owned stack row %s: %v", part.ID, err)
		}
		if stored.OwnerID == nil || *stored.OwnerID != primaryID || stored.PrimaryVersionID != nil || len(stored.LinkedChildren) != 0 {
			t.Fatalf("stored owned stack row = %#v, want owner-only stack topology", stored)
		}
	}
}
