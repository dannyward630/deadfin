// SPDX-License-Identifier: GPL-2.0-only

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORS covers the shipped default CORS policy: any origin, any method,
// any header, no credentials. Cases (a)-(f) cover the preflight and simple
// request shapes. The negative control (405 / no header without the CORS
// branch) was observed manually with the CORS intercept removed.
func TestCORS(t *testing.T) {
	// A wrapper that records whether any registered route was dispatched.
	// A preflight handled before routing must not invoke it (case d).
	wrapperCalls := 0
	rt := New(func(_ Route, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapperCalls++
			next.ServeHTTP(w, r)
		})
	})
	rt.Handle("GET", "/Users/AuthenticateByName", Route{OperationID: "AuthenticateByName", Anonymous: true}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rt.Handle("POST", "/Items", Route{OperationID: "GetItems"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	preflight := func(target string) *http.Request {
		req := httptest.NewRequest(http.MethodOptions, target, nil)
		req.Header.Set("Origin", "https://example.org")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		return req
	}

	for _, tc := range []struct {
		name     string
		req      func() *http.Request
		wantCode int
		// CORS-header expectations. Empty string means header absent.
		wantOrigin  string
		wantMethods string
		wantHeaders string
		dispatched  bool // a route wrapper should fire for non-preflight
	}{
		{
			name:        "a_preflight_registered_pattern_registered_method",
			req:         func() *http.Request { return preflight("/Users/AuthenticateByName") },
			wantCode:    http.StatusNoContent,
			wantOrigin:  "*",
			wantMethods: "POST",
			wantHeaders: "authorization,content-type",
		},
		{
			// The filed bug: requested method not registered on this pattern.
			name:        "b_preflight_registered_pattern_unregistered_method",
			req:         func() *http.Request { return preflight("/Items") },
			wantCode:    http.StatusNoContent,
			wantOrigin:  "*",
			wantMethods: "POST",
			wantHeaders: "authorization,content-type",
		},
		{
			name:        "c_preflight_path_matches_no_route",
			req:         func() *http.Request { return preflight("/No/Such/Path") },
			wantCode:    http.StatusNoContent,
			wantOrigin:  "*",
			wantMethods: "POST",
			wantHeaders: "authorization,content-type",
		},
		{
			name: "e_origin_present_plain_get_dispatched_with_origin",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/Users/AuthenticateByName", nil)
				r.Header.Set("Origin", "https://example.org")
				return r
			},
			wantCode:   http.StatusOK,
			wantOrigin: "*",
			dispatched: true,
		},
		{
			name:       "f_no_origin_dispatched_no_cors_headers",
			req:        func() *http.Request { return httptest.NewRequest(http.MethodGet, "/Users/AuthenticateByName", nil) },
			wantCode:   http.StatusOK,
			wantOrigin: "",
			dispatched: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapperCalls = 0
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, tc.req())

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantOrigin)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != tc.wantMethods {
				t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, tc.wantMethods)
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != tc.wantHeaders {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, tc.wantHeaders)
			}
			// Case d: a preflight must not invoke any route wrapper.
			if !tc.dispatched && wrapperCalls != 0 {
				t.Errorf("route wrapper invoked %d time(s); preflight must bypass wrappers", wrapperCalls)
			}
			if tc.dispatched && wrapperCalls != 1 {
				t.Errorf("route wrapper invoked %d time(s), want 1", wrapperCalls)
			}
			// Wildcard branch must never advertise credentials.
			if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("Access-Control-Allow-Credentials must be absent on wildcard policy")
			}
		})
	}
}

// TestCORSNoOriginByteIdentical asserts that a request without an Origin
// header produces byte-identical status and body to the pre-CORS behavior:
// no Access-Control-* headers are added.
func TestCORSNoOriginByteIdentical(t *testing.T) {
	rt := New()
	rt.Handle("GET", "/System/Info/Public", Route{OperationID: "GetPublicSystemInfo", Anonymous: true}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("public"))
	})

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "public" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "public")
	}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Allow-Credentials"} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("no-Origin response must not carry %s, got %q", h, got)
		}
	}
}

// TestCORSOriginOnError asserts the simple cross-origin header is present on
// error responses too, matching middleware-before-routing ordering.
func TestCORSOriginOnError(t *testing.T) {
	rt := New()
	// No route registered for this path; chi returns 404.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Definitely/Not/Registered", nil)
	req.Header.Set("Origin", "https://example.org")
	rt.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("error response Access-Control-Allow-Origin = %q, want *", got)
	}
}
