// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestAPIKeyLifecycleAuthenticatesIntegrations proves the /Auth/Keys routes
// end to end: an administrator creates a key, lists it, an integration
// such as Seerr authenticates with the key alone (Authorization Token or ApiKey),
// and a revoked key stops authenticating.
func TestAPIKeyLifecycleAuthenticatesIntegrations(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	admin := compositionAccessToken(t, compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", ""))

	if rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Auth/Keys", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without app = %d, want 400", rec.Code)
	}
	if rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Auth/Keys?app=Seerr", nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("create = %d; body %s", rec.Code, rec.Body.String())
	}

	list := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Auth/Keys", nil, admin)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d; body %s", list.Code, list.Body.String())
	}
	var result struct {
		Items []struct {
			AccessToken, AppName, DateCreated string
			DeviceIDKey                       *string `json:"DeviceId"`
		}
		TotalRecordCount int
	}
	if err := json.Unmarshal(list.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TotalRecordCount != 1 || len(result.Items) != 1 || result.Items[0].AppName != "Seerr" || len(result.Items[0].AccessToken) != 32 || result.Items[0].DateCreated == "" {
		t.Fatalf("list = %s", list.Body.String())
	}
	if result.Items[0].DeviceIDKey == nil || *result.Items[0].DeviceIDKey != "" {
		t.Errorf("DeviceId = %v, want the empty string upstream projects", result.Items[0].DeviceIDKey)
	}
	key := result.Items[0].AccessToken

	// Jellyfin 10.11+ reads a key from the Authorization header's Token or the
	// ApiKey query; X-Emby-Token and api_key need EnableLegacyAuthorization.
	withKey := func(path, token string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "10.0.0.5:51000"
		if token != "" {
			req.Header.Set("Authorization", `MediaBrowser Client="Seerr", Token="`+token+`"`)
		}
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := withKey("/Users", key); code != http.StatusOK {
		t.Errorf("GET /Users with the key in Authorization = %d, want 200", code)
	}
	if code := withKey("/Users?ApiKey="+key, ""); code != http.StatusOK {
		t.Errorf("GET /Users with ApiKey = %d, want 200", code)
	}

	if rec := authenticatedCompositionRequest(app.Router(), http.MethodDelete, "/Auth/Keys/"+key, nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if code := withKey("/Users", key); code != http.StatusUnauthorized {
		t.Errorf("GET /Users with a revoked key = %d, want 401", code)
	}
	// Revoking an unknown key is not an error, as upstream's ExecuteDelete is not.
	if rec := authenticatedCompositionRequest(app.Router(), http.MethodDelete, "/Auth/Keys/unknown", nil, admin); rec.Code != http.StatusNoContent {
		t.Errorf("revoke unknown = %d, want 204", rec.Code)
	}
}
