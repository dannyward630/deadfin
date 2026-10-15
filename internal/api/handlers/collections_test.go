// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestCollectionRouteMetadata pins the AddToCollection route registration.
func TestCollectionRouteMetadata(t *testing.T) {
	rt := router.New()
	handlers.RegisterCollections(rt, nil, nil)

	got, found := routeByOperation(rt.Routes(), "AddToCollection")
	if !found {
		t.Fatalf("AddToCollection is not registered")
	}
	want := router.Route{
		Method:      http.MethodPost,
		Pattern:     "/Collections/{collectionId}/Items",
		OperationID: "AddToCollection",
		Policy:      string(auth.PolicyCollectionManagement),
	}
	if got.Method != want.Method || got.Pattern != want.Pattern || got.OperationID != want.OperationID || got.Policy != want.Policy || got.Anonymous {
		t.Fatalf("route = %#v, want non-anonymous %#v", got, want)
	}
}

// TestCollectionNilStoreReturns503 proves the route stays registered when the
// store is absent (the route ledger path) and answers 503.
func TestCollectionNilStoreReturns503(t *testing.T) {
	rt := router.New()
	handlers.RegisterCollections(rt, nil, nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Collections/"+collUUID(t, 800).String()+"/Items?ids="+collUUID(t, 801).String(), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAddToCollectionBindingErrors proves the 400 contract for malformed input.
func TestAddToCollectionBindingErrors(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	boxSet := collBoxSetItem(t, 810, "Binding BoxSet")
	movie := collMovieItem(t, 811, "Binding Movie")
	for _, value := range []item.Item{boxSet, movie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "malformed route UUID", path: "/Collections/not-a-uuid/Items?ids=" + movie.ID.String()},
		{name: "absent ids parameter", path: "/Collections/" + boxSet.ID.String() + "/Items"},
		{name: "malformed item UUID in ids", path: "/Collections/" + boxSet.ID.String() + "/Items?ids=not-a-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterCollections(rt, items, nil)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST %s = %d, want %d; body=%s", tc.path, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

// TestAddToCollectionUnknownCollectionReturns400 proves an unknown
// collection maps to 400, as upstream's ArgumentException does.
func TestAddToCollectionUnknownCollectionReturns400(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	// No items upserted, so the collection ID does not exist.
	collectionID := collUUID(t, 820)
	childID := collUUID(t, 821)

	rt := router.New()
	handlers.RegisterCollections(rt, items, nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Collections/"+collectionID.String()+"/Items?ids="+childID.String(), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with unknown collection = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestAddToCollectionCreatesMembershipAndCollapses proves the full write
// contract: the first POST returns 204 and creates Manual membership,
// CollapseBoxSetItems=true returns the BoxSet, false returns the linked
// movies, and a repeated POST returns 204 without another relation.
func TestAddToCollectionCreatesMembershipAndCollapses(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)

	boxSet := collBoxSetItem(t, 830, "Collapse BoxSet")
	linkedMovie := collMovieItem(t, 831, "Linked Movie")
	unlinkedMovie := collMovieItem(t, 832, "Unlinked Movie")

	// Pre-link the scanner's movie through Upsert.
	boxSetWithChild := boxSet
	boxSetWithChild.LinkedChildren = []item.LinkedChildRef{
		{ItemID: linkedMovie.ID, Type: item.LinkedChildTypeManual},
	}
	for _, value := range []item.Item{boxSetWithChild, linkedMovie, unlinkedMovie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	reader := &collStoreReader{items: items}
	rt := router.New()
	handlers.RegisterCollections(rt, items, nil)
	handlers.RegisterItems(rt, reader, itemsAccessBuilder{}, clock.NewReal())

	// The first POST writes the unlinked movie as a Manual child.
	path := "/Collections/" + boxSet.ID.String() + "/Items?ids=" + unlinkedMovie.ID.String()
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first POST = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// CollapseBoxSetItems=true returns the BoxSet instead of its linked movies.
	assertCollIDs(t, rt, "true", []dto.UUID{boxSet.ID})
	// CollapseBoxSetItems=false returns the linked movies.
	assertCollIDs(t, rt, "false", []dto.UUID{linkedMovie.ID, unlinkedMovie.ID})

	// The second POST returns 204 and keeps one relation for each child.
	rec2 := httptest.NewRecorder()
	rt.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, path, nil))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second POST = %d, want 204; body=%s", rec2.Code, rec2.Body.String())
	}
	links, err := items.LinkedChildren(t.Context(), boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("LinkedChildren = %d after repeated POST, want 2", len(links))
	}
}

// TestAddToCollectionBodyIDsDoNotReplaceQueryParam proves the ids query
// parameter is the only source for child item IDs. Body item IDs must not
// replace the required ids query parameter.
func TestAddToCollectionBodyIDsDoNotReplaceQueryParam(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)

	boxSet := collBoxSetItem(t, 840, "Body IDs BoxSet")
	movie := collMovieItem(t, 841, "Query Movie")
	otherMovie := collMovieItem(t, 842, "Body Movie")

	for _, value := range []item.Item{boxSet, movie, otherMovie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	rt := router.New()
	handlers.RegisterCollections(rt, items, nil)

	// POST with movie in the query param and otherMovie in the body.
	path := "/Collections/" + boxSet.ID.String() + "/Items?ids=" + movie.ID.String()
	body := `{"Ids":["` + otherMovie.ID.String() + `"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// The query-param movie is the linked child, not the body movie.
	links, err := items.LinkedChildren(t.Context(), boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 1 || links[0].ItemID != movie.ID {
		var got []dto.UUID
		for _, l := range links {
			got = append(got, l.ItemID)
		}
		t.Fatalf("LinkedChildren = %v, want [%s]", got, movie.ID)
	}
}

func assertCollIDs(t *testing.T, rt *router.Router, collapse string, want []dto.UUID) {
	t.Helper()
	path := "/Items?IncludeItemTypes=Movie&CollapseBoxSetItems=" + collapse + "&SortBy=SortName&SortOrder=Ascending"
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, rec.Code, rec.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Items response: %v; body=%s", err, rec.Body.String())
	}
	got := make([]dto.UUID, len(page.Items))
	for i, it := range page.Items {
		got[i] = it.ID
	}
	if len(got) != len(want) {
		t.Fatalf("collapse=%s IDs = %v, want %v", collapse, got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("collapse=%s IDs = %v, want %v", collapse, got, want)
		}
	}
}

func collBoxSetItem(t *testing.T, value int, name string) item.Item {
	t.Helper()
	n := name
	return item.Item{
		ID:       collUUID(t, value),
		Kind:     item.KindBoxSet,
		Name:     &n,
		SortName: &n,
		IsFolder: true,
	}
}

func collMovieItem(t *testing.T, value int, name string) item.Item {
	t.Helper()
	n := name
	return item.Item{
		ID:       collUUID(t, value),
		Kind:     item.KindMovie,
		Name:     &n,
		SortName: &n,
	}
}

func collUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing collection UUID %d: %v", value, err)
	}
	return id
}

type collStoreReader struct {
	items   *store.ItemStore
	queries []store.ItemQuery
}

func (r *collStoreReader) Query(ctx context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	r.queries = append(r.queries, query)
	return r.items.Query(ctx, query)
}

// collCreator stores a BoxSet for CreateCollection, standing in for the
// scanner.
type collCreator struct {
	items *store.ItemStore
	id    dto.UUID
	names []string
}

func (c *collCreator) CreateCollection(ctx context.Context, name string, locked bool) (item.Item, error) {
	c.names = append(c.names, name)
	value := item.Item{ID: c.id, Kind: item.KindBoxSet, Name: &name, SortName: &name, IsFolder: true, IsLocked: locked}
	return value, c.items.Upsert(ctx, value)
}

// TestCreateAndRemoveCollection ports CreateCollection and
// RemoveFromCollection: create returns the N-format id and links the ids,
// remove unlinks members and skips non-members, and unknown ids are 400.
func TestCreateAndRemoveCollection(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	movieA := collMovieItem(t, 851, "A")
	movieB := collMovieItem(t, 852, "B")
	for _, value := range []item.Item{movieA, movieB} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	creator := &collCreator{items: items, id: collUUID(t, 850)}
	rt := router.New()
	handlers.RegisterCollections(rt, items, creator)
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	members := func() []dto.UUID {
		links, err := items.LinkedChildren(t.Context(), creator.id)
		if err != nil {
			t.Fatal(err)
		}
		out := []dto.UUID{}
		for _, link := range links {
			out = append(out, link.ItemID)
		}
		return out
	}

	if rec := do(http.MethodPost, "/Collections"); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without name = %d, want 400", rec.Code)
	}
	rec := do(http.MethodPost, "/Collections?name=Heist&isLocked=true&ids="+movieA.ID.String()+","+movieB.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"Id":"`+creator.id.String()+`"}` {
		t.Fatalf("create body = %s", body)
	}
	if len(creator.names) != 1 || creator.names[0] != "Heist" {
		t.Fatalf("creator names = %v", creator.names)
	}
	if got := members(); len(got) != 2 {
		t.Fatalf("members after create = %v", got)
	}

	base := "/Collections/" + creator.id.String() + "/Items?ids="
	if rec := do(http.MethodDelete, base+movieA.ID.String()+","+collUUID(t, 859).String()); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	if got := members(); len(got) != 1 || got[0] != movieB.ID {
		t.Fatalf("members after remove = %v, want [%s]", got, movieB.ID)
	}
	if rec := do(http.MethodDelete, "/Collections/"+movieB.ID.String()+"/Items?ids="+movieA.ID.String()); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove from a non-collection = %d, want 400", rec.Code)
	}
	if rec := do(http.MethodDelete, "/Collections/"+creator.id.String()+"/Items"); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove without ids = %d, want 400", rec.Code)
	}
	if rec := do(http.MethodPost, "/Collections?name=Other&ids="+collUUID(t, 858).String()); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with an unknown item = %d, want 400", rec.Code)
	}
}
