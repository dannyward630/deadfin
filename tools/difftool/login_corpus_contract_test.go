// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	displayPreferencesPath   = "/DisplayPreferences/usersettings"
	displayPreferencesClient = "P0-05-AuthReplay"
)

func TestLoginCorpusDisplayPreferencesOverwriteConvergesAfterFailedRightWrite(t *testing.T) {
	steps, write, read := loginCorpusDisplayPreferencesSteps(t)
	if err := requireDisplayPreferencesOverwriteBeforeRead(steps); err != nil {
		t.Fatal(err)
	}

	leftPrior := map[string]any{
		"SortBy":          "Name",
		"RememberSorting": false,
		"CustomPrefs":     map[string]any{"prior": "left"},
	}
	rightPrior := map[string]any{
		"SortBy":          "DatePlayed",
		"RememberSorting": true,
		"CustomPrefs":     map[string]any{"prior": "right"},
	}
	left := newDisplayPreferencesReplayHost(t, leftPrior)
	right := newDisplayPreferencesReplayHost(t, rightPrior)
	right.failNextWrites(1)

	directory := t.TempDir()
	corpus := writeDisplayPreferencesScenario(t, directory, steps)
	config := Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
		LeftAuth:   AuthConfig{Role: "admin", Username: "left-admin", Password: "left-password"},
		RightAuth:  AuthConfig{Role: "admin", Username: "right-admin", Password: "right-password"},
	}

	if _, err := Run(context.Background(), config); !errors.Is(err, errDifferences) {
		t.Fatalf("first replay with a right-side failed write = %v, want comparison differences", err)
	}

	want, ok := write.Body.(map[string]any)
	if !ok {
		t.Fatalf("display preference overwrite body = %#v (%T), want object", write.Body, write.Body)
	}
	if got := left.preferences(); !reflect.DeepEqual(got, want) {
		t.Fatalf("left successful overwrite = %#v, want corpus value %#v", got, want)
	}
	if got := right.preferences(); !reflect.DeepEqual(got, rightPrior) {
		t.Fatalf("right failed overwrite = %#v, want its asymmetric prior state %#v", got, rightPrior)
	}

	result, err := Run(context.Background(), config)
	if err != nil {
		t.Fatalf("rerun after right write recovery must overwrite then read equal state: %v", err)
	}
	for _, step := range result.Steps {
		if step.Status != "pass" {
			t.Fatalf("rerun step %#v, want all steps to pass", step)
		}
	}
	if got := right.preferences(); !reflect.DeepEqual(got, want) {
		t.Fatalf("right recovered overwrite = %#v, want corpus value %#v", got, want)
	}
	for _, host := range []*displayPreferencesReplayHost{left, right} {
		if got, wantMethods := host.displayRequestMethods(), []string{http.MethodPost, http.MethodGet, http.MethodPost, http.MethodGet}; !reflect.DeepEqual(got, wantMethods) {
			t.Fatalf("%s display preference requests = %#v, want %#v", host.server.URL, got, wantMethods)
		}
	}
	if read.Path != displayPreferencesPath {
		t.Fatalf("display preference read path = %q, want %q", read.Path, displayPreferencesPath)
	}
}

func TestLoginCorpusHasNoDisplayPreferencesReadBeforeMutation(t *testing.T) {
	steps, write, read := loginCorpusDisplayPreferencesSteps(t)
	if err := requireDisplayPreferencesOverwriteBeforeRead(steps); err != nil {
		t.Fatal(err)
	}

	// This is the historical read-then-write order. The contract check must stay
	// red-capable so a corpus regression cannot silently reintroduce history dependence.
	if err := requireDisplayPreferencesOverwriteBeforeRead([]Step{read, write}); err == nil {
		t.Fatal("legacy DisplayPreferences read-before-write order passed the deterministic mutation contract")
	}
}

func TestLoginCorpusCredentialSubstitutionsRemainOpaqueWhenTheyContainSourceItemIDs(t *testing.T) {
	const (
		sourceID        = "00112233445566778899aabbccddeeff"
		rightTargetID   = "right-login-item"
		username        = "username-00112233445566778899aabbccddeeff-opaque"
		password        = "password-00112233445566778899aabbccddeeff-opaque"
		disabledUser    = "disabled-username-00112233445566778899aabbccddeeff-opaque"
		disabledPass    = "disabled-password-00112233445566778899aabbccddeeff-opaque"
		invalidPassword = "difftool-deliberately-invalid-password"
	)

	for _, profile := range []struct {
		name  string
		value string
	}{
		{name: "normal"},
		{name: "Camel", value: "CamelCase"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			left := newLoginCredentialReplayHost(t, sourceID)
			right := newLoginCredentialReplayHost(t, rightTargetID)
			directory := t.TempDir()
			corpus := writeLoginCredentialScenario(t, directory)
			auth := AuthConfig{
				Role:             "admin",
				Username:         username,
				Password:         password,
				DisabledUsername: disabledUser,
				DisabledPassword: disabledPass,
			}

			_, runErr := Run(context.Background(), Config{
				Corpus:     corpus,
				LeftURL:    left.server.URL,
				RightURL:   right.server.URL,
				ResultsDir: filepath.Join(directory, "results"),
				IssuesDir:  filepath.Join(directory, "issues"),
				Profile:    profile.value,
				LeftAuth:   auth,
				RightAuth:  auth,
			})
			if runErr != nil && !errors.Is(runErr, errDifferences) {
				t.Fatalf("replay selected committed login credential corpus: %v", runErr)
			}

			want := []map[string]string{
				{"Username": username, "Pw": password}, // bootstrap
				{"Username": username, "Pw": password}, // password login Pascal response
				{"Username": username, "Pw": password}, // password login CamelCase response
				{"Username": disabledUser, "Pw": disabledPass},
				{"Username": username, "Pw": invalidPassword},
			}
			for _, host := range []*loginCredentialReplayHost{left, right} {
				got := host.loginBodies(t)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s login bodies = %#v, want opaque credential substitutions unchanged: %#v", host.server.URL, got, want)
				}
			}
			wantAccepts := []string{
				"application/json", // bootstrap
				"application/json",
				`application/json; profile="CamelCase"`,
				"application/json",
				"application/json",
			}
			if profile.value == "CamelCase" {
				for index := range wantAccepts[1:] {
					wantAccepts[index+1] = `application/json; profile="CamelCase"`
				}
			}
			for _, host := range []*loginCredentialReplayHost{left, right} {
				if got := host.loginAccepts(); !reflect.DeepEqual(got, wantAccepts) {
					t.Fatalf("%s login Accept headers = %#v, want %#v for %s two-host replay", host.server.URL, got, wantAccepts, profile.name)
				}
			}
			if runErr != nil {
				t.Fatalf("replay selected committed login credential corpus must converge after preserving opaque values: %v", runErr)
			}
		})
	}
}

func TestLoginCorpusInvalidPasswordUsesEnabledUserAfterStateSensitiveSteps(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "login.yaml"))
	if err != nil {
		t.Fatalf("load committed login corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed login corpus scenarios = %d, want one", len(scenarios))
	}
	steps := scenarios[0].Steps
	invalidIndex := -1
	invalidCount := 0
	for index, step := range steps {
		if step.Name == "password login rejects invalid password" {
			invalidCount++
			invalidIndex = index
			if step.OperationID != "AuthenticateUserByName" {
				t.Fatalf("invalid-password operation ID = %q, want AuthenticateUserByName", step.OperationID)
			}
			body, ok := step.Body.(map[string]any)
			if !ok {
				t.Fatalf("invalid-password body = %#v (%T), want object", step.Body, step.Body)
			}
			if got := body["Username"]; got != "${username}" {
				t.Fatalf("invalid-password username = %#v, want enabled-user placeholder ${username}", got)
			}
			if got := body["Pw"]; got != "difftool-deliberately-invalid-password" {
				t.Fatalf("invalid-password password = %#v, want opaque invalid password", got)
			}
		}
	}
	if invalidIndex == -1 {
		t.Fatal("invalid-password login step is missing")
	}
	if invalidCount != 1 {
		t.Fatalf("invalid-password login steps = %d, want exactly one", invalidCount)
	}
	if invalidIndex != len(steps)-1 {
		t.Fatalf("invalid-password login step index = %d, want final index %d so no later state-sensitive response is compared", invalidIndex, len(steps)-1)
	}
	if got := steps[0].Capture["username"]; got != "User.Name" {
		t.Fatalf("enabled username capture = %#v, want User.Name before invalid-password substitution", got)
	}
	stateSensitiveCount := 0
	for index, step := range steps {
		if step.Name == "password login rejects invalid password" {
			continue
		}
		switch {
		case step.Path == "/Users/AuthenticateByName", step.Path == "/Users/Me", step.Path == "/Sessions", step.Path == "/Sessions/Capabilities/Full", step.OperationID == "SHADOW:AuthenticateUser":
			stateSensitiveCount++
			if index > invalidIndex {
				t.Fatalf("state-sensitive step %q appears at index %d after invalid-password login at index %d", step.Name, index, invalidIndex)
			}
		}
	}
	if stateSensitiveCount == 0 {
		t.Fatal("committed login corpus has no state-sensitive login, current-user, or session steps before invalid-password login")
	}
}

func TestLoginCorpusExplicitlySelectsAuthenticationTokenNormalization(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load baseline normalizers: %v", err)
	}
	defaults, err := selectRules(rules, nil)
	if err != nil {
		t.Fatalf("select omitted normalizers: %v", err)
	}
	for _, rule := range defaults {
		if rule.Action == "auth_tokens" {
			t.Fatal("omitted normalizers selected auth-tokens")
		}
	}

	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "login.yaml"))
	if err != nil {
		t.Fatalf("load committed login corpus: %v", err)
	}
	selected := make(map[string]bool)
	for _, step := range scenarios[0].Steps {
		for _, normalizer := range step.selectedRules() {
			if normalizer == "auth-tokens" {
				selected[step.Name] = true
			}
		}
	}
	want := map[string]bool{
		"password login Pascal response":    true,
		"password login CamelCase response": true,
		"legacy user-id authentication":     true,
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("login auth-tokens selections = %#v, want exact explicit response selections %#v", selected, want)
	}
}

func writeLoginCredentialScenario(t *testing.T, directory string) string {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "login.yaml"))
	if err != nil {
		t.Fatalf("load committed login corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed login corpus scenarios = %d, want one", len(scenarios))
	}
	steps := make([]Step, 0, 4)
	for _, step := range scenarios[0].Steps {
		if step.Path == "/Users/AuthenticateByName" {
			steps = append(steps, step)
		}
	}
	if len(steps) != 4 {
		t.Fatalf("committed login corpus password-login steps = %d, want four", len(steps))
	}
	serializedSteps := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		serialized := map[string]any{
			"name":         step.Name,
			"method":       step.Method,
			"path":         step.Path,
			"operation_id": step.OperationID,
		}
		if len(step.Headers) != 0 {
			serialized["headers"] = step.Headers
		}
		if step.Body != nil {
			serialized["body"] = step.Body
		}
		if len(step.Capture) != 0 {
			serialized["capture"] = step.Capture
		}
		if len(step.Normalizers) != 0 {
			serialized["normalizers"] = step.Normalizers
		}
		serializedSteps = append(serializedSteps, serialized)
	}
	contents, err := yaml.Marshal(map[string]any{
		"format": scenarioFormat,
		"name":   "committed login credential substitutions",
		"steps":  serializedSteps,
	})
	if err != nil {
		t.Fatalf("marshal selected login credential steps: %v", err)
	}
	corpus := filepath.Join(directory, "login-credentials.yaml")
	writeFile(t, corpus, string(contents))
	return corpus
}

type loginCredentialReplayHost struct {
	mu     sync.Mutex
	logins []recordedRequest
	server *httptest.Server
}

func newLoginCredentialReplayHost(t *testing.T, targetID string) *loginCredentialReplayHost {
	t.Helper()
	host := &loginCredentialReplayHost{}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Items":[{"Id":"`+targetID+`","Type":"Movie","Path":"/media/login-opaque-id.mkv"}],"TotalRecordCount":1}`)
		case "/Users/AuthenticateByName":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read login body: %v", err)
			}
			var decoded map[string]string
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("decode login body: %v", err)
			}
			host.mu.Lock()
			host.logins = append(host.logins, recordedRequest{Header: r.Header.Clone(), Body: string(body)})
			host.mu.Unlock()
			if decoded["Pw"] == "difftool-deliberately-invalid-password" || decoded["Username"] == "disabled-username-00112233445566778899aabbccddeeff-opaque" {
				http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "fixture-token",
				"User":        map[string]string{"Id": "fixture-user", "Name": decoded["Username"]},
				"SessionInfo": map[string]string{"Id": "fixture-session"},
			}); err != nil {
				t.Errorf("encode login response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *loginCredentialReplayHost) loginBodies(t *testing.T) []map[string]string {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()
	bodies := make([]map[string]string, 0, len(host.logins))
	for _, request := range host.logins {
		var body map[string]string
		if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
			t.Fatalf("decode recorded login body: %v", err)
		}
		bodies = append(bodies, body)
	}
	return bodies
}

func (host *loginCredentialReplayHost) loginAccepts() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	accepts := make([]string, len(host.logins))
	for index, request := range host.logins {
		accepts[index] = request.Header.Get("Accept")
	}
	return accepts
}

func loginCorpusDisplayPreferencesSteps(t *testing.T) ([]Step, Step, Step) {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "login.yaml"))
	if err != nil {
		t.Fatalf("load committed login corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed login corpus scenarios = %d, want one", len(scenarios))
	}

	var steps []Step
	var write, read Step
	for _, step := range scenarios[0].Steps {
		if step.Path != displayPreferencesPath || displayPreferencesMutationKey(step) != displayPreferencesPath+"?client="+displayPreferencesClient {
			continue
		}
		steps = append(steps, step)
		switch step.OperationID {
		case "UpdateDisplayPreferences":
			write = step
		case "GetDisplayPreferences":
			read = step
		}
	}
	if write.OperationID == "" || read.OperationID == "" {
		t.Fatalf("committed login corpus display preference operations = %#v, want update and read for %s", steps, displayPreferencesClient)
	}
	return steps, write, read
}

func requireDisplayPreferencesOverwriteBeforeRead(steps []Step) error {
	writeIndex := -1
	mutationKey := ""
	for index, step := range steps {
		if step.Method != http.MethodPost || step.OperationID != "UpdateDisplayPreferences" {
			continue
		}
		writeIndex = index
		mutationKey = displayPreferencesMutationKey(step)
		break
	}
	if writeIndex == -1 {
		return fmt.Errorf("DisplayPreferences overwrite operation is missing")
	}
	for index, step := range steps[:writeIndex] {
		if step.Method == http.MethodGet && displayPreferencesMutationKey(step) == mutationKey {
			return fmt.Errorf("DisplayPreferences mutation key %q is read at step %d before its deterministic overwrite at step %d", mutationKey, index+1, writeIndex+1)
		}
	}
	return nil
}

func displayPreferencesMutationKey(step Step) string {
	client, _ := step.Params["client"].(string)
	return step.Path + "?client=" + client
}

func writeDisplayPreferencesScenario(t *testing.T, directory string, steps []Step) string {
	t.Helper()
	serializedSteps := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		serialized := map[string]any{
			"name":         step.Name,
			"method":       step.Method,
			"path":         step.Path,
			"operation_id": step.OperationID,
		}
		if len(step.Params) != 0 {
			serialized["params"] = step.Params
		}
		if len(step.Headers) != 0 {
			serialized["headers"] = step.Headers
		}
		if step.Body != nil {
			serialized["body"] = step.Body
		}
		if step.AuthRole != "" {
			serialized["auth_role"] = step.AuthRole
		}
		if len(step.Normalizers) != 0 {
			serialized["normalizers"] = step.Normalizers
		}
		serializedSteps = append(serializedSteps, serialized)
	}
	contents, err := yaml.Marshal(map[string]any{
		"format": scenarioFormat,
		"name":   "committed login display preferences contract",
		"steps":  serializedSteps,
	})
	if err != nil {
		t.Fatalf("marshal selected login corpus steps: %v", err)
	}
	corpus := filepath.Join(directory, "display-preferences.yaml")
	writeFile(t, corpus, string(contents))
	return corpus
}

type displayPreferencesReplayHost struct {
	mu                sync.Mutex
	current           map[string]any
	remainingFailures int
	displayRequests   []recordedRequest
	server            *httptest.Server
}

func newDisplayPreferencesReplayHost(t *testing.T, initial map[string]any) *displayPreferencesReplayHost {
	t.Helper()
	host := &displayPreferencesReplayHost{current: cloneDisplayPreferences(t, initial)}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"AccessToken":"fixture-token","User":{"Id":"fixture-user"}}`)
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
		case displayPreferencesPath:
			host.serveDisplayPreferences(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *displayPreferencesReplayHost) serveDisplayPreferences(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if got := r.URL.Query().Get("client"); got != displayPreferencesClient {
		t.Errorf("DisplayPreferences client = %q, want %q", got, displayPreferencesClient)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read DisplayPreferences request body: %v", err)
	}
	host.mu.Lock()
	host.displayRequests = append(host.displayRequests, recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body)})
	switch r.Method {
	case http.MethodPost:
		if host.remainingFailures > 0 {
			host.remainingFailures--
			host.mu.Unlock()
			http.Error(w, `{"error":"right write failed"}`, http.StatusInternalServerError)
			return
		}
		var preference map[string]any
		if err := json.Unmarshal(body, &preference); err != nil {
			host.mu.Unlock()
			t.Errorf("decode DisplayPreferences overwrite: %v", err)
			http.Error(w, "invalid overwrite", http.StatusBadRequest)
			return
		}
		host.current = preference
		host.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		preference := cloneDisplayPreferences(t, host.current)
		host.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(preference); err != nil {
			t.Errorf("encode DisplayPreferences response: %v", err)
		}
	default:
		host.mu.Unlock()
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (host *displayPreferencesReplayHost) failNextWrites(count int) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.remainingFailures = count
}

func (host *displayPreferencesReplayHost) preferences() map[string]any {
	host.mu.Lock()
	defer host.mu.Unlock()
	return cloneDisplayPreferences(nil, host.current)
}

func (host *displayPreferencesReplayHost) displayRequestMethods() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	methods := make([]string, len(host.displayRequests))
	for index, request := range host.displayRequests {
		methods[index] = request.Method
	}
	return methods
}

func cloneDisplayPreferences(t *testing.T, value map[string]any) map[string]any {
	contents, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Fatalf("marshal DisplayPreferences fixture: %v", err)
		}
		panic(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(contents, &clone); err != nil {
		if t != nil {
			t.Fatalf("unmarshal DisplayPreferences fixture: %v", err)
		}
		panic(err)
	}
	return clone
}
