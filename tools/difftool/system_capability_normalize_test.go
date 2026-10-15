// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const fullSystemRuntimeCapabilitiesNormalizer = "full-system-runtime-capabilities"

func TestNormalizerFullSystemRuntimeCapabilitiesDeletesOnlyCaseInsensitiveRootCapabilities(t *testing.T) {
	normalized, err := newNormalizer([]Rule{{Action: "full_system_runtime_capabilities"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"CanSelfRestart": true,
			"cAnSeLfUpDaTe": false,
			"SUPPORTSLIBRARYMONITOR": true,
			"systemArchitecture": "arm64",
			"CanSelfRestarted": "lookalike must remain",
			"CanSelfUpdates": "lookalike must remain",
			"SupportsLibraryMonitoring": "lookalike must remain",
			"SystemArchitectureBits": 64,
			"StartupWizardCompleted": true,
			"Nested": {
				"CanSelfRestart": true,
				"CanSelfUpdate": false,
				"SupportsLibraryMonitor": true,
				"SystemArchitecture": "nested architecture",
				"StartupWizardCompleted": false
			}
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize full system capabilities: %v", err)
	}

	want := map[string]any{
		"CanSelfRestarted":          "lookalike must remain",
		"CanSelfUpdates":            "lookalike must remain",
		"SupportsLibraryMonitoring": "lookalike must remain",
		"SystemArchitectureBits":    json.Number("64"),
		"StartupWizardCompleted":    true,
		"Nested": map[string]any{
			"CanSelfRestart":         true,
			"CanSelfUpdate":          false,
			"SupportsLibraryMonitor": true,
			"SystemArchitecture":     "nested architecture",
			"StartupWizardCompleted": false,
		},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("full-system runtime capability normalization = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerFullSystemRuntimeCapabilitiesEquatesOneSidedCanSelfUpdate(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "full_system_runtime_capabilities"}})
	left, err := normalizer.normalize(Response{
		Status: http.StatusOK,
		Body: []byte(`{
			"ProductName": "Jellyfin",
			"cAnSeLfUpDaTe": true,
			"StartupWizardCompleted": true
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize left full system info: %v", err)
	}
	right, err := normalizer.normalize(Response{
		Status: http.StatusOK,
		Body: []byte(`{
			"ProductName": "Jellyfin",
			"StartupWizardCompleted": true
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize right full system info: %v", err)
	}
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("one-sided CanSelfUpdate must be normalized away: %s", reason)
	}

	withoutRule, err := newNormalizer(nil).normalize(Response{
		Status: http.StatusOK,
		Body:   []byte(`{"CanSelfUpdate":true,"StartupWizardCompleted":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize without capability rule: %v", err)
	}
	if !reflect.DeepEqual(withoutRule.Body, map[string]any{"CanSelfUpdate": true, "StartupWizardCompleted": true}) {
		t.Fatalf("unselected full-system capability rule changed response: %#v", withoutRule.Body)
	}
}

func TestFullSystemRuntimeCapabilitiesRuleIsExplicitOnly(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load normalizer rules: %v", err)
	}

	var capabilityRule Rule
	found := false
	for _, rule := range rules {
		if rule.Name == fullSystemRuntimeCapabilitiesNormalizer {
			capabilityRule = rule
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("normalizer rule %q is missing", fullSystemRuntimeCapabilitiesNormalizer)
	}
	if capabilityRule.Action != "full_system_runtime_capabilities" || !capabilityRule.ExplicitOnly {
		t.Fatalf("capability normalizer = %#v, want explicit-only full_system_runtime_capabilities action", capabilityRule)
	}

	implicit, err := selectRules(rules, nil)
	if err != nil {
		t.Fatalf("select implicit baseline rules: %v", err)
	}
	for _, rule := range implicit {
		if rule.Name == fullSystemRuntimeCapabilitiesNormalizer {
			t.Fatal("full-system runtime capabilities must not be baseline-selected when normalizers are omitted")
		}
	}

	explicit, err := selectRules(rules, []string{fullSystemRuntimeCapabilitiesNormalizer})
	if err != nil {
		t.Fatalf("select explicit capability normalizer: %v", err)
	}
	if !reflect.DeepEqual(explicit, []Rule{capabilityRule}) {
		t.Fatalf("explicit capability selection = %#v, want %#v", explicit, []Rule{capabilityRule})
	}
}

func TestPublicCorpusSelectsRuntimeCapabilitiesOnlyForFullSystemInfo(t *testing.T) {
	scenarios, err := loadScenarios("../../corpora/public.yaml")
	if err != nil {
		t.Fatalf("load committed public corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed public corpus scenarios = %d, want one", len(scenarios))
	}

	wantSelected := map[string]string{
		"authenticated full system information Pascal response":    "application/json",
		"authenticated full system information CamelCase response": "application/json; profile=\"CamelCase\"",
	}
	selected := make(map[string]string, len(wantSelected))
	for _, step := range scenarios[0].Steps {
		if !containsNormalizer(step.selectedRules(), fullSystemRuntimeCapabilitiesNormalizer) {
			continue
		}
		wantAccept, ok := wantSelected[step.Name]
		if !ok {
			t.Fatalf("runtime capability normalizer selected by non-full-info step %q", step.Name)
		}
		if step.Method != http.MethodGet || !strings.EqualFold(step.Path, "/System/Info") || step.OperationID != "GetSystemInfo" || step.AuthRole != "admin" {
			t.Fatalf("runtime capability normalizer step = %#v, want authenticated full system info GET", step)
		}
		if step.Headers["Accept"] != wantAccept {
			t.Fatalf("runtime capability normalizer step %q Accept = %q, want %q", step.Name, step.Headers["Accept"], wantAccept)
		}
		if len(step.Normalize) != 0 {
			t.Fatalf("runtime capability normalizer step %q uses legacy normalize field", step.Name)
		}
		selected[step.Name] = step.Headers["Accept"]
	}
	if !reflect.DeepEqual(selected, wantSelected) {
		t.Fatalf("runtime capability normalizer selected steps = %#v, want %#v", selected, wantSelected)
	}
}

func containsNormalizer(normalizers []string, want string) bool {
	for _, normalizer := range normalizers {
		if normalizer == want {
			return true
		}
	}
	return false
}
