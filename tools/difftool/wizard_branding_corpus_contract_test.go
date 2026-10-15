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

var wizardBrandingConfiguration = map[string]any{
	"ServerName":                "Wizard Branding Differential",
	"UICulture":                 "de-DE",
	"MetadataCountryCode":       "DE",
	"PreferredMetadataLanguage": "de",
}

var wizardBrandingTransportNormalizers = []string{
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

func TestWizardBrandingCorpusRequiresExplicitExactStartupConfigurationRead(t *testing.T) {
	update, read := wizardBrandingConfigurationSteps(t)

	legacyRead := read
	legacyRead.Normalizers = nil
	legacyRead.Normalize = nil
	if _, _, err := requireExactWizardBrandingConfigurationRead([]Step{update, legacyRead}); err == nil {
		t.Fatal("legacy implicit-baseline startup configuration read passed the exact branding contract")
	}
	if _, _, err := requireExactWizardBrandingConfigurationRead([]Step{read, update}); err == nil {
		t.Fatal("legacy startup configuration read-before-update order passed the deterministic branding contract")
	}
}

func TestWizardBrandingCorpusUpdateReadReplaysExactConfiguredState(t *testing.T) {
	update, read := wizardBrandingConfigurationSteps(t)

	left := newWizardBrandingReplayHost(t, map[string]any{
		"ServerName": "left bootstrap name", "UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en",
	}, false)
	right := newWizardBrandingReplayHost(t, map[string]any{
		"ServerName": "right bootstrap name", "UICulture": "fr-FR", "MetadataCountryCode": "FR", "PreferredMetadataLanguage": "fr",
	}, false)

	result, err := runWizardBrandingConfigurationReplay(t, update, read, left, right)
	if err != nil {
		t.Fatalf("startup configuration update followed by read must converge distinct bootstrap hosts: %v", err)
	}
	for _, step := range result.Steps {
		if step.Status != "pass" {
			t.Fatalf("configured startup state step = %#v, want pass", step)
		}
	}
	for _, host := range []*wizardBrandingReplayHost{left, right} {
		if got, want := host.configurationMethods(), []string{http.MethodPost, http.MethodGet}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s startup configuration request order = %#v, want %#v", host.server.URL, got, want)
		}
	}
}

func TestWizardBrandingCorpusStartupReadRejectsServerNameCorruption(t *testing.T) {
	update, read := wizardBrandingConfigurationSteps(t)

	left := newWizardBrandingReplayHost(t, map[string]any{"ServerName": "left bootstrap name"}, false)
	right := newWizardBrandingReplayHost(t, map[string]any{"ServerName": "right bootstrap name"}, true)

	result, err := runWizardBrandingConfigurationReplay(t, update, read, left, right)
	if !errors.Is(err, errDifferences) {
		t.Fatalf("server that corrupts ServerName after the corpus update = %v, want comparison difference", err)
	}
	if len(result.Steps) != 2 || result.Steps[0].Status != "pass" || result.Steps[1].Status != "fail" {
		t.Fatalf("ServerName corruption must pass the update but fail its exact read: %#v", result.Steps)
	}
}

func wizardBrandingConfigurationSteps(t *testing.T) (Step, Step) {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "wizard-branding.yaml"))
	if err != nil {
		t.Fatalf("load committed wizard-branding corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed wizard-branding corpus scenarios = %d, want one", len(scenarios))
	}
	update, read, err := requireExactWizardBrandingConfigurationRead(scenarios[0].Steps)
	if err != nil {
		t.Fatal(err)
	}
	return update, read
}

func requireExactWizardBrandingConfigurationRead(steps []Step) (Step, Step, error) {
	updateIndex, readIndex := -1, -1
	var update, read Step
	for index, step := range steps {
		switch step.Name {
		case "update startup configuration":
			updateIndex = index
			update = step
		case "read updated startup configuration":
			readIndex = index
			read = step
		}
	}
	if updateIndex == -1 || readIndex == -1 {
		return Step{}, Step{}, errors.New("wizard-branding startup configuration update/read steps are missing")
	}
	if updateIndex >= readIndex {
		return Step{}, Step{}, errors.New("wizard-branding startup configuration update must precede its matching read")
	}
	if update.Method != http.MethodPost || !strings.EqualFold(update.Path, "/Startup/Configuration") || update.OperationID != "UpdateInitialConfiguration" {
		return Step{}, Step{}, errors.New("wizard-branding startup configuration update is missing or targets the wrong operation")
	}
	if !reflect.DeepEqual(update.Body, wizardBrandingConfiguration) {
		return Step{}, Step{}, errors.New("wizard-branding startup configuration update body does not retain the exact branding fields")
	}
	if read.Method != http.MethodGet || !strings.EqualFold(read.Path, "/Startup/Configuration") || read.OperationID != "GetStartupConfiguration" {
		return Step{}, Step{}, errors.New("wizard-branding updated startup configuration read is missing or targets the wrong operation")
	}
	if len(read.Normalize) != 0 || !reflect.DeepEqual(read.Normalizers, wizardBrandingTransportNormalizers) {
		return Step{}, Step{}, errors.New("wizard-branding updated startup configuration read must explicitly retain only transport volatile normalizers")
	}
	for _, name := range read.Normalizers {
		if name == "server-environment-fields" {
			return Step{}, Step{}, errors.New("wizard-branding updated startup configuration read must compare ServerName exactly")
		}
	}
	return update, read, nil
}

func runWizardBrandingConfigurationReplay(t *testing.T, update, read Step, left, right *wizardBrandingReplayHost) (Result, error) {
	t.Helper()
	directory := t.TempDir()
	serializedSteps := make([]map[string]any, 0, 2)
	for _, step := range []Step{update, read} {
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
		"name":   "committed wizard branding configuration contract",
		"steps":  serializedSteps,
	})
	if err != nil {
		t.Fatalf("marshal wizard branding configuration steps: %v", err)
	}
	corpus := filepath.Join(directory, "wizard-branding-configuration.yaml")
	writeFile(t, corpus, string(contents))
	return Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
}

type wizardBrandingReplayHost struct {
	mu                sync.Mutex
	configuration     map[string]any
	corruptServerName bool
	methods           []string
	server            *httptest.Server
}

func newWizardBrandingReplayHost(t *testing.T, initial map[string]any, corruptServerName bool) *wizardBrandingReplayHost {
	t.Helper()
	host := &wizardBrandingReplayHost{configuration: cloneWizardBrandingConfiguration(t, initial), corruptServerName: corruptServerName}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Items":[]}`)
		case strings.EqualFold(r.URL.Path, "/Startup/Configuration"):
			host.serveConfiguration(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (host *wizardBrandingReplayHost) serveConfiguration(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()
	host.methods = append(host.methods, r.Method)
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read startup configuration update: %v", err)
			http.Error(w, "read startup configuration update", http.StatusInternalServerError)
			return
		}
		var configuration map[string]any
		if err := json.Unmarshal(body, &configuration); err != nil {
			t.Errorf("decode startup configuration update: %v", err)
			http.Error(w, "decode startup configuration update", http.StatusBadRequest)
			return
		}
		host.configuration = configuration
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		configuration := cloneWizardBrandingConfiguration(t, host.configuration)
		if host.corruptServerName {
			configuration["ServerName"] = "corrupted-server-name"
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(configuration); err != nil {
			t.Errorf("encode startup configuration response: %v", err)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (host *wizardBrandingReplayHost) configurationMethods() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]string(nil), host.methods...)
}

func cloneWizardBrandingConfiguration(t *testing.T, configuration map[string]any) map[string]any {
	contents, err := json.Marshal(configuration)
	if err != nil {
		if t != nil {
			t.Fatalf("marshal startup configuration fixture: %v", err)
		}
		panic(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(contents, &clone); err != nil {
		if t != nil {
			t.Fatalf("unmarshal startup configuration fixture: %v", err)
		}
		panic(err)
	}
	return clone
}
