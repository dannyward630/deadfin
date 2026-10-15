// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestPublicSystemInfoIsAnonymousAndNegotiatesCasing(t *testing.T) {
	t.Parallel()

	rt := newSystemRouter(t)
	cases := []struct {
		name          string
		accept        string
		authorization string
		localAddress  string
		fields        []string
		wrongFields   []string
	}{
		{
			name:         "pascal without credentials",
			localAddress: "http://pascal-anonymous.deadfin.test:8096",
			fields: []string{
				"LocalAddress", "ServerName", "Version", "ProductName", "OperatingSystem", "Id", "StartupWizardCompleted",
			},
			wrongFields: []string{
				"localAddress", "serverName", "version", "productName", "operatingSystem", "id", "startupWizardCompleted",
			},
		},
		{
			name:          "pascal with credentials",
			authorization: "MediaBrowser Token=not-required",
			localAddress:  "http://pascal-authenticated.deadfin.test:8097",
			fields: []string{
				"LocalAddress", "ServerName", "Version", "ProductName", "OperatingSystem", "Id", "StartupWizardCompleted",
			},
			wrongFields: []string{
				"localAddress", "serverName", "version", "productName", "operatingSystem", "id", "startupWizardCompleted",
			},
		},
		{
			name:         "camel without credentials",
			accept:       `application/json; profile="CamelCase"`,
			localAddress: "http://camel-anonymous.deadfin.test:8098",
			fields: []string{
				"localAddress", "serverName", "version", "productName", "operatingSystem", "id", "startupWizardCompleted",
			},
			wrongFields: []string{
				"LocalAddress", "ServerName", "Version", "ProductName", "OperatingSystem", "Id", "StartupWizardCompleted",
			},
		},
		{
			name:          "camel with credentials",
			accept:        `application/json; profile="CamelCase"`,
			authorization: "MediaBrowser Token=not-required",
			localAddress:  "http://camel-authenticated.deadfin.test:8099",
			fields: []string{
				"localAddress", "serverName", "version", "productName", "operatingSystem", "id", "startupWizardCompleted",
			},
			wrongFields: []string{
				"LocalAddress", "ServerName", "Version", "ProductName", "OperatingSystem", "Id", "StartupWizardCompleted",
			},
		},
	}

	var stable map[string]string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := issueRequest(rt, http.MethodGet, tc.localAddress+"/System/Info/Public", tc.accept, tc.authorization)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /System/Info/Public status = %d, want %d", rec.Code, http.StatusOK)
			}
			wantType := "application/json; charset=utf-8"
			if tc.accept != "" {
				wantType = `application/json; profile="CamelCase"; charset=utf-8`
			}
			if got := rec.Header().Get("Content-Type"); got != wantType {
				t.Errorf("GET /System/Info/Public Content-Type = %q, want %q", got, wantType)
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET /System/Info/Public response is not JSON: %v; body=%q", err, rec.Body.String())
			}
			if len(body) != len(tc.fields) {
				t.Errorf("response has %d fields, want exact seven-field public system shape: %s", len(body), rec.Body.String())
			}

			for _, field := range tc.fields {
				if _, ok := body[field]; !ok {
					t.Errorf("response missing %q: %s", field, rec.Body.String())
				}
			}
			for _, field := range tc.wrongFields {
				if _, ok := body[field]; ok {
					t.Errorf("response unexpectedly contains %q: %s", field, rec.Body.String())
				}
			}
			assertJSONFields(t, body, map[string]any{
				tc.fields[0]: tc.localAddress,
				tc.fields[3]: "Jellyfin Server",
				tc.fields[4]: "",
				tc.fields[6]: true,
			})

			identity := make(map[string]string, 3)
			for _, index := range []int{1, 2, 5} {
				field := tc.fields[index]
				value, ok := body[field]
				if !ok {
					continue
				}
				var decoded string
				if err := json.Unmarshal(value, &decoded); err != nil {
					t.Errorf("response field %q is not a JSON string: %v", field, err)
					continue
				}
				identity[field] = decoded
				if identity[field] == "" {
					t.Errorf("response field %q is empty", field)
				}
			}

			if stable == nil {
				stable = identity
				return
			}
			for _, index := range []int{1, 2, 5} {
				if got, want := identity[tc.fields[index]], stable[cases[0].fields[index]]; got != want {
					t.Errorf("%q = %q, want stable value %q", tc.fields[index], got, want)
				}
			}
		})
	}

	assertCompatibleVersion(t, stable["Version"])
}

func TestSystemInfoRequiresAuthenticationAndProjectsFullSystemInfo(t *testing.T) {
	t.Parallel()

	rt, infoConfig := newAuthenticatedSystemRouter(t)
	wantRoute := router.Route{
		Method:      http.MethodGet,
		Pattern:     "/System/Info",
		OperationID: "GetSystemInfo",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}
	if got, found := routeByOperation(rt.Routes(), wantRoute.OperationID); !found {
		t.Fatalf("operation %q is not registered", wantRoute.OperationID)
	} else if got.Method != wantRoute.Method || got.Pattern != wantRoute.Pattern || got.OperationID != wantRoute.OperationID || got.Policy != wantRoute.Policy || got.Anonymous {
		t.Fatalf("system-info route = %#v, want %#v and non-anonymous", got, wantRoute)
	}

	unauthenticated := issueSystemInfoRequest(rt, "http://system-info.deadfin.test/System/Info", "", false)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /System/Info status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	publicFields := []string{"LocalAddress", "ServerName", "Version", "ProductName", "OperatingSystem", "Id", "StartupWizardCompleted"}
	fullOnlyFields := []string{
		"OperatingSystemDisplayName", "HasPendingRestart", "IsShuttingDown", "SupportsLibraryMonitor", "WebSocketPortNumber", "CompletedInstallations", "CanSelfRestart", "CanSelfUpdate", "CanLaunchWebBrowser", "ProgramDataPath", "WebPath", "ItemsByNamePath", "CachePath", "LogPath", "InternalMetadataPath", "TranscodingTempPath", "CastReceiverApplications", "HasUpdateAvailable", "EncoderLocation", "SystemArchitecture",
	}
	fullDefaults := map[string]any{
		"OperatingSystemDisplayName": "",
		"HasPendingRestart":          false,
		"IsShuttingDown":             false,
		"SupportsLibraryMonitor":     false,
		"WebSocketPortNumber":        8096,
		"CompletedInstallations":     []any{},
		"CanSelfRestart":             false,
		"CanSelfUpdate":              false,
		"CanLaunchWebBrowser":        false,
		"ProgramDataPath":            infoConfig.Paths.Root,
		"WebPath":                    filepath.Join(infoConfig.Paths.Root, "web"),
		"ItemsByNamePath":            infoConfig.Paths.Metadata,
		"CachePath":                  infoConfig.Paths.Cache,
		"LogPath":                    infoConfig.Paths.Log,
		"InternalMetadataPath":       infoConfig.Paths.Metadata,
		"TranscodingTempPath":        infoConfig.Paths.Transcodes,
		"CastReceiverApplications":   []any{},
		"HasUpdateAvailable":         false,
		"EncoderLocation":            "System",
		"SystemArchitecture":         runtimeSystemArchitecture(),
	}

	for _, tc := range []struct {
		name        string
		accept      string
		contentType string
		camel       bool
	}{
		{name: "Pascal", contentType: "application/json; charset=utf-8"},
		{name: "CamelCase", accept: `application/json; profile="CamelCase"`, contentType: `application/json; profile="CamelCase"; charset=utf-8`, camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const address = "http://system-info.deadfin.test"
			public := issueRequest(rt, http.MethodGet, address+"/System/Info/Public", tc.accept, "")
			if public.Code != http.StatusOK {
				t.Fatalf("GET /System/Info/Public status = %d, want %d", public.Code, http.StatusOK)
			}
			var publicBody map[string]json.RawMessage
			if err := json.Unmarshal(public.Body.Bytes(), &publicBody); err != nil {
				t.Fatalf("GET /System/Info/Public response is not JSON: %v; body=%q", err, public.Body.String())
			}
			if len(publicBody) != len(publicFields) {
				t.Fatalf("GET /System/Info/Public response has %d fields, want distinct %d-field public shape: %s", len(publicBody), len(publicFields), public.Body.String())
			}

			rec := issueSystemInfoRequest(rt, address+"/System/Info", tc.accept, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("authenticated GET /System/Info status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("GET /System/Info Content-Type = %q, want %q", got, tc.contentType)
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET /System/Info response is not JSON: %v; body=%q", err, rec.Body.String())
			}
			if got, want := len(body), len(publicFields)+len(fullOnlyFields); got != want {
				t.Errorf("GET /System/Info response has %d fields, want exact %d-field SystemInfo shape: %s", got, want, rec.Body.String())
			}
			for _, field := range publicFields {
				key := systemInfoKey(field, tc.camel)
				if got, found := body[key]; !found {
					t.Errorf("GET /System/Info response missing public field %q: %s", key, rec.Body.String())
				} else if want, found := publicBody[key]; !found {
					t.Errorf("GET /System/Info/Public response missing public field %q: %s", key, public.Body.String())
				} else if string(got) != string(want) {
					t.Errorf("GET /System/Info public field %q = %s, want existing public value %s", key, got, want)
				}
			}
			expectedDefaults := make(map[string]any, len(fullDefaults))
			for field, value := range fullDefaults {
				expectedDefaults[systemInfoKey(field, tc.camel)] = value
			}
			assertJSONFields(t, body, expectedDefaults)
			if key := systemInfoKey("PackageName", tc.camel); body[key] != nil {
				t.Errorf("GET /System/Info response unexpectedly contains omitted stock field %q: %s", key, rec.Body.String())
			}
			for _, field := range append(publicFields, fullOnlyFields...) {
				wrongKey := systemInfoKey(field, !tc.camel)
				if _, found := body[wrongKey]; found {
					t.Errorf("GET /System/Info response unexpectedly contains %q: %s", wrongKey, rec.Body.String())
				}
			}
		})
	}
}

func TestSystemInfoProjectsConfiguredRuntimePathsAndCastReceiversInBothProfiles(t *testing.T) {
	t.Parallel()

	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "configured-program-data"))
	if err != nil {
		t.Fatalf("derive configured runtime paths: %v", err)
	}
	webPath := filepath.Join(t.TempDir(), "configured-web-assets")
	receivers := []config.CastReceiverApplication{
		{ID: "F007D354", Name: "Stable"},
		{ID: "6F511C87", Name: "Unstable"},
	}

	for _, tc := range []struct {
		name         string
		accept       string
		camel        bool
		applications func() []config.CastReceiverApplication
		want         []config.CastReceiverApplication
	}{
		{name: "Pascal nil accessor", want: []config.CastReceiverApplication{}},
		{name: "Pascal nil configured value", applications: func() []config.CastReceiverApplication { return nil }, want: []config.CastReceiverApplication{}},
		{name: "Pascal empty configured value", applications: func() []config.CastReceiverApplication { return []config.CastReceiverApplication{} }, want: []config.CastReceiverApplication{}},
		{name: "Pascal configured receivers", applications: func() []config.CastReceiverApplication { return receivers }, want: receivers},
		{name: "CamelCase nil accessor", accept: `application/json; profile="CamelCase"`, camel: true, want: []config.CastReceiverApplication{}},
		{name: "CamelCase nil configured value", accept: `application/json; profile="CamelCase"`, camel: true, applications: func() []config.CastReceiverApplication { return nil }, want: []config.CastReceiverApplication{}},
		{name: "CamelCase empty configured value", accept: `application/json; profile="CamelCase"`, camel: true, applications: func() []config.CastReceiverApplication { return []config.CastReceiverApplication{} }, want: []config.CastReceiverApplication{}},
		{name: "CamelCase configured receivers", accept: `application/json; profile="CamelCase"`, camel: true, applications: func() []config.CastReceiverApplication { return receivers }, want: receivers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newAuthenticatedSystemRouterWithInfoConfig(t, handlers.SystemInfoConfig{
				Paths:                    paths,
				WebPath:                  webPath,
				CastReceiverApplications: tc.applications,
				SupportsLibraryMonitor:   true,
				WebSocketPortNumber:      19196,
			})

			rec := issueSystemInfoRequest(rt, "/System/Info", tc.accept, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("authenticated GET /System/Info status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode GET /System/Info response: %v; body: %s", err, rec.Body.String())
			}

			assertJSONFields(t, body, map[string]any{
				systemInfoKey("ProgramDataPath", tc.camel):          paths.Root,
				systemInfoKey("ItemsByNamePath", tc.camel):          paths.Metadata,
				systemInfoKey("InternalMetadataPath", tc.camel):     paths.Metadata,
				systemInfoKey("CachePath", tc.camel):                paths.Cache,
				systemInfoKey("LogPath", tc.camel):                  paths.Log,
				systemInfoKey("TranscodingTempPath", tc.camel):      paths.Transcodes,
				systemInfoKey("WebPath", tc.camel):                  webPath,
				systemInfoKey("CastReceiverApplications", tc.camel): systemInfoReceiverJSON(tc.want, tc.camel),
			})
		})
	}
}

func systemInfoReceiverJSON(applications []config.CastReceiverApplication, camel bool) []map[string]string {
	receivers := make([]map[string]string, len(applications))
	for index, application := range applications {
		receivers[index] = map[string]string{
			systemInfoKey("Id", camel):   application.ID,
			systemInfoKey("Name", camel): application.Name,
		}
	}
	return receivers
}

func systemInfoKey(field string, camel bool) string {
	if !camel {
		return field
	}
	return strings.ToLower(field[:1]) + field[1:]
}

func TestSystemPingSupportsGetAndPostWithoutCredentials(t *testing.T) {
	t.Parallel()

	rt := newSystemRouter(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, accept := range []string{"", `application/json; profile="CamelCase"`} {
			for _, authorization := range []string{"", "MediaBrowser Token=not-required"} {
				name := method + "/" + accept + "/" + authorization
				t.Run(name, func(t *testing.T) {
					rec := issueRequest(rt, method, "/System/Ping", accept, authorization)
					if rec.Code != http.StatusOK {
						t.Fatalf("%s /System/Ping status = %d, want %d", method, rec.Code, http.StatusOK)
					}
					wantType := "application/json; charset=utf-8"
					if accept != "" {
						wantType = `application/json; profile="CamelCase"; charset=utf-8`
					}
					if got := rec.Header().Get("Content-Type"); got != wantType {
						t.Errorf("%s /System/Ping Content-Type = %q, want %q", method, got, wantType)
					}

					var body string
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatalf("%s /System/Ping response is not a JSON string: %v; body=%q", method, err, rec.Body.String())
					}
					if body != "Jellyfin Server" {
						t.Errorf("%s /System/Ping body = %q, want %q", method, body, "Jellyfin Server")
					}
				})
			}
		}
	}
}

func TestSystemEndpointsRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()

	rt := newSystemRouter(t)
	for _, path := range []string{"/System/Info/Public", "/System/Ping"} {
		t.Run(path, func(t *testing.T) {
			rec := issueRequest(rt, http.MethodPut, path, "", "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("PUT %s status = %d, want %d", path, rec.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func newSystemRouter(t *testing.T) *router.Router {
	t.Helper()
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{IsStartupWizardCompleted: true})
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), manager, newSystemInfoConfig(t))
	return rt
}

func newAuthenticatedSystemRouter(t *testing.T) (*router.Router, handlers.SystemInfoConfig) {
	t.Helper()
	_, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{IsStartupWizardCompleted: true})
	authorizer := middleware.NewAuthorizer(systemInfoResolver{}, systemInfoEvaluator{}, systemInfoUsers{})
	rt := router.New(authorizer.Wrap)
	infoConfig := newSystemInfoConfig(t)
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), manager, infoConfig)
	return rt, infoConfig
}

func newAuthenticatedSystemRouterWithInfoConfig(t *testing.T, infoConfig handlers.SystemInfoConfig) *router.Router {
	t.Helper()
	_, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{IsStartupWizardCompleted: true})
	authorizer := middleware.NewAuthorizer(systemInfoResolver{}, systemInfoEvaluator{}, systemInfoUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), manager, infoConfig)
	return rt
}

func newSystemInfoConfig(t *testing.T) handlers.SystemInfoConfig {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("derive system-info paths: %v", err)
	}
	return handlers.SystemInfoConfig{
		Paths:               paths,
		WebPath:             filepath.Join(paths.Root, "web"),
		WebSocketPortNumber: 8096,
	}
}

func issueSystemInfoRequest(handler http.Handler, path, accept string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if authenticated {
		req.Header.Set("X-System-Info-Actor", "authenticated")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type systemInfoResolver struct{}

func (systemInfoResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	if request.Header.Get("X-System-Info-Actor") == "authenticated" {
		return auth.Identity{Authenticated: true, UserID: dto.UUID{0x01}}, nil
	}
	return auth.Identity{}, nil
}

type systemInfoEvaluator struct{}

func (systemInfoEvaluator) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if request.Identity.Authenticated {
		return auth.DecisionAllowed, nil
	}
	return auth.DecisionUnauthenticated, nil
}

type systemInfoUsers struct{}

func (systemInfoUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id, Policy: []byte(`{}`)}, nil
}

func issueRequest(handler http.Handler, method, path, accept, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertJSONFields(t *testing.T, body map[string]json.RawMessage, want map[string]any) {
	t.Helper()

	for field, expected := range want {
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			t.Fatalf("marshal expected %q value: %v", field, err)
		}
		if got, ok := body[field]; !ok {
			t.Errorf("response missing %q", field)
		} else if string(got) != string(expectedJSON) {
			t.Errorf("response field %q = %s, want %s", field, got, expectedJSON)
		}
	}
}

func assertCompatibleVersion(t *testing.T, version string) {
	t.Helper()

	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		t.Fatalf("Version = %q, want major.minor version compatible with Jellyfin 10.11+", version)
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 10 || major == 10 && minor < 11 {
		t.Errorf("Version = %q, want version compatible with Jellyfin 10.11+", version)
	}
}

func runtimeSystemArchitecture() string {
	switch runtime.GOARCH {
	case "amd64":
		return "X64"
	case "386":
		return "X86"
	case "arm64":
		return "Arm64"
	case "arm":
		return "Arm"
	default:
		return runtime.GOARCH
	}
}

// TestPublicSystemInfoLocalAddressIncludesPathBase verifies that the
// request-derived smart API address appends router.PathBase exactly once.
// Under base /jf, /jf/System/Info/Public reports a LocalAddress ending in /jf.
func TestPublicSystemInfoLocalAddressIncludesPathBase(t *testing.T) {
	t.Parallel()

	rt := newSystemRouter(t)
	mounted := router.Mount("/jf", rt)

	req := httptest.NewRequest(http.MethodGet, "/jf/System/Info/Public", nil)
	req.Host = "127.0.0.1:18096"
	rec := httptest.NewRecorder()
	mounted.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	raw, ok := body["LocalAddress"]
	if !ok {
		t.Fatalf("response missing LocalAddress: %s", rec.Body.String())
	}
	var localAddr string
	if err := json.Unmarshal(raw, &localAddr); err != nil {
		t.Fatalf("LocalAddress is not a string: %v", err)
	}
	want := "http://127.0.0.1:18096/jf"
	if localAddr != want {
		t.Fatalf("LocalAddress = %q, want %q", localAddr, want)
	}
}

// TestPublicSystemInfoConfiguredLocalAddressIncludesPathBase verifies that
// the configured smart API address appends router.PathBase exactly once.
// When a non-wildcard LocalAddress is configured, /jf/System/Info/Public
// reports a LocalAddress ending in /jf.
func TestPublicSystemInfoConfiguredLocalAddressIncludesPathBase(t *testing.T) {
	t.Parallel()

	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("derive paths: %v", err)
	}
	_, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{IsStartupWizardCompleted: true})
	rt := router.New()
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), manager, handlers.SystemInfoConfig{
		Paths:               paths,
		WebPath:             filepath.Join(paths.Root, "web"),
		WebSocketPortNumber: 8096,
		LocalAddress:        "192.168.1.100:18096",
	})
	mounted := router.Mount("/jf", rt)

	req := httptest.NewRequest(http.MethodGet, "/jf/System/Info/Public", nil)
	req.Host = "127.0.0.1:18096"
	rec := httptest.NewRecorder()
	mounted.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	raw, ok := body["LocalAddress"]
	if !ok {
		t.Fatalf("response missing LocalAddress: %s", rec.Body.String())
	}
	var localAddr string
	if err := json.Unmarshal(raw, &localAddr); err != nil {
		t.Fatalf("LocalAddress is not a string: %v", err)
	}
	want := "http://192.168.1.100:18096/jf"
	if localAddr != want {
		t.Fatalf("LocalAddress = %q, want %q", localAddr, want)
	}
}
