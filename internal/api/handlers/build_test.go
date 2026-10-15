// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

const buildTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testBuildInfo() dto.BuildInfo {
	return dto.BuildInfo{
		Component:           "deadfin-server",
		Version:             "v9.9.9",
		Channel:             "release",
		Revision:            buildTestSHA,
		Dirty:               false,
		BuildTime:           "2099-01-01T00:00:00Z",
		CompatibilityTarget: "12.0.0",
		APIVersion:          1,
		Capabilities:        []string{"build-info"},
	}
}

const (
	wantBuildPascal = `{"Component":"deadfin-server","Version":"v9.9.9","Channel":"release","Revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Dirty":false,"BuildTime":"2099-01-01T00:00:00Z","CompatibilityTarget":"12.0.0","ApiVersion":1,"Capabilities":["build-info"]}`
	wantBuildCamel  = `{"component":"deadfin-server","version":"v9.9.9","channel":"release","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dirty":false,"buildTime":"2099-01-01T00:00:00Z","compatibilityTarget":"12.0.0","apiVersion":1,"capabilities":["build-info"]}`
)

func serve(t *testing.T, h http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for key, values := range header {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBuildRouteMetadata(t *testing.T) {
	rt := router.New()
	handlers.RegisterBuild(rt, testBuildInfo)
	routes := rt.Routes()
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	want := router.Route{Method: http.MethodGet, Pattern: "/deadfin/v1/build", OperationID: "NATIVE:GetBuild", Anonymous: true}
	if routes[0] != want {
		t.Fatalf("route = %+v, want %+v", routes[0], want)
	}
}

func TestBuildRendersPascalAndCamel(t *testing.T) {
	rt := router.New()
	handlers.RegisterBuild(rt, testBuildInfo)

	rec := serve(t, rt, "/deadfin/v1/build", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != wantBuildPascal {
		t.Fatalf("pascal: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("pascal content type = %q", got)
	}

	rec = serve(t, rt, "/deadfin/v1/build", http.Header{"Accept": {`application/json; profile="CamelCase"`}})
	if rec.Code != http.StatusOK || rec.Body.String() != wantBuildCamel {
		t.Fatalf("camel: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != `application/json; profile="CamelCase"; charset=utf-8` {
		t.Fatalf("camel content type = %q", got)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/deadfin/v1/build", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

type buildDenyResolver struct{}

func (buildDenyResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return auth.Identity{}, nil
}

type buildDenyEvaluator struct{}

func (buildDenyEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionUnauthenticated, nil
}

type buildNoUsers struct{}

func (buildNoUsers) Get(context.Context, dto.UUID) (store.User, error) {
	return store.User{}, store.ErrNotFound
}

func TestBuildIsAnonymousBehindAuthorizer(t *testing.T) {
	authorizer := middleware.NewAuthorizer(buildDenyResolver{}, buildDenyEvaluator{}, buildNoUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterBuild(rt, testBuildInfo)
	handlers.RegisterSystem(rt, config.DefaultServerIdentity(), buildStartup{})

	if rec := serve(t, rt, "/deadfin/v1/build", nil); rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated build = %d, want 200", rec.Code)
	}
	// A non-anonymous route through the same authorizer is refused, so the
	// 200 above is the Anonymous flag, not a permissive authorizer.
	if rec := serve(t, rt, "/System/Info", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /System/Info = %d, want 401", rec.Code)
	}
}

type buildStartup struct{}

func (buildStartup) StartupWizardCompleted() bool { return true }

func TestBuildUnderBaseURLMount(t *testing.T) {
	rt := router.New()
	handlers.RegisterBuild(rt, testBuildInfo)
	handlers.RegisterSystem(rt, config.DefaultServerIdentity(), buildStartup{})
	mounted := router.Mount("/media", rt)

	for _, path := range []string{"/deadfin/v1/build", "/System/Info/Public"} {
		prefixed := serve(t, mounted, "/media"+path, nil)
		bare := serve(t, mounted, path, nil)
		if prefixed.Code != http.StatusOK {
			t.Fatalf("GET /media%s = %d, want 200", path, prefixed.Code)
		}
		if bare.Code != http.StatusFound {
			t.Fatalf("GET %s = %d, want redirect under base /media", path, bare.Code)
		}
	}
	native := serve(t, mounted, "/deadfin/v1/build", nil)
	public := serve(t, mounted, "/System/Info/Public", nil)
	if native.Code != public.Code {
		t.Fatalf("unprefixed native = %d, unprefixed public = %d; must match", native.Code, public.Code)
	}
	if native.Header().Get("Location") != public.Header().Get("Location") {
		t.Fatalf("unprefixed redirect targets differ: native %q, public %q", native.Header().Get("Location"), public.Header().Get("Location"))
	}
}

func TestBuildNoCacheWrapper(t *testing.T) {
	rt := router.New(middleware.NoCache("NATIVE:GetBuild"))
	handlers.RegisterBuild(rt, testBuildInfo)
	handlers.RegisterSystem(rt, config.DefaultServerIdentity(), buildStartup{})

	if got := serve(t, rt, "/deadfin/v1/build", nil).Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if got := serve(t, rt, "/System/Info/Public", nil).Header().Get("Cache-Control"); got != "" {
		t.Fatalf("unlisted route Cache-Control = %q, want empty", got)
	}
}

func TestBuildCallsProviderPerRequest(t *testing.T) {
	calls := 0
	var last dto.BuildInfo
	rt := router.New()
	handlers.RegisterBuild(rt, func() dto.BuildInfo {
		calls++
		last = testBuildInfo()
		return last
	})

	serve(t, rt, "/deadfin/v1/build", nil)
	last.Capabilities[0] = "mutated"
	rec := serve(t, rt, "/deadfin/v1/build", nil)
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
	var body dto.BuildInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Capabilities) != 1 || body.Capabilities[0] != "build-info" {
		t.Fatalf("second response Capabilities = %v; mutation of the first leaked", body.Capabilities)
	}
}
