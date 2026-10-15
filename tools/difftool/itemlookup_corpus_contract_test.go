// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// itemLookupCorpusPaths returns the two ItemLookup corpus file paths relative
// to the difftool working directory. It returns a fresh slice so no package-
// level mutable state is shared between tests.
func itemLookupCorpusPaths(t *testing.T) []string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root := filepath.Dir(filepath.Dir(workingDirectory))
	return []string{
		filepath.Join(root, "corpora", "itemlookup.yaml"),
		filepath.Join(root, "corpora", "itemlookup-download.yaml"),
	}
}

// itemLookupExpectedOperationIDs is the 15-operation matrix from the
// ItemLookup and RemoteImage controllers in the pinned upstream: the 14
// spec-defined operations plus GetItem (the post-Apply and post-Download
// item projection). It returns a fresh slice so no package-level mutable
// state is shared between tests.
func itemLookupExpectedOperationIDs() []string {
	return []string{
		"GetMovieRemoteSearchResults",
		"GetSeriesRemoteSearchResults",
		"GetMusicAlbumRemoteSearchResults",
		"GetMusicArtistRemoteSearchResults",
		"GetTrailerRemoteSearchResults",
		"GetMusicVideoRemoteSearchResults",
		"GetBoxSetRemoteSearchResults",
		"GetPersonRemoteSearchResults",
		"GetBookRemoteSearchResults",
		"GetExternalIdInfos",
		"GetRemoteImageProviders",
		"ApplySearchCriteria",
		"GetItem",
		"GetRemoteImages",
		"DownloadRemoteImage",
	}
}

// itemLookupElevatedOperationIDs is the subset that requires admin elevation
// per the pinned upstream controller (RequiresElevation policy).
func itemLookupElevatedOperationIDs() map[string]bool {
	return map[string]bool{
		"GetPersonRemoteSearchResults": true,
		"GetExternalIdInfos":           true,
		"ApplySearchCriteria":          true,
		"DownloadRemoteImage":          true,
	}
}

// itemLookupNoSearcherOperationIDs is the subset that has no ported searcher
// and uses a pinned unmatchable name.
func itemLookupNoSearcherOperationIDs() map[string]bool {
	return map[string]bool{
		"GetTrailerRemoteSearchResults":    true,
		"GetMusicVideoRemoteSearchResults": true,
		"GetBoxSetRemoteSearchResults":     true,
		"GetPersonRemoteSearchResults":     true,
		"GetBookRemoteSearchResults":       true,
	}
}

// TestItemLookupCorpusDefinesFullOperationContract verifies the two
// ItemLookup corpus files. Each file must parse as goldenrec-scenario/v1.
// The files must cover all operation IDs in the differential matrix.
// The step counts must be 16 main steps and 3 download-lane steps.
func TestItemLookupCorpusDefinesFullOperationContract(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	expected := itemLookupExpectedOperationIDs()
	covered := map[string]bool{}
	stepCount := map[string]int{}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("corpus file not found: %s: %v", path, err)
		}
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		for _, scenario := range scenarios {
			if scenario.Format != scenarioFormat {
				t.Fatalf("%s: format must be %q, got %q", path, scenarioFormat, scenario.Format)
			}
			stepCount[path] += len(scenario.Steps)
			for _, step := range scenario.Steps {
				if step.OperationID == "" {
					t.Fatalf("%s: step %q has no operation_id", path, step.Name)
				}
				covered[step.OperationID] = true
			}
		}
	}
	for _, op := range expected {
		if !covered[op] {
			t.Errorf("operation_id %q is not covered by any ItemLookup corpus step", op)
		}
	}
	if len(covered) != len(expected) {
		t.Errorf("covered %d operation IDs, expected %d", len(covered), len(expected))
	}
	// Verify the required step counts per file.
	const expectedMainSteps = 16
	const expectedDownloadSteps = 3
	for _, path := range paths {
		if strings.HasSuffix(path, "itemlookup.yaml") {
			if stepCount[path] != expectedMainSteps {
				t.Errorf("%s: %d steps, expected %d main steps", path, stepCount[path], expectedMainSteps)
			}
		}
		if strings.HasSuffix(path, "itemlookup-download.yaml") {
			if stepCount[path] != expectedDownloadSteps {
				t.Errorf("%s: %d steps, expected %d download-lane steps", path, stepCount[path], expectedDownloadSteps)
			}
		}
	}
}

// TestItemLookupCorpusElevationRoutesUseAdmin verifies that every
// RequiresElevation route in the corpus uses auth_role: admin.
func TestItemLookupCorpusElevationRoutesUseAdmin(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	elevated := itemLookupElevatedOperationIDs()
	for _, path := range paths {
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		for _, scenario := range scenarios {
			for _, step := range scenario.Steps {
				if elevated[step.OperationID] && step.AuthRole != "admin" {
					t.Errorf("%s: step %q (%s) requires elevation but has auth_role %q, want %q",
						path, step.Name, step.OperationID, step.AuthRole, "admin")
				}
			}
		}
	}
}

// TestItemLookupCorpusNoSearcherKindsUseUnmatchableName verifies that the five
// no-searcher kinds use the pinned unmatchable name in their SearchInfo body.
func TestItemLookupCorpusNoSearcherKindsUseUnmatchableName(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	noSearcher := itemLookupNoSearcherOperationIDs()
	const unmatchableName = "zzzzzzzz-nonexistent-lookup-item"
	for _, path := range paths {
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		for _, scenario := range scenarios {
			for _, step := range scenario.Steps {
				if !noSearcher[step.OperationID] {
					continue
				}
				body, ok := step.Body.(map[string]any)
				if !ok {
					t.Fatalf("%s: step %q body is not an object", path, step.Name)
				}
				searchInfo, ok := body["SearchInfo"].(map[string]any)
				if !ok {
					t.Fatalf("%s: step %q has no SearchInfo", path, step.Name)
				}
				name, ok := searchInfo["Name"].(string)
				if !ok {
					t.Fatalf("%s: step %q SearchInfo.Name is not a string", path, step.Name)
				}
				if name != unmatchableName {
					t.Errorf("%s: step %q (%s) uses name %q, want pinned unmatchable %q",
						path, step.Name, step.OperationID, name, unmatchableName)
				}
			}
		}
	}
}

// TestItemLookupCorpusDownloadStepsOnlyInDownloadFile verifies that Download
// steps exist only in itemlookup-download.yaml and only with cassette
// provider URLs.
func TestItemLookupCorpusDownloadStepsOnlyInDownloadFile(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	for _, path := range paths {
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		isDownloadFile := strings.HasSuffix(path, "itemlookup-download.yaml")
		for _, scenario := range scenarios {
			for _, step := range scenario.Steps {
				isDownload := step.OperationID == "DownloadRemoteImage"
				if isDownload && !isDownloadFile {
					t.Errorf("%s: Download step %q must be in itemlookup-download.yaml", path, step.Name)
				}
				if isDownloadFile && isDownload {
					imageURL, ok := step.Params["imageUrl"].(string)
					if !ok {
						t.Fatalf("%s: Download step %q has no imageUrl param", path, step.Name)
					}
					if !strings.HasPrefix(imageURL, "https://image.tmdb.org/") {
						t.Errorf("%s: Download step %q imageUrl %q is not a cassette provider URL", path, step.Name, imageURL)
					}
				}
			}
		}
	}
}

// TestItemLookupCorpusExternalIdInfosRuleCitesDivergence verifies that the
// external-id-infos normalizer rule references its divergence ID.
func TestItemLookupCorpusExternalIdInfosRuleCitesDivergence(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	for _, rule := range rules {
		if rule.Name == "external-id-infos" {
			if rule.DivergenceID != "D-28" {
				t.Errorf("external-id-infos divergence_id = %q, want D-28", rule.DivergenceID)
			}
			if !rule.ExplicitOnly {
				t.Error("external-id-infos must be explicit_only")
			}
			return
		}
	}
	t.Fatal("external-id-infos rule not found in loaded rules")
}

// TestItemLookupCorpusIncludesDisabledProviders verifies that every
// RemoteSearch step sets IncludeDisabledProviders: true in the SearchInfo
// body. This is required so stock's TMDb providers return results in the
// provider-disabled seeded library (upstream ProviderManager.cs:941).
func TestItemLookupCorpusIncludesDisabledProviders(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	searchOps := map[string]bool{
		"GetMovieRemoteSearchResults":       true,
		"GetSeriesRemoteSearchResults":      true,
		"GetMusicAlbumRemoteSearchResults":  true,
		"GetMusicArtistRemoteSearchResults": true,
		"GetTrailerRemoteSearchResults":     true,
		"GetMusicVideoRemoteSearchResults":  true,
		"GetBoxSetRemoteSearchResults":      true,
		"GetPersonRemoteSearchResults":      true,
		"GetBookRemoteSearchResults":        true,
	}
	for _, path := range paths {
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		for _, scenario := range scenarios {
			for _, step := range scenario.Steps {
				if !searchOps[step.OperationID] {
					continue
				}
				body, ok := step.Body.(map[string]any)
				if !ok {
					t.Fatalf("%s: step %q body is not an object", path, step.Name)
				}
				searchInfo, ok := body["SearchInfo"].(map[string]any)
				if !ok {
					t.Fatalf("%s: step %q has no SearchInfo", path, step.Name)
				}
				disabled, ok := searchInfo["IncludeDisabledProviders"].(bool)
				if !ok || !disabled {
					t.Errorf("%s: step %q (%s) must set IncludeDisabledProviders: true",
						path, step.Name, step.OperationID)
				}
			}
		}
	}
}

// TestItemLookupCorpusRequiredStepsPresent verifies that the corpus
// contains all required stateful steps from the spec order. The 16 main
// steps are in itemlookup.yaml. The 3 download-lane steps are in
// itemlookup-download.yaml. Each step is identified by its operation_id
// and a substring of its name so a missing step fails the test.
func TestItemLookupCorpusRequiredStepsPresent(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	type requiredStep struct {
		fileSuffix string
		nameSubstr string
		opID       string
	}
	required := []requiredStep{
		// Main corpus: 16 steps.
		{"itemlookup.yaml", "RemoteSearch Movie (populated)", "GetMovieRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch Series (populated)", "GetSeriesRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch MusicAlbum (populated)", "GetMusicAlbumRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch MusicArtist (populated)", "GetMusicArtistRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch Trailer (empty)", "GetTrailerRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch MusicVideo (empty)", "GetMusicVideoRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch BoxSet (empty)", "GetBoxSetRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch Person (empty, elevated)", "GetPersonRemoteSearchResults"},
		{"itemlookup.yaml", "RemoteSearch Book (empty)", "GetBookRemoteSearchResults"},
		{"itemlookup.yaml", "ExternalIdInfos pre-Apply", "GetExternalIdInfos"},
		{"itemlookup.yaml", "RemoteImages Providers", "GetRemoteImageProviders"},
		{"itemlookup.yaml", "Apply RemoteSearchResult (expect 204)", "ApplySearchCriteria"},
		{"itemlookup.yaml", "GET item post-Apply projection", "GetItem"},
		{"itemlookup.yaml", "RemoteImages unfiltered (populated post-Apply)", "GetRemoteImages"},
		{"itemlookup.yaml", "RemoteImages paginated (TotalRecordCount-before-pagination probe)", "GetRemoteImages"},
		{"itemlookup.yaml", "ExternalIdInfos post-Apply", "GetExternalIdInfos"},
		// Download lane: 3 steps.
		{"itemlookup-download.yaml", "RemoteImages listing (pre-Download)", "GetRemoteImages"},
		{"itemlookup-download.yaml", "Download remote image (expect 204)", "DownloadRemoteImage"},
		{"itemlookup-download.yaml", "GET item post-Download projection", "GetItem"},
	}
	for _, req := range required {
		found := false
		for _, path := range paths {
			if !strings.HasSuffix(path, req.fileSuffix) {
				continue
			}
			scenarios, err := loadScenarios(path)
			if err != nil {
				t.Fatalf("load corpus %s: %v", path, err)
			}
			for _, scenario := range scenarios {
				for _, step := range scenario.Steps {
					if step.OperationID == req.opID && strings.Contains(step.Name, req.nameSubstr) {
						found = true
					}
				}
			}
		}
		if !found {
			t.Errorf("required step not found: file=*%s name~%q op_id=%q",
				req.fileSuffix, req.nameSubstr, req.opID)
		}
	}
}

// TestItemLookupCorpusApplyBodyComplete verifies that the Apply step
// request body contains all five required RemoteSearchResult fields.
// The spec defines the Apply body as a pinned literal with Name,
// ProductionYear, ProviderIds, ImageUrl, and SearchProviderName.
func TestItemLookupCorpusApplyBodyComplete(t *testing.T) {
	paths := itemLookupCorpusPaths(t)
	requiredFields := []string{"Name", "ProductionYear", "ProviderIds", "ImageUrl", "SearchProviderName"}
	for _, path := range paths {
		if !strings.HasSuffix(path, "itemlookup.yaml") {
			continue
		}
		scenarios, err := loadScenarios(path)
		if err != nil {
			t.Fatalf("load corpus %s: %v", path, err)
		}
		for _, scenario := range scenarios {
			for _, step := range scenario.Steps {
				if step.OperationID != "ApplySearchCriteria" {
					continue
				}
				body, ok := step.Body.(map[string]any)
				if !ok {
					t.Fatalf("%s: Apply step body is not a map", path)
				}
				for _, field := range requiredFields {
					if _, present := body[field]; !present {
						t.Errorf("%s: Apply body missing required field %q", path, field)
					}
				}
			}
		}
	}
}
