// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// routeManifest is the shape of testdata/required-operations.json, a
// snapshot of every operation object in the pinned OpenAPI spec.
type routeManifest struct {
	Header     routeManifestHeader `json:"header"`
	Operations []routeManifestOp   `json:"operations"`
}

type routeManifestHeader struct {
	SpecVersion  string `json:"specVersion"`
	Source       string `json:"source"`
	SourceSha256 string `json:"sourceSha256"`
	GeneratedBy  string `json:"generatedBy"`
}

type routeManifestOp struct {
	OperationID string `json:"operationId"`
	Method      string `json:"method"`
	Path        string `json:"path"`
}

// routeTriple is the (operationId, method, path) key that identifies one
// pinned OpenAPI operation. The coverage checker compares all three fields
// so that a route registered with the correct OperationID but a wrong method
// or path does not pass the coverage check.
type routeTriple struct {
	OperationID string
	Method      string
	Path        string
}

// routeOmission is one entry in testdata/route-omissions.json, a
// hand-curated list of operations that deadfin does not register.
//
// The observed block records that a real client requested the omitted
// operation. Evidence cites the capture source and date. Issue names the
// forge issue that owns the registration. The person who files or links the
// owning issue adds the block in the same PR. Observed facts do not expire on
// a timer: an observation is history, not state. The block leaves the file
// only when the entry leaves: registration (property 2) or manifest removal
// (property 3).
type routeOmission struct {
	OperationID string                 `json:"operationId"`
	Reason      string                 `json:"reason"`
	Observed    *routeOmissionObserved `json:"observed,omitempty"`
}

// routeOmissionObserved records that a real client requested the omitted
// operation. Evidence cites the capture source and date. Issue names the
// forge issue that owns the registration.
type routeOmissionObserved struct {
	Evidence string `json:"evidence"`
	Issue    string `json:"issue"`
}

// TestSpecRouteCoverage asserts that every operation in the pinned OpenAPI
// manifest is either registered or listed as a reasoned omission, and that
// the omission list stays in sync with the registered routes.
func TestSpecRouteCoverage(t *testing.T) {
	manifest := loadRouteManifest(t)
	omissions := loadRouteOmissions(t)

	registered := registeredRoutes(t)
	registeredIDs := make(map[string]bool, len(registered))
	for r := range registered {
		registeredIDs[r.OperationID] = true
	}
	omitted := make(map[string]bool, len(omissions))
	for _, o := range omissions {
		omitted[o.OperationID] = true
	}

	manifestIDs := make(map[string]bool, len(manifest.Operations))
	for _, op := range manifest.Operations {
		manifestIDs[op.OperationID] = true
	}

	// Property 1: every manifest operation is registered or omitted. The
	// check compares the full (operationId, method, path) triple, not the
	// OperationID alone, so a route registered with the correct OperationID
	// but a wrong method or path does not pass.
	uncovered := uncoveredOperationIDs(manifest.Operations, registered, omitted)
	if len(uncovered) > 0 {
		slices.Sort(uncovered)
		t.Fatalf("%d manifest operation(s) are neither registered nor omitted: %s\n"+
			"Register each route or add a reasoned entry to testdata/route-omissions.json.",
			len(uncovered), uncovered)
	}

	// Property 2: no omission names a registered operation (stale omissions fail).
	var stale []string
	for _, o := range omissions {
		if registeredIDs[o.OperationID] {
			stale = append(stale, o.OperationID)
		}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Fatalf("%d omitted operation(s) are now registered — remove them from testdata/route-omissions.json: %s",
			len(stale), stale)
	}

	// Property 3: every omission names a manifest operation (dangling omissions fail).
	var dangling []string
	for _, o := range omissions {
		if !manifestIDs[o.OperationID] {
			dangling = append(dangling, o.OperationID)
		}
	}
	if len(dangling) > 0 {
		slices.Sort(dangling)
		t.Fatalf("%d omitted operation(s) are not in the manifest — delete them from testdata/route-omissions.json: %s",
			len(dangling), dangling)
	}
	// Property 4: every observed omission carries a complete block (non-empty
	// evidence and non-empty issue). A violation fails so a misspelled or
	// incomplete observed entry cannot pass silently.
	if violations := incompleteObservedOmissions(omissions); len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("%d observed omission(s) lack evidence or issue — register the route or complete the observed block in testdata/route-omissions.json: %s",
			len(violations), violations)
	}
}

// uncoveredOperationIDs implements property 1 of the coverage check: it
// returns the operationId of each manifest operation that is neither
// registered nor omitted. The registration lookup compares the full
// (operationId, method, path) triple, so a wrong method or path causes the
// operation to appear uncovered even when its OperationID is registered.
func uncoveredOperationIDs(ops []routeManifestOp, registered map[routeTriple]bool, omitted map[string]bool) []string {
	var uncovered []string
	for _, op := range ops {
		if registered[routeTriple{op.OperationID, op.Method, op.Path}] || omitted[op.OperationID] {
			continue
		}
		uncovered = append(uncovered, op.OperationID)
	}
	return uncovered
}

// incompleteObservedOmissions implements property 4 of the coverage check: it
// returns the operationId of each omission whose observed block is present but
// lacks a non-empty evidence or a non-empty issue. The check is pure so a
// synthetic regression test covers it without touching the real file.
func incompleteObservedOmissions(omissions []routeOmission) []string {
	var violations []string
	for _, o := range omissions {
		if o.Observed == nil {
			continue
		}
		if strings.TrimSpace(o.Observed.Evidence) == "" || strings.TrimSpace(o.Observed.Issue) == "" {
			violations = append(violations, o.OperationID)
		}
	}
	return violations
}

// TestRouteCoverageRejectsWrongMethodOrPath proves that the coverage check
// compares the full (operationId, method, path) triple, not the OperationID
// alone. It runs property 1 against a synthetic manifest that pairs one
// genuinely registered route with two entries that share its OperationID but
// have a wrong method or a wrong path. The checker must leave the wrong
// entries uncovered while it covers the matching triple. This is the
// regression for the route-coverage/triple-match finding.
func TestRouteCoverageRejectsWrongMethodOrPath(t *testing.T) {
	registered := registeredRoutes(t)
	omitted := map[string]bool{}

	// PostCapabilities is registered as POST /Sessions/Capabilities. The
	// matching manifest entry must stay covered. The wrong-method and
	// wrong-path entries share the OperationID but diverge on a second
	// field, so the triple lookup must miss and property 1 must report them
	// uncovered.
	anchor := routeTriple{"PostCapabilities", http.MethodPost, "/Sessions/Capabilities"}
	if !registered[anchor] {
		t.Fatalf("anchor %v is not registered; pick a different registered route", anchor)
	}

	matching := routeManifestOp{OperationID: "PostCapabilities", Method: http.MethodPost, Path: "/Sessions/Capabilities"}
	wrongMethod := routeManifestOp{OperationID: "PostCapabilities", Method: http.MethodGet, Path: "/Sessions/Capabilities"}
	wrongPath := routeManifestOp{OperationID: "PostCapabilities", Method: http.MethodPost, Path: "/Sessions/Capabilities/Full"}

	// The matching triple alone must be fully covered.
	if got := uncoveredOperationIDs([]routeManifestOp{matching}, registered, omitted); len(got) != 0 {
		t.Fatalf("matching triple must be covered, got uncovered %v", got)
	}

	// The wrong-method and wrong-path entries must each be uncovered. They
	// share the OperationID with the registered route, so an OperationID-only
	// check would wrongly pass. The triple check must reject both.
	if got := uncoveredOperationIDs([]routeManifestOp{wrongMethod}, registered, omitted); len(got) != 1 {
		t.Errorf("wrong-method entry must be uncovered (got %d, want 1): %v", len(got), got)
	}
	if got := uncoveredOperationIDs([]routeManifestOp{wrongPath}, registered, omitted); len(got) != 1 {
		t.Errorf("wrong-path entry must be uncovered (got %d, want 1): %v", len(got), got)
	}
}

// TestRouteCoverageObservedBlockValidation proves that property 4 rejects an
// observed omission whose block lacks evidence or issue, and that a complete
// block passes. The check runs against synthetic omissions so it does not
// touch the real route-omissions.json file.
func TestRouteCoverageObservedBlockValidation(t *testing.T) {
	complete := routeOmission{
		OperationID: "ObservedComplete",
		Observed:    &routeOmissionObserved{Evidence: "ingress log 2026-08-12", Issue: "issue-1"},
	}
	missingEvidence := routeOmission{
		OperationID: "ObservedMissingEvidence",
		Observed:    &routeOmissionObserved{Evidence: "", Issue: "issue-2"},
	}
	missingIssue := routeOmission{
		OperationID: "ObservedMissingIssue",
		Observed:    &routeOmissionObserved{Evidence: "ingress log 2026-08-12", Issue: ""},
	}
	blankObserved := routeOmission{
		OperationID: "ObservedBlank",
		Observed:    &routeOmissionObserved{Evidence: "  ", Issue: "issue-4"},
	}
	noObserved := routeOmission{OperationID: "CleanDeferral"}

	if got := incompleteObservedOmissions([]routeOmission{complete}); len(got) != 0 {
		t.Fatalf("complete observed block must pass, got violations %v", got)
	}
	if got := incompleteObservedOmissions([]routeOmission{noObserved}); len(got) != 0 {
		t.Fatalf("omission without observed block must pass, got violations %v", got)
	}

	want := []string{"ObservedBlank", "ObservedMissingEvidence", "ObservedMissingIssue"}
	got := incompleteObservedOmissions([]routeOmission{complete, missingEvidence, missingIssue, blankObserved, noObserved})
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("incompleteObservedOmissions = %v, want %v", got, want)
	}
}

// registeredRoutes returns the set of (operationId, method, path) triples
// for every registered non-shadow route. The path comes from the route
// Pattern, which uses the same template syntax as the pinned OpenAPI
// manifest (for example "/Sessions/{sessionId}/Playing").
func registeredRoutes(t *testing.T) map[routeTriple]bool {
	t.Helper()
	routes := routeLedger().Routes()
	out := make(map[routeTriple]bool, len(routes))
	for _, r := range routes {
		if isShadowOperation(r.OperationID) {
			continue
		}
		out[routeTriple{r.OperationID, r.Method, r.Pattern}] = true
	}
	return out
}

// isShadowOperation reports whether the operation ID marks a legacy alias
// route that has no counterpart in the pinned OpenAPI manifest.
func isShadowOperation(operationID string) bool {
	return strings.HasPrefix(operationID, "SHADOW:")
}

func loadRouteManifest(t *testing.T) routeManifest {
	t.Helper()
	path := filepath.Join("testdata", "required-operations.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m routeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

func loadRouteOmissions(t *testing.T) []routeOmission {
	t.Helper()
	path := filepath.Join("testdata", "route-omissions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var o []routeOmission
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return o
}
