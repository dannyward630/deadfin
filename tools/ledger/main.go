// SPDX-License-Identifier: GPL-2.0-only

// Package ledger builds the parity ledger from committed inputs.
//
// The generator reads the pinned OpenAPI spec, the Deadfin route snapshot,
// difftool results, work-package status lines, and two hand-edited YAML
// inputs (shadow-spec.yaml and annotations.yaml). It emits three generated
// files: parity.json, websocket.json, and PARITY.md.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// routeSnapshotFormat is the format string for the route snapshot.
	routeSnapshotFormat = "deadfin-route-snapshot/v1"

	// difftoolResultsFormat is the format string for result files.
	difftoolResultsFormat = "difftool-results/v1"

	// swiftfinDenominator is the measured Swiftfin REST operation count.
	swiftfinDenominator = 95

	// websocketDenominator is the upstream WebSocket message-type count.
	websocketDenominator = 34
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ledger: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	mode := "generate"
	if len(args) > 0 {
		mode = args[0]
	}

	switch mode {
	case "generate":
		return generate(root)
	case "check":
		return check(root)
	case "routes":
		return exportRoutes(root, args[1:])
	default:
		return fmt.Errorf("unknown command %q: use generate, check, or routes", mode)
	}
}

// findRoot walks up from the current directory to find the repository root
// identified by the Makefile and docs/ directory.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "docs")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find repository root (Makefile + docs/)")
		}
		dir = parent
	}
}

// generate reads all inputs, builds the ledger, and writes the three
// generated files. It returns an error if any input is invalid.
func generate(root string) error {
	inputs, err := loadInputs(root)
	if err != nil {
		return err
	}

	ledger, err := buildLedger(inputs)
	if err != nil {
		return err
	}

	parityJSON, err := json.MarshalIndent(ledger.Parity, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal parity.json: %w", err)
	}
	parityJSON = append(parityJSON, '\n')

	websocketJSON, err := json.MarshalIndent(ledger.WebSocket, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal websocket.json: %w", err)
	}
	websocketJSON = append(websocketJSON, '\n')

	parityMD := renderMarkdown(ledger)

	if err := os.MkdirAll(filepath.Join(root, "ledger"), 0o755); err != nil {
		return fmt.Errorf("mkdir ledger: %w", err)
	}

	if err := writeFile(filepath.Join(root, "ledger", "parity.json"), parityJSON); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(root, "ledger", "websocket.json"), websocketJSON); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(root, "ledger", "PARITY.md"), []byte(parityMD)); err != nil {
		return err
	}

	return nil
}

// check regenerates the files in memory and compares them with the committed
// versions. It returns a nonzero exit if any file is stale.
func check(root string) error {
	inputs, err := loadInputs(root)
	if err != nil {
		return err
	}

	ledger, err := buildLedger(inputs)
	if err != nil {
		return err
	}

	parityJSON, err := json.MarshalIndent(ledger.Parity, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal parity.json: %w", err)
	}
	parityJSON = append(parityJSON, '\n')

	websocketJSON, err := json.MarshalIndent(ledger.WebSocket, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal websocket.json: %w", err)
	}
	websocketJSON = append(websocketJSON, '\n')

	parityMD := renderMarkdown(ledger)

	var stale []string

	if err := compareFile(filepath.Join(root, "ledger", "parity.json"), parityJSON, &stale); err != nil {
		return err
	}
	if err := compareFile(filepath.Join(root, "ledger", "websocket.json"), websocketJSON, &stale); err != nil {
		return err
	}
	if err := compareFile(filepath.Join(root, "ledger", "PARITY.md"), []byte(parityMD), &stale); err != nil {
		return err
	}

	if len(stale) > 0 {
		fmt.Fprintln(os.Stderr, "ledger is stale; regenerate with: make ledger")
		for _, f := range stale {
			fmt.Fprintf(os.Stderr, "  stale: %s\n", f)
		}
		return fmt.Errorf("ledger is stale: %s", strings.Join(stale, ", "))
	}

	fmt.Println("ledger is current")
	return nil
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func compareFile(path string, expected []byte, stale *[]string) error {
	actual, err := os.ReadFile(path)
	if err != nil {
		*stale = append(*stale, filepath.Base(path)+" (missing)")
		return nil
	}
	if string(actual) != string(expected) {
		*stale = append(*stale, filepath.Base(path))
	}
	return nil
}
