// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestLoadScenariosLoadsStepBodyFromSiblingJSONFile(t *testing.T) {
	directory := t.TempDir()
	corpus := filepath.Join(directory, "profile-replay.yaml")
	profile := filepath.Join(directory, "profile.json")
	writeFile(t, profile, `{"DeviceName":"P0-05 Device","Capabilities":{"SupportsRemoteControl":true},"MaxStreamingBitrate":12000000}`)
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: profile replay
steps:
  - name: replay a pinned device profile
    method: POST
    path: /Sessions/Capabilities/Full
    body_file: profile.json
    operation_id: ReplayDeviceProfile
`)

	scenarios, err := loadScenarios(corpus)
	if err != nil {
		t.Fatalf("load scenario with body_file: %v", err)
	}
	if len(scenarios) != 1 || len(scenarios[0].Steps) != 1 {
		t.Fatalf("loaded scenarios = %#v, want one scenario with one step", scenarios)
	}
	body, ok := scenarios[0].Steps[0].Body.(map[string]any)
	if !ok {
		t.Fatalf("loaded body = %#v (%T), want decoded JSON object", scenarios[0].Steps[0].Body, scenarios[0].Steps[0].Body)
	}
	if body["DeviceName"] != "P0-05 Device" || body["MaxStreamingBitrate"] != float64(12000000) {
		t.Fatalf("loaded body = %#v, want JSON profile values", body)
	}
	capabilities, ok := body["Capabilities"].(map[string]any)
	if !ok || capabilities["SupportsRemoteControl"] != true {
		t.Fatalf("loaded profile capabilities = %#v, want decoded nested JSON", body["Capabilities"])
	}
}

func TestLoadScenariosValidatesOptionalExpectedStatus(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		wantStatus *int
		wantError  string
	}{
		{
			name: "expect status omitted",
			contents: `format: goldenrec-scenario/v1
name: optional expectation
steps:
  - name: ordinary differential replay
    method: GET
    path: /public
    operation_id: PublicInfo
`,
		},
		{
			name: "partial content expectation",
			contents: `format: goldenrec-scenario/v1
name: optional expectation
steps:
  - name: range request
    method: GET
    path: /range
    operation_id: RangeRequest
    expect:
      status: 206
`,
			wantStatus: pointerTo(206),
		},
		{
			name: "status below HTTP range",
			contents: `format: goldenrec-scenario/v1
name: invalid expectation
steps:
  - name: invalid range request
    method: GET
    path: /range
    operation_id: InvalidRangeRequest
    expect:
      status: 99
`,
			wantError: "expect.status must be between 100 and 599",
		},
		{
			name: "status above HTTP range",
			contents: `format: goldenrec-scenario/v1
name: invalid expectation
steps:
  - name: invalid range request
    method: GET
    path: /range
    operation_id: InvalidRangeRequest
    expect:
      status: 600
`,
			wantError: "expect.status must be between 100 and 599",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corpus := filepath.Join(t.TempDir(), "scenario.yaml")
			writeFile(t, corpus, test.contents)

			scenarios, err := loadScenarios(corpus)
			if test.wantError != "" {
				if err == nil {
					t.Fatalf("load scenario with %s: expected validation error", test.name)
				}
				if !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected-status validation error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("load scenario with %s: %v", test.name, err)
			}
			if len(scenarios) != 1 || len(scenarios[0].Steps) != 1 {
				t.Fatalf("loaded scenarios = %#v, want one scenario with one step", scenarios)
			}
			got := scenarios[0].Steps[0].Expect
			if test.wantStatus == nil {
				if got != nil {
					t.Fatalf("omitted expect.status loaded expectation = %#v, want none", got)
				}
				return
			}
			if got == nil || got.Status == nil || *got.Status != *test.wantStatus {
				t.Fatalf("loaded expect.status = %#v, want %d", got, *test.wantStatus)
			}
		})
	}
}

func pointerTo(value int) *int {
	return &value
}

func TestLoadScenariosRejectsStepWithBodyAndBodyFile(t *testing.T) {
	directory := t.TempDir()
	corpus := filepath.Join(directory, "ambiguous-profile.yaml")
	writeFile(t, filepath.Join(directory, "profile.json"), `{"DeviceName":"P0-05 Device"}`)
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: ambiguous profile replay
steps:
  - name: replay a pinned device profile
    method: POST
    path: /Sessions/Capabilities/Full
    body:
      DeviceName: inline profile
    body_file: profile.json
    operation_id: ReplayDeviceProfile
`)

	_, err := loadScenarios(corpus)
	if err == nil {
		t.Fatal("load scenario with both body and body_file: expected validation error")
	}
	if !strings.Contains(err.Error(), "body") || !strings.Contains(err.Error(), "body_file") {
		t.Fatalf("body/body_file validation error = %v, want both field names", err)
	}
}

func TestRunnerRejectsScenarioWithoutOperationIDBeforeWritingResults(t *testing.T) {
	host := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/public": {Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"Name":"stock"}`},
	})
	directory := t.TempDir()
	corpus := filepath.Join(directory, "untraceable.yaml")
	results := filepath.Join(directory, "results")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: untraceable
steps:
  - name: public info
    method: GET
    path: /public
`)

	output, err := runRunnerSchemaDifftool(t, "--corpus", corpus, "--left", host.server.URL, "--right", host.server.URL, "--results", results, "--issues", filepath.Join(directory, "issues"))
	if err == nil {
		t.Errorf("runner accepted a scenario whose emitted result would have no operation ID:\n%s", output)
	} else if !isInputRejected(err) {
		t.Errorf("scenario validation must reject an untraceable ledger row before comparison with exit code 2, got %v:\n%s", err, output)
	}
	assertNoResultFiles(t, results)
}

func TestResultsSchemaRequiresNonemptyOperationID(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("schema", "results.schema.json"))
	if err != nil {
		t.Fatalf("read results schema: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse results schema: %v", err)
	}
	steps := requiredSchemaObject(t, document, "properties", "steps")
	items := requiredSchemaObject(t, steps, "items")
	operationID := requiredSchemaObject(t, items, "properties", "operation-id")
	minimum, ok := operationID["minLength"].(float64)
	if !ok || minimum < 1 {
		t.Fatalf("results schema must reject empty operation-id values with a positive minLength: %#v", operationID)
	}
}

func TestResultsSchemaAcceptsNonnegativeBodyLengthCounted(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("schema", "results.schema.json"))
	if err != nil {
		t.Fatalf("read results schema: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse results schema: %v", err)
	}
	steps := requiredSchemaObject(t, document, "properties", "steps")
	items := requiredSchemaObject(t, steps, "items")
	properties := requiredSchemaObject(t, items, "properties")

	// The field must be declared as an integer with a nonnegative minimum.
	field, ok := properties["body_length_counted"].(map[string]any)
	if !ok {
		t.Fatalf("results schema must declare body_length_counted in step properties: %#v", properties)
	}
	if fieldType, want := field["type"], "integer"; fieldType != want {
		t.Fatalf("results schema body_length_counted type = %#v, want %q", fieldType, want)
	}
	minimum, ok := field["minimum"].(float64)
	if !ok || minimum < 0 {
		t.Fatalf("results schema body_length_counted must declare minimum >= 0: %#v", field)
	}

	// The field must be optional: it must not appear in the required array.
	required, ok := items["required"].([]any)
	if !ok {
		t.Fatalf("results schema step items must declare a required array: %#v", items)
	}
	for _, key := range required {
		if key == "body_length_counted" {
			t.Fatalf("results schema must not require body_length_counted: %#v", required)
		}
	}

	// The upper-bound BitrateTest result must validate. The pinned stock
	// returns 134217728 bytes without buffering the payload.
	result := Result{
		Format: "difftool-results/v1",
		RunID:  "schema-acceptance",
		Steps: []StepResult{
			{Scenario: "BitrateTest", Step: "authenticated valid upper bound", OperationID: "GetBitrateTestBytes", Status: "pass", BodyLengthCounted: 134217728},
		},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	stepArray, ok := decoded["steps"].([]any)
	if !ok || len(stepArray) != 1 {
		t.Fatalf("result must contain one step: %#v", decoded["steps"])
	}
	firstStep, ok := stepArray[0].(map[string]any)
	if !ok {
		t.Fatalf("first step must be an object: %#v", stepArray[0])
	}
	counted, ok := firstStep["body_length_counted"].(float64)
	if !ok || counted != 134217728 {
		t.Fatalf("upper-bound result must report body_length_counted=134217728: %#v", firstStep)
	}
}

func TestCheckNormalizersRejectsStateChangingScenariosBeforeReplay(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			stock := newStatefulStockHost(t)
			directory := t.TempDir()
			corpus := filepath.Join(directory, strings.ToLower(method)+".yaml")
			writeFile(t, corpus, "format: goldenrec-scenario/v1\nname: stateful-self-check\nsteps:\n  - name: mutate stock\n    method: "+method+"\n    path: /mutate\n    operation_id: MutateStock\n")

			output, err := runRunnerSchemaDifftool(t, "--corpus", corpus, "--left", stock.server.URL, "--check-normalizers", "--results", filepath.Join(directory, "results"), "--issues", filepath.Join(directory, "issues"))
			if err == nil {
				t.Errorf("--check-normalizers accepted %s against one stock origin:\n%s", method, output)
			}
			if !isInputRejected(err) {
				t.Errorf("--check-normalizers must reject %s as invalid input before comparison with exit code 2, got %v:\n%s", method, err, output)
			}
			if calls := stock.mutationCalls(); calls != 0 {
				t.Fatalf("--check-normalizers must reject %s before replaying a stock mutation, got %d mutation requests", method, calls)
			}
		})
	}
}

type statefulStockHost struct {
	mu        sync.Mutex
	mutations int
	server    *httptest.Server
}

func newStatefulStockHost(t *testing.T) *statefulStockHost {
	t.Helper()
	host := &statefulStockHost{}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Items":[]}`))
			return
		}
		if r.URL.Path == "/mutate" {
			host.mu.Lock()
			host.mutations++
			generation := host.mutations
			host.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"generation":` + strconv.Itoa(generation) + `}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *statefulStockHost) mutationCalls() int {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.mutations
}

func runRunnerSchemaDifftool(t *testing.T, args ...string) (string, error) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "difftool")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build difftool test binary: %v\n%s", err, output)
	}
	command := exec.Command(binary, args...)
	command.Dir = "."
	output, err := command.CombinedOutput()
	return string(output), err
}

func isInputRejected(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == 2
}

func assertNoResultFiles(t *testing.T, results string) {
	t.Helper()
	entries, err := os.ReadDir(results)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read results directory: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("runner wrote an untraceable result row to %s", filepath.Join(results, entry.Name()))
		}
	}
}

func requiredSchemaObject(t *testing.T, object map[string]any, keys ...string) map[string]any {
	t.Helper()
	current := object
	for _, key := range keys {
		value, ok := current[key]
		if !ok {
			t.Fatalf("schema omits %q from %#v", key, current)
		}
		next, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("schema %q must be an object, got %#v", key, value)
		}
		current = next
	}
	return current
}
