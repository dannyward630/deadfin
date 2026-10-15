// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var errDifferences = errors.New("difftool found differences")

func Run(ctx context.Context, config Config) (runResult Result, resultErr error) {
	config = defaults(config)
	if err := validateConfig(config); err != nil {
		return Result{}, err
	}
	runIssuesDir := filepath.Join(config.IssuesDir, config.RunID)
	issues, err := openIssueDirectory(runIssuesDir)
	if err != nil {
		return Result{}, fmt.Errorf("prepare issue artifacts: %w", err)
	}
	defer func() {
		if closeErr := issues.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close issue directory: %w", closeErr))
		}
	}()
	resultPath, err := prepareResultPath(config.ResultsDir, config.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("prepare result artifact: %w", err)
	}
	rules, err := loadRules(config.RulesDir)
	if err != nil {
		return Result{}, err
	}
	scenarios, err := loadScenarios(config.Corpus)
	if err != nil {
		return Result{}, err
	}
	if config.CheckNormalizers {
		if err := validateSelfCheckScenarios(scenarios); err != nil {
			return Result{}, err
		}
	}
	left, err := newHTTPHost(ctx, config.LeftURL, config.LeftAuth, config.Client)
	if err != nil {
		return Result{}, fmt.Errorf("left host: %w", err)
	}
	rightURL := config.RightURL
	rightAuth := config.RightAuth
	if config.CheckNormalizers {
		rightURL = config.LeftURL
		rightAuth = config.LeftAuth
	}
	var right *httpHost
	if config.LeftURL == rightURL && config.LeftAuth == rightAuth {
		right, err = newHTTPHost(ctx, rightURL, AuthConfig{}, config.Client)
		if err == nil {
			right.auth = left.auth
			right.token = left.token
			right.userID = left.userID
		}
	} else {
		right, err = newHTTPHost(ctx, rightURL, rightAuth, config.Client)
	}
	if err != nil {
		return Result{}, fmt.Errorf("right host: %w", err)
	}
	leftIDs, err := left.BuildItemMap(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("left item-ID map: %w", err)
	}
	rightIDs, err := right.BuildItemMap(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("right item-ID map: %w", err)
	}
	left.SetItemIDs(leftIDs)
	right.SetItemIDs(rightIDs)
	left.setSourceIDs(leftIDs)
	right.setSourceIDs(leftIDs)
	if err := issues.clear(); err != nil {
		return Result{}, fmt.Errorf("prepare issue artifacts: %w", err)
	}

	result := Result{Format: "difftool-results/v1", RunID: config.RunID}
	failureCount := 0
	for _, scenario := range scenarios {
		leftCaptures := left.replayValues()
		rightCaptures := right.replayValues()
		hostRedactions := mergeRedactionValues(left.redactionValues(), right.redactionValues())
		normalizedComparator := newNormalizedComparator()
		for _, step := range scenario.Steps {
			leftAuthoredStep, err := left.rewriteStep(step)
			if err != nil {
				return Result{}, fmt.Errorf("scenario %q, step %q, left request rewrite: %w", scenario.Name, step.Name, err)
			}
			rightAuthoredStep, err := right.rewriteStep(step)
			if err != nil {
				return Result{}, fmt.Errorf("scenario %q, step %q, right request rewrite: %w", scenario.Name, step.Name, err)
			}
			leftStep, err := substituteStep(leftAuthoredStep, leftCaptures)
			if err != nil {
				return Result{}, fmt.Errorf("scenario %q, step %q: %w", scenario.Name, step.Name, err)
			}
			rightStep, err := substituteStep(rightAuthoredStep, rightCaptures)
			if err != nil {
				return Result{}, fmt.Errorf("scenario %q, step %q: %w", scenario.Name, step.Name, err)
			}
			stepRules, err := selectRules(rules, step.selectedRules())
			if err != nil {
				return Result{}, fmt.Errorf("scenario %q, step %q: %w", scenario.Name, step.Name, err)
			}
			normalize := newNormalizer(stepRules)
			leftResponse, leftErr := left.Request(ctx, leftStep, config.Profile)
			rightResponse, rightErr := right.Request(ctx, rightStep, config.Profile)
			if leftErr == nil {
				if err := captureResponse(step, leftResponse, leftCaptures); err != nil {
					return Result{}, fmt.Errorf("scenario %q, step %q, left response: %w", scenario.Name, step.Name, err)
				}
			}
			if rightErr == nil {
				if err := captureResponse(step, rightResponse, rightCaptures); err != nil {
					return Result{}, fmt.Errorf("scenario %q, step %q, right response: %w", scenario.Name, step.Name, err)
				}
			}
			stepResult := StepResult{Scenario: scenario.Name, Step: step.Name, OperationID: step.OperationID, Status: "pass", BodyLengthCounted: leftResponse.BodyLengthCounted}
			if leftErr != nil || rightErr != nil {
				failureCount++
				stepResult.Status = "fail"
				issuePath, issueErr := issues.write(failureCount, scenario, step, leftResponse, rightResponse, normalizedResponse{}, normalizedResponse{}, requestErrorDiff(leftErr, rightErr), mergeRedactionValues(hostRedactions, captureValues(leftCaptures, rightCaptures)))
				if issueErr != nil {
					return Result{}, issueErr
				}
				stepResult.Issue = issuePath
				result.Steps = append(result.Steps, stepResult)
				continue
			}
			if step.Expect != nil && step.Expect.Status != nil && (leftResponse.Status != *step.Expect.Status || rightResponse.Status != *step.Expect.Status) {
				failureCount++
				stepResult.Status = "fail"
				difference := fmt.Sprintf("expected status: expected=%d left=%d right=%d", *step.Expect.Status, leftResponse.Status, rightResponse.Status)
				issuePath, issueErr := issues.write(failureCount, scenario, step, leftResponse, rightResponse, normalizedResponse{}, normalizedResponse{}, difference, mergeRedactionValues(hostRedactions, captureValues(leftCaptures, rightCaptures)))
				if issueErr != nil {
					return Result{}, issueErr
				}
				stepResult.Issue = issuePath
				result.Steps = append(result.Steps, stepResult)
				continue
			}
			if step.Expect != nil {
				if mismatch := checkExpectedBody(step.Expect, leftResponse, rightResponse, leftCaptures, rightCaptures); mismatch != "" {
					failureCount++
					stepResult.Status = "fail"
					issuePath, issueErr := issues.write(failureCount, scenario, step, leftResponse, rightResponse, normalizedResponse{}, normalizedResponse{}, mismatch, mergeRedactionValues(hostRedactions, captureValues(leftCaptures, rightCaptures)))
					if issueErr != nil {
						return Result{}, issueErr
					}
					stepResult.Issue = issuePath
					result.Steps = append(result.Steps, stepResult)
					continue
				}
			}
			leftNormalized, err := normalize.normalize(leftResponse, leftIDs)
			if err != nil {
				return Result{}, err
			}
			rightNormalized, err := normalize.normalize(rightResponse, rightIDs)
			if err != nil {
				return Result{}, err
			}
			if equal, diff := normalizedComparator.Compare(leftNormalized, rightNormalized, normalize.dateJitter); !equal {
				failureCount++
				stepResult.Status = "fail"
				issuePath, issueErr := issues.write(failureCount, scenario, step, leftResponse, rightResponse, leftNormalized, rightNormalized, diff, mergeRedactionValues(hostRedactions, captureValues(leftCaptures, rightCaptures)))
				if issueErr != nil {
					return Result{}, issueErr
				}
				stepResult.Issue = issuePath
			}
			result.Steps = append(result.Steps, stepResult)
		}
	}
	if err := writeResults(resultPath, result); err != nil {
		return Result{}, err
	}
	if failureCount != 0 {
		return result, errDifferences
	}
	return result, nil
}

// checkExpectedBody enforces expect.body_bytes, expect.body_length, and
// expect.assert_equal against both raw responses before normalization. Each
// check returns a nonempty difference string when a host fails the contract.
// An empty string means that the contract held. body_bytes compares exact raw
// bytes, body_length compares the counted stream total, and assert_equal
// asserts that a response scalar equals an earlier capture from the same host.
func checkExpectedBody(expect *Expect, left, right Response, leftCaptures, rightCaptures map[string]string) string {
	if expect.BodyBytes != "" {
		if string(left.Body) != expect.BodyBytes {
			return fmt.Sprintf("expected body_bytes: left=%q want %q", string(left.Body), expect.BodyBytes)
		}
		if string(right.Body) != expect.BodyBytes {
			return fmt.Sprintf("expected body_bytes: right=%q want %q", string(right.Body), expect.BodyBytes)
		}
	}
	if expect.BodyLength != nil {
		want := *expect.BodyLength
		if left.BodyLengthOverLimit || left.BodyLengthCounted != want {
			return fmt.Sprintf("expected body_length: left counted=%d over_limit=%t want %d", left.BodyLengthCounted, left.BodyLengthOverLimit, want)
		}
		if right.BodyLengthOverLimit || right.BodyLengthCounted != want {
			return fmt.Sprintf("expected body_length: right counted=%d over_limit=%t want %d", right.BodyLengthCounted, right.BodyLengthOverLimit, want)
		}
	}
	if len(expect.AssertEqual) > 0 {
		if mismatch := assertCaptureEquality("left", expect.AssertEqual, left, leftCaptures); mismatch != "" {
			return mismatch
		}
		if mismatch := assertCaptureEquality("right", expect.AssertEqual, right, rightCaptures); mismatch != "" {
			return mismatch
		}
	}
	return ""
}

// assertCaptureEquality asserts that each response scalar at the assertion's
// response path equals the value of the named prior capture from the same host.
// It decodes the response body once and reuses the shared capture traversal so
// the assertion and capture paths cannot diverge.
func assertCaptureEquality(side string, assertions map[string]string, response Response, captures map[string]string) string {
	if !json.Valid(response.Body) {
		return fmt.Sprintf("assert_equal %s: response is not JSON", side)
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Sprintf("assert_equal %s: response is not JSON", side)
	}
	for _, responsePath := range sortedCaptureNames(assertions) {
		captureName := assertions[responsePath]
		value, err := extractCapturePath(captureName, responsePath, payload)
		if err != nil {
			return fmt.Sprintf("assert_equal %s at %q: %s", side, responsePath, err)
		}
		scalar, ok := captureScalar(value)
		if !ok {
			return fmt.Sprintf("assert_equal %s at %q: value is not a scalar", side, responsePath)
		}
		captured, ok := captures[captureName]
		if !ok {
			return fmt.Sprintf("assert_equal %s at %q: capture %q is absent", side, responsePath, captureName)
		}
		if scalar != captured {
			return fmt.Sprintf("assert_equal %s at %q: response does not match capture %q", side, responsePath, captureName)
		}
	}
	return ""
}

func defaults(config Config) Config {
	if config.RulesDir == "" {
		config.RulesDir = defaultRulesDir()
	}
	if config.ResultsDir == "" {
		config.ResultsDir = "results"
	}
	if config.IssuesDir == "" {
		config.IssuesDir = ".diffs"
	}
	if config.RunID == "" {
		config.RunID = "difftool"
	}
	if config.LeftAuth.Role == "" {
		config.LeftAuth.Role = "default"
	}
	if config.RightAuth.Role == "" {
		config.RightAuth.Role = "default"
	}
	return config
}

func defaultRulesDir() string {
	if info, err := os.Stat("normalize"); err == nil && info.IsDir() {
		return "normalize"
	}
	return "tools/difftool/normalize"
}

func validateConfig(config Config) error {
	if config.Corpus == "" {
		return errors.New("--corpus is required")
	}
	if config.LeftURL == "" {
		return errors.New("--left is required")
	}
	if config.RightURL == "" && !config.CheckNormalizers {
		return errors.New("--right is required unless --check-normalizers is set")
	}
	if config.Profile != "" && config.Profile != "CamelCase" {
		return fmt.Errorf("unsupported profile %q (only CamelCase is supported)", config.Profile)
	}
	if config.RunID != safeFilename(config.RunID) {
		return fmt.Errorf("run-id must contain only letters, digits, dots, underscores, or hyphens")
	}
	for _, auth := range []AuthConfig{config.LeftAuth, config.RightAuth} {
		if auth.Username == "" && auth.Password != "" {
			return errors.New("an auth password requires an auth username")
		}
		if (auth.DisabledUsername == "") != (auth.DisabledPassword == "") {
			return errors.New("disabled-fixture username and password must be supplied together")
		}
	}
	return nil
}

func validateSelfCheckScenarios(scenarios []Scenario) error {
	for _, scenario := range scenarios {
		for _, step := range scenario.Steps {
			switch strings.ToUpper(strings.TrimSpace(step.Method)) {
			case "POST", "PUT", "PATCH", "DELETE":
				return fmt.Errorf("--check-normalizers requires read-only scenarios; scenario %q, step %q uses %s", scenario.Name, step.Name, step.Method)
			}
		}
	}
	return nil
}

func loadScenarios(corpus string) ([]Scenario, error) {
	info, err := os.Stat(corpus)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	if info.IsDir() {
		entries, err := os.ReadDir(corpus)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension == ".yaml" || extension == ".yml" || extension == ".json" {
				paths = append(paths, filepath.Join(corpus, entry.Name()))
			}
		}
		sort.Strings(paths)
	} else {
		paths = []string{corpus}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no scenario files in %s", corpus)
	}
	scenarios := make([]Scenario, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var scenario Scenario
		if err := yaml.Unmarshal(contents, &scenario); err != nil {
			return nil, fmt.Errorf("parse scenario %s: %w", path, err)
		}
		for index := range scenario.Steps {
			step := &scenario.Steps[index]
			if step.BodyFile == "" {
				continue
			}
			if step.Body != nil {
				return nil, fmt.Errorf("%s: step %d must not specify both body and body_file", path, index+1)
			}
			bodyPath := filepath.Join(filepath.Dir(path), step.BodyFile)
			body, err := os.ReadFile(bodyPath)
			if err != nil {
				return nil, fmt.Errorf("%s: step %d read body_file %q: %w", path, index+1, step.BodyFile, err)
			}
			var decoded any
			if err := json.Unmarshal(body, &decoded); err != nil {
				return nil, fmt.Errorf("%s: step %d parse body_file %q as JSON: %w", path, index+1, step.BodyFile, err)
			}
			if step.BodyFileField != "" {
				step.Body = map[string]any{step.BodyFileField: decoded}
			} else {
				step.Body = decoded
			}
			step.bodyFileLoaded = true
		}
		if err := scenario.validate(path); err != nil {
			return nil, err
		}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, nil
}

func loadRules(directory string) ([]Rule, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	knownDivergences := readDivergences()
	var rules []Rule
	names := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var file RuleFile
		if err := yaml.Unmarshal(contents, &file); err != nil {
			return nil, fmt.Errorf("parse normalizer %s: %w", entry.Name(), err)
		}
		if file.Version != "difftool-normalize/v1" {
			return nil, fmt.Errorf("normalizer %s has unsupported version %q", entry.Name(), file.Version)
		}
		for _, rule := range file.Rules {
			if err := rule.validate(knownDivergences); err != nil {
				return nil, fmt.Errorf("normalizer %s: %w", entry.Name(), err)
			}
			if _, duplicate := names[rule.Name]; duplicate {
				return nil, fmt.Errorf("duplicate normalizer rule name %q", rule.Name)
			}
			names[rule.Name] = struct{}{}
			rules = append(rules, rule)
		}
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("no normalizer rules in %s", directory)
	}
	return rules, nil
}

func readDivergences() string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		path := filepath.Join(workingDirectory, "docs", "divergences.md")
		if contents, err := os.ReadFile(path); err == nil {
			return string(contents)
		}
		parent := filepath.Dir(workingDirectory)
		if parent == workingDirectory {
			return ""
		}
		workingDirectory = parent
	}
}

func selectRules(all []Rule, selected []string) ([]Rule, error) {
	if len(selected) == 0 {
		defaults := make([]Rule, 0, len(all))
		for _, rule := range all {
			if !rule.ExplicitOnly {
				defaults = append(defaults, rule)
			}
		}
		return defaults, nil
	}
	byName := make(map[string]Rule, len(all))
	for _, rule := range all {
		byName[rule.Name] = rule
	}
	rules := make([]Rule, 0, len(selected))
	for _, name := range selected {
		rule, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("scenario references unknown normalizer %q", name)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func prepareResultPath(directory, runID string) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(directory, runID+".json")
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.IsDir() || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
			return "", fmt.Errorf("result path %q is not a regular file", path)
		}
		if err := os.Remove(path); err != nil {
			return "", err
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	return path, nil
}

func writeResults(path string, result Result) (resultErr error) {
	contents, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')

	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary result %q: %w", temporaryPath, removeErr))
		}
	}()

	if _, writeErr := file.Write(contents); writeErr != nil {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(
				fmt.Errorf("write temporary result %q: %w", temporaryPath, writeErr),
				fmt.Errorf("close temporary result %q: %w", temporaryPath, closeErr),
			)
		}
		return fmt.Errorf("write temporary result %q: %w", temporaryPath, writeErr)
	}
	if chmodErr := file.Chmod(0o644); chmodErr != nil {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(
				fmt.Errorf("chmod temporary result %q: %w", temporaryPath, chmodErr),
				fmt.Errorf("close temporary result %q: %w", temporaryPath, closeErr),
			)
		}
		return fmt.Errorf("chmod temporary result %q: %w", temporaryPath, chmodErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close temporary result %q: %w", temporaryPath, closeErr)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace result artifact %q: %w", path, err)
	}
	return nil
}
