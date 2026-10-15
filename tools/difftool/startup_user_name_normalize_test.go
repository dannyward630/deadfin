// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

const startupUserNameNormalizer = "startup-user-name"

var startupUserTransportNormalizers = []string{
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

func TestNormalizerStartupUserNameOnlyReplacesRootString(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "startup_user_name"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal root name preserves nested and unrelated names",
			body: `{
				"Name":"stock bootstrap user",
				"NameSuffix":"must remain exact",
				"Nested":{"Name":"nested DTO name"},
				"Users":[{"name":"array DTO name"}]
			}`,
			want: map[string]any{
				"Name":       "<startup-user-name>",
				"NameSuffix": "must remain exact",
				"Nested":     map[string]any{"Name": "nested DTO name"},
				"Users":      []any{map[string]any{"name": "array DTO name"}},
			},
		},
		{
			name: "Camel root name preserves nested and unrelated names",
			body: `{
				"name":"deadfin bootstrap user",
				"rename":"must remain exact",
				"Nested":{"name":"nested DTO name"},
				"Users":[{"Name":"array DTO name"}]
			}`,
			want: map[string]any{
				"name":   "<startup-user-name>",
				"rename": "must remain exact",
				"Nested": map[string]any{"name": "nested DTO name"},
				"Users":  []any{map[string]any{"Name": "array DTO name"}},
			},
		},
		{
			name: "non string root names remain exact",
			body: `{
				"NullName":null,
				"Name":null,
				"name":false,
				"NAME":42,
				"NaMe":{"Name":"object nested name"},
				"nAmE":["array entry",{"Name":"array nested name"}]
			}`,
			want: map[string]any{
				"NullName": nil,
				"Name":     nil,
				"name":     false,
				"NAME":     json.Number("42"),
				"NaMe":     map[string]any{"Name": "object nested name"},
				"nAmE":     []any{"array entry", map[string]any{"Name": "array nested name"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerStartupUserNameRequiresExplicitRuleSelection(t *testing.T) {
	response := Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"Name":"bootstrap user","Nested":{"Name":"nested name"}}`),
	}

	normalized, err := newNormalizer(nil).normalize(response, nil)
	if err != nil {
		t.Fatalf("normalize without the startup-user rule: %v", err)
	}
	if !reflect.DeepEqual(normalized.Body, map[string]any{
		"Name":   "bootstrap user",
		"Nested": map[string]any{"Name": "nested name"},
	}) {
		t.Fatalf("unselected startup-user rule changed response: %#v", normalized.Body)
	}
}

func TestStartupUserNameRuleIsExplicitOnly(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load normalizer rules: %v", err)
	}

	var startupRule Rule
	found := false
	for _, rule := range rules {
		if rule.Name == startupUserNameNormalizer {
			startupRule = rule
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("normalizer rule %q is missing", startupUserNameNormalizer)
	}
	if startupRule.Action != "startup_user_name" || !startupRule.ExplicitOnly {
		t.Fatalf("startup-user-name rule = %#v, want explicit-only startup_user_name action", startupRule)
	}

	implicit, err := selectRules(rules, nil)
	if err != nil {
		t.Fatalf("select implicit baseline rules: %v", err)
	}
	for _, rule := range implicit {
		if rule.Name == startupUserNameNormalizer {
			t.Fatal("startup-user-name must not be baseline-selected when normalizers are omitted")
		}
	}

	explicit, err := selectRules(rules, []string{startupUserNameNormalizer})
	if err != nil {
		t.Fatalf("select explicit startup-user-name rule: %v", err)
	}
	if !reflect.DeepEqual(explicit, []Rule{startupRule}) {
		t.Fatalf("explicit startup-user-name selection = %#v, want %#v", explicit, []Rule{startupRule})
	}
}

func TestStartupUserNameExplicitOnlyKeepsRuleLookupAndDuplicateGuards(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load normalizer rules: %v", err)
	}
	if _, err := selectRules(rules, []string{startupUserNameNormalizer, "not-a-normalizer"}); err == nil || !strings.Contains(err.Error(), "unknown normalizer") {
		t.Fatalf("explicit-only selection must still reject unknown names, got %v", err)
	}

	directory := t.TempDir()
	rule := "version: difftool-normalize/v1\nrules:\n  - name: duplicate\n    action: sort_json_keys\n    reason: duplicate guard test\n"
	writeFile(t, filepath.Join(directory, "one.yaml"), rule)
	writeFile(t, filepath.Join(directory, "two.yaml"), rule)
	if _, err := loadRules(directory); err == nil || !strings.Contains(err.Error(), "duplicate normalizer rule name") {
		t.Fatalf("explicit-only metadata must keep duplicate-name rejection, got %v", err)
	}
}

func TestWizardBrandingCorpusStartupUserNormalizerContract(t *testing.T) {
	steps := loadWizardBrandingStartupUserSteps(t)
	if _, _, err := requireWizardBrandingStartupUserContract(steps); err != nil {
		t.Fatal(err)
	}
}

func TestWizardBrandingCorpusStartupUserContractRejectsMissingOrBroadSelection(t *testing.T) {
	steps := loadWizardBrandingStartupUserSteps(t)

	t.Run("missing initial read selection", func(t *testing.T) {
		mutated := cloneWizardBrandingSteps(t, steps)
		for index := range mutated {
			if mutated[index].Name == "read initialized startup user" {
				mutated[index].Normalizers = nil
				break
			}
		}
		if _, _, err := requireWizardBrandingStartupUserContract(mutated); err == nil {
			t.Fatal("startup-user read without explicit selection passed corpus contract")
		}
	})

	t.Run("post mutation read mask", func(t *testing.T) {
		mutated := cloneWizardBrandingSteps(t, steps)
		for index := range mutated {
			if mutated[index].Name == "configure startup user" {
				mutated[index].Normalizers = append([]string(nil), startupUserTransportNormalizers...)
				mutated[index].Normalizers = append(mutated[index].Normalizers, startupUserNameNormalizer)
				break
			}
		}
		if _, _, err := requireWizardBrandingStartupUserContract(mutated); err == nil {
			t.Fatal("post-mutation startup-user selection passed corpus contract")
		}
	})

	t.Run("configured username differs", func(t *testing.T) {
		mutated := cloneWizardBrandingSteps(t, steps)
		for index := range mutated {
			if mutated[index].Name == "configure startup user" {
				body, ok := mutated[index].Body.(map[string]any)
				if !ok {
					t.Fatalf("configure startup-user body = %#v, want object", mutated[index].Body)
				}
				body["Name"] = "not Wizard Diff Admin"
				break
			}
		}
		if _, _, err := requireWizardBrandingStartupUserContract(mutated); err == nil {
			t.Fatal("changed configured startup username passed corpus contract")
		}
	})
}

func TestWizardBrandingStartupUserReplayNormalizesBootstrapNamesButRejectsCorruptedConfiguredName(t *testing.T) {
	initialReads, configure, err := requireWizardBrandingStartupUserContract(loadWizardBrandingStartupUserSteps(t))
	if err != nil {
		t.Fatal(err)
	}

	verifyConfiguredUser := Step{
		Name:        "verify configured startup user",
		Method:      http.MethodGet,
		Path:        "/Startup/User",
		OperationID: "GetFirstUser",
		Normalizers: append([]string(nil), startupUserTransportNormalizers...),
	}
	corpus := writeStartupUserReplayCorpus(t, append(append([]Step(nil), initialReads...), configure, verifyConfiguredUser))
	left := newStartupUserReplayHost(t, "left environment user", false)
	right := newStartupUserReplayHost(t, "right environment user", true)

	result, runErr := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(t.TempDir(), "results"),
		IssuesDir:  filepath.Join(t.TempDir(), "issues"),
	})
	if !errors.Is(runErr, errDifferences) {
		t.Fatalf("corrupted configured startup username replay = %v, want comparison difference", runErr)
	}
	if got, want := startupUserReplayStatuses(result), []string{"pass", "pass", "pass", "fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("startup-user replay statuses = %#v, want %#v", got, want)
	}
	for _, host := range []*startupUserReplayHost{left, right} {
		if got := host.configuredName(); got != "Wizard Diff Admin" {
			t.Fatalf("%s configured startup username = %q, want exact corpus value", host.server.URL, got)
		}
	}
}

func loadWizardBrandingStartupUserSteps(t *testing.T) []Step {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "wizard-branding.yaml"))
	if err != nil {
		t.Fatalf("load committed wizard-branding corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed wizard-branding corpus scenarios = %d, want one", len(scenarios))
	}
	return scenarios[0].Steps
}

func requireWizardBrandingStartupUserContract(steps []Step) ([]Step, Step, error) {
	wantReads := []struct {
		name        string
		path        string
		operationID string
	}{
		{"read initialized startup user", "/Startup/User", "GetFirstUser"},
		{"read initialized first-user alias", "/Startup/FirstUser", "GetFirstUser_2"},
	}

	indices := make([]int, len(wantReads))
	reads := make([]Step, len(wantReads))
	for wantedIndex, wanted := range wantReads {
		indices[wantedIndex] = -1
		for index, step := range steps {
			if step.Name != wanted.name {
				continue
			}
			indices[wantedIndex] = index
			reads[wantedIndex] = step
			break
		}
		if indices[wantedIndex] == -1 {
			return nil, Step{}, errors.New("wizard-branding initial startup-user read is missing")
		}
		step := reads[wantedIndex]
		if step.Method != http.MethodGet || !strings.EqualFold(step.Path, wanted.path) || step.OperationID != wanted.operationID {
			return nil, Step{}, errors.New("wizard-branding initial startup-user read targets the wrong operation")
		}
		if len(step.Normalize) != 0 || !sameStringSet(step.Normalizers, append(append([]string(nil), startupUserTransportNormalizers...), startupUserNameNormalizer)) {
			return nil, Step{}, errors.New("wizard-branding initial startup-user reads must explicitly select only transport volatiles and startup-user-name")
		}
	}

	configureIndex := -1
	var configure Step
	for index, step := range steps {
		if step.Name == "configure startup user" {
			configureIndex = index
			configure = step
			break
		}
	}
	if configureIndex == -1 || indices[0] >= configureIndex || indices[1] >= configureIndex {
		return nil, Step{}, errors.New("wizard-branding startup-user reads must precede the configure startup user mutation")
	}
	if configure.Method != http.MethodPost || !strings.EqualFold(configure.Path, "/Startup/User") || configure.OperationID != "UpdateStartupUser" {
		return nil, Step{}, errors.New("wizard-branding configure startup user step targets the wrong operation")
	}
	if !reflect.DeepEqual(configure.Body, map[string]any{"Name": "Wizard Diff Admin", "Password": "wizard-difftool-password"}) {
		return nil, Step{}, errors.New("wizard-branding configure startup user must retain the exact Wizard Diff Admin request")
	}
	if len(configure.Normalize) != 0 || len(configure.Normalizers) != 0 {
		return nil, Step{}, errors.New("wizard-branding configure startup user must not select response normalizers")
	}

	for index, step := range steps {
		for _, name := range step.selectedRules() {
			if name != startupUserNameNormalizer {
				continue
			}
			if index != indices[0] && index != indices[1] {
				return nil, Step{}, errors.New("wizard-branding startup-user-name normalizer may appear only on initial reads")
			}
		}
	}
	return reads, configure, nil
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(got))
	for _, value := range got {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func cloneWizardBrandingSteps(t *testing.T, steps []Step) []Step {
	t.Helper()
	contents, err := json.Marshal(steps)
	if err != nil {
		t.Fatalf("marshal wizard-branding steps: %v", err)
	}
	var clone []Step
	if err := json.Unmarshal(contents, &clone); err != nil {
		t.Fatalf("unmarshal wizard-branding steps: %v", err)
	}
	return clone
}

func writeStartupUserReplayCorpus(t *testing.T, steps []Step) string {
	t.Helper()
	serializedSteps := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		serialized := map[string]any{
			"name":         step.Name,
			"method":       step.Method,
			"path":         step.Path,
			"operation_id": step.OperationID,
		}
		if step.Body != nil {
			serialized["body"] = step.Body
		}
		if len(step.Normalizers) != 0 {
			serialized["normalizers"] = step.Normalizers
		}
		serializedSteps = append(serializedSteps, serialized)
	}
	contents, err := yaml.Marshal(map[string]any{
		"format": scenarioFormat,
		"name":   "startup-user-name normalization contract",
		"steps":  serializedSteps,
	})
	if err != nil {
		t.Fatalf("marshal startup-user replay corpus: %v", err)
	}
	corpus := filepath.Join(t.TempDir(), "startup-user-name.yaml")
	writeFile(t, corpus, string(contents))
	return corpus
}

func startupUserReplayStatuses(result Result) []string {
	statuses := make([]string, len(result.Steps))
	for index, step := range result.Steps {
		statuses[index] = step.Status
	}
	return statuses
}

type startupUserReplayHost struct {
	mu                    sync.Mutex
	name                  string
	configured            bool
	corruptConfiguredName bool
	server                *httptest.Server
}

func newStartupUserReplayHost(t *testing.T, initialName string, corruptConfiguredName bool) *startupUserReplayHost {
	t.Helper()
	host := &startupUserReplayHost{name: initialName, corruptConfiguredName: corruptConfiguredName}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.EqualFold(r.URL.Path, "/Startup/User"), strings.EqualFold(r.URL.Path, "/Startup/FirstUser"):
			host.serveStartupUser(t, w, r)
		case r.URL.Path == "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *startupUserReplayHost) serveStartupUser(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		name := host.name
		if host.configured && host.corruptConfiguredName {
			name = "corrupted configured name"
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"Name":   name,
			"Nested": map[string]any{"Name": "must remain exact"},
		}); err != nil {
			t.Errorf("encode startup user: %v", err)
		}
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode configure startup user: %v", err)
			http.Error(w, "decode configure startup user", http.StatusBadRequest)
			return
		}
		name, ok := body["Name"].(string)
		if !ok {
			t.Errorf("configure startup user Name = %#v, want string", body["Name"])
			http.Error(w, "missing startup user name", http.StatusBadRequest)
			return
		}
		host.name = name
		host.configured = true
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (host *startupUserReplayHost) configuredName() string {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.name
}
