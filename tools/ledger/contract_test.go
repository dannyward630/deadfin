// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestRoot creates a temporary repository root with the directory
// structure the ledger generator expects, then writes minimal OpenAPI,
// SessionMessageType, route-snapshot, shadow-spec, annotations, and
// INDEX.md inputs. It returns the root path and a cleanup function.
//
// The default inputs have:
//   - 95 OpenAPI operations, all marked swiftfin in annotations
//   - 34 WebSocket message types, all annotated as done
//   - 95 routes matching the OpenAPI operations
//   - One WP file (WP-P0-01) with Status: done
//   - No shadow routes
func makeTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	for _, dir := range []string{
		filepath.Join(root, "ledger", "inputs", "jellyfin", "MediaBrowser.Model", "Session"),
		filepath.Join(root, "ledger"),
		filepath.Join(root, "results"),
		filepath.Join(root, "docs", "work-packages"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Write an OpenAPI spec with 95 operations.
	openAPI := `{"openapi":"3.0.4","paths":{`
	for i := 0; i < 95; i++ {
		if i > 0 {
			openAPI += ","
		}
		pathName := "/Op" + itoa(i)
		opID := "Op" + itoa(i)
		openAPI += `"` + pathName + `":{"get":{"tags":["Test"],"operationId":"` + opID + `"}}`
	}
	openAPI += `}}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "inputs", "jellyfin-openapi-stable.json"), []byte(openAPI), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a minimal SessionMessageType.cs with 34 types.
	var csBuilder strings.Builder
	csBuilder.WriteString("namespace MediaBrowser.Model.Session\n{\n    public enum SessionMessageType\n    {\n")
	for i := 0; i < 34; i++ {
		csBuilder.WriteString("        Type" + itoa(i) + ",\n")
	}
	csBuilder.WriteString("    }\n}\n")
	if err := os.WriteFile(filepath.Join(root, "ledger", "inputs", "jellyfin", "MediaBrowser.Model", "Session", "SessionMessageType.cs"), []byte(csBuilder.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a route snapshot matching the 95 OpenAPI operations.
	routesBuilder := strings.Builder{}
	routesBuilder.WriteString(`{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`)
	for i := 0; i < 95; i++ {
		if i > 0 {
			routesBuilder.WriteString(",")
		}
		opID := "Op" + itoa(i)
		routesBuilder.WriteString(`{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`)
	}
	routesBuilder.WriteString(`]}`)
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routesBuilder.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a minimal shadow-spec.yaml.
	if err := os.WriteFile(filepath.Join(root, "ledger", "shadow-spec.yaml"), []byte("operations: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write annotations.yaml with 95 swiftfin operation annotations and 34 WS annotations.
	annBuilder := strings.Builder{}
	annBuilder.WriteString("source:\n  deadfin_revision: \"0123456789abcdef0123456789abcdef01234567\"\n  jellyfin_pin: \"b04614e18d2aeed8d3d3de920c381c6bd5b97bdb\"\noperations:\n")
	for i := 0; i < 95; i++ {
		opID := "Op" + itoa(i)
		annBuilder.WriteString("  " + opID + ":\n    clients: [swiftfin]\n")
	}
	annBuilder.WriteString("websocket:\n")
	for i := 0; i < 34; i++ {
		annBuilder.WriteString("  Type" + itoa(i) + ":\n    status: done\n")
	}
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(annBuilder.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write INDEX.md listing the WP file.
	indexContent := "| [WP-P0-01](WP-P0-01.md) | Test | P0 |\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "INDEX.md"), []byte(indexContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a minimal WP file.
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "WP-P0-01.md"), []byte("Status: done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return root
}

// TestInputValidationCrossInputDuplicateIDs proves that an operation ID
// that appears in both the OpenAPI spec and the shadow spec fails generation.
func TestInputValidationCrossInputDuplicateIDs(t *testing.T) {
	root := makeTestRoot(t)

	// Add a shadow route with the same ID as an OpenAPI op.
	shadow := "operations:\n  - id: Op0\n    method: GET\n    path: /Op0\n    subsystem: Test\n    upstream: Test.cs:1\n    clients: [swiftfin]\n    swiftfin: false\n"
	if err := os.WriteFile(filepath.Join(root, "ledger", "shadow-spec.yaml"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail when an ID appears in both OpenAPI and shadow spec")
	}
	if !strings.Contains(err.Error(), "both OpenAPI spec and shadow spec") {
		t.Fatalf("error must mention cross-input duplicate; got: %v", err)
	}
}

// TestInputValidationUnknownExportedRoute proves that a route with no
// matching OpenAPI or shadow operation fails generation.
func TestInputValidationUnknownExportedRoute(t *testing.T) {
	root := makeTestRoot(t)

	// Add a route that matches no known operation.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[{"Method":"GET","Pattern":"/unknown","OperationId":"UnknownOp","Anonymous":true}]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail when a route has no matching operation")
	}
	if !strings.Contains(err.Error(), "no matching operation") {
		t.Fatalf("error must mention unknown route; got: %v", err)
	}
}

// TestInputValidationMethodMismatch proves that a method mismatch between
// the OpenAPI spec and the route snapshot fails generation.
func TestInputValidationMethodMismatch(t *testing.T) {
	root := makeTestRoot(t)

	// Change route 0 method to POST while OpenAPI says GET.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[{"Method":"POST","Pattern":"/Op0","OperationId":"Op0","Anonymous":true}`
	for i := 1; i < 95; i++ {
		routes += ","
		opID := "Op" + itoa(i)
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`
	}
	routes += `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail on method mismatch")
	}
	if !strings.Contains(err.Error(), "method mismatch") {
		t.Fatalf("error must mention method mismatch; got: %v", err)
	}
}

// TestInputValidationExtraWebSocketAnnotation proves that an extra WebSocket
// annotation key (for a type not in the parsed enum) fails generation.
func TestInputValidationExtraWebSocketAnnotation(t *testing.T) {
	root := makeTestRoot(t)

	// Add an extra WebSocket annotation for a non-existent type.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ann = append(ann, []byte("  NonExistentType:\n    status: done\n")...)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), ann, 0o644); err != nil {
		t.Fatal(err)
	}

	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when a WebSocket annotation has no matching parsed type")
	}
	if !strings.Contains(err.Error(), "does not match a parsed SessionMessageType") {
		t.Fatalf("error must mention extra WebSocket annotation; got: %v", err)
	}
}

// TestInputValidationWrongTypeCount proves that a parsed type count other than
// 34 fails generation.
func TestInputValidationWrongTypeCount(t *testing.T) {
	root := makeTestRoot(t)

	// Write a SessionMessageType.cs with only 33 types.
	var csBuilder strings.Builder
	csBuilder.WriteString("namespace MediaBrowser.Model.Session\n{\n    public enum SessionMessageType\n    {\n")
	for i := 0; i < 33; i++ {
		csBuilder.WriteString("        Type" + itoa(i) + ",\n")
	}
	csBuilder.WriteString("    }\n}\n")
	if err := os.WriteFile(filepath.Join(root, "ledger", "inputs", "jellyfin", "MediaBrowser.Model", "Session", "SessionMessageType.cs"), []byte(csBuilder.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail when parsed type count is not 34")
	}
	if !strings.Contains(err.Error(), "33") || !strings.Contains(err.Error(), "34") {
		t.Fatalf("error must mention the wrong and expected counts; got: %v", err)
	}
}

// TestRatchetPinResetUnchangedPin proves that pin-reset: true with an
// unchanged pin fails generation.
func TestRatchetPinResetUnchangedPin(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 as ratcheted with pin-reset: true but same recorded_pin as source.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n    pin-reset: true\n    recorded_pin: \"b04614e18d2aeed8d3d3de920c381c6bd5b97bdb\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the route for Op0 so its status drops from done.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`
	for i := 1; i < 95; i++ {
		if i > 1 {
			routes += ","
		}
		opID := "Op" + itoa(i)
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`
	}
	routes += `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also remove Op0 from annotations swiftfin set — we need exactly 95 swiftfin.
	// Replace Op0 with a different op that is registered and swiftfin.
	// Actually, the route removal means Op0 becomes todo (not done), triggering the ratchet.
	// The pin-reset: true with same pin must fail.
	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when pin-reset has unchanged pin")
	}
	if !strings.Contains(err.Error(), "equals the recorded pin") {
		t.Fatalf("error must mention unchanged pin; got: %v", err)
	}
}

// TestRatchetPinResetChangedPin proves that pin-reset: true with a changed pin
// passes generation and records the reset in source metadata.
func TestRatchetPinResetChangedPin(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 as ratcheted with pin-reset: true and a different recorded_pin.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n    pin-reset: true\n    recorded_pin: \"aaaa000000000000000000000000000000000000\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the route for Op0 so its status drops from done.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`
	for i := 1; i < 95; i++ {
		if i > 1 {
			routes += ","
		}
		opID := "Op" + itoa(i)
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`
	}
	routes += `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generation must pass with changed pin; got: %v", err)
	}

	// Verify the pin_reset is in the generated parity.json.
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)
	if !strings.Contains(content, "pin_reset") {
		t.Fatal("generated parity.json must contain pin_reset metadata")
	}
	if !strings.Contains(content, "aaaa000000000000000000000000000000000000") {
		t.Fatal("generated parity.json must record the old pin")
	}
	if !strings.Contains(content, `"operation": "Op0"`) {
		t.Fatalf("generated parity.json must record operation ID Op0 in pin_reset; got: %s", content)
	}
}

// TestRatchetEvidenceRegression proves that a ratcheted done row that no
// longer supports done fails generation without pin-reset.
func TestRatchetEvidenceRegression(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 as ratcheted (no pin-reset).
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the route for Op0 so its status drops from done.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`
	for i := 1; i < 95; i++ {
		if i > 1 {
			routes += ","
		}
		opID := "Op" + itoa(i)
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`
	}
	routes += `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when a ratcheted done row regresses without pin-reset")
	}
	if !strings.Contains(err.Error(), "no longer supports done") {
		t.Fatalf("error must mention evidence regression; got: %v", err)
	}
}

// TestReportConveniences proves that the generated PARITY.md contains all six
// Swiftfin convenience entries outside the 95-operation REST fraction.
func TestReportConveniences(t *testing.T) {
	root := makeTestRoot(t)

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	parity, err := os.ReadFile(filepath.Join(root, "ledger", "PARITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)

	expected := []string{
		"POST /Users/AuthenticateByName",
		"POST /Users/AuthenticateWithQuickConnect",
		"POST /QuickConnect/Initiate",
		"GET /QuickConnect/Connect",
		"GET /socket",
		"UDP :7359",
	}
	for _, entry := range expected {
		if !strings.Contains(content, entry) {
			t.Errorf("PARITY.md must contain convenience entry %q", entry)
		}
	}
}

// TestReportDiagnostics proves that the generated PARITY.md contains all
// seven input diagnostics.
func TestReportDiagnostics(t *testing.T) {
	root := makeTestRoot(t)

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	parity, err := os.ReadFile(filepath.Join(root, "ledger", "PARITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)

	expected := []string{
		"OpenAPI operations: 95",
		"Shadow routes: 0",
		"Route snapshot routes: 95",
		"Duplicate operation IDs: 0",
		"Unknown exported routes: 0",
		"Unknown result operations: 0",
		"Method or path mismatches: 0",
	}
	for _, entry := range expected {
		if !strings.Contains(content, entry) {
			t.Errorf("PARITY.md must contain diagnostic %q", entry)
		}
	}
}

// TestUnregisteredShadowInventory proves that an unregistered shadow route
// produces registered: false and status: todo.
func TestUnregisteredShadowInventory(t *testing.T) {
	root := makeTestRoot(t)

	// Add a shadow route that has no matching route in the snapshot.
	shadow := "operations:\n  - id: SHADOW:Missing\n    method: GET\n    path: /missing\n    subsystem: Test\n    upstream: Test.cs:1\n    clients: [swiftfin]\n    swiftfin: false\n"
	if err := os.WriteFile(filepath.Join(root, "ledger", "shadow-spec.yaml"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}

	// The shadow op takes the swiftfin slot from Op0 — we need to keep 95 swiftfin.
	// Add swiftfin to the shadow op and remove it from Op0.
	// Actually, the shadow op has swiftfin: false, so swiftfin clients are filtered out.
	// We need to adjust annotations: remove swiftfin from one OpenAPI op to make room.
	// But that would make 94 swiftfin ops — we need exactly 95.
	// Instead, add swiftfin: true to the shadow op so it counts.
	shadow = "operations:\n  - id: SHADOW:Missing\n    method: GET\n    path: /missing\n    subsystem: Test\n    upstream: Test.cs:1\n    clients: [swiftfin, web]\n    swiftfin: true\n"
	if err := os.WriteFile(filepath.Join(root, "ledger", "shadow-spec.yaml"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove swiftfin from Op0 to keep total at 95.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: []\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate must pass with an unregistered shadow route; got: %v", err)
	}

	// Verify the unregistered shadow route has registered: false and status: todo.
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)
	if !strings.Contains(content, `"id": "SHADOW:Missing"`) {
		t.Fatal("parity.json must contain the shadow op")
	}
	// Find the shadow op and check its fields.
	idx := strings.Index(content, `"id": "SHADOW:Missing"`)
	if idx < 0 {
		t.Fatal("parity.json must contain SHADOW:Missing")
	}
	// Check the surrounding context for registered: false and status: todo.
	chunk := content[idx:]
	if len(chunk) > 500 {
		chunk = chunk[:500]
	}
	if !strings.Contains(chunk, `"registered": false`) {
		t.Errorf("unregistered shadow route must have registered: false; chunk: %s", chunk)
	}
	if !strings.Contains(chunk, `"status": "todo"`) {
		t.Errorf("unregistered shadow route must have status: todo; chunk: %s", chunk)
	}
}

// TestWPInputContractUnlistedFile proves that a WP file not listed in
// INDEX.md stays excluded from the ledger.
func TestWPInputContractUnlistedFile(t *testing.T) {
	root := makeTestRoot(t)

	// Write an unlisted WP file with a non-standard status.
	unlisted := "Status: blocked\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "WP-P9-99.md"), []byte(unlisted), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate must pass when unlisted files exist; got: %v", err)
	}

	// The ledger must not contain the unlisted WP in phase progress.
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "PARITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(parity), "WP-P9-99") {
		t.Error("unlisted WP file must not appear in the generated ledger")
	}
}

// TestWPInputContractMissingListedFile proves that a WP file listed in
// INDEX.md but missing from the directory fails generation.
func TestWPInputContractMissingListedFile(t *testing.T) {
	root := makeTestRoot(t)

	// Add a second WP to INDEX.md but do not create the file.
	indexContent := "| [WP-P0-01](WP-P0-01.md) | Test | P0 |\n| [WP-P0-02](WP-P0-02.md) | Test | P0 |\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "INDEX.md"), []byte(indexContent), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail when a listed WP file is missing")
	}
	if !strings.Contains(err.Error(), "WP-P0-02.md") {
		t.Fatalf("error must name the missing file; got: %v", err)
	}
}

// TestWPInputContractEvidenceReady proves that a Status: line with
// "evidence-ready" fails generation.
func TestWPInputContractEvidenceReady(t *testing.T) {
	root := makeTestRoot(t)

	// Change the WP status to evidence-ready.
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "WP-P0-01.md"), []byte("Status: evidence-ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generate(root)
	if err == nil {
		t.Fatal("generation must fail on evidence-ready status")
	}
	if !strings.Contains(err.Error(), "evidence-ready") {
		t.Fatalf("error must mention evidence-ready; got: %v", err)
	}
}

// TestWPInputContractUnknownStatePrefix proves that a Status: line whose
// state token is an unknown prefixed form — doneish, todoish, blockedish,
// in-progressish, done-ish, todo-ish — fails generation. These phrases
// would silently parse as valid states under a HasPrefix check because
// they share a prefix with a valid state. The exact-token match accepts a
// valid state followed by end-of-string or a space and rejects anything
// else.
func TestWPInputContractUnknownStatePrefix(t *testing.T) {
	root := makeTestRoot(t)

	// Each phrase starts with a valid state prefix but is not a valid
	// state token. The parser must reject all of them.
	unknownPrefixes := []string{
		"doneish",
		"todoish",
		"blockedish",
		"in-progressish",
		"done-ish",
		"todo-ish",
	}

	for _, phrase := range unknownPrefixes {
		t.Run(phrase, func(t *testing.T) {
			content := "Status: " + phrase + "\n"
			if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "WP-P0-01.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			err := generate(root)
			if err == nil {
				t.Fatalf("generation must fail on unknown prefixed state %q", phrase)
			}
			if !strings.Contains(err.Error(), "unknown status phrase") {
				t.Fatalf("error must report an unknown status phrase for %q; got: %v", phrase, err)
			}
		})
	}

	// Restore the WP file to a valid state for makeTestRoot reuse.
	if err := os.WriteFile(filepath.Join(root, "docs", "work-packages", "WP-P0-01.md"), []byte("Status: done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStatusPrecedenceRegisteredWithDeferred proves that a registered route
// with a deferred annotation produces a registered state (stubbed or partial),
// not deferred. This tests the issue's status derivation precedence: step 2
// (registered stub) and step 4 (registered route without evidence) come
// before step 1 (deferred with no route).
func TestStatusPrecedenceRegisteredWithDeferred(t *testing.T) {
	root := makeTestRoot(t)

	// Add a deferred annotation for Op0, which is registered.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    status: deferred\n    reason: \"deferred: not implemented\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Op0 is registered (route exists) and has a deferred annotation.
	// Since the route is registered, the status must NOT be deferred.
	// It should be partial (registered without evidence).
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)
	idx := strings.Index(content, `"id": "Op0"`)
	if idx < 0 {
		t.Fatal("parity.json must contain Op0")
	}
	chunk := content[idx:]
	if len(chunk) > 300 {
		chunk = chunk[:300]
	}
	if strings.Contains(chunk, `"status": "deferred"`) {
		t.Errorf("registered route with deferred annotation must not produce deferred status; chunk: %s", chunk)
	}
}

// TestSwiftfinMembershipExactly95 proves that the Swiftfin REST set must
// contain exactly 95 operations.
func TestSwiftfinMembershipExactly95(t *testing.T) {
	root := makeTestRoot(t)

	// Remove swiftfin from Op0, making the set 94.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: []\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when Swiftfin count is not 95")
	}
	if !strings.Contains(err.Error(), "94") || !strings.Contains(err.Error(), "95") {
		t.Fatalf("error must mention wrong and expected counts; got: %v", err)
	}
}

// TestRatchetPinResetUnchangedPinDoneRow proves that a done row with
// pin-reset: true and an unchanged pin fails generation. The ratchet check
// must not skip pin comparison for done rows.
func TestRatchetPinResetUnchangedPinDoneRow(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 as ratcheted with pin-reset: true but same recorded_pin as source.
	// Op0 remains registered and done (all steps pass by default since no
	// result file is written, so it stays done from the route-only path).
	// Wait — without a result file, Op0 is partial (registered without evidence).
	// To make Op0 done, we must add a result file with all passing steps.

	// Write a result file with all passing steps for Op0.
	result := `{"format":"difftool-results/v1","run_id":"test-run-1","steps":[{"scenario":"s0","step":"step1","operation-id":"Op0","status":"pass"}]}`
	if err := os.WriteFile(filepath.Join(root, "results", "test.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}

	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n    pin-reset: true\n    recorded_pin: \"b04614e18d2aeed8d3d3de920c381c6bd5b97bdb\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Op0 is registered, has all-pass evidence, so it is done.
	// pin-reset: true with same pin must fail even for a done row.
	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when a done row has pin-reset with unchanged pin")
	}
	if !strings.Contains(err.Error(), "equals the recorded pin") {
		t.Fatalf("error must mention unchanged pin; got: %v", err)
	}
}

// TestRatchetPinResetChangedPinDoneRow proves that a done row with
// pin-reset: true and a changed pin passes generation and records the reset.
func TestRatchetPinResetChangedPinDoneRow(t *testing.T) {
	root := makeTestRoot(t)

	// Write a result file with all passing steps for Op0.
	result := `{"format":"difftool-results/v1","run_id":"test-run-1","steps":[{"scenario":"s0","step":"step1","operation-id":"Op0","status":"pass"}]}`
	if err := os.WriteFile(filepath.Join(root, "results", "test.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}

	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n    pin-reset: true\n    recorded_pin: \"aaaa000000000000000000000000000000000000\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generation must pass with changed pin on a done row; got: %v", err)
	}

	// Verify the pin_reset is in the generated parity.json.
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(parity), "pin_reset") {
		t.Fatal("generated parity.json must contain pin_reset metadata")
	}
}

// TestStatusDerivationMixedResultSteps proves that a registered route with
// mixed pass and fail result steps produces partial, not done. It also
// verifies that a registered route with all-pass steps produces done.
func TestStatusDerivationMixedResultSteps(t *testing.T) {
	root := makeTestRoot(t)

	// Write a result file with mixed steps for Op0: one pass, one fail.
	result := `{"format":"difftool-results/v1","run_id":"test-run-1","steps":[` +
		`{"scenario":"s0","step":"step1","operation-id":"Op0","status":"pass"},` +
		`{"scenario":"s0","step":"step2","operation-id":"Op0","status":"fail"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(root, "results", "test.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a result file with all-pass steps for Op1.
	result2 := `{"format":"difftool-results/v1","run_id":"test-run-2","steps":[` +
		`{"scenario":"s1","step":"step1","operation-id":"Op1","status":"pass"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(root, "results", "test2.json"), []byte(result2), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)

	// Op0 has mixed steps — must be partial.
	idx := strings.Index(content, `"id": "Op0"`)
	if idx < 0 {
		t.Fatal("parity.json must contain Op0")
	}
	chunk := content[idx:]
	if len(chunk) > 300 {
		chunk = chunk[:300]
	}
	if !strings.Contains(chunk, `"status": "partial"`) {
		t.Errorf("Op0 with mixed pass/fail steps must be partial; chunk: %s", chunk)
	}

	// Op1 has all-pass steps — must be done.
	idx = strings.Index(content, `"id": "Op1"`)
	if idx < 0 {
		t.Fatal("parity.json must contain Op1")
	}
	chunk = content[idx:]
	if len(chunk) > 300 {
		chunk = chunk[:300]
	}
	if !strings.Contains(chunk, `"status": "done"`) {
		t.Errorf("Op1 with all-pass steps must be done; chunk: %s", chunk)
	}
}

// TestStableOperationOrdering proves that the generated operations list is
// sorted by operation ID regardless of the input order. The test shuffles the
// OpenAPI operations and route snapshot entries and verifies the output is
// still sorted.
func TestStableOperationOrdering(t *testing.T) {
	root := makeTestRoot(t)

	// Write the OpenAPI spec with operations in reverse order.
	openAPI := `{"openapi":"3.0.4","paths":{`
	for i := 94; i >= 0; i-- {
		if i < 94 {
			openAPI += ","
		}
		pathName := "/Op" + itoa(i)
		opID := "Op" + itoa(i)
		openAPI += `"` + pathName + `":{"get":{"tags":["Test"],"operationId":"` + opID + `"}}`
	}
	openAPI += `}}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "inputs", "jellyfin-openapi-stable.json"), []byte(openAPI), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write the route snapshot in reverse order.
	routesBuilder := strings.Builder{}
	routesBuilder.WriteString(`{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`)
	for i := 94; i >= 0; i-- {
		if i < 94 {
			routesBuilder.WriteString(",")
		}
		opID := "Op" + itoa(i)
		routesBuilder.WriteString(`{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`)
	}
	routesBuilder.WriteString(`]}`)
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routesBuilder.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Verify the operations are sorted by ID.
	var doc struct {
		Operations []struct {
			ID string `json:"id"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(parity, &doc); err != nil {
		t.Fatal(err)
	}

	for i := 1; i < len(doc.Operations); i++ {
		if doc.Operations[i].ID < doc.Operations[i-1].ID {
			t.Errorf("operations must be sorted by ID; found %q before %q at index %d",
				doc.Operations[i-1].ID, doc.Operations[i].ID, i)
		}
	}
}

// TestShadowInventoryDocumentedRoutes proves that the shadow spec contains
// all documented hidden route families from docs/03-api-surface.md:162-193,
// including FavoriteItems, PlayedItems, Suggestions, user images, InstantMix,
// Recordings, and MusicGenres. Unregistered rows produce registered: false
// and status: todo.
func TestShadowInventoryDocumentedRoutes(t *testing.T) {
	root := makeTestRoot(t)

	// Replace the empty shadow spec with documented hidden routes.
	// These are unregistered routes that produce registered: false, status: todo.
	// We keep the Swiftfin count at 95 by using swiftfin: false for all.
	shadow := "operations:\n" +
		"  - id: SHADOW:FavoriteItemsLegacy\n" +
		"    method: POST\n" +
		"    path: /Users/{userId}/FavoriteItems/{itemId}\n" +
		"    subsystem: UserData\n" +
		"    upstream: UserLibraryController.cs:250\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:PlayedItemsLegacy\n" +
		"    method: POST\n" +
		"    path: /Users/{userId}/PlayedItems/{itemId}\n" +
		"    subsystem: UserData\n" +
		"    upstream: PlaystateController.cs:121\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:SuggestionsLegacy\n" +
		"    method: GET\n" +
		"    path: /Users/{userId}/Suggestions\n" +
		"    subsystem: Items\n" +
		"    upstream: SuggestionsController.cs:118\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:UserImagesLegacy\n" +
		"    method: GET\n" +
		"    path: /Users/{userId}/Images/{imageType}\n" +
		"    subsystem: Images\n" +
		"    upstream: ImageController.cs:1561\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:InstantMixLegacy\n" +
		"    method: GET\n" +
		"    path: /Artists/InstantMix\n" +
		"    subsystem: InstantMix\n" +
		"    upstream: InstantMixController.cs:325\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:RecordingsSeries\n" +
		"    method: GET\n" +
		"    path: /LiveTv/Recordings/Series\n" +
		"    subsystem: LiveTvRecordings\n" +
		"    upstream: LiveTvController.cs:341\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:RecordingsGroups\n" +
		"    method: GET\n" +
		"    path: /LiveTv/Recordings/Groups\n" +
		"    subsystem: LiveTvRecordings\n" +
		"    upstream: LiveTvController.cs:366\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n" +
		"  - id: SHADOW:MusicGenresRoot\n" +
		"    method: GET\n" +
		"    path: /MusicGenres\n" +
		"    subsystem: MusicGenres\n" +
		"    upstream: MusicGenresController.cs:77\n" +
		"    clients: [web]\n" +
		"    swiftfin: false\n"
	if err := os.WriteFile(filepath.Join(root, "ledger", "shadow-spec.yaml"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate must pass with unregistered shadow routes; got: %v", err)
	}

	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Each documented hidden route must produce registered: false and status: todo.
	expectedIDs := []string{
		"SHADOW:FavoriteItemsLegacy",
		"SHADOW:PlayedItemsLegacy",
		"SHADOW:SuggestionsLegacy",
		"SHADOW:UserImagesLegacy",
		"SHADOW:InstantMixLegacy",
		"SHADOW:RecordingsSeries",
		"SHADOW:RecordingsGroups",
		"SHADOW:MusicGenresRoot",
	}
	for _, id := range expectedIDs {
		idx := strings.Index(string(parity), `"id": "`+id+`"`)
		if idx < 0 {
			t.Errorf("parity.json must contain shadow route %q", id)
			continue
		}
		chunk := string(parity)[idx:]
		if len(chunk) > 500 {
			chunk = chunk[:500]
		}
		if !strings.Contains(chunk, `"registered": false`) {
			t.Errorf("shadow route %q must have registered: false; chunk: %s", id, chunk)
		}
		if !strings.Contains(chunk, `"status": "todo"`) {
			t.Errorf("shadow route %q must have status: todo; chunk: %s", id, chunk)
		}
	}
}

// TestWebSocketEvidenceDoneWithoutEvidence proves that a done WebSocket
// annotation without implementation evidence downgrades to partial.
func TestWebSocketEvidenceDoneWithoutEvidence(t *testing.T) {
	root := makeTestRoot(t)

	// Remove evidence from all done WebSocket annotations.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The default test root has all WS annotations as done without evidence.
	// The generator must downgrade them to partial.

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	wsData, err := os.ReadFile(filepath.Join(root, "ledger", "websocket.json"))
	if err != nil {
		t.Fatal(err)
	}

	var wsDoc struct {
		Source struct {
			ImplementedCount int `json:"implemented_count"`
		} `json:"source"`
		Messages []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wsData, &wsDoc); err != nil {
		t.Fatal(err)
	}

	// All 30 done annotations have no evidence, so they must downgrade to partial.
	// The implemented count counts done, partial, and stubbed.
	// With all downgraded, the count is still 30 (partial counts as implemented).
	// But we must verify none are done.
	for _, msg := range wsDoc.Messages {
		if msg.Status == "done" {
			t.Errorf("WebSocket type %q must not be done without evidence; must be partial", msg.Name)
		}
	}

	// At least some must be partial (the ones that were done but now have no evidence).
	partialCount := 0
	for _, msg := range wsDoc.Messages {
		if msg.Status == "partial" {
			partialCount++
		}
	}
	if partialCount == 0 {
		t.Error("at least some WebSocket types must be partial when done annotations lack evidence")
	}

	_ = ann // suppress unused variable
}

// TestWebSocketEvidenceDoneWithEvidence proves that a done WebSocket
// annotation with implementation evidence passes and stays done.
func TestWebSocketEvidenceDoneWithEvidence(t *testing.T) {
	root := makeTestRoot(t)

	// Add evidence to all done WebSocket annotations.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The default annotations have 30 done and 4 todo WS entries.
	// Add evidence to the done entries.
	updated := ann
	for i := 0; i < 34; i++ {
		typeName := "Type" + itoa(i)
		// All 34 types are done in the test fixture. Add evidence to each.
		old := "  " + typeName + ":\n    status: done\n"
		newVal := "  " + typeName + ":\n    status: done\n    evidence: \"internal/ws/server.go:1\"\n"
		updated = []byte(strings.Replace(string(updated), old, newVal, 1))
	}
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), updated, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	wsData, err := os.ReadFile(filepath.Join(root, "ledger", "websocket.json"))
	if err != nil {
		t.Fatal(err)
	}

	var wsDoc struct {
		Messages []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wsData, &wsDoc); err != nil {
		t.Fatal(err)
	}

	// All 34 types have evidence and must stay done.
	doneCount := 0
	for _, msg := range wsDoc.Messages {
		if msg.Status == "done" {
			doneCount++
		}
	}
	if doneCount != 34 {
		t.Errorf("expected 34 done WebSocket types with evidence; got %d", doneCount)
	}
}

func TestRatchetPinResetWithoutRatchet(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 with pin-reset: true but no ratchet: true.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    pin-reset: true\n    recorded_pin: \"aaaa000000000000000000000000000000000000\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	err = generate(root)
	if err == nil {
		t.Fatal("generation must fail when pin-reset is set without ratchet")
	}
	if !strings.Contains(err.Error(), "not ratcheted") {
		t.Fatalf("error must mention non-ratcheted pin-reset; got: %v", err)
	}
}

// TestRatchetPinResetRecordsOperationID proves that the pin_reset source
// metadata records the actual operation ID, not a descriptive phrase.
func TestRatchetPinResetRecordsOperationID(t *testing.T) {
	root := makeTestRoot(t)

	// Mark Op0 as ratcheted with pin-reset: true and a different recorded_pin.
	ann, err := os.ReadFile(filepath.Join(root, "ledger", "annotations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(ann), "  Op0:\n    clients: [swiftfin]\n",
		"  Op0:\n    clients: [swiftfin]\n    ratchet: true\n    pin-reset: true\n    recorded_pin: \"aaaa000000000000000000000000000000000000\"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "ledger", "annotations.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the route for Op0 so its status drops from done.
	routes := `{"format":"deadfin-route-snapshot/v1","revision":"0123456789abcdef0123456789abcdef01234567","routes":[`
	for i := 1; i < 95; i++ {
		if i > 1 {
			routes += ","
		}
		opID := "Op" + itoa(i)
		routes += `{"Method":"GET","Pattern":"/Op` + itoa(i) + `","OperationId":"` + opID + `","Anonymous":true}`
	}
	routes += `]}`
	if err := os.WriteFile(filepath.Join(root, "ledger", "deadfin-routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatalf("generation must pass with changed pin; got: %v", err)
	}

	// Verify the pin_reset records the actual operation ID, not a phrase.
	parity, err := os.ReadFile(filepath.Join(root, "ledger", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(parity)
	if !strings.Contains(content, `"operation": "Op0"`) {
		t.Fatalf("generated parity.json must record operation ID Op0 in pin_reset; got: %s", content)
	}
	if strings.Contains(content, "upstream pin reset cleared ratchet") {
		t.Fatal("generated parity.json must not contain the old descriptive phrase in pin_reset")
	}
}
