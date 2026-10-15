// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemsHandlerRejectsBindingAndMapsReaderProjectionFailures keeps malformed
// input out of the store and maps failures at the public HTTP boundary; a live
// dependency must never use a placeholder 503 response.
func TestItemsHandlerRejectsBindingAndMapsReaderProjectionFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		reader *itemsHandlerReader
		build  *itemsHandlerBuilder
		status int
		reads  int
		builds int
	}{
		{name: "bad query", path: "/Items?ids=invalid-guid", reader: &itemsHandlerReader{}, build: &itemsHandlerBuilder{}, status: http.StatusBadRequest},
		{name: "bad legacy route user", path: "/Users/not-a-guid/Items", reader: &itemsHandlerReader{}, build: &itemsHandlerBuilder{}, status: http.StatusBadRequest},
		{name: "reader failure", path: "/Items", reader: &itemsHandlerReader{err: errors.New("reader unavailable")}, build: &itemsHandlerBuilder{}, status: http.StatusInternalServerError, reads: 1},
		{name: "projection failure", path: "/Items", reader: &itemsHandlerReader{result: store.ItemQueryResult{}}, build: &itemsHandlerBuilder{err: errors.New("projection unavailable")}, status: http.StatusInternalServerError, reads: 1, builds: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterItems(rt, tc.reader, tc.build, clock.NewReal())
			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if record.Code != tc.status {
				t.Fatalf("GET %s status = %d, want %d; body=%s", tc.path, record.Code, tc.status, record.Body.String())
			}
			if tc.reader.queries != tc.reads || tc.build.calls != tc.builds {
				t.Fatalf("GET %s reader/build calls = %d/%d, want %d/%d", tc.path, tc.reader.queries, tc.build.calls, tc.reads, tc.builds)
			}
		})
	}
}
