// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// libraryManagementTransportNormalizers lists the transport-header rules every
// library-management read and write step selects. They are environmental and do
// not affect the management-route contract under comparison. It returns a fresh
// slice so no package-level mutable state is shared between tests.
func libraryManagementTransportNormalizers() []string {
	return []string{
		"response-time-headers",
		"response-time-ms-header",
		"process-time-headers",
		"date-header",
		"server-stack-header",
		"content-length-header",
		"content-language-header",
		"vary-header",
	}
}

// libraryManagementWriteNormalizers is the normalizer set every POST/DELETE write
// step selects (transport headers only; no body normalization). It returns a
// fresh slice so no package-level mutable state is shared between tests.
func libraryManagementWriteNormalizers() []string {
	return libraryManagementTransportNormalizers()
}

// libraryManagementDefaultReadNormalizers is the normalizer set default-options
// GET readbacks select: the host-local ItemId scalar and host-local LibraryOptions
// defaults are masked, plus transport headers. It returns a fresh slice so no
// package-level mutable state is shared between tests.
func libraryManagementDefaultReadNormalizers() []string {
	return append(
		[]string{"library-folder-ids", "library-options-defaults"},
		libraryManagementTransportNormalizers()...)
}

// libraryManagementPersistedReadNormalizers is the normalizer set the persisted-
// options GET readback selects: the host-local ItemId scalar is masked and the
// host-local LibraryOptions defaults are masked while the persisted option
// values (EnableInternetProviders, SaveLocalMetadata) remain visible. It
// returns a fresh slice so no package-level mutable state is shared between
// tests.
func libraryManagementPersistedReadNormalizers() []string {
	return append(
		[]string{"library-folder-ids", "library-options-persisted"},
		libraryManagementTransportNormalizers()...)
}

// validCollectionTypes are the eight canonical Jellyfin CollectionType values.
// It returns a fresh slice so no package-level mutable state is shared between
// tests.
func validCollectionTypes() []string {
	return []string{
		"movies", "tvshows", "music", "musicvideos",
		"homevideos", "boxsets", "books", "mixed",
	}
}

// libraryManagementReadbackLabel generates the readback step-name label from
// the input spelling and its canonical value. It quotes the input with %q
// when it has edge whitespace and uses it plain otherwise. It returns a fresh
// result so no package-level mutable state is shared between tests.
func libraryManagementReadbackLabel(input, canonical string) string {
	if strings.TrimSpace(input) != input {
		return fmt.Sprintf("%q (%s)", input, canonical)
	}
	return fmt.Sprintf("%s (%s)", input, canonical)
}

// numericCollectionTypeCycle is one numeric collectionType spelling and its
// canonical lowercase result. The contract check generates the readback step
// name label from the input and canonical value so a changed input-to-
// canonical mapping is detected. It returns a fresh struct so no package-
// level mutable state is shared between tests.
type numericCollectionTypeCycle struct {
	input     string // the quoted YAML query value sent to the host
	canonical string // the lowercase CollectionType the host must persist
}

// numericCollectionTypeCycles is the numeric collectionType spelling matrix.
// The ordinals 0 through 7 map to the same canonical names as the string
// values in validCollectionTypes. The 01 and +5 spellings are alternate
// base-10 forms of 1 and 5. It returns a fresh slice so no package-level
// mutable state is shared between tests.
func numericCollectionTypeCycles() []numericCollectionTypeCycle {
	return []numericCollectionTypeCycle{
		{"0", "movies"},
		{"1", "tvshows"},
		{"01", "tvshows"},
		{"2", "music"},
		{"3", "musicvideos"},
		{"4", "homevideos"},
		{"5", "boxsets"},
		{"+5", "boxsets"},
		{"6", "books"},
		{"7", "mixed"},
	}
}

// alternateCollectionTypeCycle is one accepted non-canonical collectionType
// spelling and its canonical lowercase result. The contract check generates
// the readback step name label from the input and canonical value. It returns
// a fresh struct so no package-level mutable state is shared between tests.
type alternateCollectionTypeCycle struct {
	input     string // the YAML query value sent to the host
	canonical string // the lowercase CollectionType the host must persist
}

// alternateCollectionTypeCycles is the accepted alternate-spelling matrix from
// the source test at deadfin/deadfin@baa149a9e3526e65d3f8b5102066d62f92fb7c2b:
// internal/api/handlers/library_management_test.go:295-299. Stock and Deadfin
// both accept these spellings and map them to the same canonical name. It
// returns a fresh slice so no package-level mutable state is shared between
// tests.
func alternateCollectionTypeCycles() []alternateCollectionTypeCycle {
	return []alternateCollectionTypeCycle{
		{"Movies", "movies"},
		{" TVSHOWS ", "tvshows"},
		{"MuSiC", "music"},
	}
}

// rejectedCollectionTypeCycles is the rejected-spelling matrix from the source
// test at deadfin/deadfin@baa149a9e3526e65d3f8b5102066d62f92fb7c2b:internal/api/
// handlers/library_management_test.go:316. Stock and Deadfin both reject this
// spelling: the host returns HTTP 204 and persists no CollectionType (null).
// Stock accepts 8, -1, and 0,1 but Deadfin rejects them; those three are
// divergences (see docs/divergences.md D-26), not replay cycles. It returns a
// fresh slice so no package-level mutable state is shared between tests.
func rejectedCollectionTypeCycles() []string {
	return []string{"1.0"}
}

// libraryManagementCorpusSteps loads the committed library-management corpus and
// returns its single scenario's steps.
func libraryManagementCorpusSteps(t *testing.T) []Step {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "library-management.yaml"))
	if err != nil {
		t.Fatalf("load committed library-management corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed library-management corpus scenarios = %d, want one", len(scenarios))
	}
	return scenarios[0].Steps
}

// requireLibraryManagementCorpusContract verifies the committed corpus matches
// the expected six-operation, collectionType-cycle, ItemId-capture/use, and
// persisted-option-visibility contract. It derives dependency chains
// semantically — pairing each create with its same-folder cleanup, and the
// ItemId capture with its substitution — without pinning a fixed step total or
// unrelated absolute indices. Unrelated duplication and independent-cycle
// reordering remain accepted; a cycle whose cleanup changes folder or route
// is rejected.
func requireLibraryManagementCorpusContract(steps []Step) error {
	if err := libraryManagementCheckSixOperations(steps); err != nil {
		return err
	}
	if err := libraryManagementCheckCollectionTypeCycles(steps); err != nil {
		return err
	}
	if err := libraryManagementCheckPathsProbeCycle(steps); err != nil {
		return err
	}
	if err := libraryManagementCheckOptionsChain(steps); err != nil {
		return err
	}
	if err := libraryManagementCheckPersistedReadback(steps); err != nil {
		return err
	}
	if err := libraryManagementCheckNormalizerConsistency(steps); err != nil {
		return err
	}
	return nil
}

// libraryManagementStepName returns the folder name a create/cleanup step
// targets (the params["name"] string). The second result is false when the
// param is absent or not a string.
func libraryManagementStepName(step Step) (string, bool) {
	name, ok := step.Params["name"].(string)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// libraryManagementCheckSixOperations verifies every step targets one of the
// six management routes with its expected status, and that each route appears
// at least once. It scans without fixed indices.
func libraryManagementCheckSixOperations(steps []Step) error {
	type opContract struct {
		method string
		path   string
		status int
		count  int
	}
	sixOps := []opContract{
		{"GET", "/Library/VirtualFolders", 200, 0},
		{"POST", "/Library/VirtualFolders", 204, 0},
		{"DELETE", "/Library/VirtualFolders", 204, 0},
		{"POST", "/Library/VirtualFolders/Paths", 204, 0},
		{"DELETE", "/Library/VirtualFolders/Paths", 204, 0},
		{"POST", "/Library/VirtualFolders/LibraryOptions", 204, 0},
	}
	opIndex := map[string]*opContract{}
	for i := range sixOps {
		opIndex[sixOps[i].method+" "+sixOps[i].path] = &sixOps[i]
	}
	for i, step := range steps {
		op, ok := opIndex[step.Method+" "+step.Path]
		if !ok {
			return fmt.Errorf("step %d uses unknown operation %s %s", i, step.Method, step.Path)
		}
		op.count++
		if step.Expect == nil || step.Expect.Status == nil {
			return fmt.Errorf("step %d %q has no expected status", i, step.Name)
		}
		if *step.Expect.Status != op.status {
			return fmt.Errorf("step %d %q expected status = %d, want %d", i, step.Name, *step.Expect.Status, op.status)
		}
	}
	for _, op := range sixOps {
		if op.count == 0 {
			return fmt.Errorf("operation %s %s is absent from the corpus", op.method, op.path)
		}
	}
	return nil
}

// libraryManagementCheckCollectionTypeCycles derives each
// create/readback/delete cycle as a tight triple (add[i] → GET readback[i+1] →
// DELETE cleanup[i+2]) and verifies the cleanup keeps the same folder and
// route as the create. It collects the collectionType each cycle probes and
// requires the omitted, empty, invalid, eight canonical values, the ten
// numeric ordinal spellings, the three accepted alternate spellings, and the
// one rejected spelling from the source test matrix. Each numeric and
// alternate readback step name must state the input spelling and the canonical
// value so a changed mapping is detected. Cycles are matched by structure, not
// absolute index, so independent-cycle reordering stays accepted.
func libraryManagementCheckCollectionTypeCycles(steps []Step) error {
	// A plain collectionType cycle is a tight triple whose create is a named
	// POST /Library/VirtualFolders, whose readback is the next GET on the same
	// route, and whose cleanup is the step after that. The Paths probe and the
	// options chain have extra steps between create and cleanup, so they do not
	// form tight triples and are not collected here.
	type cycle struct {
		addIndex      int
		collection    any // nil = absent, "" = empty, otherwise the param value
		collectionSet bool
		readbackName  string
	}
	var cycles []cycle
	for i := 0; i+2 < len(steps); i++ {
		add, readback, cleanup := steps[i], steps[i+1], steps[i+2]
		if add.Method != "POST" || add.Path != "/Library/VirtualFolders" {
			continue
		}
		if _, ok := libraryManagementStepName(add); !ok {
			continue
		}
		if readback.Method != "GET" || readback.Path != "/Library/VirtualFolders" {
			continue
		}
		if cleanup.Method != "DELETE" {
			continue
		}
		// Tight triple: the cleanup must stay on the main route and keep the
		// create's folder. A changed route or folder breaks the cycle.
		if cleanup.Path != "/Library/VirtualFolders" {
			return fmt.Errorf("collectionType cycle at step %d cleanup route = %s, want /Library/VirtualFolders", i, cleanup.Path)
		}
		addName, _ := libraryManagementStepName(add)
		cleanupName, _ := libraryManagementStepName(cleanup)
		if cleanupName != addName {
			return fmt.Errorf("collectionType cycle at step %d cleanup folder = %q, want %q (same as create)", i, cleanupName, addName)
		}
		c := cycle{addIndex: i, readbackName: readback.Name}
		if v, ok := add.Params["collectionType"]; ok {
			c.collection = v
			c.collectionSet = true
		}
		cycles = append(cycles, c)
	}
	numericCycles := numericCollectionTypeCycles()
	alternateCycles := alternateCollectionTypeCycles()
	rejectedCycles := rejectedCollectionTypeCycles()
	wantCount := 1 + 1 + 1 + len(validCollectionTypes()) + len(numericCycles) +
		len(alternateCycles) + len(rejectedCycles)
	if len(cycles) != wantCount {
		return fmt.Errorf("collectionType cycles = %d, want %d (omitted, empty, invalid, eight valid, ten numeric, three alternate, one rejected)", len(cycles), wantCount)
	}
	// Require exactly one omitted (absent), one empty, one invalid (photos),
	// and one cycle per valid value.
	want := map[string]int{"": 1, "photos": 1}
	for _, ct := range validCollectionTypes() {
		want[ct] = 1
	}
	// Require exactly one cycle per numeric spelling.
	numericWant := map[string]int{}
	for _, nc := range numericCycles {
		numericWant[nc.input] = 1
	}
	// Require exactly one cycle per accepted alternate spelling.
	alternateWant := map[string]int{}
	for _, ac := range alternateCycles {
		alternateWant[ac.input] = 1
	}
	// Require exactly one cycle per rejected spelling.
	rejectedWant := map[string]int{}
	for _, rc := range rejectedCycles {
		rejectedWant[rc] = 1
	}
	omitted := 0
	for _, c := range cycles {
		if !c.collectionSet {
			omitted++
			continue
		}
		ct, _ := c.collection.(string)
		if _, isNumeric := numericWant[ct]; isNumeric {
			numericWant[ct]--
			continue
		}
		if _, isAlternate := alternateWant[ct]; isAlternate {
			alternateWant[ct]--
			continue
		}
		if _, isRejected := rejectedWant[ct]; isRejected {
			rejectedWant[ct]--
			continue
		}
		want[ct]--
	}
	if omitted != 1 {
		return fmt.Errorf("collectionType omitted cycles = %d, want 1", omitted)
	}
	for ct, remaining := range want {
		if remaining != 0 {
			return fmt.Errorf("collectionType %q cycles = %d, want 1", ct, 1-remaining)
		}
	}
	for ct, remaining := range numericWant {
		if remaining != 0 {
			return fmt.Errorf("numeric collectionType %q cycles = %d, want 1", ct, 1-remaining)
		}
	}
	for ct, remaining := range alternateWant {
		if remaining != 0 {
			return fmt.Errorf("alternate collectionType %q cycles = %d, want 1", ct, 1-remaining)
		}
	}
	for ct, remaining := range rejectedWant {
		if remaining != 0 {
			return fmt.Errorf("rejected collectionType %q cycles = %d, want 1", ct, 1-remaining)
		}
	}
	// Require each numeric and alternate readback step name to state the exact
	// input-to-canonical label so a changed mapping is detected. The label is
	// generated from each cycle's input and canonical value.
	numericReadback := map[string]string{}
	for _, nc := range numericCycles {
		numericReadback[nc.input] = libraryManagementReadbackLabel(nc.input, nc.canonical)
	}
	alternateReadback := map[string]string{}
	for _, ac := range alternateCycles {
		alternateReadback[ac.input] = libraryManagementReadbackLabel(ac.input, ac.canonical)
	}
	for _, c := range cycles {
		if !c.collectionSet {
			continue
		}
		ct, _ := c.collection.(string)
		if label, isNumeric := numericReadback[ct]; isNumeric {
			if !strings.Contains(c.readbackName, "numeric "+label) {
				return fmt.Errorf("numeric collectionType %q readback step %q does not contain %q", ct, c.readbackName, "numeric "+label)
			}
			continue
		}
		if label, isAlternate := alternateReadback[ct]; isAlternate {
			if !strings.Contains(c.readbackName, "alternate "+label) {
				return fmt.Errorf("alternate collectionType %q readback step %q does not contain %q", ct, c.readbackName, "alternate "+label)
			}
			continue
		}
	}
	// Require each rejected readback step name to state the spelling and the
	// no-marker result so a changed result label is detected.
	for _, c := range cycles {
		if !c.collectionSet {
			continue
		}
		ct, _ := c.collection.(string)
		if _, isRejected := rejectedWant[ct]; !isRejected {
			continue
		}
		if !strings.Contains(c.readbackName, "rejected "+ct+" ") {
			return fmt.Errorf("rejected collectionType %q readback step %q does not state the input spelling", ct, c.readbackName)
		}
		if !strings.Contains(c.readbackName, "(no marker)") {
			return fmt.Errorf("rejected collectionType %q readback step %q does not state (no marker)", ct, c.readbackName)
		}
	}
	return nil
}

// libraryManagementCheckPathsProbeCycle verifies the Paths probe: POST and
// DELETE /Library/VirtualFolders/Paths target the same folder, and that folder
// matches the enclosing create/cleanup cycle on the main route.
func libraryManagementCheckPathsProbeCycle(steps []Step) error {
	var pathsAdd, pathsRemove *int
	for i := range steps {
		if steps[i].Method == "POST" && steps[i].Path == "/Library/VirtualFolders/Paths" {
			pathsAdd = &i
		}
		if steps[i].Method == "DELETE" && steps[i].Path == "/Library/VirtualFolders/Paths" {
			pathsRemove = &i
		}
	}
	if pathsAdd == nil {
		return fmt.Errorf("POST /Library/VirtualFolders/Paths is absent from the corpus")
	}
	if pathsRemove == nil {
		return fmt.Errorf("DELETE /Library/VirtualFolders/Paths is absent from the corpus")
	}
	if *pathsAdd >= *pathsRemove {
		return fmt.Errorf("Paths add step %d must precede Paths remove step %d", *pathsAdd, *pathsRemove)
	}
	body, ok := steps[*pathsAdd].Body.(map[string]any)
	if !ok {
		return fmt.Errorf("Paths add step body = %#v, want map with Name", steps[*pathsAdd].Body)
	}
	folder, ok := body["Name"].(string)
	if !ok || folder == "" {
		return fmt.Errorf("Paths add step body Name = %#v, want non-empty string", body["Name"])
	}
	removeName, ok := libraryManagementStepName(steps[*pathsRemove])
	if !ok || removeName != folder {
		return fmt.Errorf("Paths remove step folder = %q, want %q (same as Paths add)", removeName, folder)
	}
	// The Paths add/remove must sit inside a create(folder)/cleanup(folder)
	// cycle on the main route so the probe manages the folder it created.
	create, cleanup := libraryManagementFindEnclosingCycle(steps, *pathsAdd, *pathsRemove, folder)
	if create < 0 || cleanup < 0 {
		return fmt.Errorf("Paths probe folder %q has no enclosing create/cleanup cycle on /Library/VirtualFolders", folder)
	}
	return nil
}

// libraryManagementFindEnclosingCycle returns the index of the create step
// before lo and the cleanup step after hi that both target the named folder on
// /Library/VirtualFolders. Either result is -1 when no such step exists.
func libraryManagementFindEnclosingCycle(steps []Step, lo, hi int, folder string) (create, cleanup int) {
	create, cleanup = -1, -1
	for i := lo - 1; i >= 0; i-- {
		if steps[i].Method == "POST" && steps[i].Path == "/Library/VirtualFolders" {
			if name, ok := libraryManagementStepName(steps[i]); ok && name == folder {
				create = i
				break
			}
		}
	}
	for i := hi + 1; i < len(steps); i++ {
		if steps[i].Method == "DELETE" && steps[i].Path == "/Library/VirtualFolders" {
			if name, ok := libraryManagementStepName(steps[i]); ok && name == folder {
				cleanup = i
				break
			}
		}
	}
	return create, cleanup
}

// libraryManagementCheckOptionsChain verifies the ItemId capture precedes the
// LibraryOptions substitution, that the capture reads the single folder's
// ItemId (0.ItemId), and that the substitution references the captured
// variable. Both steps must sit inside the same create/cleanup cycle.
func libraryManagementCheckOptionsChain(steps []Step) error {
	captureIndex, substituteIndex := -1, -1
	for i, step := range steps {
		if step.Method == "GET" && step.Path == "/Library/VirtualFolders" && len(step.Capture) == 1 && step.Capture["folder_item_id"] == "0.ItemId" {
			captureIndex = i
		}
		if step.Method == "POST" && step.Path == "/Library/VirtualFolders/LibraryOptions" {
			substituteIndex = i
		}
	}
	if captureIndex < 0 {
		return fmt.Errorf("ItemId capture step (GET /Library/VirtualFolders capturing folder_item_id=0.ItemId) is absent")
	}
	if substituteIndex < 0 {
		return fmt.Errorf("ItemId substitute step (POST /Library/VirtualFolders/LibraryOptions) is absent")
	}
	if captureIndex >= substituteIndex {
		return fmt.Errorf("ItemId capture step %d must precede substitute step %d", captureIndex, substituteIndex)
	}
	body, ok := steps[substituteIndex].Body.(map[string]any)
	if !ok {
		return fmt.Errorf("ItemId substitute step body = %#v, want map", steps[substituteIndex].Body)
	}
	if id, ok := body["Id"].(string); !ok || id != "${folder_item_id}" {
		return fmt.Errorf("ItemId substitute step body Id = %#v, want ${folder_item_id}", body["Id"])
	}
	// Both steps must sit inside the same create/cleanup cycle on the main
	// route. The capture is a readback, so the create precedes it; the
	// cleanup follows the substitution.
	create, cleanup := libraryManagementFindEnclosingCycle(steps, captureIndex, substituteIndex, libraryManagementOptionsProbeFolder(steps))
	if create < 0 || cleanup < 0 {
		return fmt.Errorf("ItemId capture/substitute steps have no enclosing create/cleanup cycle on /Library/VirtualFolders")
	}
	return nil
}

// libraryManagementOptionsProbeFolder returns the folder name of the create
// step that encloses the ItemId capture step. It is the folder whose options
// the chain updates.
func libraryManagementOptionsProbeFolder(steps []Step) string {
	for i, step := range steps {
		if step.Method == "GET" && step.Path == "/Library/VirtualFolders" && len(step.Capture) == 1 && step.Capture["folder_item_id"] == "0.ItemId" {
			for j := i - 1; j >= 0; j-- {
				if steps[j].Method == "POST" && steps[j].Path == "/Library/VirtualFolders" {
					if name, ok := libraryManagementStepName(steps[j]); ok {
						return name
					}
				}
			}
		}
	}
	return ""
}

// libraryManagementCheckPersistedReadback verifies the persisted-options
// readback step (the GET that captures the persisted option values) selects
// library-options-persisted (masking host-local defaults while keeping the
// persisted values) and not library-options-defaults (which masks the whole
// object), and that it captures both persisted fields.
func libraryManagementCheckPersistedReadback(steps []Step) error {
	for _, step := range steps {
		if step.Method != "GET" || step.Path != "/Library/VirtualFolders" {
			continue
		}
		if _, ok := step.Capture["persisted_enable_internet_providers"]; !ok {
			continue
		}
		for _, n := range step.Normalizers {
			if n == "library-options-defaults" {
				return fmt.Errorf("persisted-options readback selects library-options-defaults; it must not mask changed persisted options")
			}
		}
		if !sliceContains(step.Normalizers, "library-folder-ids") {
			return fmt.Errorf("persisted-options readback must still select library-folder-ids to mask the host-local ItemId")
		}
		if !sliceContains(step.Normalizers, "library-options-persisted") {
			return fmt.Errorf("persisted-options readback must select library-options-persisted to mask defaults while keeping persisted values")
		}
		if len(step.Capture) != 2 ||
			step.Capture["persisted_enable_internet_providers"] != "0.LibraryOptions.EnableInternetProviders" ||
			step.Capture["persisted_save_local_metadata"] != "0.LibraryOptions.SaveLocalMetadata" {
			return fmt.Errorf("persisted-options readback capture = %#v, want persisted_enable_internet_providers and persisted_save_local_metadata", step.Capture)
		}
		return nil
	}
	return fmt.Errorf("persisted-options readback step (GET /Library/VirtualFolders capturing persisted option values) is absent")
}

// libraryManagementIsPersistedReadback reports whether a step is the
// persisted-options readback (the GET that captures the persisted option
// values). The normalizer-consistency check uses this instead of an absolute
// index to tell the persisted readback apart from default readbacks.
func libraryManagementIsPersistedReadback(step Step) bool {
	if step.Method != "GET" || step.Path != "/Library/VirtualFolders" {
		return false
	}
	_, ok := step.Capture["persisted_enable_internet_providers"]
	return ok
}

// libraryManagementCheckNormalizerConsistency verifies every step selects the
// normalizer set its role requires: writes select transport headers only,
// default readbacks mask the host-local ItemId and LibraryOptions defaults,
// and the persisted-options readback masks defaults while keeping persisted
// values. The persisted readback is identified by its capture, not its index.
func libraryManagementCheckNormalizerConsistency(steps []Step) error {
	for i, step := range steps {
		var want []string
		switch {
		case libraryManagementIsPersistedReadback(step):
			want = libraryManagementPersistedReadNormalizers()
		case step.Method == "GET":
			want = libraryManagementDefaultReadNormalizers()
		default:
			want = libraryManagementWriteNormalizers()
		}
		if !reflect.DeepEqual(step.Normalizers, want) {
			return fmt.Errorf("step %d %q normalizers = %#v, want %#v", i, step.Name, step.Normalizers, want)
		}
	}
	return nil
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestLibraryManagementCorpusDefinesExactManagementContract loads the committed
// corpus and verifies the six operations/statuses, collectionType cycles, ItemId
// capture/use order, persisted-option visibility, and normalizer-set consistency.
func TestLibraryManagementCorpusDefinesExactManagementContract(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	if err := requireLibraryManagementCorpusContract(steps); err != nil {
		t.Fatal(err)
	}
}

// libraryManagementFindCaptureStep returns the index of the ItemId capture step
// (the GET readback that captures folder_item_id from 0.ItemId), or -1.
func libraryManagementFindCaptureStep(steps []Step) int {
	for i, step := range steps {
		if step.Method == "GET" && step.Path == "/Library/VirtualFolders" && len(step.Capture) == 1 && step.Capture["folder_item_id"] == "0.ItemId" {
			return i
		}
	}
	return -1
}

// libraryManagementFindSubstituteStep returns the index of the ItemId
// substitution step (POST /Library/VirtualFolders/LibraryOptions), or -1.
func libraryManagementFindSubstituteStep(steps []Step) int {
	for i, step := range steps {
		if step.Method == "POST" && step.Path == "/Library/VirtualFolders/LibraryOptions" {
			return i
		}
	}
	return -1
}

// libraryManagementFindPersistedReadbackStep returns the index of the
// persisted-options readback step (the GET that captures the persisted option
// values), or -1.
func libraryManagementFindPersistedReadbackStep(steps []Step) int {
	for i, step := range steps {
		if step.Method == "GET" && step.Path == "/Library/VirtualFolders" {
			if _, ok := step.Capture["persisted_enable_internet_providers"]; ok {
				return i
			}
		}
	}
	return -1
}

// libraryManagementFindPathsRemoveStep returns the index of the DELETE
// /Library/VirtualFolders/Paths step, or -1.
func libraryManagementFindPathsRemoveStep(steps []Step) int {
	for i, step := range steps {
		if step.Method == "DELETE" && step.Path == "/Library/VirtualFolders/Paths" {
			return i
		}
	}
	return -1
}

// libraryManagementCloneSteps returns a deep-enough copy of steps for mutation:
// the slice and each step's reference-typed fields (Normalizers, Capture,
// Params, Body, Expect) are copied so a mutation cannot bleed into the shared
// package-level normalizer slices or the committed corpus.
func libraryManagementCloneSteps(steps []Step) []Step {
	clone := make([]Step, len(steps))
	for i, s := range steps {
		clone[i] = s
		if s.Normalizers != nil {
			clone[i].Normalizers = append([]string(nil), s.Normalizers...)
		}
		if s.Capture != nil {
			clone[i].Capture = make(map[string]string, len(s.Capture))
			for k, v := range s.Capture {
				clone[i].Capture[k] = v
			}
		}
		if s.Params != nil {
			clone[i].Params = make(map[string]any, len(s.Params))
			for k, v := range s.Params {
				clone[i].Params[k] = v
			}
		}
		if s.Body != nil {
			clone[i].Body = libraryManagementCloneBody(s.Body)
		}
		if s.Expect != nil && s.Expect.Status != nil {
			status := *s.Expect.Status
			clone[i].Expect = &Expect{Status: &status}
		}
	}
	return clone
}

// libraryManagementCloneBody deep-copies a step body so a substitution mutation
// cannot alter the committed corpus's decoded body.
func libraryManagementCloneBody(body any) any {
	switch typed := body.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = libraryManagementCloneBody(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = libraryManagementCloneBody(v)
		}
		return out
	default:
		return body
	}
}

// TestLibraryManagementCorpusRejectsRemoval proves the contract test catches a
// removed persisted-options readback step.
func TestLibraryManagementCorpusRejectsRemoval(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	persisted := libraryManagementFindPersistedReadbackStep(steps)
	if persisted < 0 {
		t.Fatal("committed corpus has no persisted-options readback step")
	}
	broken := append(append([]Step(nil), steps[:persisted]...), steps[persisted+1:]...)
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a removed persisted-options readback step")
	}
}

// TestLibraryManagementCorpusAcceptsUnrelatedDuplication proves the contract
// does not pin unrelated duplication: duplicating a standalone readback that is
// not part of any create/readback/delete cycle stays accepted. The empty-baseline
// GET is the first step and belongs to no cycle.
func TestLibraryManagementCorpusAcceptsUnrelatedDuplication(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	broken := append([]Step(nil), steps...)
	broken = append([]Step{steps[0]}, broken...)
	if err := requireLibraryManagementCorpusContract(broken); err != nil {
		t.Fatalf("contract rejected unrelated duplication of a standalone readback: %v", err)
	}
}

// TestLibraryManagementCorpusAcceptsIndependentCycleReordering proves the
// contract does not pin independent-cycle order: swapping two whole
// collectionType create/readback/delete cycles stays accepted because each cycle
// is a self-contained tight triple matched by structure, not absolute index.
func TestLibraryManagementCorpusAcceptsIndependentCycleReordering(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	// Find two plain collectionType cycles (tight triples) and swap them whole.
	first := libraryManagementFindPlainCycle(steps, 0)
	second := libraryManagementFindPlainCycle(steps, first+3)
	if first < 0 || second < 0 {
		t.Fatal("committed corpus has fewer than two collectionType cycles to reorder")
	}
	broken := append([]Step(nil), steps...)
	a := append([]Step(nil), broken[first:first+3]...)
	b := append([]Step(nil), broken[second:second+3]...)
	copy(broken[second:second+3], a)
	copy(broken[first:first+3], b)
	if err := requireLibraryManagementCorpusContract(broken); err != nil {
		t.Fatalf("contract rejected independent-cycle reordering: %v", err)
	}
}

// libraryManagementFindPlainCycle returns the start index of the first plain
// collectionType cycle (a tight add->GET->DELETE triple on /Library/VirtualFolders
// whose create has a name), starting the search at `from`. It returns -1 when
// no plain cycle remains.
func libraryManagementFindPlainCycle(steps []Step, from int) int {
	for i := from; i+2 < len(steps); i++ {
		add, readback, cleanup := steps[i], steps[i+1], steps[i+2]
		if add.Method != "POST" || add.Path != "/Library/VirtualFolders" {
			continue
		}
		if _, ok := libraryManagementStepName(add); !ok {
			continue
		}
		if readback.Method != "GET" || readback.Path != "/Library/VirtualFolders" {
			continue
		}
		if cleanup.Method != "DELETE" || cleanup.Path != "/Library/VirtualFolders" {
			continue
		}
		return i
	}
	return -1
}

// TestLibraryManagementCorpusRejectsReordering proves the contract test catches
// capture and substitution steps that are swapped out of order.
func TestLibraryManagementCorpusRejectsReordering(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	capture := libraryManagementFindCaptureStep(steps)
	substitute := libraryManagementFindSubstituteStep(steps)
	if capture < 0 || substitute < 0 {
		t.Fatal("committed corpus has no ItemId capture/substitute steps")
	}
	broken := append([]Step(nil), steps...)
	broken[capture], broken[substitute] = broken[substitute], broken[capture]
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted capture and substitution steps out of order")
	}
}

// TestLibraryManagementCorpusRejectsStatusChange proves the contract test catches
// a changed expected status on the LibraryOptions substitution step.
func TestLibraryManagementCorpusRejectsStatusChange(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	substitute := libraryManagementFindSubstituteStep(steps)
	if substitute < 0 {
		t.Fatal("committed corpus has no LibraryOptions substitution step")
	}
	broken := libraryManagementCloneSteps(steps)
	status := 500
	broken[substitute].Expect = &Expect{Status: &status}
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a changed expected status on the LibraryOptions step")
	}
}

// TestLibraryManagementCorpusRejectsBrokenCapture proves the contract test catches
// a broken ItemId capture path (capturing the second folder instead of the
// single folder the chain created).
func TestLibraryManagementCorpusRejectsBrokenCapture(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	capture := libraryManagementFindCaptureStep(steps)
	if capture < 0 {
		t.Fatal("committed corpus has no ItemId capture step")
	}
	broken := libraryManagementCloneSteps(steps)
	broken[capture].Capture = map[string]string{"folder_item_id": "1.ItemId"}
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a broken ItemId capture path")
	}
}

// TestLibraryManagementCorpusRejectsBrokenSubstitution proves the contract test
// catches a broken ItemId substitution in the POST LibraryOptions body.
func TestLibraryManagementCorpusRejectsBrokenSubstitution(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	substitute := libraryManagementFindSubstituteStep(steps)
	if substitute < 0 {
		t.Fatal("committed corpus has no LibraryOptions substitution step")
	}
	broken := libraryManagementCloneSteps(steps)
	body := broken[substitute].Body.(map[string]any)
	body["Id"] = "hardcoded-id"
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a hardcoded ItemId instead of the captured substitution")
	}
}

// TestLibraryManagementCorpusRejectsOptionsDefaultsOnPersistedReadback proves the
// contract test catches library-options-defaults re-added to the persisted-
// options readback step — the exact masking defect the rework removed.
func TestLibraryManagementCorpusRejectsOptionsDefaultsOnPersistedReadback(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	persisted := libraryManagementFindPersistedReadbackStep(steps)
	if persisted < 0 {
		t.Fatal("committed corpus has no persisted-options readback step")
	}
	broken := libraryManagementCloneSteps(steps)
	// The helper returns a fresh slice, so the mutation cannot alias any other
	// test's normalizer set.
	broken[persisted].Normalizers = libraryManagementDefaultReadNormalizers()
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted library-options-defaults on persisted-options readback")
	}
}

// TestLibraryManagementCorpusRejectsMissingOptionsPersisted proves the contract
// test catches library-options-persisted removed from the persisted-options
// readback step. Without it, host-local default LibraryOptions fields would be
// visible and cause a false difference on the live differential.
func TestLibraryManagementCorpusRejectsMissingOptionsPersisted(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	persisted := libraryManagementFindPersistedReadbackStep(steps)
	if persisted < 0 {
		t.Fatal("committed corpus has no persisted-options readback step")
	}
	broken := libraryManagementCloneSteps(steps)
	// Keep library-folder-ids and transport headers but drop
	// library-options-persisted. The helpers return fresh slices, so the
	// mutation cannot alias or overwrite any other test's normalizer set.
	rules := []string{"library-folder-ids"}
	rules = append(rules, libraryManagementTransportNormalizers()...)
	broken[persisted].Normalizers = rules
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a persisted-options readback without library-options-persisted")
	}
}

// TestLibraryManagementCorpusRejectsMissingOperation proves the contract test
// catches a missing operation (DELETE /Library/VirtualFolders/Paths removed).
func TestLibraryManagementCorpusRejectsMissingOperation(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	pathsRemove := libraryManagementFindPathsRemoveStep(steps)
	if pathsRemove < 0 {
		t.Fatal("committed corpus has no DELETE /Library/VirtualFolders/Paths step")
	}
	broken := append(append([]Step(nil), steps[:pathsRemove]...), steps[pathsRemove+1:]...)
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a missing DELETE /Library/VirtualFolders/Paths operation")
	}
}

// TestLibraryManagementCorpusRejectsChangedCycleCleanupFolder proves the
// contract test catches a create/readback/delete cycle whose cleanup targets a
// different folder than its create. The cleanup no longer tears down the folder
// the cycle created, so the dependency chain is broken.
func TestLibraryManagementCorpusRejectsChangedCycleCleanupFolder(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindPlainCycle(steps, 0)
	if cycle < 0 {
		t.Fatal("committed corpus has no plain collectionType cycle")
	}
	broken := libraryManagementCloneSteps(steps)
	// The cleanup is the third step of the tight triple. Change its folder.
	broken[cycle+2].Params = map[string]any{"name": "WrongFolder"}
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a cycle whose cleanup folder differs from its create")
	}
}

// TestLibraryManagementCorpusRejectsChangedCycleCleanupRoute proves the
// contract test catches a create/readback/delete cycle whose cleanup is moved
// to the /Library/VirtualFolders/Paths route, breaking the cycle's required
// main-route cleanup.
func TestLibraryManagementCorpusRejectsChangedCycleCleanupRoute(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindPlainCycle(steps, 0)
	if cycle < 0 {
		t.Fatal("committed corpus has no plain collectionType cycle")
	}
	broken := libraryManagementCloneSteps(steps)
	// Move the cleanup onto the Paths route; the cycle no longer cleans up on
	// /Library/VirtualFolders.
	broken[cycle+2].Path = "/Library/VirtualFolders/Paths"
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a cycle whose cleanup route differs from /Library/VirtualFolders")
	}
}

// libraryManagementFindNumericCycle returns the start index of the first
// numeric collectionType cycle (a tight add->GET->DELETE triple on
// /Library/VirtualFolders whose create has a name and whose collectionType
// param matches the given numeric spelling), starting the search at `from`.
// It returns -1 when no such cycle remains.
func libraryManagementFindNumericCycle(steps []Step, from int, spelling string) int {
	for i := from; i+2 < len(steps); i++ {
		add, readback, cleanup := steps[i], steps[i+1], steps[i+2]
		if add.Method != "POST" || add.Path != "/Library/VirtualFolders" {
			continue
		}
		if _, ok := libraryManagementStepName(add); !ok {
			continue
		}
		if readback.Method != "GET" || readback.Path != "/Library/VirtualFolders" {
			continue
		}
		if cleanup.Method != "DELETE" || cleanup.Path != "/Library/VirtualFolders" {
			continue
		}
		ct, ok := add.Params["collectionType"].(string)
		if !ok || ct != spelling {
			continue
		}
		return i
	}
	return -1
}

// TestLibraryManagementCorpusRejectsRemovedNumericTriple proves the contract
// test catches a removed numeric collectionType cycle. The entire create,
// readback, and delete triple for one required numeric spelling is removed so
// the cycle count no longer matches the required matrix.
func TestLibraryManagementCorpusRejectsRemovedNumericTriple(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "0")
	if cycle < 0 {
		t.Fatal("committed corpus has no numeric collectionType 0 cycle")
	}
	broken := append(append([]Step(nil), steps[:cycle]...), steps[cycle+3:]...)
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a removed numeric collectionType triple")
	}
}

// TestLibraryManagementCorpusRejectsChangedNumericReadbackLabel proves the
// contract test catches a numeric collectionType readback whose input-to-
// canonical mapping label is changed. The readback step name for the numeric
// 0 (movies) cycle is rewritten to state the wrong canonical value so the
// contract detects the mismatch.
func TestLibraryManagementCorpusRejectsChangedNumericReadbackLabel(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "0")
	if cycle < 0 {
		t.Fatal("committed corpus has no numeric collectionType 0 cycle")
	}
	broken := libraryManagementCloneSteps(steps)
	// The readback is the second step of the tight triple. Change the
	// canonical value in its name from (movies) to (tvshows).
	broken[cycle+1].Name = "readback collectionType numeric 0 (tvshows)"
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a changed numeric readback canonical label")
	}
}

// TestLibraryManagementCorpusRejectsRemovedAlternateTriple proves the contract
// test catches a removed accepted alternate-spelling cycle. The entire create,
// readback, and delete triple for the Movies spelling is removed so the cycle
// count no longer matches the required matrix.
func TestLibraryManagementCorpusRejectsRemovedAlternateTriple(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "Movies")
	if cycle < 0 {
		t.Fatal("committed corpus has no alternate collectionType Movies cycle")
	}
	broken := append(append([]Step(nil), steps[:cycle]...), steps[cycle+3:]...)
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a removed alternate collectionType triple")
	}
}

// TestLibraryManagementCorpusRejectsChangedAlternateReadbackLabel proves the
// contract test catches an alternate collectionType readback whose
// input-to-canonical mapping label is changed. The readback step name for the
// Movies (movies) cycle is rewritten to state the wrong canonical value.
func TestLibraryManagementCorpusRejectsChangedAlternateReadbackLabel(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "Movies")
	if cycle < 0 {
		t.Fatal("committed corpus has no alternate collectionType Movies cycle")
	}
	broken := libraryManagementCloneSteps(steps)
	// The readback is the second step of the tight triple. Change the
	// canonical value in its name from (movies) to (tvshows).
	broken[cycle+1].Name = "readback collectionType alternate Movies (tvshows)"
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a changed alternate readback canonical label")
	}
}

// TestLibraryManagementCorpusRejectsRemovedRejectedTriple proves the contract
// test catches a removed rejected-spelling cycle. The entire create, readback,
// and delete triple for the 1.0 spelling is removed so the cycle count no longer
// matches the required matrix.
func TestLibraryManagementCorpusRejectsRemovedRejectedTriple(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "1.0")
	if cycle < 0 {
		t.Fatal("committed corpus has no rejected collectionType 1.0 cycle")
	}
	broken := append(append([]Step(nil), steps[:cycle]...), steps[cycle+3:]...)
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a removed rejected collectionType triple")
	}
}

// TestLibraryManagementCorpusRejectsChangedRejectedReadbackLabel proves the
// contract test catches a rejected collectionType readback whose no-marker
// label is changed. The readback step name for the 1.0 (no marker) cycle is
// rewritten to state the wrong result.
func TestLibraryManagementCorpusRejectsChangedRejectedReadbackLabel(t *testing.T) {
	steps := libraryManagementCorpusSteps(t)
	cycle := libraryManagementFindNumericCycle(steps, 0, "1.0")
	if cycle < 0 {
		t.Fatal("committed corpus has no rejected collectionType 1.0 cycle")
	}
	broken := libraryManagementCloneSteps(steps)
	// The readback is the second step of the tight triple. Change the
	// result label from (no marker) to (movies).
	broken[cycle+1].Name = "readback collectionType rejected 1.0 (movies)"
	if err := requireLibraryManagementCorpusContract(broken); err == nil {
		t.Fatal("contract accepted a changed rejected readback no-marker label")
	}
}

// TestLibraryManagementCorpusReplaysHostLocalItemIdCaptureAndPersistedOptions
// proves the corpus replays through the difftool loader and runner: the ItemId
// captured from the identity readback is substituted into the POST LibraryOptions
// body, and the persisted-options readback compares the changed option values
// (not masked by library-options-defaults).
func TestLibraryManagementCorpusReplaysHostLocalItemIdCaptureAndPersistedOptions(t *testing.T) {
	left := newLibraryManagementReplayHost(t, "left-folder-id", true, true)
	right := newLibraryManagementReplayHost(t, "right-folder-id", true, true)

	result, err := Run(context.Background(), Config{
		Corpus:     filepath.Join("..", "..", "corpora", "library-management.yaml"),
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(t.TempDir(), "results"),
		IssuesDir:  filepath.Join(t.TempDir(), "issues"),
		LeftAuth:   AuthConfig{Role: "admin", Username: "left-admin", Password: "left-pw"},
		RightAuth:  AuthConfig{Role: "admin", Username: "right-admin", Password: "right-pw"},
	})
	if err != nil {
		t.Fatalf("corpus replay: %v", err)
	}
	for _, step := range result.Steps {
		if step.Status != "pass" {
			t.Fatalf("corpus replay step %q = %s, want pass", step.Step, step.Status)
		}
	}

	// The captured ItemId from the left identity readback must be substituted into
	// the left POST LibraryOptions body; the right host must receive its own ItemId.
	if left.lastOptionsID != "left-folder-id" {
		t.Fatalf("left POST LibraryOptions Id = %q, want left-folder-id (captured ItemId)", left.lastOptionsID)
	}
	if right.lastOptionsID != "right-folder-id" {
		t.Fatalf("right POST LibraryOptions Id = %q, want right-folder-id (captured ItemId)", right.lastOptionsID)
	}
}

// TestLibraryManagementCorpusReplayDetectsPersistedOptionMismatch proves the
// persisted-options readback catches a Deadfin that does not persist the same
// option values as stock. The replay must fail on the persisted-options readback
// step (not pass 45/45), demonstrating the sensitivity the masking defect erased.
func TestLibraryManagementCorpusReplayDetectsPersistedOptionMismatch(t *testing.T) {
	left := newLibraryManagementReplayHost(t, "left-folder-id", true, true)
	right := newLibraryManagementReplayHost(t, "right-folder-id", false, false)

	_, err := Run(context.Background(), Config{
		Corpus:     filepath.Join("..", "..", "corpora", "library-management.yaml"),
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(t.TempDir(), "results"),
		IssuesDir:  filepath.Join(t.TempDir(), "issues"),
		LeftAuth:   AuthConfig{Role: "admin", Username: "left-admin", Password: "left-pw"},
		RightAuth:  AuthConfig{Role: "admin", Username: "right-admin", Password: "right-pw"},
	})
	if err == nil {
		t.Fatal("corpus replay must fail when the right host does not persist the same option values as the left host")
	}
	if !strings.Contains(err.Error(), "differences") {
		t.Fatalf("corpus replay error = %v, want a differences error", err)
	}
}

// --- Replay host ---

// libraryManagementReplayHost is a minimal HTTP test server that replays the
// library-management corpus against a single virtual folder. It returns the
// configured ItemId in GET /Library/VirtualFolders responses and records the Id
// received in POST /Library/VirtualFolders/LibraryOptions. Write operations
// (POST/DELETE) return 204; GET returns 200 with the folder array.
type libraryManagementReplayHost struct {
	server        *httptest.Server
	itemID        string
	enableNet     bool
	saveLocal     bool
	lastOptionsID string
}

func newLibraryManagementReplayHost(t *testing.T, itemID string, enableInternetProviders, saveLocalMetadata bool) *libraryManagementReplayHost {
	t.Helper()
	host := &libraryManagementReplayHost{
		itemID:    itemID,
		enableNet: enableInternetProviders,
		saveLocal: saveLocalMetadata,
	}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth bootstrap: POST /Users/AuthenticateByName returns an access token.
		if r.Method == "POST" && r.URL.Path == "/Users/AuthenticateByName" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"AccessToken":"replay-token","User":{"Id":"replay-user-id","Name":"ReplayAdmin"}}`)
			return
		}
		// Item-ID map: GET /Items returns an empty library for BuildItemMap.
		if r.Method == "GET" && r.URL.Path == "/Items" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
			return
		}
		switch r.Method {
		case "GET":
			if r.URL.Path == "/Library/VirtualFolders" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				fmt.Fprintf(w, `[{"Name":"ReplayProbe","ItemId":%q,"CollectionType":"movies","LibraryOptions":{"EnableInternetProviders":%t,"SaveLocalMetadata":%t,"PathInfos":[]}}]`,
					host.itemID, host.enableNet, host.saveLocal)
				return
			}
		case "POST":
			if r.URL.Path == "/Library/VirtualFolders/LibraryOptions" {
				body, _ := io.ReadAll(r.Body)
				var parsed map[string]any
				_ = json.Unmarshal(body, &parsed)
				if id, ok := parsed["Id"].(string); ok {
					host.lastOptionsID = id
				}
				w.WriteHeader(204)
				return
			}
			w.WriteHeader(204)
			return
		case "DELETE":
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(host.server.Close)
	return host
}
