// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/events"
)

func TestStartupConfigurationReadsAndUpdatesTheStockDTO(t *testing.T) {
	rt, manager, changes := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{
		ServerName:                "Before",
		UICulture:                 "fr-CA",
		MetadataCountryCode:       "CA",
		PreferredMetadataLanguage: "fr",
		LibraryUpdateDuration:     17,
		InactiveSessionThreshold:  9,
		EnableLegacyAuthorization: true,
		QuickConnectAvailable:     false,
	})
	handlers.RegisterStartup(rt, manager)
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 3})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		accept string
		want   map[string]any
	}{
		{
			name: "pascal profile",
			want: map[string]any{
				"ServerName": "Before", "UICulture": "fr-CA", "MetadataCountryCode": "CA", "PreferredMetadataLanguage": "fr",
			},
		},
		{
			name:   "camel profile",
			accept: `application/json; profile="CamelCase"`,
			want: map[string]any{
				"serverName": "Before", "uiCulture": "fr-CA", "metadataCountryCode": "CA", "preferredMetadataLanguage": "fr",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/sTaRtUp/cOnFiGuRaTiOn", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			if tc.accept == "" {
				assertJSONResponse(t, rec, tc.want)
			} else {
				assertCamelJSONResponse(t, rec, tc.want)
			}
		})
	}

	rec := request(rt, http.MethodPost, "/startup/configuration", `{"ServerName":"After","UICulture":"de-DE","MetadataCountryCode":"DE","PreferredMetadataLanguage":"de"}`, "application/json", "")
	assertStatus(t, rec, http.StatusNoContent)
	assertChange(t, subscription.Events(), config.Change{File: "system.xml"})
	assertNoChange(t, subscription.Events())
	if got, want := manager.System(), (config.ServerConfiguration{
		ServerName:                "After",
		UICulture:                 "de-DE",
		MetadataCountryCode:       "DE",
		PreferredMetadataLanguage: "de",
		LibraryUpdateDuration:     17,
		InactiveSessionThreshold:  9,
		EnableLegacyAuthorization: true,
		QuickConnectAvailable:     false,
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("system after full startup update = %#v, want %#v", got, want)
	}

	// Jellyfin maps every nullable StartupConfigurationDto property through
	// `?? string.Empty`; omitted fields clear, while fields outside that DTO
	// retain their persisted values.
	rec = request(rt, http.MethodPost, "/STARTUP/CONFIGURATION", `{"ServerName":"Partial"}`, "application/json", "")
	assertStatus(t, rec, http.StatusNoContent)
	assertChange(t, subscription.Events(), config.Change{File: "system.xml"})
	assertNoChange(t, subscription.Events())
	if got, want := manager.System(), (config.ServerConfiguration{
		ServerName:                "Partial",
		LibraryUpdateDuration:     17,
		InactiveSessionThreshold:  9,
		EnableLegacyAuthorization: true,
		QuickConnectAvailable:     false,
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("system after partial startup update = %#v, want upstream nullable DTO semantics %#v", got, want)
	}
}

func TestStartupConfigurationRejectsInvalidBodiesWithoutPersisting(t *testing.T) {
	rt, manager, changes := newManagedRouter(t)
	before := config.ServerConfiguration{
		ServerName:                "Untouched",
		UICulture:                 "es-MX",
		MetadataCountryCode:       "MX",
		PreferredMetadataLanguage: "es",
		LibraryUpdateDuration:     11,
		QuickConnectAvailable:     false,
	}
	seedSystem(t, manager, before)
	handlers.RegisterStartup(rt, manager)
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 1})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	for _, tc := range []struct {
		name        string
		body        string
		contentType string
		status      int
	}{
		{name: "malformed JSON", body: `{"ServerName":`, contentType: "application/json", status: http.StatusBadRequest},
		{name: "unsupported media type", body: `{"ServerName":"changed"}`, contentType: "text/plain", status: http.StatusUnsupportedMediaType},
		{name: "empty required body", contentType: "application/json", status: http.StatusBadRequest},
		{name: "literal null required body", body: "null", contentType: "application/json", status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodPost, "/Startup/Configuration", tc.body, tc.contentType, "")
			assertStatus(t, rec, tc.status)
			if got := manager.System(); !reflect.DeepEqual(got, before) {
				t.Fatalf("system after rejected %s = %#v, want unchanged %#v", tc.name, got, before)
			}
			assertNoChange(t, subscription.Events())
		})
	}
}

func TestStartupRemoteAccessUpdatesNetworkConfigurationOnce(t *testing.T) {
	rt, manager, changes := newManagedRouter(t)
	handlers.RegisterStartup(rt, manager)
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 2})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "explicitly disabled", body: `{"EnableRemoteAccess":false}`},
		{name: "empty DTO is disabled", body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodPost, "/sTaRtUp/rEmOtEaCcEsS", tc.body, "application/json", "")
			assertStatus(t, rec, http.StatusNoContent)
			if got, want := manager.Network(), networkWithRemoteAccess(false); !reflect.DeepEqual(got, want) {
				t.Fatalf("network after remote access update = %#v, want %#v", got, want)
			}
			assertChange(t, subscription.Events(), config.Change{File: "network.xml"})
			assertNoChange(t, subscription.Events())
		})
	}
}

func TestStartupRemoteAccessRejectsMissingRequiredBodyWithoutPersisting(t *testing.T) {
	rt, manager, changes := newManagedRouter(t)
	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		network.EnableRemoteAccess = true
		return nil
	}); err != nil {
		t.Fatalf("seed remote access: %v", err)
	}
	handlers.RegisterStartup(rt, manager)
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 1})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty body"},
		{name: "literal null", body: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodPost, "/Startup/RemoteAccess", tc.body, "application/json", "")
			assertStatus(t, rec, http.StatusBadRequest)
			if got, want := manager.Network(), networkWithRemoteAccess(true); !reflect.DeepEqual(got, want) {
				t.Fatalf("network after rejected %s = %#v, want unchanged %#v", tc.name, got, want)
			}
			assertNoChange(t, subscription.Events())
		})
	}
}

func TestBrandingConfigurationOmitsExplicitlyEmptyNullableStrings(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		*branding = config.BrandingOptions{SplashscreenEnabled: true}
		return nil
	}); err != nil {
		t.Fatalf("seed explicitly empty branding: %v", err)
	}
	handlers.RegisterBranding(rt, manager)

	for _, tc := range []struct {
		name   string
		accept string
		want   map[string]any
	}{
		{name: "Pascal", want: map[string]any{"SplashscreenEnabled": true}},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, want: map[string]any{"splashscreenEnabled": true}},
	} {
		t.Run("empty/"+tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/Branding/Configuration", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			if tc.accept == "" {
				assertJSONResponse(t, rec, tc.want)
			} else {
				assertCamelJSONResponse(t, rec, tc.want)
			}
		})
	}

	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		branding.LoginDisclaimer = "Authorized viewers only."
		branding.CustomCss = "body { color: rebeccapurple; }"
		return nil
	}); err != nil {
		t.Fatalf("set nonempty branding strings: %v", err)
	}
	for _, tc := range []struct {
		name   string
		accept string
		want   map[string]any
	}{
		{name: "Pascal", want: map[string]any{"LoginDisclaimer": "Authorized viewers only.", "CustomCss": "body { color: rebeccapurple; }", "SplashscreenEnabled": true}},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, want: map[string]any{"loginDisclaimer": "Authorized viewers only.", "customCss": "body { color: rebeccapurple; }", "splashscreenEnabled": true}},
	} {
		t.Run("nonempty/"+tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/Branding/Configuration", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			if tc.accept == "" {
				assertJSONResponse(t, rec, tc.want)
			} else {
				assertCamelJSONResponse(t, rec, tc.want)
			}
		})
	}
}

func TestBrandingEndpointsExposePersistedOptionsAndCSSAliases(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	options := config.BrandingOptions{
		LoginDisclaimer:     "Authorized viewers only.",
		CustomCss:           "body { color: rebeccapurple; }\n",
		SplashscreenEnabled: true,
	}
	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		*branding = options
		return nil
	}); err != nil {
		t.Fatalf("seed branding: %v", err)
	}
	handlers.RegisterBranding(rt, manager)

	for _, tc := range []struct {
		name   string
		accept string
		want   map[string]any
	}{
		{
			name: "pascal profile",
			want: map[string]any{
				"LoginDisclaimer": "Authorized viewers only.", "CustomCss": "body { color: rebeccapurple; }\n", "SplashscreenEnabled": true,
			},
		},
		{
			name:   "camel profile",
			accept: `application/json; profile="CamelCase"`,
			want: map[string]any{
				"loginDisclaimer": "Authorized viewers only.", "customCss": "body { color: rebeccapurple; }\n", "splashscreenEnabled": true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/bRaNdInG/cOnFiGuRaTiOn", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			if tc.accept == "" {
				assertJSONResponse(t, rec, tc.want)
			} else {
				assertCamelJSONResponse(t, rec, tc.want)
			}
		})
	}

	for _, path := range []string{"/branding/css", "/BrAnDiNg/CsS.cSs"} {
		t.Run(path, func(t *testing.T) {
			rec := request(rt, http.MethodGet, path, "", "", "")
			assertStatus(t, rec, http.StatusOK)
			if got, want := rec.Header().Get("Content-Type"), "text/css; charset=utf-8"; got != want {
				t.Errorf("GET %s Content-Type = %q, want %q", path, got, want)
			}
			if got, want := rec.Body.String(), options.CustomCss; got != want {
				t.Errorf("GET %s body = %q, want CSS bytes %q", path, got, want)
			}
		})
	}

	rec := request(rt, http.MethodGet, "/bRaNdInG/sPlAsHsCrEeN", "", "", "")
	assertStatus(t, rec, http.StatusNoContent)
}

func TestBrandingSplashscreenServesConfiguredImageAndRejectsUnavailableLocation(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterBranding(rt, manager)

	image := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}
	location := filepath.Join(t.TempDir(), "configured-splash.png")
	if err := os.WriteFile(location, image, 0o600); err != nil {
		t.Fatalf("write configured splashscreen: %v", err)
	}
	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		branding.SplashscreenLocation = location
		return nil
	}); err != nil {
		t.Fatalf("configure splashscreen location: %v", err)
	}

	rec := request(rt, http.MethodGet, "/Branding/Splashscreen", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if got, want := rec.Header().Get("Content-Type"), "image/png"; got != want {
		t.Errorf("configured splashscreen Content-Type = %q, want %q", got, want)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, image) {
		t.Errorf("configured splashscreen bytes = %x, want %x", got, image)
	}

	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		branding.SplashscreenLocation = ""
		return nil
	}); err != nil {
		t.Fatalf("clear splashscreen location: %v", err)
	}
	rec = request(rt, http.MethodGet, "/Branding/Splashscreen", "", "", "")
	assertStatus(t, rec, http.StatusNoContent)
	if got := rec.Body.Len(); got != 0 {
		t.Errorf("empty splashscreen location body length = %d, want 0", got)
	}

	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		branding.SplashscreenLocation = filepath.Join(t.TempDir(), "missing-splash.png")
		return nil
	}); err != nil {
		t.Fatalf("configure missing splashscreen location: %v", err)
	}
	rec = request(rt, http.MethodGet, "/Branding/Splashscreen", "", "", "")
	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestPublicSystemInfoReadsLiveStartupState(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{IsStartupWizardCompleted: true})
	handlers.RegisterSystem(rt, config.DefaultServerIdentity(), manager)

	rec := request(rt, http.MethodGet, "/sYsTeM/iNfO/pUbLiC", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	assertJSONFieldValues(t, rec, map[string]any{"StartupWizardCompleted": true})

	if err := manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
		system.IsStartupWizardCompleted = false
		return nil
	}); err != nil {
		t.Fatalf("set startup incomplete: %v", err)
	}
	rec = request(rt, http.MethodGet, "/System/Info/Public", "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, rec, http.StatusOK)
	assertCamelJSONFieldValues(t, rec, map[string]any{"startupWizardCompleted": false})
}

func TestBrandingRoutesDeclarePublicReadsAndElevatedWrites(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterBranding(rt, manager)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Branding/Configuration", OperationID: "GetBrandingOptions", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Branding/Css", OperationID: "GetBrandingCss", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Branding/Css.css", OperationID: "GetBrandingCss_2", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Branding/Splashscreen", OperationID: "GetSplashscreen", Anonymous: true},
		{Method: http.MethodPost, Pattern: "/Branding/Splashscreen", OperationID: "UploadCustomSplashscreen", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodDelete, Pattern: "/Branding/Splashscreen", OperationID: "DeleteCustomSplashscreen", Policy: string(auth.PolicyRequiresElevation)},
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

func TestBrandingAdminWritesExposeHonestStubBehavior(t *testing.T) {
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterBranding(rt, manager)

	upload := request(rt, http.MethodPost, "/Branding/Splashscreen", "image", "image/png", "")
	assertStatus(t, upload, http.StatusBadRequest)
	problem := assertProblemDetailsWireCasing(t, upload.Body.Bytes(), http.StatusBadRequest)
	if problem["detail"] == "" {
		t.Errorf("upload problem = %q, want populated 400 ProblemDetails", upload.Body.String())
	}

	deleted := request(rt, http.MethodDelete, "/Branding/Splashscreen", "", "", "")
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Errorf("delete = status %d body %q, want 204 with empty body", deleted.Code, deleted.Body.String())
	}
}

func newManagedRouter(t *testing.T) (*router.Router, *config.Manager, *events.Bus[config.Change]) {
	t.Helper()
	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	changes := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() {
		if err := changes.Close(context.Background()); err != nil {
			t.Errorf("close changes bus: %v", err)
		}
	})
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("open temporary Manager: %v", err)
	}
	return router.New(), manager, changes
}

func seedSystem(t *testing.T, manager *config.Manager, want config.ServerConfiguration) {
	t.Helper()
	if err := manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
		*system = want
		return nil
	}); err != nil {
		t.Fatalf("seed system configuration: %v", err)
	}
}

func request(handler http.Handler, method, path, body, contentType, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, want, rec.Body.String())
	}
}

func assertJSONResponse(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any) {
	assertJSONResponseWithContentType(t, rec, want, "application/json; charset=utf-8")
}

func assertCamelJSONResponse(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any) {
	assertJSONResponseWithContentType(t, rec, want, `application/json; profile="CamelCase"; charset=utf-8`)
}

func assertJSONResponseWithContentType(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any, wantType string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != wantType {
		t.Errorf("Content-Type = %q, want %q", got, wantType)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
	}
	if len(got) != len(want) {
		t.Errorf("response fields = %d, want exact DTO shape %d: %s", len(got), len(want), rec.Body.String())
	}
	for key, expected := range want {
		encoded, err := json.Marshal(expected)
		if err != nil {
			t.Fatalf("marshal expected %q: %v", key, err)
		}
		if value, ok := got[key]; !ok {
			t.Errorf("response missing %q: %s", key, rec.Body.String())
		} else if string(value) != string(encoded) {
			t.Errorf("response %q = %s, want %s", key, value, encoded)
		}
	}
}

func assertJSONFieldValues(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any) {
	assertJSONFieldValuesWithContentType(t, rec, want, "application/json; charset=utf-8")
}

func assertCamelJSONFieldValues(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any) {
	assertJSONFieldValuesWithContentType(t, rec, want, `application/json; profile="CamelCase"; charset=utf-8`)
}

func assertJSONFieldValuesWithContentType(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any, wantType string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != wantType {
		t.Errorf("Content-Type = %q, want %q", got, wantType)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
	}
	for key, expected := range want {
		encoded, err := json.Marshal(expected)
		if err != nil {
			t.Fatalf("marshal expected %q: %v", key, err)
		}
		if value, ok := got[key]; !ok {
			t.Errorf("response missing %q: %s", key, rec.Body.String())
		} else if string(value) != string(encoded) {
			t.Errorf("response %q = %s, want %s", key, value, encoded)
		}
	}
}

func assertChange(t *testing.T, changes <-chan config.Change, want config.Change) {
	t.Helper()
	select {
	case got := <-changes:
		if got != want {
			t.Fatalf("configuration change = %#v, want %#v", got, want)
		}
	default:
		t.Fatal("configuration mutation did not publish a change")
	}
}

func assertNoChange(t *testing.T, changes <-chan config.Change) {
	t.Helper()
	select {
	case got := <-changes:
		t.Fatalf("unexpected configuration change %#v", got)
	default:
	}
}

func networkWithRemoteAccess(enabled bool) config.NetworkConfiguration {
	network := config.DefaultNetworkConfiguration()
	network.EnableRemoteAccess = enabled
	return network
}
