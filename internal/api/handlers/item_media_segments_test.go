// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestMediaSegmentsEmptyResultForVisibleItem pins the stock response with no
// MediaSegmentProviders (D-60): 200 and an empty QueryResult. The
// includeSegmentTypes filter accepts CSV, repeated keys, any name casing, and
// the numeric enum values.
func TestMediaSegmentsEmptyResultForVisibleItem(t *testing.T) {
	itemID := itemsHandlerUUID(t, 860)
	for _, query := range []string{
		"",
		"?includeSegmentTypes=Intro,Outro",
		"?includeSegmentTypes=intro&includeSegmentTypes=COMMERCIAL",
		"?includeSegmentTypes=0,5",
	} {
		t.Run(query, func(t *testing.T) {
			reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindEpisode}}}}}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/MediaSegments/"+itemID.Dashed()+query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /MediaSegments/{id}%s = %d, want 200: %s", query, rec.Code, rec.Body.String())
			}
			if got, want := rec.Body.String(), `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`; got != want {
				t.Fatalf("body = %s, want %s", got, want)
			}
			if len(reader.queries) != 1 || len(reader.queries[0].ItemIDs) != 1 || reader.queries[0].ItemIDs[0] != itemID {
				t.Fatalf("reader queries = %#v, want one lookup of %s", reader.queries, itemID.Dashed())
			}
		})
	}
}

// TestMediaSegmentsMissingOrHiddenItemIsNotFound covers the stock 404: the
// user-scoped item lookup returns nothing when the item does not exist or
// the caller cannot see it.
func TestMediaSegmentsMissingOrHiddenItemIsNotFound(t *testing.T) {
	itemID := itemsHandlerUUID(t, 861)
	reader := &itemExtrasReader{}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/MediaSegments/"+itemID.Dashed(), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /MediaSegments/{missing} = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestMediaSegmentsIgnoresQueryUserID proves the lookup follows the caller.
// The stock operation has no userId parameter, so a query userId must not
// select another user's visibility.
func TestMediaSegmentsIgnoresQueryUserID(t *testing.T) {
	itemID := itemsHandlerUUID(t, 862)
	otherID := itemsHandlerUUID(t, 863)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/MediaSegments/"+itemID.Dashed()+"?userId="+otherID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /MediaSegments/{id}?userId = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if query := reader.queries[0]; query.User != nil || query.UserID != nil {
		t.Fatalf("lookup user = %#v / %v, want the unauthenticated caller (nil)", query.User, query.UserID)
	}
}

func TestMediaSegmentsRejectsMalformedRequestsBeforeReads(t *testing.T) {
	valid := itemsHandlerUUID(t, 864).Dashed()
	for _, path := range []string{
		"/MediaSegments/not-a-guid",
		"/MediaSegments/" + dto.NilUUID.Dashed(),
		"/MediaSegments/" + valid + "?includeSegmentTypes=Credits",
		"/MediaSegments/" + valid + "?includeSegmentTypes=6",
	} {
		t.Run(path, func(t *testing.T) {
			reader := &itemExtrasReader{}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusBadRequest || len(reader.queries) != 0 {
				t.Fatalf("GET %s = %d reads=%d, want 400/no reads", path, rec.Code, len(reader.queries))
			}
		})
	}
}
