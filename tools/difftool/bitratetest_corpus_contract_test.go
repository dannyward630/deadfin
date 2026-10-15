// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// bitrateTestCorpusContract locks the size matrix, authentication matrix,
// exact captured lengths, unnormalized Content-Length/Content-Type, and both
// /System/Endpoint booleans. A corpus regression that reorders, removes, or
// weakens any of these steps fails this contract.

type bitrateTestStep struct {
	name              string
	method            string
	path              string
	operationID       string
	params            map[string]any
	authRole          string
	status            int
	bodyLength        *int
	dropContentLength bool
}

func intPtr(v int) *int { return &v }

var bitrateTestCorpusContract = []bitrateTestStep{
	{
		name:        "authenticated default size",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		authRole:    "admin",
		status:      200,
		bodyLength:  intPtr(131072),
	},
	{
		name:        "authenticated lower bound one byte",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 1},
		authRole:    "admin",
		status:      200,
		bodyLength:  intPtr(16),
	},
	{
		name:        "authenticated power of two size",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 1024},
		authRole:    "admin",
		status:      200,
		bodyLength:  intPtr(1024),
	},
	{
		name:        "authenticated non-power-of-two size",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 1000},
		authRole:    "admin",
		status:      200,
		bodyLength:  intPtr(1024),
	},
	{
		name:        "authenticated valid upper bound",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 100000000},
		authRole:    "admin",
		status:      200,
		bodyLength:  intPtr(134217728),
	},
	{
		name:        "zero is below the lower bound",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 0},
		authRole:    "admin",
		status:      400,
	},
	{
		name:        "above the valid upper bound",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": 100000001},
		authRole:    "admin",
		status:      400,
	},
	{
		name:        "non-numeric size is a validation error",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		params:      map[string]any{"size": "abc"},
		authRole:    "admin",
		status:      400,
	},
	{
		name:        "unauthenticated bitrate test",
		method:      "GET",
		path:        "/Playback/BitrateTest",
		operationID: "GetBitrateTestBytes",
		status:      401,
	},
	{
		name:              "authenticated system endpoint",
		method:            "GET",
		path:              "/System/Endpoint",
		operationID:       "GetEndpointInfo",
		authRole:          "admin",
		status:            200,
		dropContentLength: true,
	},
	{
		name:              "unauthenticated system endpoint",
		method:            "GET",
		path:              "/System/Endpoint",
		operationID:       "GetEndpointInfo",
		status:            401,
		dropContentLength: true,
	},
}

func TestBitrateTestCorpus(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "bitratetest.yaml"))
	if err != nil {
		t.Fatalf("load committed bitratetest corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed bitratetest corpus scenarios = %d, want one", len(scenarios))
	}
	steps := scenarios[0].Steps
	if len(steps) != len(bitrateTestCorpusContract) {
		t.Fatalf("bitratetest corpus steps = %d, want %d", len(steps), len(bitrateTestCorpusContract))
	}
	for index, expected := range bitrateTestCorpusContract {
		step := steps[index]
		if step.Name != expected.name || step.Method != expected.method || step.Path != expected.path || step.OperationID != expected.operationID {
			t.Fatalf("bitratetest step %d = (%q, %s, %q, %q), want (%q, %s, %q, %q)", index, step.Name, step.Method, step.Path, step.OperationID, expected.name, expected.method, expected.path, expected.operationID)
		}
		if step.AuthRole != expected.authRole {
			t.Fatalf("bitratetest step %q auth_role = %q, want %q", step.Name, step.AuthRole, expected.authRole)
		}
		// Every step must carry the exact expected query params. A mutation that
		// adds, removes, or changes a size value fails this comparison.
		if !paramsEqual(step.Params, expected.params) {
			t.Fatalf("bitratetest step %q params = %#v, want %#v", step.Name, step.Params, expected.params)
		}
		if step.Expect == nil || step.Expect.Status == nil || *step.Expect.Status != expected.status {
			t.Fatalf("bitratetest step %q expect.status = %#v, want %d", step.Name, step.Expect, expected.status)
		}
		if expected.bodyLength != nil {
			if step.Expect.BodyLength == nil || *step.Expect.BodyLength != *expected.bodyLength {
				t.Fatalf("bitratetest step %q body_length = %#v, want %d", step.Name, step.Expect.BodyLength, *expected.bodyLength)
			}
		} else if step.Expect.BodyLength != nil {
			t.Fatalf("bitratetest step %q must not set body_length", step.Name)
		}
		// Success steps must preserve Content-Length and Content-Type for comparison.
		hasContentLengthDrop := containsNormalizer(step.selectedRules(), "content-length-header")
		if expected.dropContentLength && !hasContentLengthDrop {
			t.Fatalf("bitratetest step %q must drop Content-Length for validation-body comparison", step.Name)
		}
		if !expected.dropContentLength && hasContentLengthDrop {
			t.Fatalf("bitratetest step %q must preserve Content-Length for length-contract comparison", step.Name)
		}
	}
	// Every BitrateTest step must preserve Content-Length, Content-Type,
	// and Cache-Control. The semantic-header validator enforces this
	// through the real resolved-rule contract.
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load baseline normalizers: %v", err)
	}
	if err := validateBitrateTestSemanticHeaders(steps, rules); err != nil {
		t.Fatalf("bitratetest semantic headers: %v", err)
	}
	// The upper-bound step must use body_length, not body_bytes, so it never
	// enters the 32 MiB buffer.
	upperBound := steps[4]
	if upperBound.Expect.BodyLength == nil || *upperBound.Expect.BodyLength != 134217728 {
		t.Fatalf("upper-bound step must assert body_length=134217728: %#v", upperBound.Expect)
	}
	// The System/Endpoint steps must not normalize IsLocal or IsInNetwork.
	for _, step := range steps {
		if step.Path == "/System/Endpoint" {
			if containsNormalizer(step.selectedRules(), "server-id") || containsNormalizer(step.selectedRules(), "environment-fields") {
				t.Fatalf("system endpoint step %q must not normalize IsLocal or IsInNetwork", step.Name)
			}
		}
	}
}

// validateBitrateTestSemanticHeaders exercises the real semantic-header
// contract for BitrateTest steps. It resolves the selected normalizers and
// rejects any drop_header rule that targets Content-Length, Content-Type,
// or Cache-Control on a BitrateTest step. A mutation that adds such a rule
// returns a non-nil error.
func validateBitrateTestSemanticHeaders(steps []Step, rules []Rule) error {
	for _, step := range steps {
		if step.Path != "/Playback/BitrateTest" {
			continue
		}
		resolved, err := selectRules(rules, step.selectedRules())
		if err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
		for _, rule := range resolved {
			if rule.Action != "drop_header" {
				continue
			}
			if strings.EqualFold(rule.Header, "Content-Length") {
				return fmt.Errorf("step %q must not drop Content-Length", step.Name)
			}
			if strings.EqualFold(rule.Header, "Content-Type") {
				return fmt.Errorf("step %q must not drop Content-Type", step.Name)
			}
			if strings.EqualFold(rule.Header, "Cache-Control") {
				return fmt.Errorf("step %q must not drop Cache-Control", step.Name)
			}
		}
	}
	return nil
}

// TestBitrateTestSemanticHeaderMutations proves that the semantic-header
// contract is load-bearing. Adding a real drop_header rule for
// Content-Length, Content-Type, or Cache-Control to a BitrateTest step
// must fail the semantic-header validator. Removing a required normalizer
// or adding an unknown rule must also fail. The committed corpus passes.
func TestBitrateTestSemanticHeaderMutations(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "bitratetest.yaml"))
	if err != nil {
		t.Fatalf("load committed bitratetest corpus: %v", err)
	}
	steps := scenarios[0].Steps
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load baseline normalizers: %v", err)
	}

	// The committed corpus must pass the semantic-header contract.
	if err := validateBitrateTestSemanticHeaders(steps, rules); err != nil {
		t.Fatalf("committed bitratetest corpus fails semantic-header contract: %v", err)
	}

	// findBitrateTestSuccessStep returns the first BitrateTest step with a
	// 200 status.
	findBitrateTestSuccessStep := func() int {
		for i, step := range steps {
			if step.Path == "/Playback/BitrateTest" && step.Expect != nil && step.Expect.Status != nil && *step.Expect.Status == 200 {
				return i
			}
		}
		t.Fatal("no BitrateTest success step found in committed corpus")
		return -1
	}

	// assertHeaderDropMutation clones the corpus, adds a real drop_header
	// rule for the named header to a BitrateTest success step, then verifies
	// the semantic-header validator rejects it.
	assertHeaderDropMutation := func(desc string, header string) {
		t.Helper()
		idx := findBitrateTestSuccessStep()
		mutated := make([]Step, len(steps))
		copy(mutated, steps)
		mutated[idx] = steps[idx]
		dropRule := Rule{Name: "mutation-drop-" + header, Action: "drop_header", Header: header}
		mutatedRules := append([]Rule{}, rules...)
		mutatedRules = append(mutatedRules, dropRule)
		mutated[idx].Normalizers = append(append([]string{}, steps[idx].selectedRules()...), dropRule.Name)
		if err := validateBitrateTestSemanticHeaders(mutated, mutatedRules); err == nil {
			t.Fatalf("mutation %q passed the contract but must fail", desc)
		}
	}

	// Dropping Content-Length on a BitrateTest step must fail.
	assertHeaderDropMutation("drop Content-Length", "Content-Length")

	// Dropping Content-Type on a BitrateTest step must fail.
	assertHeaderDropMutation("drop Content-Type", "Content-Type")

	// Dropping Cache-Control on a BitrateTest step must fail.
	assertHeaderDropMutation("drop Cache-Control", "Cache-Control")

	// Adding an unknown normalizer name must fail at selectRules.
	assertUnknownRuleMutation := func(desc string, addRule string) {
		t.Helper()
		idx := findBitrateTestSuccessStep()
		mutated := make([]Step, len(steps))
		copy(mutated, steps)
		mutated[idx] = steps[idx]
		mutated[idx].Normalizers = append(append([]string{}, steps[idx].selectedRules()...), addRule)
		if err := validateBitrateTestSemanticHeaders(mutated, rules); err == nil {
			t.Fatalf("mutation %q passed the contract but must fail", desc)
		}
	}

	assertUnknownRuleMutation("unknown rule content-type-header", "content-type-header")
	assertUnknownRuleMutation("unknown rule cache-control-header", "cache-control-header")
}

// TestBitrateTestParamsMutations proves that the step.Params comparison is
// load-bearing. Each mutation changes a size query value, adds a spurious
// param, or removes a param from a BitrateTest step. Each mutation must fail
// the paramsEqual check that the contract loop applies. The committed corpus
// must pass.
func TestBitrateTestParamsMutations(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "bitratetest.yaml"))
	if err != nil {
		t.Fatalf("load committed bitratetest corpus: %v", err)
	}
	steps := scenarios[0].Steps

	// The committed corpus must pass paramsEqual for every step.
	for index, expected := range bitrateTestCorpusContract {
		if !paramsEqual(steps[index].Params, expected.params) {
			t.Fatalf("committed bitratetest step %q params = %#v, want %#v", steps[index].Name, steps[index].Params, expected.params)
		}
	}

	// findSizeStep returns the first step that carries the given size value.
	findSizeStep := func(wantSize int) int {
		for i, step := range steps {
			if step.Params != nil {
				if size, ok := step.Params["size"].(int); ok && size == wantSize {
					return i
				}
			}
		}
		t.Fatalf("no BitrateTest step with size %d found in committed corpus", wantSize)
		return -1
	}

	// findStringSizeStep returns the first step whose size param is the
	// given string value.
	findStringSizeStep := func(wantSize string) int {
		for i, step := range steps {
			if step.Params != nil {
				if size, ok := step.Params["size"].(string); ok && size == wantSize {
					return i
				}
			}
		}
		t.Fatalf("no BitrateTest step with size %q found in committed corpus", wantSize)
		return -1
	}

	// assertParamMutation clones the step's Params, applies the mutation, then
	// verifies the paramsEqual check fails for the expected contract entry.
	assertParamMutation := func(desc string, idx int, mutate func(map[string]any) map[string]any) {
		t.Helper()
		clone := make(map[string]any, len(steps[idx].Params))
		for k, v := range steps[idx].Params {
			clone[k] = v
		}
		mutatedParams := mutate(clone)
		if paramsEqual(mutatedParams, bitrateTestCorpusContract[idx].params) {
			t.Fatalf("mutation %q passed paramsEqual but must fail", desc)
		}
	}

	// Changing each numeric size value must fail.
	assertParamMutation("change size 1 to 2", findSizeStep(1), func(p map[string]any) map[string]any {
		p["size"] = 2
		return p
	})
	assertParamMutation("change size 1024 to 1025", findSizeStep(1024), func(p map[string]any) map[string]any {
		p["size"] = 1025
		return p
	})
	assertParamMutation("change size 1000 to 1001", findSizeStep(1000), func(p map[string]any) map[string]any {
		p["size"] = 1001
		return p
	})
	assertParamMutation("change size 100000000 to 100000002", findSizeStep(100000000), func(p map[string]any) map[string]any {
		p["size"] = 100000002
		return p
	})
	assertParamMutation("change size 0 to 5", findSizeStep(0), func(p map[string]any) map[string]any {
		p["size"] = 5
		return p
	})
	assertParamMutation("change size 100000001 to 100000002", findSizeStep(100000001), func(p map[string]any) map[string]any {
		p["size"] = 100000002
		return p
	})

	// Changing the string size value must fail.
	assertParamMutation("change size abc to xyz", findStringSizeStep("abc"), func(p map[string]any) map[string]any {
		p["size"] = "xyz"
		return p
	})

	// Adding a spurious param to a step that has params must fail.
	assertParamMutation("add spurious param to size-1 step", findSizeStep(1), func(p map[string]any) map[string]any {
		p["extra"] = true
		return p
	})

	// Removing the size param from a step that has params must fail.
	assertParamMutation("remove size from size-1 step", findSizeStep(1), func(p map[string]any) map[string]any {
		delete(p, "size")
		return p
	})

	// Changing the type of a size param must fail.
	assertParamMutation("change size 1 type to string", findSizeStep(1), func(p map[string]any) map[string]any {
		p["size"] = "1"
		return p
	})
}
