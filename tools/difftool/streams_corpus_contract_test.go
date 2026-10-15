// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const stockStaticStreamItemID = "7e897749137a8161f7a084cfad246bd3"

var staticStreamNormalizers = []string{
	"json-key-order",
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-language-header",
}

var streamsCorpusContract = []struct {
	name        string
	method      string
	path        string
	operationID string
	params      map[string]any
	headers     map[string]string
	authRole    string
	normalizers []string
	status      int
}{
	{
		name:        "static stream full GET",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream",
		operationID: "GetVideoStream",
		params:      staticStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "static stream full HEAD by container",
		authRole:    "fixture",
		method:      http.MethodHead,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream.mkv",
		operationID: "HeadVideoStreamByContainer",
		params:      staticStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "static stream prefix range",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream",
		operationID: "GetVideoStream",
		params:      staticStreamParams(),
		headers:     map[string]string{"Range": "bytes=0-9"},
		normalizers: staticStreamNormalizers,
		status:      http.StatusPartialContent,
	},
	{
		name:        "static stream suffix range by container",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream.mkv",
		operationID: "GetVideoStreamByContainer",
		params:      staticStreamParams(),
		headers:     map[string]string{"Range": "bytes=-10"},
		normalizers: staticStreamNormalizers,
		status:      http.StatusPartialContent,
	},
	{
		name:        "static stream malformed range",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream",
		operationID: "GetVideoStream",
		params:      staticStreamParams(),
		headers:     map[string]string{"Range": "bytes=not-a-range"},
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "static stream unsatisfiable range by container",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Videos/7e897749137a8161f7a084cfad246bd3/stream.mkv",
		operationID: "GetVideoStreamByContainer",
		params:      staticStreamParams(),
		headers:     map[string]string{"Range": "bytes=999999999-"},
		normalizers: staticStreamNormalizers,
		status:      http.StatusRequestedRangeNotSatisfiable,
	},
	{
		name:        "audio static stream full GET",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/stream",
		operationID: "GetAudioStream",
		params:      staticAudioStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio static stream full HEAD",
		authRole:    "fixture",
		method:      http.MethodHead,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/stream",
		operationID: "HeadAudioStream",
		params:      staticAudioStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio static stream full GET by container",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/stream.mp3",
		operationID: "GetAudioStreamByContainer",
		params:      staticAudioStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio static stream full HEAD by container",
		authRole:    "fixture",
		method:      http.MethodHead,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/stream.mp3",
		operationID: "HeadAudioStreamByContainer",
		params:      staticAudioStreamParams(),
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio static stream prefix range",
		authRole:    "fixture",
		method:      http.MethodGet,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/stream",
		operationID: "GetAudioStream",
		params:      staticAudioStreamParams(),
		headers:     map[string]string{"Range": "bytes=0-9"},
		normalizers: staticStreamNormalizers,
		status:      http.StatusPartialContent,
	},
	{
		name:        "universal audio direct stream GET",
		method:      http.MethodGet,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/universal",
		operationID: "GetUniversalAudioStream",
		params:      universalAudioStreamParams(),
		authRole:    "fixture",
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "universal audio direct stream HEAD",
		method:      http.MethodHead,
		path:        "/Audio/7d876c24abd39daf70475c9430cc55fb/universal",
		operationID: "HeadUniversalAudioStream",
		params:      universalAudioStreamParams(),
		authRole:    "fixture",
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio download with attachment filename",
		method:      http.MethodGet,
		path:        "/Items/7d876c24abd39daf70475c9430cc55fb/Download",
		operationID: "GetDownload",
		authRole:    "fixture",
		normalizers: staticStreamNormalizers,
		status:      http.StatusOK,
	},
	{
		name:        "audio download prefix range with attachment filename",
		method:      http.MethodGet,
		path:        "/Items/7d876c24abd39daf70475c9430cc55fb/Download",
		operationID: "GetDownload",
		headers:     map[string]string{"Range": "bytes=0-9"},
		authRole:    "fixture",
		normalizers: staticStreamNormalizers,
		status:      http.StatusPartialContent,
	},
}

func TestStreamsCorpusDefinesStaticDirectStreamHTTPContract(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "streams.yaml"))
	if err != nil {
		t.Fatalf("load committed streams corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed streams corpus scenarios = %d, want one", len(scenarios))
	}
	if err := requireStreamsCorpusContract(scenarios[0].Steps); err != nil {
		t.Fatal(err)
	}
}

func staticStreamParams() map[string]any {
	return map[string]any{"Static": true, "MediaSourceId": stockStaticStreamItemID}
}

const stockAudioStreamItemID = "7d876c24abd39daf70475c9430cc55fb"

func staticAudioStreamParams() map[string]any {
	return map[string]any{"Static": true, "MediaSourceId": stockAudioStreamItemID}
}

func universalAudioStreamParams() map[string]any {
	return map[string]any{"Container": []any{"mp3"}, "MediaSourceId": stockAudioStreamItemID}
}

func requireStreamsCorpusContract(steps []Step) error {
	if len(steps) != len(streamsCorpusContract) {
		return fmt.Errorf("streams corpus steps = %d, want %d direct stream cases", len(steps), len(streamsCorpusContract))
	}
	for index, expected := range streamsCorpusContract {
		step := steps[index]
		if step.Name != expected.name || step.Method != expected.method || step.Path != expected.path || step.OperationID != expected.operationID {
			return fmt.Errorf("streams step %d = (%q, %s, %q, %q), want (%q, %s, %q, %q)", index, step.Name, step.Method, step.Path, step.OperationID, expected.name, expected.method, expected.path, expected.operationID)
		}
		if !reflect.DeepEqual(step.Params, expected.params) {
			return fmt.Errorf("streams step %q params = %#v, want %#v", step.Name, step.Params, expected.params)
		}
		if !reflect.DeepEqual(step.Headers, expected.headers) {
			return fmt.Errorf("streams step %q headers = %#v, want %#v", step.Name, step.Headers, expected.headers)
		}
		if step.AuthRole != expected.authRole {
			return fmt.Errorf("streams step %q auth_role = %q, want %q", step.Name, step.AuthRole, expected.authRole)
		}
		if len(step.Normalize) != 0 || !reflect.DeepEqual(step.Normalizers, expected.normalizers) {
			return fmt.Errorf("streams step %q normalizers = %#v / %#v, want explicit %#v", step.Name, step.Normalize, step.Normalizers, expected.normalizers)
		}
		if step.Expect == nil || step.Expect.Status == nil || *step.Expect.Status != expected.status {
			return fmt.Errorf("streams step %q expect.status = %#v, want %d", step.Name, step.Expect, expected.status)
		}
		if step.Body != nil || step.BodyFile != "" || step.BodyFileField != "" {
			return fmt.Errorf("stream step %q must compare the response without imposing a request body", step.Name)
		}
	}
	return nil
}

func TestStreamsCorpusReplaysHostLocalItemIDsAcrossNativeAndCamelCase(t *testing.T) {
	const (
		rightStreamItemID = "0123456789abcdef0123456789abcdef"
		rightAudioItemID  = "fedcba9876543210fedcba9876543210"
	)

	for _, profile := range []struct {
		name  string
		value string
	}{
		{name: "native"},
		{name: "CamelCase", value: "CamelCase"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			left := newStaticStreamReplayHost(t, stockStaticStreamItemID, stockAudioStreamItemID)
			right := newStaticStreamReplayHost(t, rightStreamItemID, rightAudioItemID)

			result, err := Run(context.Background(), Config{
				Corpus:     filepath.Join("..", "..", "corpora", "streams.yaml"),
				LeftURL:    left.server.URL,
				RightURL:   right.server.URL,
				ResultsDir: filepath.Join(t.TempDir(), "results"),
				IssuesDir:  filepath.Join(t.TempDir(), "issues"),
				Profile:    profile.value,
				LeftAuth:   AuthConfig{Role: "fixture", Username: "left-fixture", Password: "left-password"},
				RightAuth:  AuthConfig{Role: "fixture", Username: "right-fixture", Password: "right-password"},
			})
			if err != nil {
				t.Fatalf("replay committed streams corpus: %v", err)
			}
			if len(result.Steps) != len(streamsCorpusContract) {
				t.Fatalf("stream replay results = %#v, want one passing result for each corpus step", result.Steps)
			}
			for _, step := range result.Steps {
				if step.Status != "pass" {
					t.Fatalf("stream replay step = %#v, want pass", step)
				}
			}

			for _, host := range []*staticStreamReplayHost{left, right} {
				host.requireCorpusRequests(t, profile.value)
			}
		})
	}
}

type staticStreamReplayRequest struct {
	method        string
	path          string
	mediaSourceID string
	rangeHeader   string
	static        string
	container     string
	authenticated bool
	accept        string
}

type staticStreamReplayHost struct {
	mu          sync.Mutex
	itemID      string
	audioItemID string
	requests    []staticStreamReplayRequest
	server      *httptest.Server
}

func newStaticStreamReplayHost(t *testing.T, itemID, audioItemID string) *staticStreamReplayHost {
	t.Helper()
	host := &staticStreamReplayHost{itemID: itemID, audioItemID: audioItemID}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			host.serveAuthentication(w)
			return
		case "/Items":
			host.serveItemMap(w)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/Videos/") || strings.HasPrefix(r.URL.Path, "/Audio/") || r.URL.Path == "/Items/"+host.audioItemID+"/Download" {
			host.recordStreamRequest(r)
			host.serveStream(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *staticStreamReplayHost) serveAuthentication(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"AccessToken":"fixture-token"}`)
}

func (host *staticStreamReplayHost) serveItemMap(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"Items":[{"Id":%q,"Type":"Movie","Path":"/media/static-direct-stream.mkv","MediaSourceId":%q,"MediaSources":[{"Id":%q,"Path":"/media/static-direct-stream.mkv"}]},{"Id":%q,"Type":"Audio","Path":"/media/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3","MediaSourceId":%q,"MediaSources":[{"Id":%q,"Path":"/media/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3"}]}],"TotalRecordCount":2}`, host.itemID, host.itemID, host.itemID, host.audioItemID, host.audioItemID, host.audioItemID)
}

func (host *staticStreamReplayHost) recordStreamRequest(r *http.Request) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.requests = append(host.requests, staticStreamReplayRequest{
		method:        r.Method,
		path:          r.URL.Path,
		mediaSourceID: r.URL.Query().Get("MediaSourceId"),
		rangeHeader:   r.Header.Get("Range"),
		static:        r.URL.Query().Get("Static"),
		container:     r.URL.Query().Get("Container"),
		authenticated: strings.Contains(r.Header.Get("Authorization"), `Token="fixture-token"`),
		accept:        r.Header.Get("Accept"),
	})
}

func (host *staticStreamReplayHost) serveStream(w http.ResponseWriter, r *http.Request) {
	expected, ok := host.expectedStreamRequest(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	contentType, contentLength, contentRange := "video/x-matroska", 10, ""
	if strings.HasPrefix(r.URL.Path, "/Audio/") || strings.HasPrefix(r.URL.Path, "/Items/") {
		contentType, contentLength = "audio/mpeg", 8685
	}
	if expected.status == http.StatusPartialContent {
		contentLength = 10
		if strings.HasPrefix(r.URL.Path, "/Audio/") {
			contentRange = "bytes 0-9/8685"
		} else {
			contentRange = "bytes 0-9/10"
		}
	}
	if expected.status == http.StatusRequestedRangeNotSatisfiable {
		contentLength, contentRange = 0, "bytes */10"
	}
	contentDisposition := ""
	if r.URL.Path == "/Items/"+host.audioItemID+"/Download" {
		contentDisposition = `attachment; filename="1 - Synthetic Track 1.mp3"; filename*=UTF-8''1%20-%20Synthetic%20Track%201.mp3`
	}
	writeStaticStreamResponse(w, r, expected.status, contentType, contentLength, contentRange, contentDisposition)
}

func (host *staticStreamReplayHost) expectedStreamRequest(r *http.Request) (struct {
	name        string
	method      string
	path        string
	operationID string
	params      map[string]any
	headers     map[string]string
	authRole    string
	normalizers []string
	status      int
}, bool,
) {
	for _, expected := range streamsCorpusContract {
		if r.Method == expected.method && r.URL.Path == host.rewriteStreamPath(expected.path) && r.Header.Get("Range") == expectedRangeHeader(expected.headers) {
			return expected, true
		}
	}
	return struct {
		name        string
		method      string
		path        string
		operationID string
		params      map[string]any
		headers     map[string]string
		authRole    string
		normalizers []string
		status      int
	}{}, false
}

func writeStaticStreamResponse(w http.ResponseWriter, r *http.Request, status int, contentType string, contentLength int, contentRange, contentDisposition string) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)
	if contentRange != "" {
		w.Header().Set("Content-Range", contentRange)
	}
	if contentDisposition != "" {
		w.Header().Set("Content-Disposition", contentDisposition)
	}
	w.Header().Set("Content-Length", fmt.Sprint(contentLength))
	w.WriteHeader(status)
	if r.Method != http.MethodHead && contentLength != 0 {
		_, _ = io.WriteString(w, strings.Repeat("0", contentLength))
	}
}

func (host *staticStreamReplayHost) requireCorpusRequests(t *testing.T, profile string) {
	t.Helper()
	host.mu.Lock()
	requests := append([]staticStreamReplayRequest(nil), host.requests...)
	host.mu.Unlock()
	if len(requests) != len(streamsCorpusContract) {
		t.Fatalf("%s stream requests = %#v, want one host-local request for each corpus step", host.server.URL, requests)
	}

	for index, expected := range streamsCorpusContract {
		request := requests[index]
		if request.method != expected.method || request.path != host.rewriteStreamPath(expected.path) || request.rangeHeader != expectedRangeHeader(expected.headers) {
			t.Fatalf("%s stream request %d = %#v, want method=%q path=%q Range=%q", host.server.URL, index, request, expected.method, host.rewriteStreamPath(expected.path), expectedRangeHeader(expected.headers))
		}
		if sourceID, hasSource := expected.params["MediaSourceId"]; hasSource && request.mediaSourceID != host.rewriteItemID(sourceID.(string)) {
			t.Fatalf("%s stream request %q MediaSourceId = %q, want host-local item ID %q", host.server.URL, expected.name, request.mediaSourceID, host.rewriteItemID(sourceID.(string)))
		}
		if static, hasStatic := expected.params["Static"]; hasStatic && request.static != fmt.Sprint(static) {
			t.Fatalf("%s stream request %q Static = %q, want %q", host.server.URL, expected.name, request.static, fmt.Sprint(static))
		} else if !hasStatic && request.static != "" {
			t.Fatalf("%s stream request %q unexpectedly supplied Static=%q", host.server.URL, expected.name, request.static)
		}
		if container, hasContainer := expected.params["Container"]; hasContainer && request.container != container.([]any)[0].(string) {
			t.Fatalf("%s stream request %q Container = %q, want %q", host.server.URL, expected.name, request.container, container.([]any)[0].(string))
		}
		if request.authenticated != (expected.authRole != "") {
			t.Fatalf("%s stream request %q authenticated = %t, want %t", host.server.URL, expected.name, request.authenticated, expected.authRole != "")
		}
		if profile == "CamelCase" && request.accept != `application/json; profile="CamelCase"` {
			t.Fatalf("%s CamelCase stream request %q Accept = %q, want profile negotiation", host.server.URL, expected.name, request.accept)
		}
	}
}

func (host *staticStreamReplayHost) rewriteStreamPath(path string) string {
	return strings.ReplaceAll(strings.ReplaceAll(path, stockStaticStreamItemID, host.itemID), stockAudioStreamItemID, host.audioItemID)
}

func (host *staticStreamReplayHost) rewriteItemID(itemID string) string {
	if itemID == stockAudioStreamItemID {
		return host.audioItemID
	}
	return host.itemID
}

func expectedRangeHeader(headers map[string]string) string {
	if headers == nil {
		return ""
	}
	return headers["Range"]
}
