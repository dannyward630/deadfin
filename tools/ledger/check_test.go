// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckRejectsStaleGeneratedFile proves that check mode exits nonzero and
// names each stale file when a generated file has been modified or is missing.
// It must not accept a rewritten generated file. It tests all three generated
// files: parity.json, websocket.json, and PARITY.md.
func TestCheckRejectsStaleGeneratedFile(t *testing.T) {
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

	// Write an OpenAPI spec with 95 operations, all marked swiftfin.
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
	var routesBuilder strings.Builder
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

	// Generate the files first.
	if err := generate(root); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Verify check passes with matching files.
	if err := check(root); err != nil {
		t.Fatalf("check must pass with fresh generated files; got: %v", err)
	}

	// Test 1: corrupt parity.json — check must fail and name parity.json.
	corruptAndVerify(t, root, "parity.json", func(original []byte) []byte {
		return append(original, []byte("\n  // stale")...)
	})

	// Test 2: corrupt websocket.json — check must fail and name websocket.json.
	corruptAndVerify(t, root, "websocket.json", func(original []byte) []byte {
		return []byte(`{"stale": true}` + "\n")
	})

	// Test 3: corrupt PARITY.md — check must fail and name PARITY.md.
	corruptAndVerify(t, root, "PARITY.md", func(original []byte) []byte {
		return []byte("# Stale content\n")
	})

	// Test 4: remove parity.json — check must fail and report it missing.
	removeAndVerify(t, root, "parity.json")

	// Test 5: remove websocket.json — check must fail and report it missing.
	removeAndVerify(t, root, "websocket.json")

	// Test 6: remove PARITY.md — check must fail and report it missing.
	removeAndVerify(t, root, "PARITY.md")
}

// corruptAndVerify corrupts a generated file, runs check, verifies it fails
// with the filename in the error message, then regenerates the file.
func corruptAndVerify(t *testing.T, root string, filename string, corrupt func([]byte) []byte) {
	t.Helper()
	path := filepath.Join(root, "ledger", filename)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt(original), 0o644); err != nil {
		t.Fatal(err)
	}
	err = check(root)
	if err == nil {
		t.Errorf("check must reject stale %s, but it passed", filename)
	}
	if err != nil && !strings.Contains(err.Error(), filename) {
		t.Errorf("check error for %s must name the file; got: %v", filename, err)
	}
	// Regenerate to restore the file.
	if err := generate(root); err != nil {
		t.Fatalf("regenerate failed after corrupting %s: %v", filename, err)
	}
}

// removeAndVerify removes a generated file, runs check, verifies it fails
// with "missing" in the error message, then regenerates the file.
func removeAndVerify(t *testing.T, root string, filename string) {
	t.Helper()
	path := filepath.Join(root, "ledger", filename)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	err := check(root)
	if err == nil {
		t.Errorf("check must reject missing %s, but it passed", filename)
	}
	if err != nil && !strings.Contains(err.Error(), "missing") {
		t.Errorf("check error for missing %s must say missing; got: %v", filename, err)
	}
	// Regenerate to restore the file.
	if err := generate(root); err != nil {
		t.Fatalf("regenerate failed after removing %s: %v", filename, err)
	}
}

// itoa converts a non-negative integer to its decimal string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
