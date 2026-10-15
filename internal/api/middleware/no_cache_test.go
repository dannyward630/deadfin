// SPDX-License-Identifier: GPL-2.0-only

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
)

func TestNoCacheStampsMatchingOperationOnHandlerSuccess(t *testing.T) {
	t.Parallel()
	wrapped := NoCache("GetBitrateTestBytes", "GetEndpointInfo")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := wrapped(router.Route{OperationID: "GetBitrateTestBytes"}, inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Playback/BitrateTest", nil))

	if got := rec.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

func TestNoCacheStampsMatchingOperationOnAuthorizer401(t *testing.T) {
	t.Parallel()
	wrapped := NoCache("GetBitrateTestBytes", "GetEndpointInfo")
	// The inner handler models the authorizer path: it writes a bare 401
	// and returns before a downstream handler runs.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	handler := wrapped(router.Route{OperationID: "GetEndpointInfo"}, inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/System/Endpoint", nil))

	if got := rec.Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

func TestNoCacheLeavesNonMatchingRouteUntouched(t *testing.T) {
	t.Parallel()
	wrapped := NoCache("GetBitrateTestBytes", "GetEndpointInfo")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "present")
		w.WriteHeader(http.StatusOK)
	})
	handler := wrapped(router.Route{OperationID: "GetPlaybackInfo"}, inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/{itemId}/PlaybackInfo", nil))

	if got := rec.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want empty for a non-matching route", got)
	}
	if got := rec.Header().Get("X-Test"); got != "present" {
		t.Fatalf("X-Test = %q, want %q (downstream behavior must not change)", got, "present")
	}
}
