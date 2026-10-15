// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemDetailAndLegacyReturnBareProjection pins UserLibraryController's
// non-wrapper detail shape and the legacy route's user-id precedence.
func TestItemDetailAndLegacyReturnBareProjection(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 50), itemsHandlerUUID(t, 51)
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}, TotalRecordCount: 1}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: itemID, Name: "Detail"}}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, builder, clock.NewReal())

	for _, path := range []string{
		"/Items/" + itemID.Dashed() + "?userId=" + userID.Dashed(),
		"/Users/" + userID.Dashed() + "/Items/" + itemID.Dashed() + "?userId=" + itemsHandlerUUID(t, 52).Dashed(),
	} {
		record := httptest.NewRecorder()
		rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body=%s", path, record.Code, record.Body.String())
		}
		if record.Body.String() == "" || record.Body.String()[0] != '{' {
			t.Fatalf("GET %s response = %q, want bare item object", path, record.Body.String())
		}
	}
	if reader.queries != 2 || builder.calls != 2 {
		t.Fatalf("detail reader/builder calls = %d/%d, want 2/2", reader.queries, builder.calls)
	}
	if builder.detailedCalls != 2 {
		t.Fatalf("detail BuildDetailed calls = %d, want 2", builder.detailedCalls)
	}
	if got := reader.last.ItemIDs; len(got) != 1 || got[0] != itemID || reader.last.UserID == nil || *reader.last.UserID != userID {
		t.Fatalf("detail ItemQuery = %#v, want exact item and route user", reader.last)
	}
}

func TestItemDetailReturnsBadRequestForMalformedIDAndNotFoundForEmptyPage(t *testing.T) {
	userID := itemsHandlerUUID(t, 60)
	for _, tc := range []struct {
		path   string
		result store.ItemQueryResult
		status int
	}{
		{path: "/Items/not-a-guid?userId=" + userID.Dashed(), status: http.StatusBadRequest},
		{path: "/Items/" + itemsHandlerUUID(t, 61).Dashed() + "?userId=" + userID.Dashed(), status: http.StatusNotFound},
	} {
		reader := &itemsHandlerReader{result: tc.result}
		rt := router.New()
		handlers.RegisterItems(rt, reader, &itemsHandlerBuilder{}, clock.NewReal())
		record := httptest.NewRecorder()
		rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if record.Code != tc.status {
			t.Fatalf("GET %s status = %d, want %d", tc.path, record.Code, tc.status)
		}
	}
}
