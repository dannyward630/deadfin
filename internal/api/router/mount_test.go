// SPDX-License-Identifier: GPL-2.0-only

package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
)

// innerHandler records the request method, path, RawQuery, and context
// PathBase it received.
type innerHandler struct {
	method   string
	path     string
	rawQuery string
	pathBase string
	called   bool
}

func (h *innerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	h.method = r.Method
	h.path = r.URL.Path
	h.rawQuery = r.URL.RawQuery
	h.pathBase = router.PathBase(r.Context())
	w.WriteHeader(http.StatusOK)
}

func newMountInner() *innerHandler { return &innerHandler{} }

func issueMountRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	handler.ServeHTTP(rec, req)
	return rec
}

func assertLocation(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (Location=%q)", rec.Code, want, rec.Header().Get("Location"))
	}
}

// TestMountEmptyBaseRootRedirect verifies that an empty BaseUrl redirects
// GET / to web/ with status 302 and does not call the inner handler.
func TestMountEmptyBaseRootRedirect(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("", inner)

	rec := issueMountRequest(h, http.MethodGet, "/")
	assertStatus(t, rec, http.StatusFound)
	assertLocation(t, rec, "web/")
	if inner.called {
		t.Fatalf("inner handler called for root redirect")
	}
}

// TestMountBlankBaseRootRedirect verifies that a whitespace-only BaseUrl
// also redirects GET / to web/ with status 302.
func TestMountBlankBaseRootRedirect(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("   ", inner)

	rec := issueMountRequest(h, http.MethodGet, "/")
	assertStatus(t, rec, http.StatusFound)
	assertLocation(t, rec, "web/")
	if inner.called {
		t.Fatalf("inner handler called for root redirect")
	}
}

// TestMountEmptyBaseDelegatesOtherPaths verifies that an empty or blank
// BaseUrl delegates non-root requests to the inner handler unchanged. Each
// delegated request must preserve the method, path, and RawQuery, and the
// context PathBase must be empty.
func TestMountEmptyBaseDelegatesOtherPaths(t *testing.T) {
	t.Parallel()

	// Each case gives the expected path and RawQuery that the inner
	// handler must observe. The method must match the request method.
	cases := []struct {
		name      string
		method    string
		target    string
		wantPath  string
		wantQuery string
	}{
		{name: "api path", method: http.MethodGet, target: "/System/Info/Public", wantPath: "/System/Info/Public", wantQuery: ""},
		{name: "socket", method: http.MethodGet, target: "/socket", wantPath: "/socket", wantQuery: ""},
		{name: "web path", method: http.MethodGet, target: "/web/", wantPath: "/web/", wantQuery: ""},
		{name: "deep path", method: http.MethodGet, target: "/Users/Public/Items", wantPath: "/Users/Public/Items", wantQuery: ""},
		{name: "post root", method: http.MethodPost, target: "/", wantPath: "/", wantQuery: ""},
		{name: "delete deep", method: http.MethodDelete, target: "/Users/Me", wantPath: "/Users/Me", wantQuery: ""},
		{name: "health", method: http.MethodGet, target: "/health", wantPath: "/health", wantQuery: ""},
		{name: "robots", method: http.MethodGet, target: "/robots.txt", wantPath: "/robots.txt", wantQuery: ""},
		{name: "query preserved", method: http.MethodGet, target: "/System/Info/Public?format=json", wantPath: "/System/Info/Public", wantQuery: "format=json"},
		{name: "multi query", method: http.MethodGet, target: "/Items?limit=10&offset=2", wantPath: "/Items", wantQuery: "limit=10&offset=2"},
	}

	for _, base := range []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "blank", value: "   "},
	} {
		t.Run(base.name, func(t *testing.T) {
			t.Parallel()
			inner := newMountInner()
			h := router.Mount(base.value, inner)

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					inner.called = false
					rec := issueMountRequest(h, tc.method, tc.target)
					assertStatus(t, rec, http.StatusOK)
					if !inner.called {
						t.Fatalf("inner handler not called for %s %s", tc.method, tc.target)
					}
					if inner.method != tc.method {
						t.Fatalf("method = %q, want %q", inner.method, tc.method)
					}
					if inner.path != tc.wantPath {
						t.Fatalf("path = %q, want %q", inner.path, tc.wantPath)
					}
					if inner.rawQuery != tc.wantQuery {
						t.Fatalf("rawQuery = %q, want %q", inner.rawQuery, tc.wantQuery)
					}
					if inner.pathBase != "" {
						t.Fatalf("PathBase = %q, want empty", inner.pathBase)
					}
				})
			}
		})
	}
}

// TestMountPrefixedAPIAndSocketDispatch verifies that prefixed API and
// /socket requests dispatch to inner with PathBase set and the prefix
// stripped.
func TestMountPrefixedAPIAndSocketDispatch(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "api path", target: "/jf/System/Info/Public", want: "/System/Info/Public"},
		{name: "socket", target: "/jf/socket", want: "/socket"},
		{name: "deep api", target: "/jf/Users/Public", want: "/Users/Public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner.called = false
			rec := issueMountRequest(h, http.MethodGet, tc.target)
			assertStatus(t, rec, http.StatusOK)
			if !inner.called {
				t.Fatalf("inner handler not called")
			}
			if inner.path != tc.want {
				t.Fatalf("inner path = %q, want %q", inner.path, tc.want)
			}
			if inner.pathBase != "/jf" {
				t.Fatalf("PathBase = %q, want /jf", inner.pathBase)
			}
		})
	}
}

// TestMountCaseInsensitivePrefix verifies the prefix matches
// case-insensitively.
func TestMountCaseInsensitivePrefix(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "upper prefix", target: "/JF/System/Info/Public"},
		{name: "mixed prefix", target: "/Jf/System/Info/Public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner.called = false
			rec := issueMountRequest(h, http.MethodGet, tc.target)
			assertStatus(t, rec, http.StatusOK)
			if !inner.called {
				t.Fatalf("inner handler not called")
			}
			if inner.pathBase != "/jf" {
				t.Fatalf("PathBase = %q, want /jf", inner.pathBase)
			}
		})
	}
}

// TestMountRedirectTargets verifies the redirect status and relative/absolute
// Location for bare base, base+slash, root, unprefixed deep path, /health,
// and /health/. The Location values match the authoritative stock captures
// of the BaseUrl relative redirects.
func TestMountRedirectTargets(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	for _, tc := range []struct {
		name       string
		target     string
		wantStatus int
		wantLoc    string
	}{
		{name: "bare base", target: "/jf", wantStatus: http.StatusFound, wantLoc: "jf/web/"},
		{name: "base slash", target: "/jf/", wantStatus: http.StatusFound, wantLoc: "web/"},
		{name: "root", target: "/", wantStatus: http.StatusFound, wantLoc: "jf/web/"},
		{name: "unprefixed deep", target: "/foo/bar", wantStatus: http.StatusFound, wantLoc: "../jf/web/"},
		{name: "health", target: "/health", wantStatus: http.StatusFound, wantLoc: "/jf/health"},
		{name: "health slash", target: "/health/", wantStatus: http.StatusFound, wantLoc: "/jf/health"},
		{name: "single segment", target: "/foo", wantStatus: http.StatusFound, wantLoc: "jf/web/"},
		{name: "three segments", target: "/a/b/c", wantStatus: http.StatusFound, wantLoc: "../../jf/web/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner.called = false
			rec := issueMountRequest(h, http.MethodGet, tc.target)
			assertStatus(t, rec, tc.wantStatus)
			assertLocation(t, rec, tc.wantLoc)
			if inner.called {
				t.Fatalf("inner handler called for redirect %s", tc.name)
			}
		})
	}
}

// TestMountHealthRedirectIsCaseInsensitive verifies /health and /health/
// match case-insensitively.
func TestMountHealthRedirectIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "lowercase", target: "/health"},
		{name: "uppercase", target: "/Health"},
		{name: "mixed", target: "/HeAlTh"},
		{name: "uppercase slash", target: "/Health/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := issueMountRequest(h, http.MethodGet, tc.target)
			assertStatus(t, rec, http.StatusFound)
			assertLocation(t, rec, "/jf/health")
		})
	}
}

// TestMountJfishDoesNotDispatch verifies that /jfish does not dispatch as
// /ish under base /jf (pinned Map non-match behavior).
func TestMountJfishDoesNotDispatch(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	// /jfish does not match /jf at a path-segment boundary, so it is an
	// unprefixed request and gets redirected, not dispatched as /ish.
	rec := issueMountRequest(h, http.MethodGet, "/jfish")
	assertStatus(t, rec, http.StatusFound)
	assertLocation(t, rec, "jf/web/")
	if inner.called {
		t.Fatalf("inner handler must not be called for /jfish")
	}
}

// TestMountQueryPreservation verifies the query string is preserved when
// stripping the prefix.
func TestMountQueryPreservation(t *testing.T) {
	t.Parallel()
	inner := newMountInner()
	h := router.Mount("/jf", inner)

	rec := issueMountRequest(h, http.MethodGet, "/jf/System/Info/Public?format=json")
	assertStatus(t, rec, http.StatusOK)
	if inner.path != "/System/Info/Public" {
		t.Fatalf("inner path = %q, want /System/Info/Public", inner.path)
	}
	if inner.rawQuery != "format=json" {
		t.Fatalf("inner rawQuery = %q, want format=json", inner.rawQuery)
	}
}

// TestPathBaseFromContextWithoutMount verifies PathBase returns empty for a
// plain context.
func TestPathBaseFromContextWithoutMount(t *testing.T) {
	t.Parallel()
	if got := router.PathBase(context.Background()); got != "" {
		t.Fatalf("PathBase on plain context = %q, want empty", got)
	}
}

// TestMountNormalization verifies the mount normalizes the configured base
// (adds leading slash, removes trailing slash). Whitespace-only becomes empty
// and triggers the empty-base redirect wrapper. Surrounding whitespace on a
// non-blank value is preserved, matching the upstream setter.
func TestMountNormalization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base string
		want string
	}{
		{base: "jf", want: "/jf"},
		{base: "/jf/", want: "/jf"},
		{base: "/jf", want: "/jf"},
		{base: "  /jf  ", want: "/  /jf  "},
	} {
		inner := newMountInner()
		h := router.Mount(tc.base, inner)
		target := &url.URL{Path: tc.want + "/System/Ping"}
		req := &http.Request{Method: http.MethodGet, URL: target}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertStatus(t, rec, http.StatusOK)
		if inner.pathBase != tc.want {
			t.Fatalf("base %q: PathBase = %q, want %q", tc.base, inner.pathBase, tc.want)
		}
	}
}
