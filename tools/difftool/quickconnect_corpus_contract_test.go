// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"testing"
)

// substitutionVarPattern matches ${variable} references in step params,
// headers, and body values. The capture group holds the variable name.
var substitutionVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// quickconnectCorpusContract locks the required QuickConnect state-transition,
// error, metadata, expected-status, raw-bool, host-local capture/substitution,
// and replay-equality steps. A corpus regression that reorders, removes, or
// weakens any of these steps fails this contract.

type quickconnectStep struct {
	name         string
	method       string
	path         string
	operationID  string
	authRole     string
	headers      map[string]string
	params       map[string]any
	body         map[string]any
	capture      map[string]string
	status       int
	bodyBytes    string
	assertEqual  map[string]string
	hasVolatiles bool
}

var quickconnectCorpusContract = []quickconnectStep{
	{
		name:        "QuickConnect enabled scalar",
		method:      "GET",
		path:        "/QuickConnect/Enabled",
		operationID: "GetQuickConnectEnabled",
		status:      200,
		bodyBytes:   "true",
	},
	{
		name:        "initiate with client and device metadata",
		method:      "POST",
		path:        "/QuickConnect/Initiate",
		operationID: "InitiateQuickConnect",
		headers: map[string]string{
			"Authorization": `MediaBrowser Client="Swiftfin", Device="iPhone", DeviceId="swiftfin-quickconnect", Version="1.0.0"`,
		},
		capture:      map[string]string{"qc_secret": "Secret", "qc_code": "Code"},
		status:       200,
		hasVolatiles: true,
	},
	{
		name:         "pending poll",
		method:       "GET",
		path:         "/QuickConnect/Connect",
		operationID:  "GetQuickConnectState",
		params:       map[string]any{"secret": "${qc_secret}"},
		status:       200,
		hasVolatiles: true,
	},
	{
		name:        "authenticated authorization",
		method:      "POST",
		path:        "/QuickConnect/Authorize",
		operationID: "AuthorizeQuickConnect",
		authRole:    "admin",
		params:      map[string]any{"code": "${qc_code}", "userId": "<user-id>"},
		status:      200,
		bodyBytes:   "true",
	},
	{
		name:         "authorized poll",
		method:       "GET",
		path:         "/QuickConnect/Connect",
		operationID:  "GetQuickConnectState",
		params:       map[string]any{"secret": "${qc_secret}"},
		status:       200,
		hasVolatiles: true,
	},
	{
		name:        "first redemption",
		method:      "POST",
		path:        "/Users/AuthenticateWithQuickConnect",
		operationID: "AuthenticateWithQuickConnect",
		body:        map[string]any{"Secret": "${qc_secret}"},
		capture:     map[string]string{"qc_access_token": "AccessToken", "qc_session_id": "SessionInfo.Id"},
		status:      200,
	},
	{
		name:        "second redemption replays the authorized result",
		method:      "POST",
		path:        "/Users/AuthenticateWithQuickConnect",
		operationID: "AuthenticateWithQuickConnect",
		body:        map[string]any{"Secret": "${qc_secret}"},
		status:      200,
		assertEqual: map[string]string{"AccessToken": "qc_access_token", "SessionInfo.Id": "qc_session_id"},
	},
	{
		name:        "unknown secret",
		method:      "GET",
		path:        "/QuickConnect/Connect",
		operationID: "GetQuickConnectState",
		params:      map[string]any{"secret": "difftool-unknown-secret"},
		status:      404,
	},
	{
		name:        "missing secret",
		method:      "GET",
		path:        "/QuickConnect/Connect",
		operationID: "GetQuickConnectState",
		status:      400,
	},
	{
		name:        "missing device metadata",
		method:      "POST",
		path:        "/QuickConnect/Initiate",
		operationID: "InitiateQuickConnect",
		status:      400,
	},
	{
		name:        "unknown code",
		method:      "POST",
		path:        "/QuickConnect/Authorize",
		operationID: "AuthorizeQuickConnect",
		authRole:    "admin",
		params:      map[string]any{"code": "000000", "userId": "<user-id>"},
		status:      404,
	},
	{
		name:        "unauthorized authorization",
		method:      "POST",
		path:        "/QuickConnect/Authorize",
		operationID: "AuthorizeQuickConnect",
		params:      map[string]any{"code": "000000", "userId": "<user-id>"},
		status:      401,
	},
}

func TestQuickConnectCorpus(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "quickconnect.yaml"))
	if err != nil {
		t.Fatalf("load committed quickconnect corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed quickconnect corpus scenarios = %d, want one", len(scenarios))
	}
	steps := scenarios[0].Steps
	if len(steps) != len(quickconnectCorpusContract) {
		t.Fatalf("quickconnect corpus steps = %d, want %d", len(steps), len(quickconnectCorpusContract))
	}
	for index, expected := range quickconnectCorpusContract {
		step := steps[index]
		if step.Name != expected.name || step.Method != expected.method || step.Path != expected.path || step.OperationID != expected.operationID {
			t.Fatalf("quickconnect step %d = (%q, %s, %q, %q), want (%q, %s, %q, %q)", index, step.Name, step.Method, step.Path, step.OperationID, expected.name, expected.method, expected.path, expected.operationID)
		}
		if step.AuthRole != expected.authRole {
			t.Fatalf("quickconnect step %q auth_role = %q, want %q", step.Name, step.AuthRole, expected.authRole)
		}
		if !mapsEqual(step.Headers, expected.headers) {
			t.Fatalf("quickconnect step %q headers = %#v, want %#v", step.Name, step.Headers, expected.headers)
		}
		if !paramsEqual(step.Params, expected.params) {
			t.Fatalf("quickconnect step %q params = %#v, want %#v", step.Name, step.Params, expected.params)
		}
		if !mapsEqual(step.Capture, expected.capture) {
			t.Fatalf("quickconnect step %q capture = %#v, want %#v", step.Name, step.Capture, expected.capture)
		}
		if !bodyEqual(step.Body, expected.body) {
			t.Fatalf("quickconnect step %q body = %#v, want %#v", step.Name, step.Body, expected.body)
		}
		if step.Expect == nil || step.Expect.Status == nil || *step.Expect.Status != expected.status {
			t.Fatalf("quickconnect step %q expect.status = %#v, want %d", step.Name, step.Expect, expected.status)
		}
		if step.Expect.BodyBytes != expected.bodyBytes {
			t.Fatalf("quickconnect step %q body_bytes = %q, want %q", step.Name, step.Expect.BodyBytes, expected.bodyBytes)
		}
		if !mapsEqual(step.Expect.AssertEqual, expected.assertEqual) {
			t.Fatalf("quickconnect step %q assert_equal = %#v, want %#v", step.Name, step.Expect.AssertEqual, expected.assertEqual)
		}
		if expected.hasVolatiles && !containsNormalizer(step.selectedRules(), "quickconnect-volatiles") {
			t.Fatalf("quickconnect step %q must select quickconnect-volatiles", step.Name)
		}
	}
	// The second redemption must assert replay equality against the first.
	redemption := steps[6]
	if redemption.Expect.AssertEqual["AccessToken"] != "qc_access_token" {
		t.Fatalf("second redemption must assert AccessToken equality: %#v", redemption.Expect.AssertEqual)
	}
	if redemption.Expect.AssertEqual["SessionInfo.Id"] != "qc_session_id" {
		t.Fatalf("second redemption must assert SessionInfo.Id equality: %#v", redemption.Expect.AssertEqual)
	}
}

// validateCaptureContract exercises the real capture and substitution
// contract for a set of steps. It calls validateCapture on each step
// capture map and verifies that every capture variable referenced in a
// later step has a prior capture. A deletion or wrong-path mutation
// that breaks this contract returns a non-nil error.
func validateCaptureContract(steps []Step) error {
	priorCaptures := make(map[string]bool)
	for i, step := range steps {
		if err := validateCapture(step.Capture); err != nil {
			return fmt.Errorf("step %d %q: %w", i, step.Name, err)
		}
		for _, name := range sortedCaptureNames(step.Capture) {
			priorCaptures[name] = true
		}
		// Verify that every capture variable referenced in params, headers,
		// and body has a prior capture. This mirrors the substitution
		// contract that the runner enforces through substituteStep.
		if err := validateSubstitutionReferences(step, priorCaptures); err != nil {
			return fmt.Errorf("step %d %q: %w", i, step.Name, err)
		}
		// Verify that assert_equal references a prior capture. This mirrors
		// the validateAssertEqual contract that Scenario.validate enforces.
		if step.Expect != nil && len(step.Expect.AssertEqual) > 0 {
			if err := validateAssertEqual(step.Expect.AssertEqual, priorCaptures); err != nil {
				return fmt.Errorf("step %d %q: %w", i, step.Name, err)
			}
		}
	}
	return nil
}

// validateSubstitutionReferences checks that every capture variable
// referenced in step params, headers, and body has a prior capture.
// This mirrors the error that substituteStep returns at runtime.
func validateSubstitutionReferences(step Step, priorCaptures map[string]bool) error {
	for _, ref := range collectSubstitutionRefs(step) {
		if !priorCaptures[ref] {
			return fmt.Errorf("references capture %q that no prior step captured", ref)
		}
	}
	return nil
}

// collectSubstitutionRefs returns the set of capture variable names
// referenced in step params, headers, and body.
func collectSubstitutionRefs(step Step) []string {
	var refs []string
	for _, value := range step.Params {
		refs = append(refs, extractRefs(value)...)
	}
	for _, value := range step.Headers {
		refs = append(refs, extractRefs(value)...)
	}
	refs = append(refs, extractRefs(step.Body)...)
	return refs
}

func extractRefs(value any) []string {
	var refs []string
	switch v := value.(type) {
	case string:
		for _, ref := range substitutionVarPattern.FindAllStringSubmatch(v, -1) {
			refs = append(refs, ref[1])
		}
	case map[string]any:
		for _, entry := range v {
			refs = append(refs, extractRefs(entry)...)
		}
	case []any:
		for _, entry := range v {
			refs = append(refs, extractRefs(entry)...)
		}
	}
	return refs
}

// TestQuickConnectCorpusCaptureMutations proves that the capture-equality
// contract is load-bearing. Each mutation must fail through the real
// validateCapture or substitution contract. The committed corpus passes.
func TestQuickConnectCorpusCaptureMutations(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "quickconnect.yaml"))
	if err != nil {
		t.Fatalf("load committed quickconnect corpus: %v", err)
	}
	originalSteps := scenarios[0].Steps

	// The committed corpus must pass the real contract.
	if err := validateCaptureContract(originalSteps); err != nil {
		t.Fatalf("committed quickconnect corpus fails capture contract: %v", err)
	}

	// cloneCaptureMap returns a shallow copy of a capture map so a mutation
	// does not corrupt the original.
	cloneCaptureMap := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		c := make(map[string]string, len(m))
		for k, v := range m {
			c[k] = v
		}
		return c
	}

	// findStepsWithCapture returns the step indices that declare the named
	// capture variable in the original corpus.
	findStepsWithCapture := func(captureName string) []int {
		var indices []int
		for i, step := range originalSteps {
			if _, ok := step.Capture[captureName]; ok {
				indices = append(indices, i)
			}
		}
		return indices
	}

	// assertCaptureMutation clones the corpus, applies a mutation to each
	// step capture map, then verifies the mutation fails the real contract.
	assertCaptureMutation := func(desc string, mutate func(capture map[string]string) map[string]string) {
		t.Helper()
		mutated := make([]Step, len(originalSteps))
		for i, step := range originalSteps {
			mutated[i] = step
			mutated[i].Capture = mutate(cloneCaptureMap(step.Capture))
		}
		if err := validateCaptureContract(mutated); err == nil {
			t.Fatalf("capture mutation %q passed the contract but must fail", desc)
		}
	}

	// Delete qc_secret from the initiate step. Steps that reference
	// ${qc_secret} must fail the substitution contract.
	assertCaptureMutation("delete qc_secret", func(capture map[string]string) map[string]string {
		delete(capture, "qc_secret")
		return capture
	})

	// Delete qc_code from the initiate step. The authorize step references
	// ${qc_code} and must fail the substitution contract.
	assertCaptureMutation("delete qc_code", func(capture map[string]string) map[string]string {
		delete(capture, "qc_code")
		return capture
	})

	// Wrong-path mutations for each capture variable. An empty path segment
	// fails parseCapturePath, which validateCapture enforces.
	for _, captureName := range []string{"qc_secret", "qc_code", "qc_access_token", "qc_session_id"} {
		indices := findStepsWithCapture(captureName)
		if len(indices) == 0 {
			t.Fatalf("committed corpus does not capture %q", captureName)
		}
		assertCaptureMutation("wrong path for "+captureName, func(capture map[string]string) map[string]string {
			if _, ok := capture[captureName]; ok {
				capture[captureName] = "wrong..path"
			}
			return capture
		})
	}
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func paramsEqual(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if !valueEqual(right[key], value) {
			return false
		}
	}
	return true
}

func bodyEqual(left any, right map[string]any) bool {
	leftMap, ok := left.(map[string]any)
	if !ok {
		return right == nil
	}
	return paramsEqual(leftMap, right)
}

func valueEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue
	case int:
		rightValue, ok := right.(int)
		return ok && leftValue == rightValue
	case int64:
		rightValue, ok := right.(int)
		return ok && int64(rightValue) == leftValue
	case float64:
		switch rightValue := right.(type) {
		case float64:
			return leftValue == rightValue
		case int:
			return leftValue == float64(rightValue)
		case int64:
			return leftValue == float64(rightValue)
		default:
			return false
		}
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		return ok && paramsEqual(leftValue, rightValue)
	default:
		return left == right
	}
}
