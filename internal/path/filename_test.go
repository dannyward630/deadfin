// SPDX-License-Identifier: GPL-2.0-only

package path

import (
	"strings"
	"testing"
)

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

func FuzzValidFilename(f *testing.F) {
	for _, seed := range []string{"Road Trip", "a/b\\c", "..", "x:y*?", "\x00\x1f"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := ValidFilename(name)
		if strings.ContainsAny(got, "/\\\x00") {
			t.Fatalf("ValidFilename(%q) = %q keeps a separator or NUL", name, got)
		}
	})
}
