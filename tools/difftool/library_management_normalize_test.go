// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// virtualFolderBody builds a VirtualFolders response array with the given
// folder entries (already-encoded JSON object fragments).
func virtualFolderBody(folders ...string) string {
	out := "["
	for i, f := range folders {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out + "]"
}

// normalizeLibrary runs the named library actions against a VirtualFolders
// response body and returns the normalized response.
func normalizeLibrary(t *testing.T, body string, actions ...string) normalizedResponse {
	t.Helper()
	rules := make([]Rule, 0, len(actions))
	for _, action := range actions {
		rules = append(rules, Rule{Name: action, Action: action})
	}
	result, err := newNormalizer(rules).normalize(Response{Status: 200, Body: []byte(body)}, nil)
	if err != nil {
		t.Fatalf("normalize VirtualFolders response: %v", err)
	}
	return result
}

// TestLibraryFolderIDsNormalizesOnlyStringItemId proves the folder-ids
func TestLibraryFolderIDsNormalizesOnlyStringItemId(t *testing.T) {
	for _, test := range []struct {
		name    string
		idKey   string
		nameKey string
		ctKey   string
		body    string
	}{
		{
			name:    "Pascal ItemId",
			idKey:   "ItemId",
			nameKey: "Name",
			ctKey:   "CollectionType",
			body:    virtualFolderBody(`{"Name":"Cinema","ItemId":"aabbccdd","CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":false}}`),
		},
		{
			name:    "camel itemId",
			idKey:   "itemId",
			nameKey: "name",
			ctKey:   "collectionType",
			body:    virtualFolderBody(`{"name":"Cinema","itemId":"aabbccdd","collectionType":"movies","libraryOptions":{"enableInternetProviders":false}}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeLibrary(t, test.body, "library_folder_ids")
			folder := normalized.Body.([]any)[0].(map[string]any)
			if got := folder[test.idKey]; got != "<library-folder-id>" {
				t.Fatalf("normalized %s = %#v, want %q", test.idKey, got, "<library-folder-id>")
			}
			if got := folder[test.nameKey]; got != "Cinema" {
				t.Fatalf("%s changed: %#v, want %q", test.nameKey, got, "Cinema")
			}
			if got := folder[test.ctKey]; got != "movies" {
				t.Fatalf("%s changed: %#v, want %q", test.ctKey, got, "movies")
			}
		})
	}
}

// TestLibraryFolderIDsPreservesNonStringItemId proves a wrong-type ItemId
// (number, boolean, object, array, null) is left intact so the type mismatch
// stays comparable. Only the documented scalar (string) identity normalizes.
func TestLibraryFolderIDsPreservesNonStringItemId(t *testing.T) {
	for _, test := range []struct {
		name  string
		idRaw string
		want  any
	}{
		{name: "number", idRaw: `123`, want: json.Number("123")},
		{name: "boolean", idRaw: `true`, want: true},
		{name: "object", idRaw: `{"nested":1}`, want: map[string]any{"nested": json.Number("1")}},
		{name: "array", idRaw: `["a","b"]`, want: []any{"a", "b"}},
		{name: "null", idRaw: `null`, want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := virtualFolderBody(`{"Name":"Cinema","ItemId":` + test.idRaw + `}`)
			normalized := normalizeLibrary(t, body, "library_folder_ids")
			folder := normalized.Body.([]any)[0].(map[string]any)
			if got := folder["ItemId"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("non-string ItemId normalized to %#v, want exact %#v", got, test.want)
			}
		})
	}
}

// TestLibraryFolderIDsPreservesArrayOrder proves the folder-ids normalizer does
// not sort the VirtualFolders array, so response-order differences remain
// comparable. The previous implementation sorted by Name and erased order.
func TestLibraryFolderIDsPreservesArrayOrder(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Zeta","ItemId":"id-z"}`, `{"Name":"Alpha","ItemId":"id-a"}`),
		"library_folder_ids")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Alpha","ItemId":"id-a"}`, `{"Name":"Zeta","ItemId":"id-z"}`),
		"library_folder_ids")
	if equal, reason := equalNormalized(left, right, 0); equal || reason != "JSON differs at $[0].Name" {
		t.Fatalf("different array order must remain unequal: equal=%t reason=%q", equal, reason)
	}
}

// TestLibraryFolderIDsDoesNotMaskLibraryOptions proves the folder-ids
// normalizer leaves LibraryOptions untouched, so changed persisted-option
// values remain unequal when the options normalizer is not selected. This is
// the boundary the corpus relies on: library_folder_ids normalizes only the
// host-local ItemId scalar; LibraryOptions is the options normalizer's scope.
func TestLibraryFolderIDsDoesNotMaskLibraryOptions(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true}}`),
		"library_folder_ids")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-b","LibraryOptions":{"EnableInternetProviders":false,"SaveLocalMetadata":false}}`),
		"library_folder_ids")
	if equal, reason := equalNormalized(left, right, 0); equal || reason != "JSON differs at $[0].LibraryOptions.EnableInternetProviders" {
		t.Fatalf("changed persisted options must remain unequal with only library_folder_ids: equal=%t reason=%q", equal, reason)
	}
}

// TestLibraryFolderIDsEquatesHostLocalFolderIDs proves the documented
// host-local value — a string ItemId — normalizes so two hosts with different
// folder identities compare equal.
func TestLibraryFolderIDsEquatesHostLocalFolderIDs(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","CollectionType":"movies","Locations":[],"RefreshStatus":"Idle"}`),
		"library_folder_ids")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","CollectionType":"movies","Locations":[],"RefreshStatus":"Idle"}`),
		"library_folder_ids")
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("host-local string ItemId values must compare equal: %s", reason)
	}
}

// TestLibraryOptionsDefaultsNormalizesOnlyObject proves the options normalizer
// replaces a JSON-object LibraryOptions with a stable token in both casing
// profiles while leaving ItemId, Name, and CollectionType exact.
func TestLibraryOptionsDefaultsNormalizesOnlyObject(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		optsKey string
		nameKey string
		idKey   string
	}{
		{
			name:    "Pascal LibraryOptions",
			body:    virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":false}}`),
			optsKey: "LibraryOptions",
			nameKey: "Name",
			idKey:   "ItemId",
		},
		{
			name:    "camel libraryOptions",
			body:    virtualFolderBody(`{"name":"Cinema","itemId":"id-a","libraryOptions":{"enableInternetProviders":true}}`),
			optsKey: "libraryOptions",
			nameKey: "name",
			idKey:   "itemId",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeLibrary(t, test.body, "library_options_defaults")
			folder := normalized.Body.([]any)[0].(map[string]any)
			if got := folder[test.optsKey]; got != "<library-options>" {
				t.Fatalf("normalized %s = %#v, want %q", test.optsKey, got, "<library-options>")
			}
			if got := folder[test.nameKey]; got != "Cinema" {
				t.Fatalf("%s changed: %#v, want %q", test.nameKey, got, "Cinema")
			}
			if got := folder[test.idKey]; got != "id-a" {
				t.Fatalf("%s changed: %#v, want %q", test.idKey, got, "id-a")
			}
		})
	}
}

// TestLibraryOptionsDefaultsPreservesNonObject proves a wrong-type
// LibraryOptions (string, number, boolean, array, null) is left intact so the
// type mismatch stays comparable. Only the documented JSON-object shape
// (server compatibility defaults) normalizes.
func TestLibraryOptionsDefaultsPreservesNonObject(t *testing.T) {
	for _, test := range []struct {
		name    string
		optsRaw string
		want    any
	}{
		{name: "string", optsRaw: `"not-an-object"`, want: "not-an-object"},
		{name: "number", optsRaw: `42`, want: json.Number("42")},
		{name: "boolean", optsRaw: `true`, want: true},
		{name: "array", optsRaw: `["a","b"]`, want: []any{"a", "b"}},
		{name: "null", optsRaw: `null`, want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":` + test.optsRaw + `}`)
			normalized := normalizeLibrary(t, body, "library_options_defaults")
			folder := normalized.Body.([]any)[0].(map[string]any)
			if got := folder["LibraryOptions"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("non-object LibraryOptions normalized to %#v, want exact %#v", got, test.want)
			}
		})
	}
}

// TestLibraryOptionsDefaultsEquatesHostLocalDefaults proves the documented
// host-local value — default LibraryOptions objects that differ only in
// server compatibility defaults — normalizes so two hosts compare equal.
func TestLibraryOptionsDefaultsEquatesHostLocalDefaults(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","CollectionType":"movies","LibraryOptions":{"Enabled":true,"EnablePhotos":true,"PathInfos":[]}}`),
		"library_folder_ids", "library_options_defaults")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-b","CollectionType":"movies","LibraryOptions":{"Enabled":false,"EnablePhotos":false,"PathInfos":null}}`),
		"library_folder_ids", "library_options_defaults")
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("default LibraryOptions with differing host-local defaults must compare equal: %s", reason)
	}
}

// TestLibraryOptionsDefaultsDoesNotMaskChangedOptions proves the options
// normalizer, applied to two responses whose LibraryOptions objects carry
// different changed (persisted) values, makes them equal because it replaces
// the whole object with a token. This documents the masking boundary: the
// corpus must omit this action on persisted-options readback so the changed
// values stay comparable. The companion test
// TestLibraryFolderIDsDoesNotMaskLibraryOptions proves the boundary holds
// when the action is omitted.
func TestLibraryOptionsDefaultsMasksChangedOptionsByDesign(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":{"EnableInternetProviders":true}}`),
		"library_options_defaults")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":{"EnableInternetProviders":false}}`),
		"library_options_defaults")
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("library_options_defaults masks all object LibraryOptions by design; expected equal, got %s", reason)
	}
}

// TestLibraryOptionsDefaultsDoesNotTouchItemId proves the options normalizer
// leaves ItemId untouched, so a wrong-type or differing ItemId stays
// comparable.
func TestLibraryOptionsDefaultsDoesNotTouchItemId(t *testing.T) {
	left := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":123,"LibraryOptions":{"EnableInternetProviders":true}}`),
		"library_options_defaults")
	right := normalizeLibrary(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":{"EnableInternetProviders":true}}`),
		"library_options_defaults")
	if equal, reason := equalNormalized(left, right, 0); equal {
		t.Fatalf("differing ItemId must remain unequal under library_options_defaults: equal=%t reason=%q", equal, reason)
	}
}

// libraryManagementCorpusRules loads the committed library-management corpus,
// finds the step selected by `match`, and resolves its normalizer rules
// through the real loadRules/selectRules path the runner uses. This is the
// corpus-derived resolution the persisted-options sensitivity and specificity
// tests exercise: they never restate the rule set or manually map action
// names, so a corpus that selects the wrong rules is caught.
func libraryManagementCorpusRules(t *testing.T, match func(Step) bool) []Rule {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "library-management.yaml"))
	if err != nil {
		t.Fatalf("load committed library-management corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed library-management corpus scenarios = %d, want one", len(scenarios))
	}
	for _, step := range scenarios[0].Steps {
		if match(step) {
			rules, err := loadRules("normalize")
			if err != nil {
				t.Fatalf("load baseline normalizers: %v", err)
			}
			selected, err := selectRules(rules, step.selectedRules())
			if err != nil {
				t.Fatalf("resolve persisted-readback rules: %v", err)
			}
			return selected
		}
	}
	t.Fatal("committed library-management corpus has no step matching the persisted-options readback selector")
	return nil
}

// libraryManagementPersistedReadbackRules resolves the normalizer rules the
// corpus's persisted-options readback step selects (the GET that captures the
// persisted option values), via the real loadRules/selectRules path.
func libraryManagementPersistedReadbackRules(t *testing.T) []Rule {
	t.Helper()
	return libraryManagementCorpusRules(t, func(step Step) bool {
		if step.Method != "GET" || step.Path != "/Library/VirtualFolders" {
			return false
		}
		_, ok := step.Capture["persisted_enable_internet_providers"]
		return ok
	})
}

// libraryManagementDefaultReadbackRules resolves the normalizer rules a
// default-options readback step selects (the first GET /Library/VirtualFolders
// that is not the persisted-options readback), via the real loadRules/selectRules
// path.
func libraryManagementDefaultReadbackRules(t *testing.T) []Rule {
	t.Helper()
	return libraryManagementCorpusRules(t, func(step Step) bool {
		if step.Method != "GET" || step.Path != "/Library/VirtualFolders" {
			return false
		}
		if _, ok := step.Capture["persisted_enable_internet_providers"]; ok {
			return false
		}
		if _, ok := step.Capture["folder_item_id"]; ok {
			return false
		}
		return true
	})
}

// normalizeLibraryWithRules runs a resolved slice of baseline rules (as
// loadRules/selectRules produce them) against a VirtualFolders response body
// and returns the normalized response.
func normalizeLibraryWithRules(t *testing.T, body string, rules []Rule) normalizedResponse {
	t.Helper()
	result, err := newNormalizer(rules).normalize(Response{Status: 200, Body: []byte(body)}, nil)
	if err != nil {
		t.Fatalf("normalize VirtualFolders response: %v", err)
	}
	return result
}

// TestLibraryManagementPersistedOptionsSensitivity proves the persisted-options
// readback normalizer set — resolved from the committed corpus's persisted-
// options readback step via loadRules/selectRules — reports a JSON difference
// when two hosts persist different EnableInternetProviders or SaveLocalMetadata
// values. This is the sensitivity the masking defect erased: the corpus's
// persisted-options readback step must catch a Deadfin that does not persist
// the same option values as stock.
func TestLibraryManagementPersistedOptionsSensitivity(t *testing.T) {
	rules := libraryManagementPersistedReadbackRules(t)
	for _, tt := range []struct {
		name      string
		leftBody  string
		rightBody string
		wantDiff  string
	}{
		{
			name:      "EnableInternetProviders differs",
			leftBody:  virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"PathInfos":[]}}`),
			rightBody: virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","LibraryOptions":{"EnableInternetProviders":false,"SaveLocalMetadata":true,"PathInfos":[]}}`),
			wantDiff:  "JSON differs at $[0].LibraryOptions.EnableInternetProviders",
		},
		{
			name:      "SaveLocalMetadata differs",
			leftBody:  virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"PathInfos":[]}}`),
			rightBody: virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":false,"PathInfos":[]}}`),
			wantDiff:  "JSON differs at $[0].LibraryOptions.SaveLocalMetadata",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			left := normalizeLibraryWithRules(t, tt.leftBody, rules)
			right := normalizeLibraryWithRules(t, tt.rightBody, rules)
			equal, reason := equalNormalized(left, right, 0)
			if equal {
				t.Fatalf("persisted-options readback must report a difference when %s; got equal", tt.name)
			}
			if reason != tt.wantDiff {
				t.Fatalf("persisted-options readback difference = %q, want %q", reason, tt.wantDiff)
			}
		})
	}
}

// TestLibraryManagementPersistedOptionsSpecificity proves the persisted-options
// readback normalizer set — resolved from the committed corpus — accepts two
// responses with identical persisted values but differing host-local values: the
// host-local ItemId scalar is masked, and host-local default LibraryOptions
// fields are masked while the persisted values stay visible. Only the persisted
// option values are compared; the host-local ItemId and defaults are not
// management-route contract.
func TestLibraryManagementPersistedOptionsSpecificity(t *testing.T) {
	rules := libraryManagementPersistedReadbackRules(t)
	t.Run("host-local ItemId masked", func(t *testing.T) {
		left := normalizeLibraryWithRules(t,
			virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"PathInfos":[]}}`),
			rules)
		right := normalizeLibraryWithRules(t,
			virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"PathInfos":[]}}`),
			rules)
		if equal, reason := equalNormalized(left, right, 0); !equal {
			t.Fatalf("host-local ItemId must be masked on persisted-options readback: %s", reason)
		}
	})
	t.Run("host-local defaults masked", func(t *testing.T) {
		left := normalizeLibraryWithRules(t,
			virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"Enabled":true,"EnablePhotos":true,"PathInfos":[],"TypeOptions":[]}}`),
			rules)
		right := normalizeLibraryWithRules(t,
			virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":true,"SaveLocalMetadata":true,"Enabled":false,"EnablePhotos":false,"PathInfos":null,"TypeOptions":null,"PreferredMetadataLanguage":"en"}}`),
			rules)
		if equal, reason := equalNormalized(left, right, 0); !equal {
			t.Fatalf("host-local default LibraryOptions fields must be masked on persisted-options readback: %s", reason)
		}
	})
}

// TestLibraryManagementPersistedOptionsPreservesNonObjectLibraryOptions proves
// the persisted-options readback normalizer set — resolved from the committed
// corpus — leaves a non-object LibraryOptions (wrong type) intact, so the type
// mismatch stays comparable. This is the same type boundary as the defaults
// action, exercised through the corpus-resolved rule set.
func TestLibraryManagementPersistedOptionsPreservesNonObjectLibraryOptions(t *testing.T) {
	rules := libraryManagementPersistedReadbackRules(t)
	for _, tt := range []struct {
		name    string
		optsRaw string
		want    any
	}{
		{name: "string", optsRaw: `"not-an-object"`, want: "not-an-object"},
		{name: "number", optsRaw: `42`, want: json.Number("42")},
		{name: "boolean", optsRaw: `true`, want: true},
		{name: "array", optsRaw: `["a","b"]`, want: []any{"a", "b"}},
		{name: "null", optsRaw: `null`, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := virtualFolderBody(`{"Name":"Cinema","ItemId":"id-a","LibraryOptions":` + tt.optsRaw + `}`)
			normalized := normalizeLibraryWithRules(t, body, rules)
			folder := normalized.Body.([]any)[0].(map[string]any)
			if got := folder["LibraryOptions"]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("non-object LibraryOptions normalized to %#v, want exact %#v", got, tt.want)
			}
		})
	}
}

// TestDefaultReadbackStillMasksLibraryOptions proves the default-options
// readback normalizer set — resolved from the committed corpus's first default
// readback step — masks differing host-local default LibraryOptions, so two
// hosts with different compatibility defaults compare equal. This is the
// specificity the corpus relies on for default-options readbacks; it must not
// regress when the persisted-options readback drops the defaults action.
func TestDefaultReadbackStillMasksLibraryOptions(t *testing.T) {
	rules := libraryManagementDefaultReadbackRules(t)
	left := normalizeLibraryWithRules(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"stock-id","CollectionType":"movies","LibraryOptions":{"Enabled":true,"EnablePhotos":true,"PathInfos":[]}}`),
		rules)
	right := normalizeLibraryWithRules(t,
		virtualFolderBody(`{"Name":"Cinema","ItemId":"deadfin-id","CollectionType":"movies","LibraryOptions":{"Enabled":false,"EnablePhotos":false,"PathInfos":null}}`),
		rules)
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("default-options readback must mask host-local LibraryOptions defaults: %s", reason)
	}
}
