// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type capturedCarrierRequest struct {
	header http.Header
	query  url.Values
}

func TestScenarioReplayAuthCarriersApplyOnlyTheirSelectedCredential(t *testing.T) {
	const token = "bootstrap token +/?&="

	for _, tc := range []struct {
		name           string
		carrierYAML    string
		selectedHeader string
		selectedQuery  string
	}{
		{name: "legacy authorization", selectedHeader: "Authorization"},
		{name: "X-Emby authorization", carrierYAML: "x_emby_authorization", selectedHeader: "X-Emby-Authorization"},
		{name: "X-Emby token", carrierYAML: "x_emby_token", selectedHeader: "X-Emby-Token"},
		{name: "X-MediaBrowser token", carrierYAML: "x_mediabrowser_token", selectedHeader: "X-MediaBrowser-Token"},
		{name: "ApiKey query", carrierYAML: "query_api_key", selectedQuery: "ApiKey"},
		{name: "api_key query", carrierYAML: "query_api_key_lower", selectedQuery: "api_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var replay capturedCarrierRequest
			bootstrapCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/AuthenticateByName" {
					bootstrapCalls++
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
					return
				}
				replay.header = r.Header.Clone()
				replay.query = r.URL.Query()
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			directory := t.TempDir()
			carrier := ""
			if tc.carrierYAML != "" {
				carrier = "    auth_carrier: " + tc.carrierYAML + "\n"
			}
			corpus := filepath.Join(directory, "carrier.yaml")
			writeFile(t, corpus, "format: goldenrec-scenario/v1\nname: selected credential carrier\nsteps:\n  - name: replay with selected credential carrier\n    method: GET\n    path: /replay\n    params:\n      keep: query survives\n      ApiKey: scenario-api-key\n      api_key: scenario-api-key-lower\n    headers:\n      X-Keep: header survives\n      Authorization: scenario-authorization\n      X-Emby-Authorization: scenario-emby-authorization\n      X-Emby-Token: scenario-emby-token\n      X-MediaBrowser-Token: scenario-mediabrowser-token\n    auth_role: fixture\n"+carrier+"    operation_id: ReplayCarrier\n")
			scenarios, err := loadScenarios(corpus)
			if err != nil {
				t.Fatalf("load scenario: %v", err)
			}

			host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
			if err != nil {
				t.Fatalf("bootstrap host: %v", err)
			}
			if _, err := host.Request(context.Background(), scenarios[0].Steps[0], ""); err != nil {
				t.Fatalf("replay scenario: %v", err)
			}

			if bootstrapCalls != 1 {
				t.Fatalf("one role bootstrap must provide the replay credential, got %d bootstrap calls", bootstrapCalls)
			}
			if got := replay.header.Get("X-Keep"); got != "header survives" {
				t.Fatalf("unrelated scenario header = %q, want preserved value", got)
			}
			if got := replay.query.Get("keep"); got != "query survives" {
				t.Fatalf("unrelated scenario query = %q, want preserved value", got)
			}

			for name, supplied := range map[string]string{
				"Authorization":        "scenario-authorization",
				"X-Emby-Authorization": "scenario-emby-authorization",
				"X-Emby-Token":         "scenario-emby-token",
				"X-MediaBrowser-Token": "scenario-mediabrowser-token",
			} {
				got := replay.header.Get(name)
				if name == tc.selectedHeader {
					if name == "Authorization" || name == "X-Emby-Authorization" {
						if !strings.Contains(got, `Token="`+token+`"`) {
							t.Fatalf("selected %s = %q, want MediaBrowser token credential", name, got)
						}
					} else if got != token {
						t.Fatalf("selected %s = %q, want bootstrapped token", name, got)
					}
					continue
				}
				if got != supplied {
					t.Fatalf("unselected %s = %q, want preserved scenario value %q", name, got, supplied)
				}
			}

			for name, supplied := range map[string]string{
				"ApiKey":  "scenario-api-key",
				"api_key": "scenario-api-key-lower",
			} {
				got := replay.query.Get(name)
				if name == tc.selectedQuery {
					if got != token {
						t.Fatalf("selected query %s = %q, want bootstrapped token", name, got)
					}
					continue
				}
				if got != supplied {
					t.Fatalf("unselected query %s = %q, want preserved scenario value %q", name, got, supplied)
				}
			}
		})
	}
}

func TestHTTPHostAuthCarrierQueryEscapesBootstrappedToken(t *testing.T) {
	const token = "spaces + slash/? and ampersand&equals="
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
			return
		}
		rawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
	if err != nil {
		t.Fatalf("bootstrap host: %v", err)
	}
	_, err = host.Request(context.Background(), Step{Name: "escaped query credential", Method: http.MethodGet, Path: "/replay", Params: map[string]any{"keep": "safe value", "ApiKey": "replaced"}, AuthRole: "fixture", AuthCarrier: AuthCarrierQueryAPIKey}, "")
	if err != nil {
		t.Fatalf("replay request: %v", err)
	}

	want := url.Values{"ApiKey": {token}, "keep": {"safe value"}}.Encode()
	if rawQuery != want {
		t.Fatalf("replay query = %q, want URL-escaped credential query %q", rawQuery, want)
	}
}

func TestHTTPHostAuthCarrierDoesNotInjectCredentialsWithoutAuthRole(t *testing.T) {
	bootstrapCalls := 0
	var replay capturedCarrierRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			bootstrapCalls++
		}
		replay.header = r.Header.Clone()
		replay.query = r.URL.Query()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create unauthenticated host: %v", err)
	}
	_, err = host.Request(context.Background(), Step{Name: "anonymous request", Method: http.MethodGet, Path: "/public", Params: map[string]any{"keep": "value"}, Headers: map[string]string{"X-Keep": "header"}, AuthCarrier: AuthCarrierQueryAPIKey}, "")
	if err != nil {
		t.Fatalf("anonymous replay: %v", err)
	}

	if bootstrapCalls != 0 {
		t.Fatalf("anonymous replay must not bootstrap a role credential, got %d bootstrap calls", bootstrapCalls)
	}
	if replay.header.Get("Authorization") != "" || replay.header.Get("X-Emby-Authorization") != "" || replay.header.Get("X-Emby-Token") != "" || replay.header.Get("X-MediaBrowser-Token") != "" {
		t.Fatalf("anonymous replay unexpectedly injected a credential header: %#v", replay.header)
	}
	if replay.query.Get("ApiKey") != "" || replay.query.Get("api_key") != "" {
		t.Fatalf("anonymous replay unexpectedly injected a credential query: %#v", replay.query)
	}
	if replay.header.Get("X-Keep") != "header" || replay.query.Get("keep") != "value" {
		t.Fatalf("anonymous replay must preserve ordinary request metadata: headers=%#v query=%#v", replay.header, replay.query)
	}
}

func TestHTTPHostReusesOneBootstrappedRoleTokenAcrossReplaySteps(t *testing.T) {
	const token = "reused-bootstrap-token"
	bootstrapCalls := 0
	var replayTokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			bootstrapCalls++
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
			return
		}
		replayTokens = append(replayTokens, r.Header.Get("X-Emby-Token"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
	if err != nil {
		t.Fatalf("bootstrap host: %v", err)
	}
	for _, path := range []string{"/first", "/second"} {
		if _, err := host.Request(context.Background(), Step{Name: path, Method: http.MethodGet, Path: path, AuthRole: "fixture", AuthCarrier: AuthCarrierXEmbyToken}, ""); err != nil {
			t.Fatalf("replay %s: %v", path, err)
		}
	}

	if bootstrapCalls != 1 {
		t.Fatalf("replaying one role across steps must bootstrap once, got %d calls", bootstrapCalls)
	}
	if len(replayTokens) != 2 || replayTokens[0] != token || replayTokens[1] != token {
		t.Fatalf("each replay must reuse the bootstrapped role token: got %#v, want two uses of %q", replayTokens, token)
	}
}

func TestScenarioValidationRejectsExplicitEmptyOrUnknownAuthCarrierBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		carrier string
	}{
		{name: "empty carrier", carrier: `""`},
		{name: "unknown carrier", carrier: "not-a-carrier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Error(w, "scenario validation should have prevented this request", http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)

			directory := t.TempDir()
			rules := filepath.Join(directory, "rules")
			if err := os.Mkdir(rules, 0o755); err != nil {
				t.Fatalf("create empty rules directory: %v", err)
			}
			writeFile(t, filepath.Join(rules, "stable.yaml"), "version: difftool-normalize/v1\nrules:\n  - name: stable\n    action: sort_json_keys\n    reason: deterministic test rule\n")
			corpus := filepath.Join(directory, "invalid-carrier.yaml")
			writeFile(t, corpus, "format: goldenrec-scenario/v1\nname: reject invalid carrier\nsteps:\n  - name: invalid credential carrier\n    method: GET\n    path: /never\n    auth_role: fixture\n    auth_carrier: "+tc.carrier+"\n    operation_id: InvalidCarrier\n")
			_, err := Run(context.Background(), Config{Corpus: corpus, LeftURL: server.URL, RightURL: server.URL, RulesDir: rules, ResultsDir: filepath.Join(directory, "results"), IssuesDir: filepath.Join(directory, "issues"), LeftAuth: AuthConfig{Role: "fixture", Username: "user", Password: "password"}, RightAuth: AuthConfig{Role: "fixture", Username: "user", Password: "password"}})
			if err == nil {
				t.Fatal("invalid auth_carrier must reject the scenario")
			}
			if !strings.Contains(err.Error(), "unknown auth carrier") {
				t.Fatalf("invalid carrier error = %v, want an auth carrier validation error", err)
			}
			if calls != 0 {
				t.Fatalf("invalid auth_carrier must fail validation before network, got %d requests", calls)
			}
		})
	}
}

func TestRunnerReusesEquivalentSameOriginBootstrapAuthentication(t *testing.T) {
	var state struct {
		sync.Mutex
		bootstrapCalls              int
		protectedRequests           int
		successfulProtectedRequests int
		validToken                  string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.Lock()
		defer state.Unlock()

		if r.URL.Path == "/Users/AuthenticateByName" {
			state.bootstrapCalls++
			state.validToken = fmt.Sprintf("bootstrap-%d", state.bootstrapCalls)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, state.validToken)
			return
		}

		authenticated := strings.Contains(r.Header.Get("Authorization"), `Token="`+state.validToken+`"`) || r.Header.Get("X-Emby-Token") == state.validToken
		switch r.URL.Path {
		case "/Items":
			if !authenticated {
				http.Error(w, "expired bootstrap token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
		case "/protected":
			state.protectedRequests++
			if !authenticated {
				http.Error(w, "expired bootstrap token", http.StatusUnauthorized)
				return
			}
			state.successfulProtectedRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Name":"protected"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	rules := filepath.Join(directory, "rules")
	if err := os.Mkdir(rules, 0o755); err != nil {
		t.Fatalf("create rules directory: %v", err)
	}
	writeFile(t, filepath.Join(rules, "stable.yaml"), "version: difftool-normalize/v1\nrules:\n  - name: stable\n    action: sort_json_keys\n    reason: deterministic test rule\n")
	corpus := filepath.Join(directory, "same-origin-auth.yaml")
	writeFile(t, corpus, "format: goldenrec-scenario/v1\nname: same-origin bootstrap authentication\nsteps:\n  - name: protected replay\n    method: GET\n    path: /protected\n    auth_role: fixture\n    auth_carrier: x_emby_token\n    operation_id: ProtectedReplay\n")
	auth := AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}

	if _, err := Run(context.Background(), Config{Corpus: corpus, LeftURL: server.URL, RightURL: server.URL, RulesDir: rules, ResultsDir: filepath.Join(directory, "results"), IssuesDir: filepath.Join(directory, "issues"), LeftAuth: auth, RightAuth: auth}); err != nil {
		t.Errorf("same-origin authenticated replay: %v", err)
	}

	state.Lock()
	defer state.Unlock()
	if state.bootstrapCalls != 1 {
		t.Errorf("same-origin equivalent auth must bootstrap once, got %d /Users/AuthenticateByName requests", state.bootstrapCalls)
	}
	if state.protectedRequests != 2 || state.successfulProtectedRequests != 2 {
		t.Errorf("protected replay requests = %d total, %d successful; want two successful requests", state.protectedRequests, state.successfulProtectedRequests)
	}
}

type authCarrierRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip authCarrierRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestAuthCarrierTokenIsRedactedFromReplayErrorAndIssueArtifact(t *testing.T) {
	const token = "do-not-expose-bootstrap-token"
	client := &http.Client{Transport: authCarrierRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/Users/AuthenticateByName" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"AccessToken":"` + token + `"}`)), Request: request}, nil
		}
		return nil, fmt.Errorf("replay transport failed for %s", request.URL)
	})}
	host, err := newHTTPHost(context.Background(), "http://difftool.invalid", AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, client)
	if err != nil {
		t.Fatalf("bootstrap host: %v", err)
	}
	step := Step{Name: "redact replay error", Method: http.MethodGet, Path: "/replay", AuthRole: "fixture", AuthCarrier: AuthCarrierQueryAPIKey}
	_, err = host.Request(context.Background(), step, "")
	if err == nil {
		t.Fatal("failing replay transport must surface an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("replay error exposes bootstrapped token: %v", err)
	}

	directory := t.TempDir()
	issue, err := writeIssue(directory, 1, Scenario{Name: "redact carrier token"}, step, Response{}, Response{}, normalizedResponse{}, normalizedResponse{}, requestErrorDiff(err, nil))
	if err != nil {
		t.Fatalf("write issue artifact: %v", err)
	}
	contents, err := os.ReadFile(issue)
	if err != nil {
		t.Fatalf("read issue artifact: %v", err)
	}
	if strings.Contains(string(contents), token) {
		t.Fatalf("issue artifact exposes bootstrapped token:\n%s", contents)
	}
}

func TestHTTPHostBootstrapFailureRedactsTokenFromServerError(t *testing.T) {
	const token = "server-error-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/AuthenticateByName" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"AccessToken":"`+token+`","message":"credential rejected"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	_, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
	if err == nil {
		t.Fatal("rejected bootstrap must surface an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("bootstrap error exposes token returned by server: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("bootstrap error must retain its HTTP failure context: %v", err)
	}
}

type redirectCredentialCase struct {
	name string
	step func(secret string) Step
}

func redirectCredentialCases() []redirectCredentialCase {
	return []redirectCredentialCase{
		{
			name: "authorization",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierAuthorization}
			},
		},
		{
			name: "X-Emby authorization",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierXEmbyAuthorization}
			},
		},
		{
			name: "X-Emby token",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierXEmbyToken}
			},
		},
		{
			name: "X-MediaBrowser token",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierXMediaBrowserToken}
			},
		},
		{
			name: "ApiKey query",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierQueryAPIKey}
			},
		},
		{
			name: "api_key query",
			step: func(string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierQueryAPIKeyLower}
			},
		},
		{
			name: "Cookie header",
			step: func(secret string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", Headers: map[string]string{"Cookie": "session=" + secret}}
			},
		},
		{
			name: "query token",
			step: func(secret string) Step {
				return Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", Params: map[string]any{"token": secret}}
			},
		},
	}
}

func requestContainsSecret(request *http.Request, secret string) bool {
	if strings.Contains(request.URL.RawQuery, secret) {
		return true
	}
	for _, values := range request.Header {
		for _, value := range values {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
	return false
}

func TestHTTPHostCrossOriginRedirectDoesNotForwardCredentials(t *testing.T) {
	for _, tc := range redirectCredentialCases() {
		t.Run(tc.name, func(t *testing.T) {
			const token = "redirect-bootstrap-secret"
			secret := "redirect-" + strings.ReplaceAll(tc.name, " ", "-") + "-secret"
			var destinationCalls int
			var destinationLeakedSecret bool
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				destinationCalls++
				destinationLeakedSecret = requestContainsSecret(request, token) || requestContainsSecret(request, secret)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(destination.Close)

			var sourceSawSecret bool
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/Users/AuthenticateByName":
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
				case "/redirect":
					sourceSawSecret = requestContainsSecret(request, token) || requestContainsSecret(request, secret)
					http.Redirect(w, request, destination.URL+"/destination", http.StatusFound)
				default:
					http.NotFound(w, request)
				}
			}))
			t.Cleanup(source.Close)

			host, err := newHTTPHost(context.Background(), source.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
			if err != nil {
				t.Fatalf("bootstrap host: %v", err)
			}
			_, err = host.Request(context.Background(), tc.step(secret), "")

			if !sourceSawSecret {
				t.Fatal("redirect source did not receive the credential, so the redirect assertion is not exercising a credentialed request")
			}
			if err != nil && destinationCalls != 0 {
				t.Fatalf("cross-origin redirect returned %v after destination received %d requests", err, destinationCalls)
			}
			if destinationCalls > 1 {
				t.Fatalf("cross-origin redirect destination calls = %d, want at most one", destinationCalls)
			}
			if destinationLeakedSecret {
				t.Fatal("cross-origin redirect forwarded a credential to the destination")
			}
		})
	}
}

func TestHTTPHostSameOriginRelativeRedirectRetainsCredentials(t *testing.T) {
	for _, tc := range redirectCredentialCases() {
		t.Run(tc.name, func(t *testing.T) {
			const token = "relative-redirect-bootstrap-secret"
			secret := "relative-" + strings.ReplaceAll(tc.name, " ", "-") + "-secret"
			var destinationCalls int
			var destinationSawSecret bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/Users/AuthenticateByName":
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
				case "/redirect":
					http.Redirect(w, request, "/destination", http.StatusFound)
				case "/destination":
					destinationCalls++
					destinationSawSecret = requestContainsSecret(request, token) || requestContainsSecret(request, secret)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, request)
				}
			}))
			t.Cleanup(server.Close)

			host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
			if err != nil {
				t.Fatalf("bootstrap host: %v", err)
			}
			response, err := host.Request(context.Background(), tc.step(secret), "")
			if err != nil {
				t.Fatalf("same-origin relative redirect: %v", err)
			}
			if response.Status != http.StatusNoContent {
				t.Fatalf("same-origin redirect status = %d, want %d", response.Status, http.StatusNoContent)
			}
			if destinationCalls != 1 || !destinationSawSecret {
				t.Fatalf("same-origin destination calls = %d, saw credential = %t; want one credentialed destination request", destinationCalls, destinationSawSecret)
			}
		})
	}
}

func TestHTTPHostRedirectOriginIncludesSchemeAndEffectivePort(t *testing.T) {
	for _, tc := range []struct {
		name            string
		baseURL         string
		location        string
		wantDestination bool
	}{
		{name: "scheme", baseURL: "https://origin.example.test:8443", location: "http://origin.example.test:8443/destination"},
		{name: "effective port", baseURL: "https://origin.example.test:8443", location: "https://origin.example.test:9443/destination"},
		{name: "implicit HTTPS port", baseURL: "https://origin.example.test", location: "https://origin.example.test:443/destination", wantDestination: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "origin-boundary-secret"
			var destinationCalls int
			client := &http.Client{Transport: authCarrierRoundTripper(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/Users/AuthenticateByName":
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"AccessToken":"` + token + `"}`)), Request: request}, nil
				case "/redirect":
					if request.Header.Get("X-Emby-Token") != token {
						t.Fatalf("redirect source token = %q, want %q", request.Header.Get("X-Emby-Token"), token)
					}
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{tc.location}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
				case "/destination":
					destinationCalls++
					return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				default:
					t.Fatalf("unexpected request URL %s", request.URL)
					return nil, nil
				}
			})}

			host, err := newHTTPHost(context.Background(), tc.baseURL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, client)
			if err != nil {
				t.Fatalf("bootstrap host: %v", err)
			}
			response, err := host.Request(context.Background(), Step{Name: "redirect", Method: http.MethodGet, Path: "/redirect", AuthRole: "fixture", AuthCarrier: AuthCarrierXEmbyToken}, "")
			if tc.wantDestination {
				if err != nil {
					t.Fatalf("same-origin redirect through implicit HTTPS port: %v", err)
				}
				if destinationCalls != 1 {
					t.Fatalf("same-origin redirect through implicit HTTPS port reached destination %d times, want once", destinationCalls)
				}
				return
			}
			if err == nil && response.Status != http.StatusFound {
				t.Fatalf("cross-origin redirect was neither rejected nor retained as HTTP %d: status = %d", http.StatusFound, response.Status)
			}
			if destinationCalls != 0 {
				t.Fatalf("cross-origin redirect reached destination %d times", destinationCalls)
			}
		})
	}
}
