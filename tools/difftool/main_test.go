// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

type fixtureHost struct {
	t        *testing.T
	token    string
	mu       sync.Mutex
	requests map[string][]recordedRequest
	server   *httptest.Server
}

func newFixtureHost(t *testing.T, token string, responses map[string]fixtureResponse) *fixtureHost {
	t.Helper()
	host := &fixtureHost{t: t, token: token, requests: make(map[string][]recordedRequest)}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		request := recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body)}
		host.mu.Lock()
		host.requests[r.URL.Path] = append(host.requests[r.URL.Path], request)
		host.mu.Unlock()

		if r.URL.Path == "/Users/AuthenticateByName" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"AccessToken":%q}`, token)
			return
		}
		response, ok := responses[r.URL.Path]
		if !ok {
			response = fixtureResponse{Status: http.StatusNotFound, Body: `{"error":"missing fixture"}`}
		}
		for key, value := range response.Headers {
			w.Header().Set(key, value)
		}
		if response.Status == 0 {
			response.Status = http.StatusOK
		}
		w.WriteHeader(response.Status)
		_, _ = io.WriteString(w, response.Body)
	}))
	t.Cleanup(host.server.Close)
	return host
}

type fixtureResponse struct {
	Status  int
	Headers map[string]string
	Body    string
}

func (host *fixtureHost) onlyRequest(t *testing.T, path string) recordedRequest {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()
	requests := host.requests[path]
	if len(requests) != 1 {
		t.Fatalf("%s received %d requests for %s, want one: %#v", host.server.URL, len(requests), path, requests)
	}
	return requests[0]
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runDifftool(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("go", append([]string{"run", "."}, args...)...)
	command.Dir = "."
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}

func runDifftoolObserved(t *testing.T, args ...string) (string, []string, error) {
	t.Helper()
	command := exec.Command("go", append([]string{"run", "."}, args...)...)
	command.Dir = "."
	childArgs := append([]string(nil), command.Args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), childArgs, err
}

func writeCLICredentials(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "credentials.json")
	writeFile(t, path, `{"left":{"username":"left-user","password":"left-password","disabled_username":"left-disabled-user","disabled_password":"left-disabled-password"},"right":{"username":"right-user","password":"right-password","disabled_username":"right-disabled-user","disabled_password":"right-disabled-password"}}`)
	return path
}

func requireResultsSchema(t *testing.T, document map[string]any) []any {
	t.Helper()
	if document["format"] != "difftool-results/v1" {
		t.Fatalf("results format = %#v, want difftool-results/v1", document["format"])
	}
	if runID, ok := document["run_id"].(string); !ok || runID == "" {
		t.Fatalf("results run_id must be a nonempty string: %#v", document["run_id"])
	}
	steps, ok := document["steps"].([]any)
	if !ok {
		t.Fatalf("results steps must be an array: %#v", document["steps"])
	}
	for _, value := range steps {
		record, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("results step is not an object: %#v", value)
		}
		for _, key := range []string{"scenario", "step", "operation-id", "status"} {
			if _, ok := record[key]; !ok {
				t.Fatalf("results step omits required %q: %#v", key, record)
			}
		}
		if scenario, ok := record["scenario"].(string); !ok || scenario == "" {
			t.Fatalf("results scenario must be nonempty: %#v", record)
		}
		if step, ok := record["step"].(string); !ok || step == "" {
			t.Fatalf("results step must be nonempty: %#v", record)
		}
		if _, ok := record["operation-id"].(string); !ok {
			t.Fatalf("results operation-id must be a string: %#v", record)
		}
		if status, ok := record["status"].(string); !ok || (status != "pass" && status != "fail") {
			t.Fatalf("results status must be pass or fail: %#v", record)
		}
	}
	return steps
}

func resultRecords(t *testing.T, resultsDir string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		t.Fatalf("read results directory: %v", err)
	}
	var records []map[string]any
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(resultsDir, entry.Name()))
		if err != nil {
			t.Fatalf("read results file: %v", err)
		}
		var document map[string]any
		if err := json.Unmarshal(payload, &document); err != nil {
			t.Fatalf("results JSON is invalid: %v\n%s", err, payload)
		}
		values := requireResultsSchema(t, document)
		for _, value := range values {
			record := value.(map[string]any)
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		t.Fatalf("no per-step records found in %s", resultsDir)
	}
	return records
}

func hasStatus(record map[string]any, status string) bool {
	for _, key := range []string{"status", "result", "outcome"} {
		if value, ok := record[key].(string); ok && value == status {
			return true
		}
	}
	return false
}

func TestScenarioReplayPreservesPairedRequestsAndIsolatesAuth(t *testing.T) {
	left := newFixtureHost(t, "left-secret", map[string]fixtureResponse{
		"/echo": {Headers: map[string]string{"Content-Type": "application/json", "X-Contract": "same"}, Body: `{"Payload":{"ID":"fixed","Name":"ok"}}`},
	})
	right := newFixtureHost(t, "right-secret", map[string]fixtureResponse{
		"/echo": {Headers: map[string]string{"Content-Type": "application/json", "X-Contract": "same"}, Body: `{"Payload":{"Name":"ok","ID":"fixed"}}`},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "scenario.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: fidelity
steps:
  - name: paired POST
    method: POST
    path: /echo
    params:
      q: hello world
      include: A,B
    headers:
      X-Scenario-Header: exact-value
      Content-Type: application/json
    body: '{"Device":"Swiftfin","Enabled":true}'
    auth_role: fixture
    operation_id: EchoFixture
`)
	results := filepath.Join(directory, "results")
	issues := filepath.Join(directory, "issues")
	credentials := writeCLICredentials(t, directory)
	output, childArgs, err := runDifftoolObserved(t,
		"--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL,
		"--results", results, "--issues", issues,
		"--credentials-file", credentials, "--left-auth-role", "fixture", "--right-auth-role", "fixture",
		"--profile", "CamelCase",
	)
	for _, secret := range []string{"left-password", "right-password", "left-disabled-password", "right-disabled-password"} {
		if strings.Contains(strings.Join(childArgs, "\x00"), secret) {
			t.Fatalf("child argv exposes credential %q: %#v", secret, childArgs)
		}
		if strings.Contains(output, secret) {
			t.Fatalf("CLI output exposes credential %q: %s", secret, output)
		}
	}
	if err != nil {
		t.Fatalf("difftool unexpectedly failed: %v\n%s", err, output)
	}

	leftRequest := left.onlyRequest(t, "/echo")
	rightRequest := right.onlyRequest(t, "/echo")
	for _, request := range []recordedRequest{leftRequest, rightRequest} {
		if request.Method != http.MethodPost || request.Path != "/echo" {
			t.Fatalf("request fidelity lost method/path: %#v", request)
		}
		if request.Query != "include=A%2CB&q=hello+world" && request.Query != "q=hello+world&include=A%2CB" {
			t.Fatalf("request fidelity lost query: %q", request.Query)
		}
		if request.Header.Get("X-Scenario-Header") != "exact-value" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request fidelity lost scenario headers: %#v", request.Header)
		}
		if request.Header.Get("Accept") != `application/json; profile="CamelCase"` {
			t.Fatalf("CamelCase profile was not negotiated exactly: %q", request.Header.Get("Accept"))
		}
		if request.Body != `{"Device":"Swiftfin","Enabled":true}` {
			t.Fatalf("request fidelity lost body: %q", request.Body)
		}
	}
	if !strings.Contains(leftRequest.Header.Get("Authorization"), `Token="left-secret"`) || strings.Contains(leftRequest.Header.Get("Authorization"), "right-secret") {
		t.Fatalf("left request leaked or omitted isolated auth: %q", leftRequest.Header.Get("Authorization"))
	}
	if !strings.Contains(rightRequest.Header.Get("Authorization"), `Token="right-secret"`) || strings.Contains(rightRequest.Header.Get("Authorization"), "left-secret") {
		t.Fatalf("right request leaked or omitted isolated auth: %q", rightRequest.Header.Get("Authorization"))
	}

	records := resultRecords(t, results)
	if len(records) != 1 || !hasStatus(records[0], "pass") || records[0]["operation-id"] != "EchoFixture" {
		t.Fatalf("results must contain the scenario operation and one pass: %#v", records)
	}
}

func TestScenarioReplayRejectsMatchingStatusesThatViolateExpectation(t *testing.T) {
	left := newFixtureHost(t, "unused", nil)
	right := newFixtureHost(t, "unused", nil)

	directory := t.TempDir()
	corpus := filepath.Join(directory, "unexpected-status.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: unexpected-status
steps:
  - name: range request must be partial content
    method: GET
    path: /range
    operation_id: RangeRequest
    expect:
      status: 206
`)
	results := filepath.Join(directory, "results")
	issues := filepath.Join(directory, "issues")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", issues)
	if err == nil {
		t.Fatalf("difftool passed matching 404 responses despite expect.status=206\n%s", output)
	}

	records := resultRecords(t, results)
	if len(records) != 1 || !hasStatus(records[0], "fail") || records[0]["operation-id"] != "RangeRequest" {
		t.Fatalf("unexpected-status scenario must record one failed operation: %#v", records)
	}
	entries, readErr := os.ReadDir(filepath.Join(issues, "difftool"))
	if readErr != nil {
		t.Fatalf("read run-scoped issue artifacts: %v", readErr)
	}
	var issueFiles []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			issueFiles = append(issueFiles, entry)
		}
	}
	if len(issueFiles) != 1 {
		t.Fatalf("unexpected expected status must emit exactly one issue file, got %d", len(issueFiles))
	}
	issue, readErr := os.ReadFile(filepath.Join(issues, "difftool", issueFiles[0].Name()))
	if readErr != nil {
		t.Fatalf("read issue artifact: %v", readErr)
	}
	for _, required := range []string{"expected=206", "left=404", "right=404"} {
		if !strings.Contains(string(issue), required) {
			t.Errorf("expected-status issue artifact omits %q:\n%s", required, issue)
		}
	}
}

func TestDifftoolReportsOneFaultAndCompleteIssueArtifact(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/json":  {Headers: map[string]string{"Content-Type": "application/json", "ETag": "same"}, Body: `{"Name":"same","Nested":{"A":1,"B":2}}`},
		"/fault": {Headers: map[string]string{"Content-Type": "application/json", "ETag": "same"}, Body: `{"Name":"left"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/json":  {Headers: map[string]string{"ETag": "same", "Content-Type": "application/json"}, Body: `{"Nested":{"B":2,"A":1},"Name":"same"}`},
		"/fault": {Headers: map[string]string{"Content-Type": "application/json", "ETag": "same"}, Body: `{"Name":"right"}`},
	})

	directory := t.TempDir()
	corpus := filepath.Join(directory, "one-fault.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: one-fault
steps:
  - name: JSON key order is structural
    method: GET
    path: /json
    operation_id: JSONOrder
  - name: seeded response fault
    method: GET
    path: /fault
    operation_id: SeededFault
`)
	results := filepath.Join(directory, "results")
	issues := filepath.Join(directory, "issues")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", issues)
	if err == nil {
		t.Fatalf("difftool passed despite seeded fault\n%s", output)
	}
	records := resultRecords(t, results)
	if len(records) != 2 || !hasStatus(records[0], "pass") || !hasStatus(records[1], "fail") {
		t.Fatalf("seeded one-fault corpus must produce one pass and one failure: %#v", records)
	}
	entries, readErr := os.ReadDir(filepath.Join(issues, "difftool"))
	if readErr != nil {
		t.Fatalf("read run-scoped issue artifacts: %v", readErr)
	}
	var issueFiles []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			issueFiles = append(issueFiles, entry)
		}
	}
	if len(issueFiles) != 1 {
		t.Fatalf("one seeded fault must emit exactly one issue file, got %d", len(issueFiles))
	}
	issue, readErr := os.ReadFile(filepath.Join(issues, "difftool", issueFiles[0].Name()))
	if readErr != nil {
		t.Fatalf("read issue artifact: %v", readErr)
	}
	for _, required := range []string{"one-fault", "seeded response fault", "GET", "/fault", "left", "right", "normalized", "diff", "SeededFault"} {
		if !strings.Contains(strings.ToLower(string(issue)), strings.ToLower(required)) {
			t.Errorf("issue artifact omits %q:\n%s", required, issue)
		}
	}
}

func TestDifftoolComparesHeadersAndPlaylistText(t *testing.T) {
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:6.0,\nsegment.ts\n"
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/playlist":      {Headers: map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "X-Playlist-Mode": "live"}, Body: playlist},
		"/playlist-text": {Headers: map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "X-Playlist-Mode": "same"}, Body: "#EXTM3U\n#EXTINF:6.0,\nleft.ts\n"},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/playlist":      {Headers: map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "X-Playlist-Mode": "different"}, Body: playlist},
		"/playlist-text": {Headers: map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "X-Playlist-Mode": "same"}, Body: "#EXTM3U\n#EXTINF:6.0,\nright.ts\n"},
	})
	directory := t.TempDir()
	corpus := filepath.Join(directory, "playlist.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: playlist
steps:
  - name: header fault
    method: GET
    path: /playlist
    operation_id: PlaylistHeaders
  - name: playlist text fault
    method: GET
    path: /playlist-text
    operation_id: PlaylistText
`)
	results := filepath.Join(directory, "results")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", filepath.Join(directory, "issues"))
	if err == nil {
		t.Fatalf("header or playlist mismatch was ignored\n%s", output)
	}
	records := resultRecords(t, results)
	if len(records) != 2 || !hasStatus(records[0], "fail") || !hasStatus(records[1], "fail") {
		t.Fatalf("header and playlist text faults must each fail: %#v", records)
	}
}

func TestDifftoolRejectsMalformedScenariosAndUnexplainedRules(t *testing.T) {
	directory := t.TempDir()
	left := newFixtureHost(t, "unused", nil)
	right := newFixtureHost(t, "unused", nil)
	invalidScenario := filepath.Join(directory, "invalid.yaml")
	writeFile(t, invalidScenario, "format: goldenrec-scenario/v1\nname: invalid\nsteps:\n  - name: missing path\n    method: GET\n")
	output, err := runDifftool(t, "--corpus", invalidScenario, "--left", left.server.URL, "--right", right.server.URL)
	if err == nil || !strings.Contains(strings.ToLower(output), "path") {
		t.Fatalf("missing scenario path must be rejected with a useful error: %v\n%s", err, output)
	}

	validScenario := filepath.Join(directory, "valid.yaml")
	writeFile(t, validScenario, "format: goldenrec-scenario/v1\nname: valid\nsteps:\n  - name: anything\n    method: GET\n    path: /anything\n")
	rules := filepath.Join(directory, "rules")
	if mkdirErr := os.Mkdir(rules, 0o700); mkdirErr != nil {
		t.Fatalf("create rule directory: %v", mkdirErr)
	}
	writeFile(t, filepath.Join(rules, "unexplained.yaml"), "version: difftool-normalize/v1\nrules:\n  - name: hide-server-id\n    action: server_id\n")
	output, err = runDifftool(t, "--corpus", validScenario, "--left", left.server.URL, "--right", right.server.URL, "--rules", rules)
	if err == nil || !strings.Contains(strings.ToLower(output), "reason") {
		t.Fatalf("normalizer without a reason must be rejected: %v\n%s", err, output)
	}
}

func TestDifftoolMapsOnlyStableItemIdentities(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"left-item","Type":"Movie","Path":"/fixture/film.mkv"}]}`},
		"/item":  {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Id":"left-item","ParentId":"left-item","Unrelated":"left-item-suffix"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/Items": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Items":[{"Id":"right-item","Type":"Movie","Path":"/fixture/film.mkv"}]}`},
		"/item":  {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Id":"right-item","ParentId":"right-item","Unrelated":"left-item-suffix"}`},
	})
	directory := t.TempDir()
	corpus := filepath.Join(directory, "item-map.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: item-map
steps:
  - name: mapped item identifiers
    method: GET
    path: /item
    operation_id: GetMappedItem
`)
	results := filepath.Join(directory, "results")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--results", results, "--issues", filepath.Join(directory, "issues"))
	if err != nil {
		t.Fatalf("stable (Type,Path) item identities must normalize server-local IDs: %v\n%s", err, output)
	}
	if records := resultRecords(t, results); len(records) != 1 || !hasStatus(records[0], "pass") {
		t.Fatalf("mapped item result must be one pass: %#v", records)
	}
	for _, host := range []*fixtureHost{left, right} {
		items := host.onlyRequest(t, "/Items")
		if items.Query != "Fields=Path&Limit=10000&Recursive=true&StartIndex=0" {
			t.Fatalf("item map request must retain Fields=Path and begin pagination at StartIndex=0: %q", items.Query)
		}
	}
}

func TestCheckNormalizersReplaysOneReferenceHost(t *testing.T) {
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/public": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ServerId":"reference","Name":"stock"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/public": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ServerId":"wrong","Name":"fault"}`},
	})
	directory := t.TempDir()
	corpus := filepath.Join(directory, "normalizer-check.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: normalizer-check
steps:
  - name: public info
    method: GET
    path: /public
    operation_id: GetPublicSystemInfo
`)
	results := filepath.Join(directory, "results")
	output, err := runDifftool(t, "--corpus", corpus, "--left", left.server.URL, "--right", right.server.URL, "--check-normalizers", "--results", results, "--issues", filepath.Join(directory, "issues"))
	if err != nil {
		t.Fatalf("check-normalizers must self-compare the left reference host: %v\n%s", err, output)
	}
	if records := resultRecords(t, results); len(records) != 1 || !hasStatus(records[0], "pass") {
		t.Fatalf("normalizer self-check must emit one passing record: %#v", records)
	}
	right.mu.Lock()
	defer right.mu.Unlock()
	if got := len(right.requests["/public"]); got != 0 {
		t.Fatalf("check-normalizers must not replay corpus requests to --right, got %d", got)
	}
}

func TestDifftoolCredentialFileRejectsUnsafeOrIncompleteInputsWithoutLeakingSecrets(t *testing.T) {
	const hiddenValue = "credential-file-secret-must-not-leak"
	directory := t.TempDir()
	valid := writeCLICredentials(t, directory)
	if err := os.Chmod(valid, 0o644); err != nil {
		t.Fatalf("make credential file mode insecure: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "mode", path: valid, want: "0600"},
		{name: "missing", path: filepath.Join(directory, "missing.json"), want: "credential"},
	}
	empty := filepath.Join(directory, "empty.json")
	writeFile(t, empty, "")
	tests = append(tests, struct {
		name string
		path string
		want string
	}{name: "empty", path: empty, want: "credential"})
	missingField := filepath.Join(directory, "missing-field.json")
	writeFile(t, missingField, `{"left":{"username":"left-user","password":"left-password","disabled_username":"left-disabled-user","disabled_password":"left-disabled-password"},"right":{"username":"right-user","password":"right-password","disabled_username":"right-disabled-user"}}`)
	tests = append(tests, struct {
		name string
		path string
		want string
	}{name: "missing required field", path: missingField, want: "disabled_password"})
	unknown := filepath.Join(directory, "unknown.json")
	writeFile(t, unknown, `{"left":{"username":"left-user","password":"left-password","disabled_username":"left-disabled-user","disabled_password":"left-disabled-password","unexpected":"`+hiddenValue+`"},"right":{"username":"right-user","password":"right-password","disabled_username":"right-disabled-user","disabled_password":"right-disabled-password"}}`)
	tests = append(tests, struct {
		name string
		path string
		want string
	}{name: "unknown JSON field", path: unknown, want: "unknown"})
	noSecret := filepath.Join(directory, "no-secret.json")
	writeFile(t, noSecret, `{"left":{"username":"left-user","password":"","disabled_username":"left-disabled-user","disabled_password":"left-disabled-password"},"right":{"username":"right-user","password":"","disabled_username":"right-disabled-user","disabled_password":"right-disabled-password"}}`)
	tests = append(tests, struct {
		name string
		path string
		want string
	}{name: "empty secret", path: noSecret, want: "password"})

	target := filepath.Join(directory, "symlink-target.json")
	writeFile(t, target, `{"left":{"username":"left-user","password":"`+hiddenValue+`","disabled_username":"left-disabled-user","disabled_password":"left-disabled-password"},"right":{"username":"right-user","password":"right-password","disabled_username":"right-disabled-user","disabled_password":"right-disabled-password"}}`)
	symlink := filepath.Join(directory, "credentials-link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("create credential symlink: %v", err)
	}
	tests = append(tests, struct {
		name string
		path string
		want string
	}{name: "symlink", path: symlink, want: "symlink"})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, childArgs, err := runDifftoolObserved(t, "--credentials-file", test.path)
			if err == nil {
				t.Fatalf("unsafe credential file %s was accepted:\n%s", test.path, output)
			}
			if !strings.Contains(strings.ToLower(output), test.want) {
				t.Fatalf("credential file %s rejection = %q, want mention of %q", test.path, output, test.want)
			}
			if strings.Contains(strings.Join(childArgs, "\x00"), hiddenValue) || strings.Contains(output, hiddenValue) {
				t.Fatalf("credential failure exposes secret in argv or output: argv=%#v output=%s", childArgs, output)
			}
		})
	}
}

func TestDifftoolRejectsLegacyPasswordFlags(t *testing.T) {
	for _, legacyFlag := range []string{
		"--left-auth-password",
		"--right-auth-password",
		"--left-disabled-password",
		"--right-disabled-password",
	} {
		t.Run(legacyFlag, func(t *testing.T) {
			output, childArgs, err := runDifftoolObserved(t, legacyFlag)
			if err == nil {
				t.Fatalf("legacy password flag %s was accepted", legacyFlag)
			}
			if !strings.Contains(output, "flag provided but not defined") {
				t.Fatalf("legacy password flag %s rejection = %q, want unknown flag error", legacyFlag, output)
			}
			if strings.Contains(strings.Join(childArgs, "\x00"), "password=") {
				t.Fatalf("legacy password flag test unexpectedly placed a secret in child argv: %#v", childArgs)
			}
		})
	}
}
