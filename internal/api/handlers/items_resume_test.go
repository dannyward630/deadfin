// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestResumeRoutesUseTheResumableQueryShape pins ItemsController.GetResumeItems:
// resume is recursive, owns its alternate-version result, cannot collapse it,
// and orders by most-recent playback before regular pagination.
func TestResumeRoutesUseTheResumableQueryShape(t *testing.T) {
	userID := itemsHandlerUUID(t, 70)
	reader := &itemsHandlerReader{result: store.ItemQueryResult{}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, &itemsHandlerBuilder{}, clock.NewReal())
	for _, path := range []string{
		"/UserItems/Resume?userId=" + userID.Dashed() + "&startIndex=3&limit=2",
		"/Users/" + userID.Dashed() + "/Items/Resume?startIndex=3&limit=2",
	} {
		record := httptest.NewRecorder()
		rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, record.Code)
		}
	}
	if reader.queries != 2 {
		t.Fatalf("resume ItemReader calls = %d, want 2", reader.queries)
	}
	query := reader.last
	if query.UserID == nil || *query.UserID != userID || query.IsResumable == nil || !*query.IsResumable || !query.Recursive || !query.IncludeOwnedItems || query.CollapseBoxSetItems == nil || *query.CollapseBoxSetItems {
		t.Fatalf("resume ItemQuery = %#v, want source-pinned resumable controls", query)
	}
	if len(query.SortBy) != 1 || query.SortBy[0] != store.ItemSortByDatePlayed || len(query.SortOrder) != 1 || query.SortOrder[0] != store.SortOrderDescending || query.StartIndex == nil || *query.StartIndex != 3 || query.Limit == nil || *query.Limit != 2 {
		t.Fatalf("resume order/page = %#v", query)
	}
}

// TestResumeRoutesForwardSearchTerm pins ItemsController.GetResumeItems using
// the same Items query binding path as GetItems: searchTerm reaches the
// InternalItemsQuery before the resume-specific controls are overlaid.
func TestResumeRoutesForwardSearchTerm(t *testing.T) {
	userID := itemsHandlerUUID(t, 72)
	reader := &itemsHandlerReader{result: store.ItemQueryResult{}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, &itemsHandlerBuilder{}, clock.NewReal())
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/UserItems/Resume?userId="+userID.Dashed()+"&sEaRcHtErM=needle", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET /UserItems/Resume status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if reader.queries != 1 || reader.last.SearchTerm != "needle" {
		t.Fatalf("resume reader calls/SearchTerm = %d/%q, want 1/%q", reader.queries, reader.last.SearchTerm, "needle")
	}
}
