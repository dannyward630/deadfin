// SPDX-License-Identifier: GPL-2.0-only

package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

func TestRouteWrapperReceivesRegisteredMetadataAfterCaseNormalization(t *testing.T) {
	var received router.Route
	wrapperCalls := 0
	rt := router.New(func(route router.Route, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			wrapperCalls++
			received = route
			next.ServeHTTP(w, request)
		})
	})
	registered := router.Route{
		OperationID: "DownloadItem",
		Policy:      string(auth.PolicyDownload),
	}
	rt.Handle(http.MethodGet, "/Items/{itemId}/Download", registered, func(w http.ResponseWriter, request *http.Request) {
		if itemID := request.PathValue("itemId"); itemID != "AbC123" {
			t.Errorf("case-normalized request changed item id = %q, want AbC123", itemID)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/items/AbC123/download", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if wrapperCalls != 1 {
		t.Fatalf("wrapper calls = %d, want 1", wrapperCalls)
	}
	if received.Method != http.MethodGet || received.Pattern != "/Items/{itemId}/Download" || received.OperationID != registered.OperationID || received.Policy != registered.Policy || received.Anonymous != registered.Anonymous {
		t.Errorf("route metadata = %+v, want registered metadata %+v", received, registered)
	}
}
