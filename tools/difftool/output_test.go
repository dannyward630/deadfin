// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func issueArtifactContents(t *testing.T, directory string) map[string]string {
	t.Helper()
	artifacts := make(map[string]string)
	walkErr := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		artifacts[relative] = string(contents)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk issue artifacts: %v", walkErr)
	}
	return artifacts
}

func TestDifftoolIssueArtifactRedactsNestedSecretsAndTokenURLs(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Headers: map[string]string{
				"Content-Type": "application/json",
				"Set-Cookie":   "session=response-left-cookie-secret; HttpOnly",
				"X-API-Key":    "response-left-header-api-key",
				"X-Callback":   "https://response-left.example.test/callback?access_token=response-left-header-url-token",
			},
			Body: `{"credentials":{"api_key":"response-left-body-api-key","nested":{"refresh_token":"response-left-body-refresh-token"}},"callback_url":"https://response-left.example.test/body?token=response-left-body-url-token"}`,
		},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Headers: map[string]string{
				"Content-Type": "application/json",
				"Set-Cookie":   "session=response-right-cookie-secret; HttpOnly",
				"X-API-Key":    "response-right-header-api-key",
				"X-Callback":   "https://response-right.example.test/callback?access_token=response-right-header-url-token",
			},
			Body: `{"credentials":{"api_key":"response-right-body-api-key","nested":{"refresh_token":"response-right-body-refresh-token"}},"callback_url":"https://response-right.example.test/body?token=response-right-body-url-token"}`,
		},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "secrets.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: artifact redaction
steps:
  - name: redact every artifact surface
    method: POST
    path: /leak
    params:
      api_key: request-metadata-api-key
      nested:
        refresh_token: request-metadata-refresh-token
      callback_url: https://request-metadata.example.test/callback?token=request-metadata-url-token
    headers:
      Content-Type: application/json
      X-API-Key: request-metadata-header-api-key
      X-Callback: https://request-metadata.example.test/header?access_token=request-metadata-header-url-token
    body:
      credentials:
        api_key: request-body-api-key
        nested:
          refresh_token: request-body-refresh-token
      callback_url: https://request-body.example.test/callback?token=request-body-url-token
    operation_id: ArtifactRedaction
`)
	issues := filepath.Join(directory, "issues")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", filepath.Join(directory, "results"), "--issues", issues)
	if err == nil {
		t.Fatalf("difftool passed despite the deliberately different responses\n%s", output)
	}

	artifacts := issueArtifactContents(t, issues)
	if len(artifacts) != 1 {
		t.Fatalf("one response mismatch must produce one issue artifact, got %#v", artifacts)
	}
	for path, contents := range artifacts {
		for _, secret := range []string{
			"request-metadata-api-key", "request-metadata-refresh-token", "request-metadata-url-token", "request-metadata-header-api-key", "request-metadata-header-url-token",
			"request-body-api-key", "request-body-refresh-token", "request-body-url-token",
			"response-left-body-api-key", "response-left-body-refresh-token", "response-left-body-url-token", "response-right-body-api-key", "response-right-body-refresh-token", "response-right-body-url-token",
			"response-left-header-api-key", "response-left-header-url-token", "response-right-header-api-key", "response-right-header-url-token",
			"response-left-cookie-secret", "response-right-cookie-secret",
		} {
			if strings.Contains(contents, secret) {
				t.Errorf("issue artifact %s exposes secret %q:\n%s", path, secret, contents)
			}
		}
	}
}

func TestRunRedactsLiveBootstrapCredentialsEchoedOnEveryIssueSurface(t *testing.T) {
	const (
		leftUsername  = "left-live-bootstrap-user"
		leftPassword  = "left-live-bootstrap-password"
		leftToken     = "left-live-bootstrap-token"
		rightUsername = "right-live-bootstrap-user"
		rightPassword = "right-live-bootstrap-password"
		rightToken    = "right-live-bootstrap-token"
	)

	left := newRuntimeSecretEchoServer(t, "left", leftToken)
	right := newRuntimeSecretEchoServer(t, "right", rightToken)
	directory := t.TempDir()
	issues := filepath.Join(directory, "issues")
	results := filepath.Join(directory, "results")
	corpus := filepath.Join(directory, "runtime-secret-echo.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: live bootstrap credentials never leave difftool artifacts
steps:
  - name: echo credentials as plain text and an unknown header
    method: GET
    path: /plain
    auth_role: fixture
    auth_carrier: query_api_key
    operation_id: RuntimeSecretPlainEcho
  - name: echo credentials in unknown JSON and URL values
    method: GET
    path: /json
    auth_role: fixture
    auth_carrier: query_api_key
    operation_id: RuntimeSecretJSONEcho
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.URL,
		RightURL:   right.URL,
		RulesDir:   writeCaptureRule(t, directory),
		ResultsDir: results,
		IssuesDir:  issues,
		LeftAuth:   AuthConfig{Role: "fixture", Username: leftUsername, Password: leftPassword},
		RightAuth:  AuthConfig{Role: "fixture", Username: rightUsername, Password: rightPassword},
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("credential-echo responses must produce comparison differences, got %v", err)
	}

	issueArtifacts := issueArtifactContents(t, issues)
	if len(issueArtifacts) != 2 {
		t.Fatalf("two deliberately different credential echoes must produce two issue artifacts, got %#v", issueArtifacts)
	}
	artifacts := issueArtifactContents(t, issues)
	resultEntries, readErr := os.ReadDir(results)
	if readErr != nil {
		t.Fatalf("read result artifacts: %v", readErr)
	}
	for _, entry := range resultEntries {
		if entry.IsDir() {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(results, entry.Name()))
		if readErr != nil {
			t.Fatalf("read result artifact %s: %v", entry.Name(), readErr)
		}
		artifacts[filepath.Join("results", entry.Name())] = string(contents)
	}
	for path, contents := range artifacts {
		for _, secret := range []string{leftUsername, leftPassword, leftToken, rightUsername, rightPassword, rightToken} {
			if strings.Contains(contents, secret) {
				t.Errorf("artifact %s exposes runtime credential %q:\n%s", path, secret, contents)
			}
		}
	}
	for _, secret := range []string{leftUsername, leftPassword, leftToken, rightUsername, rightPassword, rightToken} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("run error exposes runtime credential %q: %v", secret, err)
		}
	}

	for path, contents := range issueArtifacts {
		for _, benign := range []string{"benign-header-", "benign-plain-", "benign-url-"} {
			if !strings.Contains(contents, benign) {
				t.Errorf("issue artifact %s discarded benign context %q while redacting credentials:\n%s", path, benign, contents)
			}
		}
		if !strings.Contains(contents, "## Difference\n\n") {
			t.Errorf("issue artifact %s omits its difference section:\n%s", path, contents)
		}
	}
}

func newRuntimeSecretEchoServer(t *testing.T, side, token string) *httptest.Server {
	t.Helper()
	var credentials struct {
		username string
		password string
	}
	var credentialsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/Users/AuthenticateByName" {
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode bootstrap credentials: %v", err)
				http.Error(w, "invalid bootstrap payload", http.StatusBadRequest)
				return
			}
			credentialsMu.Lock()
			credentials.username = body["Username"]
			credentials.password = body["Pw"]
			credentialsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
			return
		}

		credentialsMu.Lock()
		username, password := credentials.username, credentials.password
		credentialsMu.Unlock()
		apiKey := request.URL.Query().Get("ApiKey")
		w.Header().Set("X-Echo", fmt.Sprintf("username=%s password=%s api_key=%s benign-header-%s", username, password, apiKey, side))
		switch request.URL.Path {
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "plain token=%s password=%s api_key=%s benign-plain-%s url=https://runtime.example.test/callback?signature=%s&keep=benign-url-%s", token, password, apiKey, side, token, side)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"side":%q,"unrecognized_echo":%q,"unrecognized_url":%q}`, side, "token="+token+" password="+password+" api_key="+apiKey+" benign-plain-"+side, "https://runtime.example.test/callback?signature="+token+"&keep=benign-url-"+side)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunRedactsShortConfiguredPasswordsWithoutEmptyValueCorruption(t *testing.T) {
	const (
		leftPassword          = "~"
		rightPassword         = ""
		leftDisabledPassword  = "~~~"
		rightDisabledPassword = "~~"
	)
	shortPasswords := []string{leftPassword, rightPassword, leftDisabledPassword, rightDisabledPassword}
	left := newShortCredentialSurfaceServer(t, "left", "left-short-secret-token", shortPasswords)
	right := newShortCredentialSurfaceServer(t, "right", "right-short-secret-token", shortPasswords)
	directory := t.TempDir()
	issues := filepath.Join(directory, "issues")
	results := filepath.Join(directory, "results")
	corpus := filepath.Join(directory, "short-passwords.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: short configured passwords remain secret
steps:
  - name: transport error preserves only safe URL context
    method: GET
    path: /transport?opaque_auth=${auth_password}&opaque_disabled=${disabled_password}&empty=benign-empty-secret-sentinel&keep=benign-transport-context
    auth_role: fixture
    operation_id: ShortPasswordTransport
  - name: arbitrary response surfaces keep benign context
    method: GET
    path: /echo
    auth_role: fixture
    operation_id: ShortPasswordResponse
`)

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.URL,
		RightURL:   right.URL,
		RulesDir:   writeCaptureRule(t, directory),
		ResultsDir: results,
		IssuesDir:  issues,
		LeftAuth: AuthConfig{
			Role: "fixture", Username: "left-short-user", Password: leftPassword,
			DisabledUsername: "left-short-disabled", DisabledPassword: leftDisabledPassword,
		},
		RightAuth: AuthConfig{
			Role: "fixture", Username: "right-short-user", Password: rightPassword,
			DisabledUsername: "right-short-disabled", DisabledPassword: rightDisabledPassword,
		},
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("short-password transport and response mismatches must produce comparison differences, got %v", err)
	}

	issuesByPath := issueArtifactContents(t, issues)
	if len(issuesByPath) != 2 {
		t.Fatalf("transport and response mismatches must produce two issue artifacts, got %#v", issuesByPath)
	}
	artifacts := issueArtifactContents(t, issues)
	entries, readErr := os.ReadDir(results)
	if readErr != nil {
		t.Fatalf("read result artifacts: %v", readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(results, entry.Name()))
		if readErr != nil {
			t.Fatalf("read result artifact %s: %v", entry.Name(), readErr)
		}
		artifacts[filepath.Join("results", entry.Name())] = string(contents)
	}
	for path, contents := range artifacts {
		for _, secret := range []string{leftPassword, leftDisabledPassword, rightDisabledPassword} {
			if strings.Contains(contents, secret) {
				t.Errorf("artifact %s exposes configured short password %q:\n%s", path, secret, contents)
			}
		}
	}
	for _, secret := range []string{leftPassword, leftDisabledPassword, rightDisabledPassword} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("run error exposes configured short password %q: %v", secret, err)
		}
	}

	for path, contents := range issuesByPath {
		if !strings.Contains(contents, "benign-empty-secret-sentinel") {
			t.Errorf("issue artifact %s was globally corrupted by the empty auth password:\n%s", path, contents)
		}
		if strings.Contains(path, "transport") && !strings.Contains(contents, "benign-transport-context") {
			t.Errorf("transport error artifact %s discarded benign URL context:\n%s", path, contents)
		}
		if strings.Contains(path, "response") {
			for _, heading := range []string{"X-Echo", "## Difference", "## Left raw response", "## Left normalized response"} {
				if !strings.Contains(contents, heading) {
					t.Errorf("response artifact %s omits arbitrary response surface %q:\n%s", path, heading, contents)
				}
			}
		}
	}
}

func newShortCredentialSurfaceServer(t *testing.T, side, token string, passwords []string) *httptest.Server {
	t.Helper()
	secretEcho := strings.Join(passwords, "|")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/Users/AuthenticateByName" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
			return
		}
		switch request.URL.Path {
		case "/transport":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server response writer does not support hijacking")
				http.Error(w, "transport failure unavailable", http.StatusInternalServerError)
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack transport connection: %v", err)
				return
			}
			_ = connection.Close()
		case "/echo":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("X-Echo", "opaque="+secretEcho+" benign-empty-secret-sentinel")
			_, _ = fmt.Fprintf(w, "opaque=%s benign-empty-secret-sentinel side=%s url=https://runtime.example.test/echo?opaque_password=%s&keep=benign-response-url", secretEcho, side, secretEcho)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDifftoolIssueArtifactRedactsNestedMalformedURLsWithoutDiscardingSafeStructure(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Body: `{"result":"left","metadata":{"callback_url":"https://response-left-user:response-left-password@response-left.example.test/nested?mode=inspect&access_token=response-left-query-token&broken;component=value#view=summary&refresh_token=response-left-fragment-token"}}`,
		},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Body: `{"result":"right","metadata":{"callback_url":"https://response-right-user:response-right-password@response-right.example.test/nested?mode=inspect&access_token=response-right-query-token&broken;component=value#view=summary&refresh_token=response-right-fragment-token"}}`,
		},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "nested-malformed-url.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: nested malformed URL artifact redaction
steps:
  - name: redact nested URL credentials and tokens
    method: POST
    path: /leak
    body:
      metadata:
        callback_url: "https://request-user:request-password@request.example.test/nested?mode=inspect&access_token=request-query-token&broken;component=value#view=summary&refresh_token=request-fragment-token"
    operation_id: NestedMalformedURLRedaction
`)
	issues := filepath.Join(directory, "issues")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", filepath.Join(directory, "results"), "--issues", issues)
	if err == nil {
		t.Fatalf("difftool passed despite the deliberately different responses\n%s", output)
	}

	artifacts := issueArtifactContents(t, issues)
	if len(artifacts) != 1 {
		t.Fatalf("one response mismatch must produce one issue artifact, got %#v", artifacts)
	}
	for path, contents := range artifacts {
		for _, secret := range []string{
			"request-user", "request-password", "request-query-token", "request-fragment-token",
			"response-left-user", "response-left-password", "response-left-query-token", "response-left-fragment-token",
			"response-right-user", "response-right-password", "response-right-query-token", "response-right-fragment-token",
		} {
			if strings.Contains(contents, secret) {
				t.Errorf("issue artifact %s exposes nested URL secret %q:\n%s", path, secret, contents)
			}
		}
		for _, safeURLPart := range []string{
			"request.example.test/nested", "response-left.example.test/nested", "response-right.example.test/nested",
			"mode=inspect", "view=summary",
		} {
			if !strings.Contains(contents, safeURLPart) {
				t.Errorf("issue artifact %s must retain intelligible safe URL structure %q:\n%s", path, safeURLPart, contents)
			}
		}
	}
}

func TestDifftoolIssueArtifactRedactsSemicolonSeparatedAndValuelessURLSecrets(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Body: `{"result":"left","metadata":{"semicolon_url":"https://response-left.example.test/semicolon?mode=view;access_token=response-left-semicolon-secret","valueless_url":"https://response-left.example.test/valueless?token-response-left-valueless-secret"}}`,
		},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/leak": {
			Body: `{"result":"right","metadata":{"semicolon_url":"https://response-right.example.test/semicolon?mode=view;access_token=response-right-semicolon-secret","valueless_url":"https://response-right.example.test/valueless?token-response-right-valueless-secret"}}`,
		},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "query-delimiter-redaction.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: query delimiter artifact redaction
steps:
  - name: redact semicolon and valueless query secrets
    method: POST
    path: /leak
    body:
      metadata:
        semicolon_url: "https://request.example.test/semicolon?mode=view;access_token=request-semicolon-secret"
        valueless_url: "https://request.example.test/valueless?token-request-valueless-secret"
    operation_id: QueryDelimiterRedaction
`)
	issues := filepath.Join(directory, "issues")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", filepath.Join(directory, "results"), "--issues", issues)
	if err == nil {
		t.Fatalf("difftool passed despite the deliberately different responses\n%s", output)
	}

	artifacts := issueArtifactContents(t, issues)
	if len(artifacts) != 1 {
		t.Fatalf("one response mismatch must produce one issue artifact, got %#v", artifacts)
	}
	for path, contents := range artifacts {
		for _, secret := range []string{
			"request-semicolon-secret", "response-left-semicolon-secret", "response-right-semicolon-secret",
			"token-request-valueless-secret", "token-response-left-valueless-secret", "token-response-right-valueless-secret",
		} {
			if strings.Contains(contents, secret) {
				t.Errorf("issue artifact %s exposes semicolon-delimited or valueless URL secret %q:\n%s", path, secret, contents)
			}
		}
		for _, safeURLPart := range []string{
			"request.example.test/semicolon", "response-left.example.test/semicolon", "response-right.example.test/semicolon", "mode=view",
			"request.example.test/valueless", "response-left.example.test/valueless", "response-right.example.test/valueless",
		} {
			if !strings.Contains(contents, safeURLPart) {
				t.Errorf("issue artifact %s must retain safe URL structure %q:\n%s", path, safeURLPart, contents)
			}
		}
	}
}

func TestDifftoolIssueArtifactsDiscardStaleHigherOrdinalsOnRerun(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/first":    {Body: `{"side":"left-first"}`},
		"/obsolete": {Body: `{"side":"left-obsolete"}`},
		"/current":  {Body: `{"side":"left-current"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/first":    {Body: `{"side":"right-first"}`},
		"/obsolete": {Body: `{"side":"right-obsolete"}`},
		"/current":  {Body: `{"side":"right-current"}`},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "rerun.yaml")
	issues := filepath.Join(directory, "issues")
	results := filepath.Join(directory, "results")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: stale artifact cleanup
steps:
  - name: first fault
    method: GET
    path: /first
    operation_id: FirstFault
  - name: obsolete fault
    method: GET
    path: /obsolete
    operation_id: ObsoleteFault
`)
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", issues, "--run-id", "artifact-refresh")
	if err == nil {
		t.Fatalf("first run passed despite two deliberately different responses\n%s", output)
	}
	if artifacts := issueArtifactContents(t, issues); len(artifacts) != 2 {
		t.Fatalf("first run must produce two issue artifacts, got %#v", artifacts)
	}

	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: stale artifact cleanup
steps:
  - name: current fault
    method: GET
    path: /current
    operation_id: CurrentFault
`)
	output, err = runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", issues, "--run-id", "artifact-refresh")
	if err == nil {
		t.Fatalf("rerun passed despite the deliberately different current response\n%s", output)
	}

	artifacts := issueArtifactContents(t, issues)
	if len(artifacts) != 1 {
		t.Fatalf("rerun must leave exactly its current issue artifact, without stale higher ordinals: %#v", artifacts)
	}
	for path, contents := range artifacts {
		if !strings.Contains(contents, "current fault") {
			t.Errorf("current issue artifact %s does not identify the rerun failure:\n%s", path, contents)
		}
		if strings.Contains(contents, "obsolete fault") || strings.Contains(path, "obsolete") || strings.Contains(path, "002-") {
			t.Errorf("stale higher-ordinal issue artifact survived rerun at %s:\n%s", path, contents)
		}
	}
}
