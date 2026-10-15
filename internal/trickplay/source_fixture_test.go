// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// openTrickplayTestSource supplies a real, caller-owned pinned descriptor for
// tests that exercise generation behavior unrelated to source mutation.
func openTrickplayTestSource(t *testing.T) *sourcefile.Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("pinned trickplay test source"), 0o600); err != nil {
		t.Fatalf("WriteFile(source fixture): %v", err)
	}
	source, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("Open(source fixture): %v", err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("Close(source fixture): %v", err)
		}
	})
	return source
}
