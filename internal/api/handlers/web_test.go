// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/config"
)

const webIndexHTML = "<!DOCTYPE html><html><body>deadfin web</body></html>"

// newWebRouter registers the system handlers with a real asset directory at
// the configured web path and returns the router plus that directory.
func newWebRouter(t *testing.T) (*router.Router, string) {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("derive paths: %v", err)
	}
	webPath := filepath.Join(paths.Root, "web")
	for name, contents := range map[string]string{
		"index.html": webIndexHTML,
		"main.js":    "console.log('deadfin');",
		"sub.data":   "subtitles-octopus payload",
	} {
		writeWebAsset(t, webPath, name, contents)
	}
	rt := router.New()
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), completedWebStartup{}, handlers.SystemInfoConfig{
		Paths:   paths,
		WebPath: webPath,
	})
	return rt, webPath
}

func writeWebAsset(t *testing.T, webPath, name, contents string) {
	t.Helper()
	target := filepath.Join(webPath, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create asset directory: %v", err)
	}
	if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
		t.Fatalf("write asset %s: %v", name, err)
	}
}

type completedWebStartup struct{}

func (completedWebStartup) StartupWizardCompleted() bool { return true }

func issueWebRequest(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("test request carries auth header %q, want none", got)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWebServesStaticAssetsAnonymously(t *testing.T) {
	rt, _ := newWebRouter(t)

	for _, tc := range []struct {
		name            string
		path            string
		wantStatus      int
		wantContentType string
		wantCache       string
		wantBody        string
	}{
		{
			name:            "index via directory request",
			path:            "/web/",
			wantStatus:      http.StatusOK,
			wantContentType: "text/html",
			wantCache:       "no-cache",
			wantBody:        webIndexHTML,
		},
		{
			name:            "index by name",
			path:            "/web/index.html",
			wantStatus:      http.StatusOK,
			wantContentType: "text/html",
			wantCache:       "no-cache",
			wantBody:        webIndexHTML,
		},
		{
			name:            "javascript asset without no-cache",
			path:            "/web/main.js",
			wantStatus:      http.StatusOK,
			wantContentType: "text/javascript",
			wantCache:       "",
			wantBody:        "console.log('deadfin');",
		},
		{
			name:            "subtitles-octopus data mapping",
			path:            "/web/sub.data",
			wantStatus:      http.StatusOK,
			wantContentType: "application/octet-stream",
			wantCache:       "",
			wantBody:        "subtitles-octopus payload",
		},
		{
			name:       "missing asset",
			path:       "/web/nope.js",
			wantStatus: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := issueWebRequest(t, rt, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != tc.wantContentType {
				t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.wantContentType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("GET %s Cache-Control = %q, want %q", tc.path, got, tc.wantCache)
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Errorf("GET %s body = %q, want %q", tc.path, got, tc.wantBody)
			}
		})
	}
}

func TestWebRedirectsMountAndRoot(t *testing.T) {
	rt, _ := newWebRouter(t)

	for _, tc := range []struct {
		name         string
		path         string
		wantStatus   int
		wantLocation string
	}{
		{name: "mount without slash", path: "/web", wantStatus: http.StatusMovedPermanently, wantLocation: "http://example.com/web/"},
		{name: "mount keeps query", path: "/web?a=b", wantStatus: http.StatusMovedPermanently, wantLocation: "http://example.com/web/?a=b"},
		{name: "root redirects to relative web", path: "/", wantStatus: http.StatusFound, wantLocation: "web/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := issueWebRequest(t, rt, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLocation {
				t.Errorf("GET %s Location = %q, want %q", tc.path, got, tc.wantLocation)
			}
		})
	}
}

// TestWebDirectoryWithoutIndexReturns404 locks in upstream's UseDefaultFiles
// semantics: a directory request is served or slash-redirected only when a
// default index.html file exists. A directory with no index.html answers 404
// for both the slashed and unslashed request, never a 301 redirect. The
// pinned Jellyfin image returns 404 for /web/<no-index-dir> (verified
// against the stock 12.0.0 container), not a slash redirect.
func TestWebDirectoryWithoutIndexReturns404(t *testing.T) {
	rt, webPath := newWebRouter(t)
	writeWebAsset(t, webPath, "empty-subdir/.keep", "")

	for _, path := range []string{"/web/empty-subdir", "/web/empty-subdir/"} {
		rec := issueWebRequest(t, rt, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (directory with no index.html redirects/serves nothing upstream)", path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("GET %s emitted Location %q, want none", path, loc)
		}
	}
}

func TestWebRefusesPathTraversal(t *testing.T) {
	rt, webPath := newWebRouter(t)

	// The secret sits outside the web root; a traversal escape would reach it.
	secret := filepath.Join(filepath.Dir(webPath), "secret.txt")
	if err := os.WriteFile(secret, []byte("outside the web root"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "plain dot-dot", path: "/web/../secret.txt"},
		{name: "deep plain dot-dot", path: "/web/../../../../etc/passwd"},
		{name: "encoded dot-dot", path: "/web/%2e%2e/secret.txt"},
		{name: "deep encoded dot-dot", path: "/web/%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://deadfin.test"+tc.path, nil)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want 404 or 400", tc.path, rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, "outside the web root") || strings.Contains(body, "root:") {
				t.Fatalf("GET %s leaked file contents: %q", tc.path, body)
			}
		})
	}
}

func TestWebRoutesAbsentWithoutAssetDirectory(t *testing.T) {
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("derive paths: %v", err)
	}
	rt := router.New()
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), completedWebStartup{}, handlers.SystemInfoConfig{
		Paths:   paths,
		WebPath: filepath.Join(paths.Root, "web"), // does not exist
	})

	for _, route := range rt.Routes() {
		if strings.HasPrefix(route.Pattern, "/web") || route.Pattern == "/" {
			t.Fatalf("web route %#v registered although the asset directory is missing", route)
		}
	}
	for _, path := range []string{"/", "/web", "/web/", "/web/index.html"} {
		rec := issueWebRequest(t, rt, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404 with no web routes", path, rec.Code)
		}
	}
}

// TestWebRouteLedgerRegistersConditionalWebOperations is the route-ledger
// fixture for the conditional /web routes. The
// asset-less exported ledger (cmd/deadfin routeLedger -> newRouter with empty
// paths) is unchanged by design — registerWeb is a no-op when no web directory
// exists (see TestWebRoutesAbsentWithoutAssetDirectory), so the exact-set
// ledger in composition_test.go's assertCompositionRoutes is unaffected. When
// a web directory is present, the three conditional web operations enter the
// route table with the metadata below; this test asserts that ledger entry.
func TestWebRouteLedgerRegistersConditionalWebOperations(t *testing.T) {
	rt, _ := newWebRouter(t)

	want := map[string]router.Route{
		"GET /":           {OperationID: "SHADOW:RedirectRootToWebClient", Anonymous: true},
		"GET /web":        {OperationID: "SHADOW:RedirectWebClientRoot", Anonymous: true},
		"GET /web/*":      {OperationID: "SHADOW:GetWebClientResource", Anonymous: true},
		"GET /robots.txt": {OperationID: "SHADOW:RedirectRobotsTxt", Anonymous: true},
	}
	got := make(map[string]router.Route)
	for _, route := range rt.Routes() {
		key := route.Method + " " + route.Pattern
		if _, ok := want[key]; ok {
			got[key] = router.Route{OperationID: route.OperationID, Policy: route.Policy, Anonymous: route.Anonymous}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("conditional web route ledger = %v, want all of %v (missing web routes from the route table)", got, want)
	}
	for key, wantRoute := range want {
		if gotRoute := got[key]; gotRoute != wantRoute {
			t.Errorf("conditional web route %s = %#v, want %#v", key, gotRoute, wantRoute)
		}
	}
}

// TestWebRedirectSlashWithPathBase verifies that under a non-empty BaseUrl,
// redirectSlash prepends the configured prefix so /jf/web redirects to
// http://host/jf/web/, with query preserved.
func TestWebRedirectSlashWithPathBase(t *testing.T) {
	rt, _ := newWebRouter(t)

	mounted := router.Mount("/jf", rt)
	rec := issueWebRequest(t, mounted, "/jf/web")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	wantLoc := "http://example.com/jf/web/"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
}

// TestWebRedirectSlashWithPathBasePreservesQuery verifies the query is
// preserved under a base path.
func TestWebRedirectSlashWithPathBasePreservesQuery(t *testing.T) {
	rt, _ := newWebRouter(t)
	mounted := router.Mount("/jf", rt)

	rec := issueWebRequest(t, mounted, "/jf/web?a=b")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	wantLoc := "http://example.com/jf/web/?a=b"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
}

// TestRobotsRedirectReturnsLiteralWebRobotsTxt verifies the /robots.txt route
// returns 302 with literal relative Location: web/robots.txt.
func TestRobotsRedirectReturnsLiteralWebRobotsTxt(t *testing.T) {
	rt, _ := newWebRouter(t)

	rec := issueWebRequest(t, rt, "/robots.txt")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	wantLoc := "web/robots.txt"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
}

// TestRobotsRedirectUnderBasePath verifies that under a base, /jf/robots.txt
// resolves the relative target to /jf/web/robots.txt.
func TestRobotsRedirectUnderBasePath(t *testing.T) {
	rt, _ := newWebRouter(t)
	mounted := router.Mount("/jf", rt)

	rec := issueWebRequest(t, mounted, "/jf/robots.txt")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	wantLoc := "web/robots.txt"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
}

// TestRobotsRouteAbsentWithoutWebDirectory verifies robots.txt is not
// registered when no web directory exists.
func TestRobotsRouteAbsentWithoutWebDirectory(t *testing.T) {
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("derive paths: %v", err)
	}
	rt := router.New()
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), completedWebStartup{}, handlers.SystemInfoConfig{
		Paths: paths,
	})

	rec := issueWebRequest(t, rt, "/robots.txt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with no web directory", rec.Code)
	}
}

// TestWebHardeningHeaders checks that every /web/ response carries
// Referrer-Policy and nosniff, and that the Content-Security-Policy is sent
// only when the client is deadfin-web (divergence D-68).
func TestWebHardeningHeaders(t *testing.T) {
	const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	for _, tc := range []struct {
		component string
		wantCSP   string
	}{
		{component: "deadfin-web", wantCSP: wantCSP},
		{component: "", wantCSP: ""},
		{component: "other-web", wantCSP: ""},
	} {
		t.Run("component="+tc.component, func(t *testing.T) {
			paths, err := config.NewPaths(t.TempDir())
			if err != nil {
				t.Fatalf("derive paths: %v", err)
			}
			webPath := filepath.Join(paths.Root, "web")
			writeWebAsset(t, webPath, "index.html", webIndexHTML)
			writeWebAsset(t, webPath, "main.js", "console.log('deadfin');")
			rt := router.New()
			handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), completedWebStartup{}, handlers.SystemInfoConfig{
				Paths: paths, WebPath: webPath, WebComponent: tc.component,
			})
			for _, path := range []string{"/web/", "/web/index.html", "/web/main.js", "/web/missing.js"} {
				rec := issueWebRequest(t, rt, path)
				if got := rec.Header().Get("Content-Security-Policy"); got != tc.wantCSP {
					t.Errorf("GET %s Content-Security-Policy = %q, want %q", path, got, tc.wantCSP)
				}
				if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
					t.Errorf("GET %s Referrer-Policy = %q, want no-referrer", path, got)
				}
				if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", path, got)
				}
			}
		})
	}
}
