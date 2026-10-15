// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRoutesWithExtra rewrites the route snapshot with the 95 default
// routes plus one extra route JSON object.
func writeRoutesWithExtra(t *testing.T, root, extra string) {
	t.Helper()
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`
	for i := 0; i < 95; i++ {
		if i > 0 {
			routes += ","
		}
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"Op` + itoa(i) + `","Anonymous":true}`
	}
	routes += "," + extra + `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
}

const nativeBuildRoute = `{"Method":"GET","Pattern":"/deadfin/v1/build","OperationId":"NATIVE:GetBuild","Anonymous":true}`

// TestNativeRouteAcceptedAndExcludedFromParity proves an exact NATIVE: route
// passes validation, never appears as a parity operation, leaves the
// Swiftfin denominator at 95, and is counted in the diagnostics line.
func TestNativeRouteAcceptedAndExcludedFromParity(t *testing.T) {
	root := makeTestRoot(t)
	baselineInputs, err := loadInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := buildLedger(baselineInputs)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Summary.SwiftfinTotal != 95 {
		t.Fatalf("baseline Swiftfin denominator = %d, want 95", baseline.Summary.SwiftfinTotal)
	}
	writeRoutesWithExtra(t, root, nativeBuildRoute)

	if err := generate(root); err != nil {
		t.Fatalf("generate failed with a native route: %v", err)
	}

	parityJSON, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parity parityDoc
	if err := json.Unmarshal(parityJSON, &parity); err != nil {
		t.Fatal(err)
	}
	if len(parity.Operations) != 95 {
		t.Fatalf("operations = %d, want 95 (native route must not be a parity operation)", len(parity.Operations))
	}
	for _, op := range parity.Operations {
		if strings.HasPrefix(op.ID, "NATIVE:") {
			t.Fatalf("parity.json contains native operation %q", op.ID)
		}
	}
	if parity.Source.RouteCount != 96 {
		t.Fatalf("route_count = %d, want 96 (snapshot includes the native route)", parity.Source.RouteCount)
	}
	if !reflect.DeepEqual(parity.Operations, baseline.Parity.Operations) {
		t.Fatal("native route changed compatibility operations or their parity status")
	}
	nativeInputs, err := loadInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	native, err := buildLedger(nativeInputs)
	if err != nil {
		t.Fatal(err)
	}
	if native.Summary.SwiftfinTotal != 95 || native.Summary.SwiftfinDone != baseline.Summary.SwiftfinDone {
		t.Fatalf("native route changed Swiftfin fraction: %d/%d, baseline %d/95",
			native.Summary.SwiftfinDone, native.Summary.SwiftfinTotal, baseline.Summary.SwiftfinDone)
	}

	md, err := os.ReadFile(filepath.Join(root, "ledger", "PARITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Registered routes alone are not evidence of completed parity.
	for _, want := range []string{"Native routes (excluded from parity): 1", fmt.Sprintf("Done: %d / 95", baseline.Summary.SwiftfinDone), "Unknown exported routes: 0"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("PARITY.md must contain %q", want)
		}
	}
}

// TestNativeRouteInvalidSuffixRejected proves the NATIVE: class is exact:
// a lowercase or empty suffix is neither native nor a known operation.
func TestNativeRouteInvalidSuffixRejected(t *testing.T) {
	for _, id := range []string{"NATIVE:getBuild", "NATIVE:", "NATIVE:Get-Build"} {
		t.Run(id, func(t *testing.T) {
			root := makeTestRoot(t)
			writeRoutesWithExtra(t, root, `{"Method":"GET","Pattern":"/deadfin/v1/x","OperationId":"`+id+`","Anonymous":true}`)
			err := generate(root)
			if err == nil {
				t.Fatalf("generation must fail for %q", id)
			}
			if !strings.Contains(err.Error(), "not a valid native operation ID") {
				t.Fatalf("error must name the invalid native ID; got: %v", err)
			}
		})
	}
}

// TestNativeResultStepRejected proves a difftool result naming a native
// operation fails: native routes carry no parity evidence.
func TestNativeResultStepRejected(t *testing.T) {
	root := makeTestRoot(t)
	writeRoutesWithExtra(t, root, nativeBuildRoute)
	result := `{"format":"difftool-results/v1","run_id":"test-run-1","steps":[{"scenario":"s0","step":"step1","operation-id":"NATIVE:GetBuild","status":"pass"}]}`
	if err := os.WriteFile(filepath.Join(root, "results", "test.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail when a result names a native operation")
	}
	if !strings.Contains(err.Error(), "native routes are not parity operations") {
		t.Fatalf("error must explain the native exclusion; got: %v", err)
	}
}
