// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestScenarioReplaySubstitutesHostLocalAuthCredentialsWithoutLeakingThem(t *testing.T) {
	const (
		leftUsername  = "left-runtime-username"
		leftPassword  = "left-runtime-password"
		rightUsername = "right-runtime-username"
		rightPassword = "right-runtime-password"
	)
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login":     {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"authenticated":true}`},
		"/different": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"side":"left"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login":     {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"authenticated":true}`},
		"/different": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"side":"right"}`},
	})

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "runtime-credentials.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: host-local runtime credentials
steps:
  - name: replay login with host-local runtime credentials
    method: POST
    path: /login
    body:
      Username: ${auth_username}
      Pw: ${auth_password}
    operation_id: ReplayLogin
  - name: intentionally different response produces a redacted issue artifact
    method: GET
    path: /different
    operation_id: DifferentResponse
`)

	corpusContents, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	for _, credential := range []string{leftUsername, leftPassword, rightUsername, rightPassword} {
		if strings.Contains(string(corpusContents), credential) {
			t.Fatalf("corpus embeds runtime credential %q:\n%s", credential, corpusContents)
		}
	}

	issues := filepath.Join(directory, "issues")
	_, err = Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  issues,
		LeftAuth:   AuthConfig{Username: leftUsername, Password: leftPassword},
		RightAuth:  AuthConfig{Username: rightUsername, Password: rightPassword},
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("later deliberately different response must produce a comparison error: %v", err)
	}

	for _, tc := range []struct {
		name     string
		host     *fixtureHost
		username string
		password string
	}{
		{name: "left", host: left, username: leftUsername, password: leftPassword},
		{name: "right", host: right, username: rightUsername, password: rightPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(tc.host.onlyRequest(t, "/login").Body), &body); err != nil {
				t.Fatalf("decode replay login body: %v", err)
			}
			if body["Username"] != tc.username || body["Pw"] != tc.password {
				t.Fatalf("non-bootstrap login body = %#v, want this host's runtime credentials", body)
			}
		})
	}

	artifacts := issueArtifactContents(t, issues)
	if len(artifacts) != 1 {
		t.Fatalf("one deliberately different response must produce one issue artifact, got %#v", artifacts)
	}
	for _, credential := range []string{leftUsername, leftPassword, rightUsername, rightPassword} {
		if strings.Contains(err.Error(), credential) {
			t.Fatalf("comparison error exposes runtime credential %q: %v", credential, err)
		}
		for path, contents := range artifacts {
			if strings.Contains(contents, credential) {
				t.Fatalf("issue artifact %s exposes runtime credential %q:\n%s", path, credential, contents)
			}
		}
	}
}

func TestScenarioReplayCapturesHostLocalResponseValuesForLaterRequests(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login": {
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"AccessToken":"left-token","User":{"Id":"left-user"},"SessionInfo":{"Id":"left-session"}}`,
		},
		"/uses/left-user": {
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"result":"ok"}`,
		},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login": {
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"AccessToken":"right-token","User":{"Id":"right-user"},"SessionInfo":{"Id":"right-session"}}`,
		},
		"/uses/right-user": {
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"result":"ok"}`,
		},
	})

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "capture.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: host-local response captures
steps:
  - name: login response provides symbolic values
    method: POST
    path: /login
    capture:
      token: AccessToken
      user_id: User.Id
      session_id: SessionInfo.Id
    operation_id: Login
  - name: subsequent request resolves each host's own captures
    method: POST
    path: /uses/${user_id}
    params:
      session: ${session_id}
    headers:
      X-Session-Token: Bearer ${token}
    body:
      token: ${token}
      user: ${user_id}
      session: ${session_id}
      unmodified_number: 7
    operation_id: UseCapturedValues
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("replay must continue through later captured request after the intentionally host-specific login response: %v", err)
	}

	assertCapturedRequest(t, left.onlyRequest(t, "/uses/left-user"), "left-token", "left-user", "left-session")
	assertCapturedRequest(t, right.onlyRequest(t, "/uses/right-user"), "right-token", "right-user", "right-session")
}

func TestScenarioReplayPreservesOpaqueCapturedValuesWhileMappingLiteralItemReferences(t *testing.T) {
	const (
		sourceID      = "00112233445566778899aabbccddeeff"
		rightTargetID = "right-destination-item"
		token         = "token-00112233445566778899aabbccddeeff-opaque"
		session       = "session-00112233445566778899aabbccddeeff-opaque"
	)

	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items":             {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"` + sourceID + `","Type":"Movie","Path":"/media/opaque-id.mkv"}],"TotalRecordCount":1}`},
		"/login":             {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"AccessToken":"` + token + `","SessionInfo":{"Id":"` + session + `"}}`},
		"/items/" + sourceID: {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"result":"ok"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items":                  {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"` + rightTargetID + `","Type":"Movie","Path":"/media/opaque-id.mkv"}],"TotalRecordCount":1}`},
		"/login":                  {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"AccessToken":"` + token + `","SessionInfo":{"Id":"` + session + `"}}`},
		"/items/" + rightTargetID: {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"result":"ok"}`},
	})

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "opaque-captures.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: opaque captured values retain embedded item IDs
steps:
  - name: capture opaque response values
    method: POST
    path: /login
    capture:
      token: AccessToken
      session: SessionInfo.Id
    operation_id: Login
  - name: map authored item references without changing captures
    method: POST
    path: /items/00112233445566778899aabbccddeeff?pathItemId=00112233445566778899aabbccddeeff
    params:
      queryItemId: 00112233445566778899aabbccddeeff
      token: ${token}
      session: ${session}
    headers:
      X-Literal-Item-ID: 00112233445566778899aabbccddeeff
      X-Opaque-Token: ${token}
      X-Opaque-Session: ${session}
    body:
      ItemId: 00112233445566778899aabbccddeeff
      Nested:
        OwnerId: 00112233445566778899aabbccddeeff
      OpaqueToken: ${token}
      OpaqueSession: ${session}
    operation_id: UseOpaqueCaptures
`)

	if _, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	}); err != nil {
		t.Fatalf("replay with mapped literal IDs and equal opaque captures: %v", err)
	}

	assertOpaqueCapturedReplayRequest(t, left.onlyRequest(t, "/items/"+sourceID), sourceID, token, session)
	assertOpaqueCapturedReplayRequest(t, right.onlyRequest(t, "/items/"+rightTargetID), rightTargetID, token, session)
}

func assertOpaqueCapturedReplayRequest(t *testing.T, request recordedRequest, targetID, token, session string) {
	t.Helper()
	query, err := url.ParseQuery(request.Query)
	if err != nil {
		t.Fatalf("parse replay query: %v", err)
	}
	for _, name := range []string{"pathItemId", "queryItemId"} {
		if got := query.Get(name); got != targetID {
			t.Fatalf("literal item query %s = %q, want host target %q", name, got, targetID)
		}
	}
	if got := query.Get("token"); got != token {
		t.Fatalf("opaque captured token query = %q, want byte-for-byte capture %q", got, token)
	}
	if got := query.Get("session"); got != session {
		t.Fatalf("opaque captured session query = %q, want byte-for-byte capture %q", got, session)
	}
	if got := request.Header.Get("X-Literal-Item-ID"); got != targetID {
		t.Fatalf("literal item header = %q, want host target %q", got, targetID)
	}
	if got := request.Header.Get("X-Opaque-Token"); got != token {
		t.Fatalf("opaque captured token header = %q, want byte-for-byte capture %q", got, token)
	}
	if got := request.Header.Get("X-Opaque-Session"); got != session {
		t.Fatalf("opaque captured session header = %q, want byte-for-byte capture %q", got, session)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		t.Fatalf("decode replay body: %v", err)
	}
	if got := body["ItemId"]; got != targetID {
		t.Fatalf("literal item body ID = %#v, want host target %q", got, targetID)
	}
	nested, ok := body["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("replay body nested value = %#v, want object", body["Nested"])
	}
	if got := nested["OwnerId"]; got != targetID {
		t.Fatalf("literal nested item body ID = %#v, want host target %q", got, targetID)
	}
	if got := body["OpaqueToken"]; got != token {
		t.Fatalf("opaque captured token body = %#v, want byte-for-byte capture %q", got, token)
	}
	if got := body["OpaqueSession"]; got != session {
		t.Fatalf("opaque captured session body = %#v, want byte-for-byte capture %q", got, session)
	}
}

func TestScenarioReplayCapturesPascalCaseSessionPathFromCamelCaseProfileResponse(t *testing.T) {
	const sessionID = "fixture-session-id"

	var (
		capabilityRequests []recordedRequest
		requestsMu         sync.Mutex
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			if got := r.Header.Get("Accept"); got != `application/json; profile="CamelCase"` {
				t.Errorf("CamelCase login request Accept = %q, want profile negotiation", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessionInfo":{"id":"fixture-session-id"}}`))
		case "/Sessions/Capabilities/Full":
			requestsMu.Lock()
			capabilityRequests = append(capabilityRequests, recordedRequest{Query: r.URL.RawQuery})
			requestsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "camelcase-session-capture.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: CamelCase profile session capture
steps:
  - name: password login response captures a session
    method: POST
    path: /Users/AuthenticateByName
    capture:
      session_id: SessionInfo.Id
    operation_id: AuthenticateUserByName
  - name: report session capabilities
    method: POST
    path: /Sessions/Capabilities/Full
    params:
      id: ${session_id}
    operation_id: PostFullCapabilities
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    server.URL,
		RightURL:   server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
		Profile:    "CamelCase",
	})
	if err != nil {
		t.Fatalf("CamelCase response must resolve the declared PascalCase capture path: %v", err)
	}

	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(capabilityRequests) != 2 {
		t.Fatalf("captured session must replay the capability request for both hosts, got %#v", capabilityRequests)
	}
	for _, request := range capabilityRequests {
		query, err := url.ParseQuery(request.Query)
		if err != nil {
			t.Fatalf("parse capability request query: %v", err)
		}
		if got := query.Get("id"); got != sessionID {
			t.Fatalf("capability request session id = %q, want captured session %q", got, sessionID)
		}
	}
}

func TestCaptureResponseTraversesDTOCasingWithoutRecasingDictionaryKeys(t *testing.T) {
	t.Run("declared PascalCase path resolves lower-camel DTO properties", func(t *testing.T) {
		captures := make(map[string]string)
		err := captureResponse(
			Step{Capture: map[string]string{"session_id": "SessionInfo.Id"}},
			Response{Body: []byte(`{"sessionInfo":{"id":"camel-session-id"}}`)},
			captures,
		)
		if err != nil {
			t.Fatalf("capture lower-camel DTO properties: %v", err)
		}
		if got := captures["session_id"]; got != "camel-session-id" {
			t.Fatalf("captured session ID = %q, want lower-camel DTO value", got)
		}
	})

	t.Run("exact PascalCase key wins over lower-camel DTO property", func(t *testing.T) {
		captures := make(map[string]string)
		err := captureResponse(
			Step{Capture: map[string]string{"session_id": "SessionInfo.Id"}},
			Response{Body: []byte(`{"SessionInfo":{"Id":"exact-session-id"},"sessionInfo":{"id":"camel-session-id"}}`)},
			captures,
		)
		if err != nil {
			t.Fatalf("capture exact PascalCase DTO property: %v", err)
		}
		if got := captures["session_id"]; got != "exact-session-id" {
			t.Fatalf("captured session ID = %q, want exact PascalCase value", got)
		}
	})

	t.Run("recased DTO parent does not recase ProviderIds dictionary key", func(t *testing.T) {
		captures := make(map[string]string)
		err := captureResponse(
			Step{Capture: map[string]string{"tmdb": "Item.ProviderIds.Tmdb"}},
			Response{Body: []byte(`{"item":{"providerIds":{"tmdb":"lowercase-map-value"}}}`)},
			captures,
		)
		if err == nil {
			t.Fatal("lowercase dictionary key must not satisfy declared exact Tmdb capture path")
		}
		if !strings.Contains(err.Error(), "path is absent") {
			t.Fatalf("dictionary key casing error = %v, want absent-path error", err)
		}
		if _, ok := captures["tmdb"]; ok {
			t.Fatalf("lowercase dictionary key must not be captured: %#v", captures)
		}
	})
}

func TestScenarioReplayCapturesHostLocalArrayItemIDs(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items":                      {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"left-first"},{"Id":"left-middle"},{"Id":"left-later"}]}`},
		"/uses/left-first/left-later": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"result":"left"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items":                        {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"right-first"},{"Id":"right-middle"},{"Id":"right-later"}]}`},
		"/uses/right-first/right-later": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"result":"right"}`},
	})

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "array-captures.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: host-local array response captures
steps:
  - name: item response provides indexed symbolic values
    method: GET
    path: /Items
    capture:
      first_item_id: Items.0.Id
      later_item_id: Items.2.Id
    operation_id: GetItems
  - name: later request resolves each host's indexed captures
    method: GET
    path: /uses/${first_item_id}/${later_item_id}
    operation_id: UseIndexedItemIDs
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("replay must continue through a request using host-local indexed captures: %v", err)
	}
	left.onlyRequest(t, "/uses/left-first/left-later")
	right.onlyRequest(t, "/uses/right-first/right-later")
}

func TestCaptureResponseTraversesArrayIndicesAndPreservesNumericObjectKeys(t *testing.T) {
	captures := make(map[string]string)
	err := captureResponse(
		Step{Capture: map[string]string{
			"first_item_id":  "Items.0.Id",
			"later_item_id":  "Items.2.Id",
			"numeric_map_id": "ByOrdinal.0.Id",
		}},
		Response{Body: []byte(`{"Items":[{"Id":"first-item"},{"Id":"middle-item"},{"Id":"later-item"}],"ByOrdinal":{"0":{"Id":"numeric-map-key"}}}`)},
		captures,
	)
	if err != nil {
		t.Fatalf("capture array and numeric object-key paths: %v", err)
	}
	for name, want := range map[string]string{
		"first_item_id":  "first-item",
		"later_item_id":  "later-item",
		"numeric_map_id": "numeric-map-key",
	} {
		if got := captures[name]; got != want {
			t.Fatalf("capture %q = %q, want %q", name, got, want)
		}
	}
}

func TestCaptureResponseRejectsInvalidArrayTraversal(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "negative index", path: "Items.-1.Id", body: `{"Items":[{"Id":"one"}]}`, want: `capture "item_id" at "Items.-1.Id": array index "-1" is invalid`},
		{name: "signed index", path: "Items.+1.Id", body: `{"Items":[{"Id":"one"}]}`, want: `capture "item_id" at "Items.+1.Id": array index "+1" is invalid`},
		{name: "leading-zero index", path: "Items.01.Id", body: `{"Items":[{"Id":"one"}]}`, want: `capture "item_id" at "Items.01.Id": array index "01" is invalid`},
		{name: "nonnumeric index", path: "Items.one.Id", body: `{"Items":[{"Id":"one"}]}`, want: `capture "item_id" at "Items.one.Id": array index "one" is invalid`},
		{name: "out-of-range index", path: "Items.1.Id", body: `{"Items":[{"Id":"one"}]}`, want: `capture "item_id" at "Items.1.Id": array index "1" is out of range`},
		{name: "scalar traversal", path: "Item.Id.Value", body: `{"Item":{"Id":"scalar"}}`, want: `capture "item_id" at "Item.Id.Value": cannot traverse scalar at segment "Value"`},
		{name: "missing object key", path: "Item.Missing", body: `{"Item":{"Id":"present"}}`, want: `capture "item_id" at "Item.Missing": path is absent`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captures := make(map[string]string)
			err := captureResponse(Step{Capture: map[string]string{"item_id": tc.path}}, Response{Body: []byte(tc.body)}, captures)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("capture error = %v, want %q", err, tc.want)
			}
			if _, ok := captures["item_id"]; ok {
				t.Fatalf("failed capture must not populate values: %#v", captures)
			}
		})
	}
}

func TestCaptureResponseTerminalScalarContract(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{name: "string", body: `{"Value":"text"}`, want: "text"},
		{name: "number", body: `{"Value":123.45}`, want: "123.45"},
		{name: "boolean", body: `{"Value":true}`, want: "true"},
		{name: "null", body: `{"Value":null}`, wantErr: `capture "value" at "Value": value is not a scalar`},
		{name: "array", body: `{"Value":["text"]}`, wantErr: `capture "value" at "Value": value is not a scalar`},
		{name: "object", body: `{"Value":{"nested":"text"}}`, wantErr: `capture "value" at "Value": value is not a scalar`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captures := make(map[string]string)
			err := captureResponse(Step{Capture: map[string]string{"value": "Value"}}, Response{Body: []byte(tc.body)}, captures)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("capture error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("capture terminal %s: %v", tc.name, err)
			}
			if got := captures["value"]; got != tc.want {
				t.Fatalf("captured terminal %s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestScenarioReplayRejectsUnknownCaptureVariableWithoutLeakingCapturedValues(t *testing.T) {
	const token = "captured-token-must-not-leak"
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"AccessToken":"` + token + `"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/login": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"AccessToken":"` + token + `"}`},
	})

	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	corpus := filepath.Join(directory, "unknown-variable.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: unknown capture variable
steps:
  - name: capture a private token
    method: POST
    path: /login
    capture:
      token: AccessToken
    operation_id: Login
  - name: unknown variable is a hard error
    method: GET
    path: /uses/${missing}
    operation_id: UnknownCaptureVariable
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
	if err == nil {
		t.Fatal("an unknown capture variable must fail replay instead of being sent literally")
	}
	for _, secret := range []string{token} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("unknown capture failure exposes captured value in error: %v", err)
		}
	}
	for path, contents := range issueArtifactContents(t, filepath.Join(directory, "issues")) {
		for _, secret := range []string{token} {
			if strings.Contains(contents, secret) {
				t.Fatalf("unknown capture failure exposes captured value in issue artifact %s:\n%s", path, contents)
			}
		}
	}
}

func TestScenarioReplayRejectsMissingAndNonScalarCaptureValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		path string
	}{
		{name: "missing JSON path", body: `{"User":{}}`, path: "User.Id"},
		{name: "non-scalar JSON value", body: `{"User":{"Id":{"nested":true}}}`, path: "User.Id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := newFixtureHost(t, "unused", map[string]fixtureResponse{
				"/login": {Headers: map[string]string{"Content-Type": "application/json"}, Body: tc.body},
			})
			right := newFixtureHost(t, "unused", map[string]fixtureResponse{
				"/login": {Headers: map[string]string{"Content-Type": "application/json"}, Body: tc.body},
			})

			directory := t.TempDir()
			rules := writeCaptureRule(t, directory)
			corpus := filepath.Join(directory, "invalid-capture.yaml")
			writeFile(t, corpus, "format: goldenrec-scenario/v1\nname: invalid response capture\nsteps:\n  - name: response capture must resolve a scalar\n    method: POST\n    path: /login\n    capture:\n      user_id: "+tc.path+"\n    operation_id: Login\n")

			_, err := Run(context.Background(), Config{Corpus: corpus, LeftURL: left.server.URL, RightURL: right.server.URL, RulesDir: rules, ResultsDir: filepath.Join(directory, "results"), IssuesDir: filepath.Join(directory, "issues")})
			if err == nil {
				t.Fatalf("%s must fail replay rather than accepting an invalid capture", tc.name)
			}
		})
	}
}

func writeCaptureRule(t *testing.T, directory string) string {
	t.Helper()
	rules := filepath.Join(directory, "rules")
	if err := os.Mkdir(rules, 0o755); err != nil {
		t.Fatalf("create rules directory: %v", err)
	}
	writeFile(t, filepath.Join(rules, "stable.yaml"), "version: difftool-normalize/v1\nrules:\n  - name: stable\n    action: sort_json_keys\n    reason: deterministic capture test rule\n")
	return rules
}

func assertCapturedRequest(t *testing.T, request recordedRequest, token, userID, sessionID string) {
	t.Helper()
	query, err := url.ParseQuery(request.Query)
	if err != nil {
		t.Fatalf("parse replay query: %v", err)
	}
	if query.Get("session") != sessionID {
		t.Fatalf("replay query session = %q, want host-local session %q", query.Get("session"), sessionID)
	}
	if got := request.Header.Get("X-Session-Token"); got != "Bearer "+token {
		t.Fatalf("replay header = %q, want host-local token", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		t.Fatalf("decode replay body: %v", err)
	}
	if body["token"] != token || body["user"] != userID || body["session"] != sessionID || body["unmodified_number"] != float64(7) {
		t.Fatalf("replay body = %#v, want host-local substitutions and intact non-string value", body)
	}
}
