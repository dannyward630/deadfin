// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

func TestPluginRoutesDeclarePinnedOperationsAndPolicies(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/web/ConfigurationPage", OperationID: "GetDashboardConfigurationPage", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/web/ConfigurationPages", OperationID: "GetConfigurationPages", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Packages", OperationID: "GetPackages", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Packages/{name}", OperationID: "GetPackageInfo", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Packages/Installed/{name}", OperationID: "InstallPackage", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodDelete, Pattern: "/Packages/Installing/{packageId}", OperationID: "CancelPackageInstallation", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Repositories", OperationID: "GetRepositories", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Repositories", OperationID: "SetRepositories", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Plugins", OperationID: "GetPlugins", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodDelete, Pattern: "/Plugins/{pluginId}", OperationID: "UninstallPlugin", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodDelete, Pattern: "/Plugins/{pluginId}/{version}", OperationID: "UninstallPluginByVersion", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Plugins/{pluginId}/{version}/Disable", OperationID: "DisablePlugin", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Plugins/{pluginId}/{version}/Enable", OperationID: "EnablePlugin", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Plugins/{pluginId}/{version}/Image", OperationID: "GetPluginImage", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Plugins/{pluginId}/Configuration", OperationID: "GetPluginConfiguration", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Plugins/{pluginId}/Configuration", OperationID: "UpdatePluginConfiguration", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Plugins/{pluginId}/Manifest", OperationID: "GetPluginManifest", Policy: string(auth.PolicyRequiresElevation)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got != want {
				t.Errorf("route %q = %#v, want %#v", want.OperationID, got, want)
			}
		})
	}
}

func TestPluginListEndpointsReturnEmptyArrays(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, path := range []string{"/Packages", "/Repositories"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d; body=%q", path, recorder.Code, http.StatusOK, recorder.Body.String())
			}
			var values []json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &values); err != nil {
				t.Fatalf("GET %s response is not an array: %v; body=%q", path, err, recorder.Body.String())
			}
			if values == nil || len(values) != 0 {
				t.Errorf("GET %s response = %s, want []", path, recorder.Body.String())
			}
		})
	}
}

// TestPluginsReportsFourInTreeProviderIdentities verifies that GET /Plugins
// returns the four stable in-tree metadata provider identities with the
// upstream wire shape.
func TestPluginsReportsFourInTreeProviderIdentities(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Plugins", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /Plugins status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var plugins []map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &plugins); err != nil {
		t.Fatalf("GET /Plugins response is not an array: %v; body=%q", err, recorder.Body.String())
	}
	if len(plugins) != 4 {
		t.Fatalf("GET /Plugins returned %d plugins, want 4; body=%s", len(plugins), recorder.Body.String())
	}

	wantNames := map[string]bool{
		"TMDb":                             false,
		"MusicBrainz":                      false,
		"AudioDB":                          false,
		"ListenBrainz Similarity Provider": false,
	}
	wantIDs := map[string]string{
		"TMDb":                             "b8715ed16c4745289ad3f72deb539cd4",
		"MusicBrainz":                      "8c95c4d2e50c4fb0a4f36c06ff0f9a1a",
		"AudioDB":                          "a629c0dafac54c7e931a7174223f14c8",
		"ListenBrainz Similarity Provider": "a5b2e8c19d4f4a3b8c7e6f1a2b3c4d5e",
	}
	for _, plugin := range plugins {
		name, err := json.Marshal(plugin["Name"])
		if err != nil {
			t.Fatalf("marshal plugin Name: %v", err)
		}
		var nameStr string
		if err := json.Unmarshal(name, &nameStr); err != nil {
			t.Fatalf("unmarshal plugin Name: %v", err)
		}
		if _, ok := wantNames[nameStr]; !ok {
			t.Errorf("unexpected plugin Name %q; body=%s", nameStr, recorder.Body.String())
			continue
		}
		wantNames[nameStr] = true

		id, err := json.Marshal(plugin["Id"])
		if err != nil {
			t.Fatalf("marshal plugin Id: %v", err)
		}
		var idStr string
		if err := json.Unmarshal(id, &idStr); err != nil {
			t.Fatalf("unmarshal plugin Id: %v", err)
		}
		if idStr != wantIDs[nameStr] {
			t.Errorf("plugin %q Id = %q, want %q", nameStr, idStr, wantIDs[nameStr])
		}

		if canUninstall, _ := json.Marshal(plugin["CanUninstall"]); string(canUninstall) != "false" {
			t.Errorf("plugin %q CanUninstall = %s, want false", nameStr, string(canUninstall))
		}
		if status, _ := json.Marshal(plugin["Status"]); string(status) != `"Active"` {
			t.Errorf("plugin %q Status = %s, want \"Active\"", nameStr, string(status))
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("plugin %q missing from GET /Plugins response; body=%s", name, recorder.Body.String())
		}
	}
}

// TestConfigurationPagesReportsOnePagePerProvider verifies that
// GET /web/ConfigurationPages returns one page per in-tree provider.
func TestConfigurationPagesReportsOnePagePerProvider(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPages", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /web/ConfigurationPages status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var pages []map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &pages); err != nil {
		t.Fatalf("GET /web/ConfigurationPages response is not an array: %v; body=%q", err, recorder.Body.String())
	}
	if len(pages) != 4 {
		t.Fatalf("GET /web/ConfigurationPages returned %d pages, want 4; body=%s", len(pages), recorder.Body.String())
	}

	wantNames := map[string]bool{
		"TMDb":                             false,
		"MusicBrainz":                      false,
		"AudioDB":                          false,
		"ListenBrainz Similarity Provider": false,
	}
	wantPluginIDs := map[string]string{
		"TMDb":                             "b8715ed16c4745289ad3f72deb539cd4",
		"MusicBrainz":                      "8c95c4d2e50c4fb0a4f36c06ff0f9a1a",
		"AudioDB":                          "a629c0dafac54c7e931a7174223f14c8",
		"ListenBrainz Similarity Provider": "a5b2e8c19d4f4a3b8c7e6f1a2b3c4d5e",
	}
	for _, page := range pages {
		name, err := json.Marshal(page["Name"])
		if err != nil {
			t.Fatalf("marshal page Name: %v", err)
		}
		var nameStr string
		if err := json.Unmarshal(name, &nameStr); err != nil {
			t.Fatalf("unmarshal page Name: %v", err)
		}
		if _, ok := wantNames[nameStr]; !ok {
			t.Errorf("unexpected page Name %q; body=%s", nameStr, recorder.Body.String())
			continue
		}
		wantNames[nameStr] = true

		pluginID, err := json.Marshal(page["PluginId"])
		if err != nil {
			t.Fatalf("marshal page PluginId: %v", err)
		}
		var pluginIDStr string
		if err := json.Unmarshal(pluginID, &pluginIDStr); err != nil {
			t.Fatalf("unmarshal page PluginId: %v", err)
		}
		if pluginIDStr != wantPluginIDs[nameStr] {
			t.Errorf("page %q PluginId = %q, want %q", nameStr, pluginIDStr, wantPluginIDs[nameStr])
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("page %q missing from GET /web/ConfigurationPages response; body=%s", name, recorder.Body.String())
		}
	}
}

// TestConfigurationPageServesPinnedHTMLOnePerPage verifies that each advertised
// configuration page returns 200, text/html, and non-empty pinned config.html
// content.
func TestConfigurationPageServesPinnedHTMLOnePerPage(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, name := range []string{
		"TMDb",
		"MusicBrainz",
		"AudioDB",
		"ListenBrainz Similarity Provider",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			path := "/web/ConfigurationPage?name=" + urlQueryEscape(name)
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%q", path, recorder.Code, recorder.Body.String())
			}
			contentType := recorder.Header().Get("Content-Type")
			if !strings.HasPrefix(contentType, "text/html") {
				t.Fatalf("GET %s Content-Type = %q, want text/html", path, contentType)
			}
			if recorder.Body.Len() == 0 {
				t.Fatalf("GET %s body is empty; want non-empty pinned config.html", path)
			}
			if !strings.Contains(recorder.Body.String(), "<!DOCTYPE html>") {
				t.Fatalf("GET %s body does not start with DOCTYPE; body starts with %q", path, recorder.Body.String()[:min(50, recorder.Body.Len())])
			}
		})
	}
}

// TestConfigurationPageRejectsUnknownName verifies that an unknown page name
// returns 404 ProblemDetails.
func TestConfigurationPageRejectsUnknownName(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?name=Unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /web/ConfigurationPage?name=Unknown status = %d, want 404; body=%q", recorder.Code, recorder.Body.String())
	}
	assertProblemDetailsWireCasing(t, recorder.Body.Bytes(), http.StatusNotFound)
}

// TestConfigurationPageUsesScalarQueryBinding verifies that the configuration
// page handler uses the shared scalar query binder, so mixed-case keys select
// the same page and conflicting case aliases return 400 Bad Request.
func TestConfigurationPageUsesScalarQueryBinding(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	t.Run("uppercase key selects the requested page", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?NAME=TMDb", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET ?NAME=TMDb status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
		}
		contentType := recorder.Header().Get("Content-Type")
		if !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("GET ?NAME=TMDb Content-Type = %q, want text/html", contentType)
		}
		if !strings.Contains(recorder.Body.String(), "<!DOCTYPE html>") {
			t.Fatalf("GET ?NAME=TMDb body does not start with DOCTYPE; body starts with %q", recorder.Body.String()[:min(50, recorder.Body.Len())])
		}
	})

	t.Run("lowercase and uppercase keys return the same page", func(t *testing.T) {
		lower := httptest.NewRecorder()
		rt.ServeHTTP(lower, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?name=TMDb", nil))
		if lower.Code != http.StatusOK {
			t.Fatalf("GET ?name=TMDb status = %d, want 200; body=%q", lower.Code, lower.Body.String())
		}
		upper := httptest.NewRecorder()
		rt.ServeHTTP(upper, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?NAME=TMDb", nil))
		if upper.Code != http.StatusOK {
			t.Fatalf("GET ?NAME=TMDb status = %d, want 200; body=%q", upper.Code, upper.Body.String())
		}
		if lower.Body.String() != upper.Body.String() {
			t.Fatalf("lower and upper key responses differ; lower starts with %q, upper starts with %q", lower.Body.String()[:min(50, lower.Body.Len())], upper.Body.String()[:min(50, upper.Body.Len())])
		}
	})

	t.Run("conflicting case aliases return 400", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?name=TMDb&NAME=AudioDB", nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET ?name=TMDb&NAME=AudioDB status = %d, want 400; body=%q", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "<!DOCTYPE html>") {
			t.Fatalf("GET ?name=TMDb&NAME=AudioDB returned a configuration page; body starts with %q", recorder.Body.String()[:min(50, recorder.Body.Len())])
		}
	})

	t.Run("missing name returns 404", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET /web/ConfigurationPage (no name) status = %d, want 404; body=%q", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("blank name returns 404", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?name=%20", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET /web/ConfigurationPage?name=%%20 status = %d, want 404; body=%q", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("unknown name returns 404", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/web/ConfigurationPage?name=Unknown", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET /web/ConfigurationPage?name=Unknown status = %d, want 404; body=%q", recorder.Code, recorder.Body.String())
		}
	})
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(s, " ", "%20")
}

func TestPluginInstallAndVersionedUninstallRejectUnavailableSupport(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/Packages/Installed/example", nil),
		httptest.NewRequest(http.MethodDelete, "/Plugins/11111111-1111-1111-1111-111111111111/1.0.0", nil),
	} {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d; body=%q", request.Method, request.URL.Path, recorder.Code, http.StatusNotFound, recorder.Body.String())
		}
		problem := assertProblemDetailsWireCasing(t, recorder.Body.Bytes(), http.StatusNotFound)
		if !strings.Contains(strings.ToLower(problem["detail"]), "plugin support is not available") {
			t.Errorf("%s %s detail = %q, want unavailable detail", request.Method, request.URL.Path, problem["detail"])
		}
	}
}

func TestMissingPackageAndPluginImageReturnProblemDetailsNotFound(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, path := range []string{
		"/Packages/example",
		"/Plugins/11111111-1111-1111-1111-111111111111/1.0.0/Image",
	} {
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want %d; body=%q", path, recorder.Code, http.StatusNotFound, recorder.Body.String())
		}
		problem := assertProblemDetailsWireCasing(t, recorder.Body.Bytes(), http.StatusNotFound)
		if problem["detail"] == "" {
			t.Errorf("GET %s response = %q, want populated 404 ProblemDetails", path, recorder.Body.String())
		}
	}
}

func TestPluginRepositoryWritesAndInstallCancellationAreNoContent(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlugins(rt)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/Repositories", strings.NewReader("[]")),
		httptest.NewRequest(http.MethodDelete, "/Packages/Installing/11111111-1111-1111-1111-111111111111", nil),
	} {
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
			t.Errorf("%s %s = status %d body %q, want 204 with empty body", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
}

// assertProblemDetailsWireCasing validates that a ProblemDetails body uses the
// pinned lowercase wire keys (title, status, detail) and has no PascalCase
// equivalents. Decoding into a map asserts exact JSON key casing instead of
// relying on case-insensitive Go struct field matching.
func assertProblemDetailsWireCasing(t *testing.T, body []byte, wantStatus int) map[string]string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("response is not ProblemDetails JSON: %v; body=%q", err, body)
	}
	for _, pascal := range []string{"Title", "Status", "Detail"} {
		if _, ok := raw[pascal]; ok {
			t.Fatalf("ProblemDetails body has PascalCase key %q; want lowercase; body=%s", pascal, body)
		}
	}
	problem := make(map[string]string, 3)
	for _, key := range []string{"title", "status", "detail"} {
		value, ok := raw[key]
		if !ok {
			t.Fatalf("ProblemDetails body lacks lowercase key %q; body=%s", key, body)
		}
		if key == "status" {
			var status int
			if err := json.Unmarshal(value, &status); err != nil {
				t.Fatalf("decode ProblemDetails status: %v; body=%s", err, body)
			}
			if status != wantStatus {
				t.Fatalf("ProblemDetails status = %d, want %d; body=%s", status, wantStatus, body)
			}
			problem[key] = strconv.Itoa(status)
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			t.Fatalf("decode ProblemDetails %q: %v; body=%s", key, err, body)
		}
		problem[key] = text
	}
	return problem
}
