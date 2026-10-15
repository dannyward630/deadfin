// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parseSessionMessageTypes reads the 34 upstream WebSocket message types from
// SessionMessageType.cs:8-49.
func parseSessionMessageTypes(root string) ([]string, error) {
	path := filepath.Join(root, "ledger", "inputs", "jellyfin", "MediaBrowser.Model", "Session", "SessionMessageType.cs")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read SessionMessageType.cs: %w", err)
	}
	defer file.Close()

	var types []string
	scanner := bufio.NewScanner(file)
	inEnum := false

	for scanner.Scan() {
		line := scanner.Text()

		// Start after the enum opening.
		if strings.Contains(line, "public enum SessionMessageType") {
			inEnum = true
			continue
		}

		if !inEnum {
			continue
		}

		// Stop at the closing brace.
		if strings.TrimSpace(line) == "}" {
			break
		}

		// Skip comments and blank lines.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Extract the enum value name.
		name := strings.TrimSuffix(trimmed, ",")
		name = strings.TrimSpace(name)
		if name == "" || name == "{" {
			continue
		}

		types = append(types, name)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SessionMessageType.cs: %w", err)
	}

	return types, nil
}
