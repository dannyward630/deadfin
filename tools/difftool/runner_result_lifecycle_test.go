// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunFatalFailureDiscardsOnlyItsPriorResult(t *testing.T) {
	tests := []struct {
		name        string
		captureBody string
		corpus      string
		wantError   string
	}{
		{
			name:        "missing capture",
			captureBody: `{"present":"value"}`,
			corpus: `format: goldenrec-scenario/v1
name: missing response capture
steps:
  - name: capture must exist
    method: GET
    path: /capture
    capture:
      required_value: Missing
    operation_id: MissingCapture
`,
			wantError: "capture",
		},
		{
			name:        "malformed JSON capture",
			captureBody: `{"required_value":`,
			corpus: `format: goldenrec-scenario/v1
name: malformed response capture
steps:
  - name: capture requires JSON
    method: GET
    path: /capture
    capture:
      required_value: required_value
    operation_id: MalformedCapture
`,
			wantError: "response is not JSON",
		},
		{
			name:        "non JSON capture",
			captureBody: `not-json`,
			corpus: `format: goldenrec-scenario/v1
name: text response capture
steps:
  - name: capture requires JSON
    method: GET
    path: /capture
    capture:
      required_value: required_value
    operation_id: NonJSONCapture
`,
			wantError: "response is not JSON",
		},
		{
			name:        "unknown normalizer",
			captureBody: `{"result":"ok"}`,
			corpus: `format: goldenrec-scenario/v1
name: unknown normalizer
steps:
  - name: selected normalizer must exist
    method: GET
    path: /normalizer
    normalizers:
      - absent-rule
    operation_id: UnknownNormalizer
`,
			wantError: "unknown normalizer",
		},
		{
			name:        "unresolved request setup value",
			captureBody: `{"result":"ok"}`,
			corpus: `format: goldenrec-scenario/v1
name: request setup error
steps:
  - name: unresolved template aborts replay
    method: GET
    path: /request/${missing_value}
    operation_id: UnresolvedRequest
`,
			wantError: "missing_value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			results := filepath.Join(directory, "results")
			issues := filepath.Join(directory, "issues")
			corpus := filepath.Join(directory, "scenario.yaml")
			rules := writeCaptureRule(t, directory)
			const runID = "lifecycle-target"
			const unrelatedRunID = "unrelated-run"

			host := newFixtureHost(t, "unused", map[string]fixtureResponse{
				"/seed":       {Body: `{"result":"seed"}`},
				"/capture":    {Body: test.captureBody},
				"/normalizer": {Body: `{"result":"normalizer"}`},
				"/fresh":      {Body: `{"result":"fresh"}`},
			})
			config := Config{
				Corpus:     corpus,
				LeftURL:    host.server.URL,
				RightURL:   host.server.URL,
				RulesDir:   rules,
				ResultsDir: results,
				IssuesDir:  issues,
				RunID:      runID,
			}

			writeFile(t, corpus, `format: goldenrec-scenario/v1
name: prior all pass result
steps:
  - name: prior passing step
    method: GET
    path: /seed
    operation_id: PriorPass
`)
			if _, err := Run(context.Background(), config); err != nil {
				t.Fatalf("seed prior all-pass result: %v", err)
			}
			target := filepath.Join(results, runID+".json")
			assertCurrentResultSchema(t, target, runID, "prior all pass result", "pass")

			unrelated := filepath.Join(results, unrelatedRunID+".json")
			unrelatedContents := []byte("{\n  \"format\": \"difftool-results/v1\",\n  \"run_id\": \"unrelated-run\",\n  \"steps\": [{\"scenario\": \"unrelated\", \"step\": \"pass\", \"operation-id\": \"UnrelatedPass\", \"status\": \"pass\"}]\n}\n")
			if err := os.WriteFile(unrelated, unrelatedContents, 0o600); err != nil {
				t.Fatalf("seed unrelated result: %v", err)
			}

			writeFile(t, corpus, test.corpus)
			_, err := Run(context.Background(), config)
			if err == nil {
				t.Fatal("fatal replay unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("fatal replay error = %v, want %q", err, test.wantError)
			}
			assertResultAbsent(t, target)
			assertFileContents(t, unrelated, unrelatedContents)

			writeFile(t, corpus, `format: goldenrec-scenario/v1
name: current successful rerun
steps:
  - name: current passing step
    method: GET
    path: /fresh
    operation_id: CurrentPass
`)
			if _, err := Run(context.Background(), config); err != nil {
				t.Fatalf("successful rerun after fatal replay: %v", err)
			}
			assertCurrentResultSchema(t, target, runID, "current successful rerun", "pass")
			assertFileContents(t, unrelated, unrelatedContents)
			assertNoResultTemporaryFiles(t, results, runID)
		})
	}
}

func assertResultAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			t.Fatalf("stale result survived fatal replay at %s", path)
		}
		t.Fatalf("stat stale result %s: %v", path, err)
	}
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("contents of unrelated result %s changed:\n got: %s\nwant: %s", path, got, want)
	}
}

func TestWriteResultsPublishesSuccessfulRerunAtomically(t *testing.T) {
	directory := t.TempDir()
	const runID = "atomic-rerun"
	target := filepath.Join(directory, runID+".json")
	previous := Result{
		Format: "difftool-results/v1",
		RunID:  runID,
		Steps:  []StepResult{{Scenario: "prior all pass", Step: "prior", OperationID: "PriorPass", Status: "pass"}},
	}
	if err := writeResults(target, previous); err != nil {
		t.Fatalf("seed prior result: %v", err)
	}

	current := Result{Format: "difftool-results/v1", RunID: runID, Steps: make([]StepResult, 75_000)}
	for index := range current.Steps {
		current.Steps[index] = StepResult{
			Scenario:    "current successful rerun",
			Step:        fmt.Sprintf("current-%06d", index),
			OperationID: fmt.Sprintf("CurrentPass%06d", index),
			Status:      "pass",
		}
	}

	ready := make(chan struct{})
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	readFailure := make(chan error, 1)
	go func() {
		defer close(readerDone)
		firstRead := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			contents, err := os.ReadFile(target)
			if err != nil {
				readFailure <- fmt.Errorf("read concurrently published result: %w", err)
				return
			}
			if !json.Valid(contents) {
				readFailure <- errors.New("concurrent reader observed a partial result document")
				return
			}
			if firstRead {
				firstRead = false
				close(ready)
			}
			runtime.Gosched()
		}
	}()
	<-ready
	if err := writeResults(target, current); err != nil {
		close(stop)
		<-readerDone
		t.Fatalf("publish current result: %v", err)
	}
	close(stop)
	<-readerDone
	select {
	case err := <-readFailure:
		t.Fatal(err)
	default:
	}

	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read current result: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("current result is not complete JSON: %v", err)
	}
	steps := requireResultsSchema(t, document)
	if len(steps) != len(current.Steps) {
		t.Fatalf("published step count = %d, want %d", len(steps), len(current.Steps))
	}
	last := steps[len(steps)-1].(map[string]any)
	if last["step"] != current.Steps[len(current.Steps)-1].Step || last["operation-id"] != current.Steps[len(current.Steps)-1].OperationID {
		t.Fatalf("published result retained stale or incomplete last step: %#v", last)
	}
}

func assertCurrentResultSchema(t *testing.T, path, runID, scenario, status string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("result %s is not complete JSON: %v\n%s", path, err, contents)
	}
	steps := requireResultsSchema(t, document)
	if document["run_id"] != runID {
		t.Fatalf("result run_id = %#v, want %q", document["run_id"], runID)
	}
	if len(steps) != 1 {
		t.Fatalf("result steps = %#v, want one current step", steps)
	}
	step := steps[0].(map[string]any)
	if step["scenario"] != scenario || step["status"] != status {
		t.Fatalf("result step = %#v, want scenario %q with status %q", step, scenario, status)
	}
}

func assertNoResultTemporaryFiles(t *testing.T, results, runID string) {
	t.Helper()
	entries, err := os.ReadDir(results)
	if err != nil {
		t.Fatalf("read results directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != runID+".json" && strings.Contains(entry.Name(), runID) {
			t.Fatalf("successful atomic result publication left temporary file %s", filepath.Join(results, entry.Name()))
		}
	}
}

func TestRunScopesIssueArtifactsByRunIDAndCleansOnlySameRunStaleArtifacts(t *testing.T) {
	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	results := filepath.Join(directory, "results")
	issues := filepath.Join(directory, "issues")
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/stale":   {Body: `{"side":"left-stale"}`},
		"/shared":  {Body: `{"side":"left-shared"}`},
		"/current": {Body: `{"side":"left-current"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/stale":   {Body: `{"side":"right-stale"}`},
		"/shared":  {Body: `{"side":"right-shared"}`},
		"/current": {Body: `{"side":"right-current"}`},
	})
	firstCorpus := filepath.Join(directory, "first.yaml")
	writeFile(t, firstCorpus, `format: goldenrec-scenario/v1
name: first issue lifecycle
steps:
  - name: stale failure
    method: GET
    path: /stale
    operation_id: StaleFailure
  - name: shared failure
    method: GET
    path: /shared
    operation_id: SharedFailure
`)
	firstConfig := Config{
		Corpus:     firstCorpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: results,
		IssuesDir:  issues,
		RunID:      "first-run",
	}
	firstResult, err := Run(context.Background(), firstConfig)
	if !errors.Is(err, errDifferences) {
		t.Fatalf("first run = %v, want differences", err)
	}
	firstIssues := resultIssuePaths(t, firstResult)
	if len(firstIssues) != 2 {
		t.Fatalf("first run issue links = %#v, want two", firstIssues)
	}

	secondCorpus := filepath.Join(directory, "second.yaml")
	writeFile(t, secondCorpus, `format: goldenrec-scenario/v1
name: second issue lifecycle
steps:
  - name: shared failure
    method: GET
    path: /shared
    operation_id: SharedFailure
`)
	secondResult, err := Run(context.Background(), Config{
		Corpus:     secondCorpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: results,
		IssuesDir:  issues,
		RunID:      "second-run",
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("second run = %v, want differences", err)
	}
	secondIssues := resultIssuePaths(t, secondResult)
	if len(secondIssues) != 1 {
		t.Fatalf("second run issue links = %#v, want one", secondIssues)
	}
	for _, path := range firstIssues {
		assertIssueLinkValid(t, path)
	}
	for _, path := range secondIssues {
		assertIssueLinkValid(t, path)
	}
	if firstIssues[1] == secondIssues[0] {
		t.Fatalf("distinct run IDs reused issue artifact %q", firstIssues[1])
	}

	currentCorpus := filepath.Join(directory, "current.yaml")
	writeFile(t, currentCorpus, `format: goldenrec-scenario/v1
name: first rerun issue lifecycle
steps:
  - name: current failure
    method: GET
    path: /current
    operation_id: CurrentFailure
`)
	firstConfig.Corpus = currentCorpus
	currentResult, err := Run(context.Background(), firstConfig)
	if !errors.Is(err, errDifferences) {
		t.Fatalf("same RunID rerun = %v, want differences", err)
	}
	currentIssues := resultIssuePaths(t, currentResult)
	if len(currentIssues) != 1 {
		t.Fatalf("same RunID rerun issue links = %#v, want one", currentIssues)
	}
	assertIssueLinkValid(t, currentIssues[0])
	for _, stale := range firstIssues {
		if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				t.Fatalf("same RunID rerun retained stale issue artifact %s", stale)
			}
			t.Fatalf("stat stale issue artifact %s: %v", stale, err)
		}
	}
	for _, path := range secondIssues {
		assertIssueLinkValid(t, path)
	}
}

func TestRunRejectsRunIssueDirectorySymlinkBeforeCleanupOrWrite(t *testing.T) {
	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	results := filepath.Join(directory, "results")
	issues := filepath.Join(directory, "issues")
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/different": {Body: `{"side":"left"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/different": {Body: `{"side":"right"}`},
	})
	corpus := filepath.Join(directory, "scenario.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: owned issue directory
steps:
  - name: produces issue artifact
    method: GET
    path: /different
    operation_id: ProducesIssue
`)
	config := Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: results,
		IssuesDir:  issues,
	}

	config.RunID = "attacker-run"
	if _, err := Run(context.Background(), config); !errors.Is(err, errDifferences) {
		t.Fatalf("seed attacker run = %v, want differences", err)
	}
	attackerDir := filepath.Join(issues, config.RunID)
	attackerResult := filepath.Join(results, config.RunID+".json")
	attackerResultContents, err := os.ReadFile(attackerResult)
	if err != nil {
		t.Fatalf("read attacker result: %v", err)
	}

	config.RunID = "target-run"
	if _, err := Run(context.Background(), config); !errors.Is(err, errDifferences) {
		t.Fatalf("seed target run = %v, want differences", err)
	}
	targetDir := filepath.Join(issues, config.RunID)
	targetIssues, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatalf("read target issue directory: %v", err)
	}
	if len(targetIssues) == 0 {
		t.Fatal("target run did not create a tool-owned issue artifact")
	}
	targetIssueContents := make(map[string][]byte, len(targetIssues))
	for _, entry := range targetIssues {
		if entry.IsDir() {
			t.Fatalf("target issue entry %q is a directory", entry.Name())
		}
		path := filepath.Join(targetDir, entry.Name())
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read target issue artifact %s: %v", path, readErr)
		}
		targetIssueContents[entry.Name()] = contents
	}
	sentinel := filepath.Join(targetDir, "target-sentinel.txt")
	const sentinelContents = "must not be deleted or overwritten"
	if err := os.WriteFile(sentinel, []byte(sentinelContents), 0o600); err != nil {
		t.Fatalf("write target sentinel: %v", err)
	}

	if err := os.RemoveAll(attackerDir); err != nil {
		t.Fatalf("remove attacker issue directory: %v", err)
	}
	if err := os.Symlink(targetDir, attackerDir); err != nil {
		t.Fatalf("replace attacker issue directory with symlink: %v", err)
	}
	config.RunID = "attacker-run"
	if _, err := Run(context.Background(), config); err == nil {
		t.Fatal("run with symlinked issue directory unexpectedly succeeded")
	}

	assertFileContents(t, sentinel, []byte(sentinelContents))
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatalf("read target issue directory after rejected run: %v", err)
	}
	if len(entries) != len(targetIssues)+1 {
		t.Fatalf("target issue directory entries after rejected run = %d, want %d", len(entries), len(targetIssues)+1)
	}
	for name, contents := range targetIssueContents {
		assertFileContents(t, filepath.Join(targetDir, name), contents)
	}
	assertFileContents(t, attackerResult, attackerResultContents)
}

func TestRunRejectsNonDirectoryRunIssuePathBeforeCleanupOrWrite(t *testing.T) {
	directory := t.TempDir()
	rules := writeCaptureRule(t, directory)
	issues := filepath.Join(directory, "issues")
	results := filepath.Join(directory, "results")
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/different": {Body: `{"side":"left"}`},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/different": {Body: `{"side":"right"}`},
	})
	corpus := filepath.Join(directory, "scenario.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: non directory issue path
steps:
  - name: must not write issue artifact
    method: GET
    path: /different
    operation_id: NonDirectoryIssuePath
`)
	const runID = "file-run"
	issuePath := filepath.Join(issues, runID)
	if err := os.MkdirAll(issues, 0o755); err != nil {
		t.Fatalf("create issues parent: %v", err)
	}
	const sentinelContents = "not a directory"
	if err := os.WriteFile(issuePath, []byte(sentinelContents), 0o600); err != nil {
		t.Fatalf("write non-directory issue path: %v", err)
	}

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		RulesDir:   rules,
		ResultsDir: results,
		IssuesDir:  issues,
		RunID:      runID,
	})
	if err == nil {
		t.Fatal("run with non-directory issue path unexpectedly succeeded")
	}
	assertFileContents(t, issuePath, []byte(sentinelContents))
	if _, statErr := os.Stat(filepath.Join(results, runID+".json")); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			t.Fatalf("run wrote result despite invalid issue path")
		}
		t.Fatalf("stat result after invalid issue path: %v", statErr)
	}
}

func resultIssuePaths(t *testing.T, result Result) []string {
	t.Helper()
	paths := make([]string, 0, len(result.Steps))
	for _, step := range result.Steps {
		if step.Status != "fail" {
			continue
		}
		if step.Issue == "" {
			t.Fatalf("failed result step omits issue link: %#v", step)
		}
		paths = append(paths, step.Issue)
	}
	return paths
}

func assertIssueLinkValid(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("result issue link %q is invalid: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("result issue link %q resolves to non-regular mode %v", path, info.Mode())
	}
}
