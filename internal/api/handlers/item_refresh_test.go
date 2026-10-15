// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

const itemRefreshID = "aabbccddeeff00112233445566778899"

type fakeItemReader struct {
	items []item.Item
	err   error
	query store.ItemQuery
}

func (f *fakeItemReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	f.query = query
	if f.err != nil {
		return store.ItemQueryResult{}, f.err
	}
	return store.ItemQueryResult{Items: f.items, TotalRecordCount: len(f.items)}, nil
}

type fakeItemRefreshTrigger struct {
	calls   int
	options library.RefreshOptions
	err     error
}

func (f *fakeItemRefreshTrigger) TriggerRefresh(options library.RefreshOptions) error {
	f.calls++
	f.options = options
	return f.err
}

func newItemRefreshRouter(reader handlers.ItemReader, trigger handlers.ItemRefreshTrigger) *router.Router {
	rt := router.New()
	handlers.RegisterItemRefresh(rt, reader, trigger)
	return rt
}

func refreshingItem(t *testing.T) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(itemRefreshID)
	if err != nil {
		t.Fatalf("parse fixture id: %v", err)
	}
	return item.Item{ID: id, Kind: item.KindMovie}
}

func TestItemRefreshRouteRegistration(t *testing.T) {
	rt := newItemRefreshRouter(&fakeItemReader{}, &fakeItemRefreshTrigger{})
	for _, route := range rt.Routes() {
		if route.Method == http.MethodPost && route.Pattern == "/Items/{itemId}/Refresh" {
			if got, want := route.OperationID, "RefreshItem"; got != want {
				t.Fatalf("item refresh operation ID = %q, want %q", got, want)
			}
			if got, want := route.Policy, string(auth.PolicyRequiresElevation); got != want {
				t.Fatalf("item refresh policy = %q, want %q", got, want)
			}
			if route.Anonymous {
				t.Fatal("item refresh must not be anonymous")
			}
			return
		}
	}
	t.Fatal("POST /Items/{itemId}/Refresh was not registered")
}

// TestItemRefreshForcesFullRefresh proves the endpoint threads a full/replace
// refresh intent to the scanner seam for an item that already exists — the lever
// that re-runs the remote metadata stage on already-scanned items.
func TestItemRefreshForcesFullRefresh(t *testing.T) {
	reader := &fakeItemReader{items: []item.Item{refreshingItem(t)}}
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(reader, trigger)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh?metadataRefreshMode=FullRefresh&imageRefreshMode=FullRefresh&replaceAllMetadata=true&replaceAllImages=true", nil)
	rt.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusNoContent; got != want {
		t.Fatalf("full refresh status = %d, want %d", got, want)
	}
	if trigger.calls != 1 {
		t.Fatalf("trigger calls = %d, want 1", trigger.calls)
	}
	want := library.RefreshOptions{
		Metadata:           library.RefreshModeFullRefresh,
		Image:              library.RefreshModeFullRefresh,
		ReplaceAllMetadata: true,
		ReplaceAllImages:   true,
		Targets:            []library.RefreshTarget{{ItemID: refreshingItem(t).ID}},
	}
	if !reflect.DeepEqual(trigger.options, want) {
		t.Fatalf("threaded refresh options = %#v, want %#v", trigger.options, want)
	}
	if len(reader.query.ItemIDs) != 1 || reader.query.ItemIDs[0].String() != itemRefreshID {
		t.Fatalf("existence query item ids = %v, want [%s]", reader.query.ItemIDs, itemRefreshID)
	}
}

// TestItemRefreshBindsRegenerateTrickplay proves the elevated, authenticated
// item-refresh route emits exactly its item's regeneration ID for an explicit
// request and no IDs for ordinary refreshes. The trigger remains asynchronous:
// a 204 means it accepted the refresh, not that image generation completed
// inline.
func TestItemRefreshBindsRegenerateTrickplay(t *testing.T) {
	wantItemID := refreshingItem(t).ID
	for _, tc := range []struct {
		name   string
		suffix string
		want   []dto.UUID
	}{
		{name: "requested", suffix: "?regenerateTrickplay=true", want: []dto.UUID{wantItemID}},
		{name: "omitted retains ordinary refresh"},
		{name: "explicit false retains ordinary refresh", suffix: "?regenerateTrickplay=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeItemReader{items: []item.Item{refreshingItem(t)}}
			trigger := &fakeItemRefreshTrigger{}
			rt := newItemRefreshRouter(reader, trigger)

			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh"+tc.suffix, nil))

			if got, want := recorder.Code, http.StatusNoContent; got != want {
				t.Fatalf("refresh status = %d, want %d", got, want)
			}
			if got, want := trigger.calls, 1; got != want {
				t.Fatalf("trigger calls = %d, want %d", got, want)
			}
			if got := trigger.options.RegenerateTrickplayItemIDs; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RegenerateTrickplayItemIDs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestItemRefreshBindsModeCaseInsensitively confirms the metadataRefreshMode enum
// binds by upstream name regardless of casing (ASP.NET model binding parity).
func TestItemRefreshBindsModeCaseInsensitively(t *testing.T) {
	reader := &fakeItemReader{items: []item.Item{refreshingItem(t)}}
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(reader, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh?MetadataRefreshMode=fullrefresh", nil))

	if got, want := recorder.Code, http.StatusNoContent; got != want {
		t.Fatalf("case-insensitive mode status = %d, want %d", got, want)
	}
	if trigger.options.Metadata != library.RefreshModeFullRefresh {
		t.Fatalf("bound metadata mode = %d, want FullRefresh", trigger.options.Metadata)
	}
}

// TestItemRefreshDefaultModeIsNone mirrors upstream: the query default is
// MetadataRefreshMode.None, so a bare refresh threads no remote work.
func TestItemRefreshDefaultModeIsNone(t *testing.T) {
	reader := &fakeItemReader{items: []item.Item{refreshingItem(t)}}
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(reader, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh", nil))

	if got, want := recorder.Code, http.StatusNoContent; got != want {
		t.Fatalf("default refresh status = %d, want %d", got, want)
	}
	want := library.RefreshOptions{Targets: []library.RefreshTarget{{ItemID: refreshingItem(t).ID}}}
	if got := trigger.options; !reflect.DeepEqual(got, want) {
		t.Fatalf("default threaded options = %#v, want zero (None modes, no replace)", got)
	}
}

// TestItemRefreshScopesToTargetItem proves the handler names the refreshed
// item and its path, so the scanner refreshes only the library that holds it.
// Upstream ProviderManager.RefreshItem refreshes only the target's subtree.
func TestItemRefreshScopesToTargetItem(t *testing.T) {
	target := refreshingItem(t)
	path := "/media/animu"
	target.Kind = item.KindCollectionFolder
	target.Path = &path
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(&fakeItemReader{items: []item.Item{target}}, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh", nil))

	if got, want := recorder.Code, http.StatusNoContent; got != want {
		t.Fatalf("scoped refresh status = %d, want %d", got, want)
	}
	want := []library.RefreshTarget{{ItemID: target.ID, Path: path}}
	if got := trigger.options.Targets; !reflect.DeepEqual(got, want) {
		t.Fatalf("refresh targets = %#v, want %#v", got, want)
	}
}

func TestItemRefreshMissingItemIsNotFound(t *testing.T) {
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(&fakeItemReader{}, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh?metadataRefreshMode=FullRefresh", nil))

	if got, want := recorder.Code, http.StatusNotFound; got != want {
		t.Fatalf("missing item status = %d, want %d", got, want)
	}
	if trigger.calls != 0 {
		t.Fatalf("trigger calls for missing item = %d, want 0", trigger.calls)
	}
}

func TestItemRefreshRejectsMalformedID(t *testing.T) {
	trigger := &fakeItemRefreshTrigger{}
	rt := newItemRefreshRouter(&fakeItemReader{items: []item.Item{refreshingItem(t)}}, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/not-a-uuid/Refresh", nil))

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("malformed id status = %d, want %d", got, want)
	}
	if trigger.calls != 0 {
		t.Fatalf("trigger calls for malformed id = %d, want 0", trigger.calls)
	}
}

func TestItemRefreshMapsClosedScannerToServiceUnavailable(t *testing.T) {
	trigger := &fakeItemRefreshTrigger{err: library.ErrScannerClosed}
	rt := newItemRefreshRouter(&fakeItemReader{items: []item.Item{refreshingItem(t)}}, trigger)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Items/"+itemRefreshID+"/Refresh?metadataRefreshMode=FullRefresh", nil))

	if got, want := recorder.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("closed scanner status = %d, want %d", got, want)
	}
}
