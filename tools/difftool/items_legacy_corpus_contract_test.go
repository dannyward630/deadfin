// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var legacyItemsTransportNormalizers = []string{
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

var legacyItemsDTOResponseNormalizers = append([]string{
	"item-id-map",
	"server-id",
	"entity-tags",
	"item-created-dates",
	"user-data-keys",
}, legacyItemsTransportNormalizers...)

var legacyItemsUserViewsNormalizers = append([]string{
	"item-id-map",
	"server-id",
	"entity-tags",
	"item-created-dates",
	"user-data-keys",
	"user-view-ids",
}, legacyItemsTransportNormalizers...)

var legacyItemsGroupingOptionsNormalizers = append([]string{"user-view-ids"}, legacyItemsTransportNormalizers...)

var legacyItemsLoginNormalizers = append([]string{
	"server-id",
	"auth-tokens",
	"login-session-volatiles",
}, legacyItemsTransportNormalizers...)

type legacyItemsCorpusStep struct {
	name        string
	method      string
	path        string
	operationID string
	params      map[string]any
	capture     map[string]string
	normalizers []string
}

var legacyItemsCorpusContract = []legacyItemsCorpusStep{
	{
		name:        "legacy items password login",
		method:      http.MethodPost,
		path:        "/Users/AuthenticateByName",
		operationID: "AuthenticateUserByName",
		capture:     map[string]string{"user_id": "User.Id"},
		normalizers: legacyItemsLoginNormalizers,
	},
	{
		name:        "select legacy movie item",
		method:      http.MethodGet,
		path:        "/Items",
		operationID: "GetItems",
		params: map[string]any{
			"Recursive":              true,
			"IncludeItemTypes":       []any{"Movie"},
			"SortBy":                 []any{"SortName"},
			"SortOrder":              []any{"Ascending"},
			"Limit":                  1,
			"EnableTotalRecordCount": true,
			"Fields":                 []any{"Path", "MediaSources", "MediaStreams"},
		},
		capture:     map[string]string{"movie_id": "Items.0.Id"},
		normalizers: legacyItemsDTOResponseNormalizers,
	},
	{
		name:        "legacy user items list",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/Items",
		operationID: "SHADOW:GetItemsLegacy",
		params: map[string]any{
			"Recursive":              true,
			"IncludeItemTypes":       []any{"Movie"},
			"SortBy":                 []any{"SortName"},
			"SortOrder":              []any{"Ascending"},
			"Limit":                  2,
			"EnableTotalRecordCount": true,
			"Fields":                 []any{"Path", "MediaSources", "MediaStreams"},
		},
		normalizers: legacyItemsDTOResponseNormalizers,
	},
	{
		name:        "legacy user item by id",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/Items/${movie_id}",
		operationID: "SHADOW:GetItemLegacy",
		params:      map[string]any{"Fields": []any{"Path", "MediaSources", "MediaStreams"}},
		normalizers: legacyItemsDTOResponseNormalizers,
	},
	{
		name:        "legacy user latest items",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/Items/Latest",
		operationID: "SHADOW:GetLatestMediaLegacy",
		params: map[string]any{
			"IncludeItemTypes": []any{"Movie"},
			"Limit":            2,
			"GroupItems":       false,
			"Fields":           []any{"Path", "MediaSources", "MediaStreams"},
		},
		normalizers: legacyItemsDTOResponseNormalizers,
	},
	{
		name:        "legacy user resumable items",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/Items/Resume",
		operationID: "SHADOW:GetResumeItemsLegacy",
		params: map[string]any{
			"Limit":                  2,
			"EnableTotalRecordCount": true,
			"Fields":                 []any{"Path", "MediaSources", "MediaStreams"},
		},
		normalizers: legacyItemsDTOResponseNormalizers,
	},
	{
		name:        "legacy user views",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/Views",
		operationID: "SHADOW:GetUserViewsLegacy",
		params: map[string]any{
			"IncludeHidden":          false,
			"IncludeExternalContent": false,
		},
		normalizers: legacyItemsUserViewsNormalizers,
	},
	{
		name:        "legacy user grouping options",
		method:      http.MethodGet,
		path:        "/Users/${user_id}/GroupingOptions",
		operationID: "SHADOW:GetGroupingOptionsLegacy",
		normalizers: legacyItemsGroupingOptionsNormalizers,
	},
}

func TestItemsLegacyCorpusDefinesExactAuthenticatedLegacyTwinContract(t *testing.T) {
	steps := legacyItemsCorpusSteps(t)
	if err := requireLegacyItemsCorpusContract(steps); err != nil {
		t.Fatal(err)
	}
}

func TestItemsLegacyCorpusRejectsDuplicateOrOutOfOrderLegacyCaptureSteps(t *testing.T) {
	steps := legacyItemsCorpusSteps(t)
	broken := append([]Step(nil), steps...)
	broken[3], broken[4] = broken[4], broken[3]
	if err := requireLegacyItemsCorpusContract(broken); err == nil {
		t.Fatal("legacy corpus contract accepted an item request before its movie_id capture")
	}

	broken = append(append([]Step(nil), steps...), steps[2])
	if err := requireLegacyItemsCorpusContract(broken); err == nil {
		t.Fatal("legacy corpus contract accepted a duplicate legacy Items route")
	}
}

func TestItemsLegacyCorpusReplaysHostLocalCapturesWithProfileSafeAccept(t *testing.T) {
	for _, profile := range []struct {
		name   string
		value  string
		accept string
	}{
		{name: "Pascal", accept: "application/json"},
		{name: "Camel", value: "CamelCase", accept: `application/json; profile="CamelCase"`},
	} {
		t.Run(profile.name, func(t *testing.T) {
			left := newLegacyItemsReplayHost(t, "left-user", "left-movie", "left-source", "left-server")
			right := newLegacyItemsReplayHost(t, "right-user", "right-movie", "right-source", "right-server")

			result, err := Run(context.Background(), Config{
				Corpus:     filepath.Join("..", "..", "corpora", "items-legacy.yaml"),
				LeftURL:    left.server.URL,
				RightURL:   right.server.URL,
				ResultsDir: filepath.Join(t.TempDir(), "results"),
				IssuesDir:  filepath.Join(t.TempDir(), "issues"),
				Profile:    profile.value,
				LeftAuth:   AuthConfig{Role: "admin", Username: "left-admin", Password: "left-password"},
				RightAuth:  AuthConfig{Role: "admin", Username: "right-admin", Password: "right-password"},
			})
			if err != nil {
				t.Fatalf("legacy corpus replay with host-local user and movie IDs: %v", err)
			}
			for _, step := range result.Steps {
				if step.Status != "pass" {
					t.Fatalf("legacy corpus replay step = %#v, want pass", step)
				}
			}

			for _, host := range []*legacyItemsReplayHost{left, right} {
				requests := host.corpusRequests()
				if len(requests) != len(legacyItemsCorpusContract) {
					t.Fatalf("%s corpus requests = %#v, want %d explicit corpus steps", host.server.URL, requests, len(legacyItemsCorpusContract))
				}
				for index, expected := range legacyItemsCorpusContract {
					request := requests[index]
					if request.method != expected.method {
						t.Fatalf("%s corpus request %d method = %q, want %q", host.server.URL, index, request.method, expected.method)
					}
					if request.accept != profile.accept {
						t.Fatalf("%s corpus request %q Accept = %q, want %q", host.server.URL, expected.name, request.accept, profile.accept)
					}
				}
				wantPaths := []string{
					"/Users/AuthenticateByName",
					"/Items",
					"/Users/" + host.userID + "/Items",
					"/Users/" + host.userID + "/Items/" + host.movieID,
					"/Users/" + host.userID + "/Items/Latest",
					"/Users/" + host.userID + "/Items/Resume",
					"/Users/" + host.userID + "/Views",
					"/Users/" + host.userID + "/GroupingOptions",
				}
				gotPaths := make([]string, len(requests))
				for index, request := range requests {
					gotPaths[index] = request.path
				}
				if !reflect.DeepEqual(gotPaths, wantPaths) {
					t.Fatalf("%s captured legacy request paths = %#v, want host-local captures %#v", host.server.URL, gotPaths, wantPaths)
				}
			}
		})
	}
}

func legacyItemsCorpusSteps(t *testing.T) []Step {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "items-legacy.yaml"))
	if err != nil {
		t.Fatalf("load committed legacy items corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed legacy items corpus scenarios = %d, want one", len(scenarios))
	}
	return scenarios[0].Steps
}

func requireLegacyItemsCorpusContract(steps []Step) error {
	if len(steps) != len(legacyItemsCorpusContract) {
		return fmt.Errorf("legacy items corpus steps = %d, want %d", len(steps), len(legacyItemsCorpusContract))
	}
	for index, expected := range legacyItemsCorpusContract {
		step := steps[index]
		if step.Name != expected.name || step.Method != expected.method || step.Path != expected.path || step.OperationID != expected.operationID {
			return fmt.Errorf("legacy items step %d = (%q, %s, %q, %q), want (%q, %s, %q, %q)", index, step.Name, step.Method, step.Path, step.OperationID, expected.name, expected.method, expected.path, expected.operationID)
		}
		if !reflect.DeepEqual(step.Params, expected.params) {
			return fmt.Errorf("legacy items step %q params = %#v, want %#v", step.Name, step.Params, expected.params)
		}
		if !reflect.DeepEqual(step.Capture, expected.capture) {
			return fmt.Errorf("legacy items step %q capture = %#v, want %#v", step.Name, step.Capture, expected.capture)
		}
		if len(step.Normalize) != 0 || !reflect.DeepEqual(step.Normalizers, expected.normalizers) {
			return fmt.Errorf("legacy items step %q normalizers = %#v / %#v, want %#v", step.Name, step.Normalize, step.Normalizers, expected.normalizers)
		}
		expectedHeaders := map[string]string{"Accept": "application/json"}
		if index == 0 {
			expectedHeaders["Authorization"] = `MediaBrowser Client="Goldenrec legacy items corpus", Device="Goldenrec legacy items corpus", DeviceId="goldenrec-legacy-items", Version="1.0.0"`
		}
		if !reflect.DeepEqual(step.Headers, expectedHeaders) {
			return fmt.Errorf("legacy items step %q headers = %#v, want explicit profile-safe JSON Accept and required login metadata", step.Name, step.Headers)
		}
		if index == 0 {
			body, ok := step.Body.(map[string]any)
			if !ok || !reflect.DeepEqual(body, map[string]any{"Username": "${auth_username}", "Pw": "${auth_password}"}) {
				return fmt.Errorf("legacy login body = %#v, want runtime credential placeholders", step.Body)
			}
			continue
		}
		if step.AuthRole != "admin" {
			return fmt.Errorf("legacy items step %q auth role = %q, want admin", step.Name, step.AuthRole)
		}
		if step.Body != nil || step.BodyFile != "" || step.BodyFileField != "" {
			return fmt.Errorf("legacy items read step %q must not carry a request body", step.Name)
		}
	}
	return nil
}

type legacyItemsRequest struct {
	method string
	path   string
	accept string
}

type legacyItemsReplayHost struct {
	mu           sync.Mutex
	userID       string
	movieID      string
	authRequests int
	sourceID     string
	serverID     string
	requests     []legacyItemsRequest
	server       *httptest.Server
}

func newLegacyItemsReplayHost(t *testing.T, userID, movieID, sourceID, serverID string) *legacyItemsReplayHost {
	t.Helper()
	host := &legacyItemsReplayHost{userID: userID, movieID: movieID, sourceID: sourceID, serverID: serverID}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			host.serveLogin(t, w, r)
			return
		}
		if r.URL.Path == "/Items" && r.URL.Query().Get("Limit") == "10000" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(host.itemsPage(false)))
			return
		}
		host.mu.Lock()
		host.requests = append(host.requests, legacyItemsRequest{method: r.Method, path: r.URL.Path, accept: r.Header.Get("Accept")})
		host.mu.Unlock()

		camel := strings.Contains(r.Header.Get("Accept"), `profile="CamelCase"`)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Items", "/Users/" + host.userID + "/Items", "/Users/" + host.userID + "/Items/Resume":
			_, _ = w.Write([]byte(host.itemsPage(camel)))
		case "/Users/" + host.userID + "/Items/" + host.movieID:
			_, _ = w.Write([]byte(host.itemDTO(camel)))
		case "/Users/" + host.userID + "/Items/Latest":
			_, _ = w.Write([]byte("[" + host.itemDTO(camel) + "]"))
		case "/Users/" + host.userID + "/Views":
			_, _ = w.Write([]byte(host.userViews(camel)))
		case "/Users/" + host.userID + "/GroupingOptions":
			_, _ = w.Write([]byte(host.groupingOptions(camel)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *legacyItemsReplayHost) serveLogin(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body map[string]string
	host.mu.Lock()
	host.authRequests++
	if host.authRequests > 1 {
		host.requests = append(host.requests, legacyItemsRequest{method: r.Method, path: r.URL.Path, accept: r.Header.Get("Accept")})
	}
	host.mu.Unlock()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode login body: %v", err)
	}
	if body["Username"] == "" || body["Pw"] == "" {
		t.Errorf("login body = %#v, want runtime username and password", body)
	}
	camel := strings.Contains(r.Header.Get("Accept"), `profile="CamelCase"`)
	w.Header().Set("Content-Type", "application/json")
	if camel {
		_, _ = fmt.Fprintf(w, `{"accessToken":"%s-token","user":{"id":"%s","name":"admin","policy":{},"serverId":"%s"},"sessionInfo":{"id":"%s-session","userId":"%s","client":"difftool","deviceId":"device","deviceName":"device"}}`, host.userID, host.userID, host.serverID, host.userID, host.userID)
		return
	}
	_, _ = fmt.Fprintf(w, `{"AccessToken":"%s-token","User":{"Id":"%s","Name":"admin","Policy":{},"ServerId":"%s"},"SessionInfo":{"Id":"%s-session","UserId":"%s","Client":"difftool","DeviceId":"device","DeviceName":"device"}}`, host.userID, host.userID, host.serverID, host.userID, host.userID)
}

func (host *legacyItemsReplayHost) itemsPage(camel bool) string {
	if camel {
		return `{"items":[` + host.itemDTO(true) + `],"totalRecordCount":1,"startIndex":0}`
	}
	return `{"Items":[` + host.itemDTO(false) + `],"TotalRecordCount":1,"StartIndex":0}`
}

func (host *legacyItemsReplayHost) userViews(camel bool) string {
	if camel {
		return fmt.Sprintf(`{"items":[{"id":"%s-movies","name":"Golden Movies","collectionType":"movies","type":"CollectionFolder","displayPreferencesId":"%s-movies","parentId":"%s-root","userData":{"itemId":"%s-movies","key":"shared-view-key"}},{"id":"%s-tv","name":"Golden TV","collectionType":"tvshows","type":"CollectionFolder","displayPreferencesId":"%s-tv","parentId":"%s-root","userData":{"itemId":"%s-tv","key":"shared-view-key"}}],"totalRecordCount":2,"startIndex":0}`, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID)
	}
	return fmt.Sprintf(`{"Items":[{"Id":"%s-movies","Name":"Golden Movies","CollectionType":"movies","Type":"CollectionFolder","DisplayPreferencesId":"%s-movies","ParentId":"%s-root","UserData":{"ItemId":"%s-movies","Key":"shared-view-key"}},{"Id":"%s-tv","Name":"Golden TV","CollectionType":"tvshows","Type":"CollectionFolder","DisplayPreferencesId":"%s-tv","ParentId":"%s-root","UserData":{"ItemId":"%s-tv","Key":"shared-view-key"}}],"TotalRecordCount":2,"StartIndex":0}`, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID, host.userID)
}

func (host *legacyItemsReplayHost) groupingOptions(camel bool) string {
	if camel {
		return fmt.Sprintf(`[{"id":"%s-movies","name":"Golden Movies"},{"id":"%s-tv","name":"Golden TV"}]`, host.userID, host.userID)
	}
	return fmt.Sprintf(`[{"Id":"%s-movies","Name":"Golden Movies"},{"Id":"%s-tv","Name":"Golden TV"}]`, host.userID, host.userID)
}

func (host *legacyItemsReplayHost) itemDTO(camel bool) string {
	if camel {
		return fmt.Sprintf(`{"id":"%s","type":"Movie","path":"/fixture/legacy-movie.mkv","serverId":"%s","dateCreated":"2026-07-27T00:00:00Z","imageTags":{"Primary":"%s-image"},"imageBlurHashes":{"Primary":{"%s-image":"shared-blurhash"}},"userData":{"key":"%s-user-data","itemId":"%s","playbackPositionTicks":0},"mediaSources":[{"id":"%s","path":"/fixture/legacy-movie.mkv"}],"mediaStreams":[]}`, host.movieID, host.serverID, host.serverID, host.serverID, host.serverID, host.movieID, host.sourceID)
	}
	return fmt.Sprintf(`{"Id":"%s","Type":"Movie","Path":"/fixture/legacy-movie.mkv","ServerId":"%s","DateCreated":"2026-07-27T00:00:00Z","ImageTags":{"Primary":"%s-image"},"ImageBlurHashes":{"Primary":{"%s-image":"shared-blurhash"}},"UserData":{"Key":"%s-user-data","ItemId":"%s","PlaybackPositionTicks":0},"MediaSources":[{"Id":"%s","Path":"/fixture/legacy-movie.mkv"}],"MediaStreams":[]}`, host.movieID, host.serverID, host.serverID, host.serverID, host.serverID, host.movieID, host.sourceID)
}

func (host *legacyItemsReplayHost) corpusRequests() []legacyItemsRequest {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]legacyItemsRequest(nil), host.requests...)
}
