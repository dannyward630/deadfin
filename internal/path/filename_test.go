// SPDX-License-Identifier: GPL-2.0-only

package path

import "testing"

// TestValidFilename pins ManagedFileSystem.GetValidFilename's character set
// (ManagedFileSystem.cs:21-28): each invalid character becomes one space.
func TestValidFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Road Trip":       "Road Trip",
		"AC/DC: Best?":    "AC DC  Best ",
		"a\\b<c>d|e*f\"g": "a b c d e f g",
		"tab\there":       "tab here",
		"Ünïcode ✓":       "Ünïcode ✓",
	} {
		if got := ValidFilename(in); got != want {
			t.Errorf("ValidFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
