// SPDX-License-Identifier: GPL-2.0-only

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
)

// Jellyfin's application paths keep independently managed stores beneath one
// program-data root. Configuration itself is deliberately separate from the
// database/data directory so installation and backup tools can address them
// independently.
func TestPathsFollowUpstreamProgramDataLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")

	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatalf("NewPaths(%q): %v", root, err)
	}

	want := config.Paths{
		Root:       root,
		Data:       filepath.Join(root, "data"),
		Config:     filepath.Join(root, "config"),
		Cache:      filepath.Join(root, "cache"),
		Log:        filepath.Join(root, "log"),
		Metadata:   filepath.Join(root, "metadata"),
		Transcodes: filepath.Join(root, "cache", "transcodes"),
		Trickplay:  filepath.Join(root, "data", "trickplay"),
	}
	if paths != want {
		t.Fatalf("NewPaths(%q) = %#v, want %#v", root, paths, want)
	}
}

func TestPathsCleanAndAbsolutizeRelativeProgramDataRoot(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)

	paths, err := config.NewPaths(filepath.Join(".", "instance", "..", "server-data"))
	if err != nil {
		t.Fatalf("NewPaths(relative root): %v", err)
	}
	root := filepath.Join(workingDirectory, "server-data")
	want := config.Paths{
		Root:       root,
		Data:       filepath.Join(root, "data"),
		Config:     filepath.Join(root, "config"),
		Cache:      filepath.Join(root, "cache"),
		Log:        filepath.Join(root, "log"),
		Metadata:   filepath.Join(root, "metadata"),
		Transcodes: filepath.Join(root, "cache", "transcodes"),
		Trickplay:  filepath.Join(root, "data", "trickplay"),
	}
	if paths != want {
		t.Fatalf("NewPaths(relative root) = %#v, want clean absolute paths %#v", paths, want)
	}
}
