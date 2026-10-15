// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemsRouteSearchTermOverridesCollapseBoxSetItemsAtQueryHandoff keeps the
// public ItemsController rule at the HTTP boundary: a real search returns its
// concrete matches, even when the caller explicitly requests collapsing. Empty
// and omitted searches must retain the caller's explicit collapse choice.
func TestItemsRouteSearchTermOverridesCollapseBoxSetItemsAtQueryHandoff(t *testing.T) {
	for _, tc := range []struct {
		name, parameters string
		wantSearch       string
		wantCollapse     bool
	}{
		{name: "nonempty search overrides explicit true", parameters: "SearchTerm=Collection&CollapseBoxSetItems=true", wantSearch: "Collection", wantCollapse: false},
		{name: "empty search preserves explicit true", parameters: "SearchTerm=&CollapseBoxSetItems=true", wantCollapse: true},
		{name: "absent search preserves explicit true", parameters: "CollapseBoxSetItems=true", wantCollapse: true},
		{name: "empty search preserves explicit false", parameters: "SearchTerm=&CollapseBoxSetItems=false", wantCollapse: false},
		{name: "absent search preserves explicit false", parameters: "CollapseBoxSetItems=false", wantCollapse: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			itemID := itemsHandlerUUID(t, 700)
			reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}
			builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: itemID}}}
			routes := router.New()
			handlers.RegisterItems(routes, reader, builder, clock.NewReal())

			recorder := httptest.NewRecorder()
			routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items?"+tc.parameters, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET /Items = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			if reader.queries != 1 || reader.last.SearchTerm != tc.wantSearch || reader.last.CollapseBoxSetItems == nil || *reader.last.CollapseBoxSetItems != tc.wantCollapse {
				t.Fatalf("reader query = %#v after %q, want one query with SearchTerm=%q CollapseBoxSetItems=%t", reader.last, tc.parameters, tc.wantSearch, tc.wantCollapse)
			}
		})
	}
}

// TestItemsRouteSearchTermCollapseBoxSetItemsUsesUncollapsedResultBeforePaging
// runs the HTTP route against ItemStore. The search must keep the three movie
// matches, calculate their total before offsetting, then page those children
// rather than replacing them with the containing BoxSet.
func TestItemsRouteSearchTermCollapseBoxSetItemsUsesUncollapsedResultBeforePaging(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	alpha := itemsSearchCollapseMovie(t, 701, "Collection Alpha")
	bravo := itemsSearchCollapseMovie(t, 702, "Collection Bravo")
	charlie := itemsSearchCollapseMovie(t, 703, "Collection Charlie")
	boxSetName := "Synthetic Collection"
	boxSet := item.Item{
		ID:       itemsHandlerUUID(t, 704),
		Kind:     item.KindBoxSet,
		Name:     &boxSetName,
		SortName: &boxSetName,
		IsFolder: true,
		LinkedChildren: []item.LinkedChildRef{
			{ItemID: alpha.ID, Type: item.LinkedChildTypeManual},
			{ItemID: bravo.ID, Type: item.LinkedChildTypeManual},
			{ItemID: charlie.ID, Type: item.LinkedChildTypeManual},
		},
	}
	for _, value := range []item.Item{alpha, bravo, charlie, boxSet} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	reader := &itemsSearchCollapseStoreReader{items: items}
	routes := router.New()
	handlers.RegisterItems(routes, reader, itemsAccessBuilder{}, clock.NewReal())

	for _, tc := range []struct {
		name, search, collapse string
		start, total           int
		want                   []dto.UUID
		wantCollapse           bool
	}{
		{name: "nonempty search overrides explicit true", search: "&SearchTerm=Collection", collapse: "true", start: 1, total: 3, want: []dto.UUID{bravo.ID}, wantCollapse: false},
		{name: "empty search preserves explicit true", search: "&SearchTerm=", collapse: "true", start: 0, total: 1, want: []dto.UUID{boxSet.ID}, wantCollapse: true},
		{name: "absent search preserves explicit true", collapse: "true", start: 0, total: 1, want: []dto.UUID{boxSet.ID}, wantCollapse: true},
		{name: "empty search preserves explicit false", search: "&SearchTerm=", collapse: "false", start: 1, total: 3, want: []dto.UUID{bravo.ID}, wantCollapse: false},
		{name: "absent search preserves explicit false", collapse: "false", start: 1, total: 3, want: []dto.UUID{bravo.ID}, wantCollapse: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, profile := range []struct {
				name, accept string
			}{
				{name: "native"},
				{name: "camel", accept: `application/json; profile="CamelCase"`},
			} {
				t.Run(profile.name, func(t *testing.T) {
					path := "/Items?IncludeItemTypes=Movie&CollapseBoxSetItems=" + tc.collapse + tc.search + "&SortBy=SortName&StartIndex=" + strconv.Itoa(tc.start) + "&Limit=1&EnableTotalRecordCount=true"
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodGet, path, nil)
					if profile.accept != "" {
						request.Header.Set("Accept", profile.accept)
					}
					routes.ServeHTTP(recorder, request)
					if recorder.Code != http.StatusOK {
						t.Fatalf("GET %s = %d, want 200: %s", path, recorder.Code, recorder.Body.String())
					}

					var page dto.QueryResult[dto.BaseItemDto]
					if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
						t.Fatalf("decode Items response: %v; body=%s", err, recorder.Body.String())
					}
					got := make([]dto.UUID, len(page.Items))
					for index := range page.Items {
						got[index] = page.Items[index].ID
					}
					if !reflect.DeepEqual(got, tc.want) || page.TotalRecordCount != tc.total || page.StartIndex != tc.start {
						t.Fatalf("Items IDs/total/start = %v/%d/%d, want %v/%d/%d", got, page.TotalRecordCount, page.StartIndex, tc.want, tc.total, tc.start)
					}
					query := reader.queries[len(reader.queries)-1]
					if query.CollapseBoxSetItems == nil || *query.CollapseBoxSetItems != tc.wantCollapse {
						t.Fatalf("store handoff CollapseBoxSetItems = %v, want %t", query.CollapseBoxSetItems, tc.wantCollapse)
					}
				})
			}
		})
	}
}

func itemsSearchCollapseMovie(t *testing.T, value int, name string) item.Item {
	t.Helper()
	return item.Item{ID: itemsHandlerUUID(t, value), Kind: item.KindMovie, Name: &name, SortName: &name}
}

type itemsSearchCollapseStoreReader struct {
	items   *store.ItemStore
	queries []store.ItemQuery
}

func (reader *itemsSearchCollapseStoreReader) Query(ctx context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.Query(ctx, query)
}
