// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestUserViewsUseStableStockChildCountSentinels keeps the controller-specific
// DtoService behavior out of generic folder projection. A UserView reports a
// deterministic upstream-valid 1..9 sentinel even when the actual inventory is
// outside that range; an ordinary Folder still exposes its real direct count.
func TestUserViewsUseStableStockChildCountSentinels(t *testing.T) {
	h := newP104Harness(t)
	user := h.createUser(t, "View User", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{"OrderedViews":[]}`))
	h.identities["views"] = auth.Identity{Authenticated: true, UserID: user.ID}
	items := store.NewItemStore(h.stores)

	moviesID := p104UUID(t, "00000000-0000-0000-0000-000000008401")
	tvID := p104UUID(t, "00000000-0000-0000-0000-000000008402")
	ordinaryID := p104UUID(t, "00000000-0000-0000-0000-000000008403")
	moviesName, tvName, ordinaryName := "Many Movies", "Empty TV", "Ordinary Folder"
	moviesType, tvType := item.CollectionTypeMovies, item.CollectionTypeTVShows
	for _, value := range []item.Item{
		{ID: moviesID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &moviesName, SortName: &moviesName, Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &moviesType}}},
		{ID: tvID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &tvName, SortName: &tvName, Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &tvType}}},
		{ID: ordinaryID, Kind: item.KindFolder, IsFolder: true, Name: &ordinaryName, SortName: &ordinaryName},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert UserViews parent %s: %v", value.ID, err)
		}
	}
	for index := range 12 {
		childID := p104UUID(t, fmt.Sprintf("00000000-0000-0000-0000-%012d", 9_000+index))
		if err := items.Upsert(t.Context(), item.Item{ID: childID, Kind: item.KindMovie, IsMovie: true, ParentID: &moviesID}); err != nil {
			t.Fatalf("upsert collection child %d: %v", index, err)
		}
	}
	for index := range 11 {
		childID := p104UUID(t, fmt.Sprintf("00000000-0000-0000-0000-%012d", 9_100+index))
		if err := items.Upsert(t.Context(), item.Item{ID: childID, Kind: item.KindMovie, IsMovie: true, ParentID: &ordinaryID}); err != nil {
			t.Fatalf("upsert ordinary child %d: %v", index, err)
		}
	}

	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, h.users, nil), h.identity.ID.String())
	handlers.RegisterUserViews(h.rt, library.NewViews(items, h.users), builder)

	viewCounts := func() map[string]int {
		t.Helper()
		record := p104Call(h.rt, http.MethodGet, "/UserViews", "", "", "application/json", "views", p104MediaBrowserMetadata)
		assertStatus(t, record, http.StatusOK)
		page := p104RawObject(t, record.Body.Bytes())
		rows := p104RawArray(t, page["Items"])
		counts := make(map[string]int, len(rows))
		for _, raw := range rows {
			view := p104RawObject(t, raw)
			id := p104String(t, view["Id"])
			countRaw, found := view["ChildCount"]
			if !found {
				t.Fatalf("UserView %s omitted ChildCount: %s", id, record.Body.String())
			}
			count, err := strconv.Atoi(string(countRaw))
			if err != nil {
				t.Fatalf("UserView %s ChildCount %s is not an integer: %v", id, countRaw, err)
			}
			counts[id] = count
		}
		return counts
	}

	first, second := viewCounts(), viewCounts()
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("UserViews ChildCount changed for unchanged view IDs: first=%#v second=%#v", first, second)
	}
	for _, id := range []string{moviesID.String(), tvID.String()} {
		count, found := first[id]
		if !found {
			t.Fatalf("UserViews response omitted CollectionFolder %s: %#v", id, first)
		}
		if count < 1 || count > 9 {
			t.Fatalf("UserView %s ChildCount = %d, want upstream-valid sentinel in [1,9]", id, count)
		}
	}

	ordinary, err := items.Get(t.Context(), ordinaryID)
	if err != nil {
		t.Fatalf("load ordinary folder: %v", err)
	}
	projected, err := builder.Build(t.Context(), []item.Item{ordinary}, store.ItemQueryDTOOptions{Fields: []string{"ChildCount"}}, nil)
	if err != nil {
		t.Fatalf("build ordinary folder ChildCount: %v", err)
	}
	if len(projected.Items) != 1 || projected.Items[0].ChildCount == nil || *projected.Items[0].ChildCount != 11 {
		t.Fatalf("ordinary Folder ChildCount = %#v, want real direct child count 11", projected.Items)
	}
}
