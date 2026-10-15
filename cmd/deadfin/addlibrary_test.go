// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/library"
)

// TestRunAddLibrarySeedsInternetProviders proves the add-library CLI persists
// EnableInternetProviders=true in options.xml. A CLI-created library must be
// eligible for the remote-metadata provider stage; the refresh pipeline gates
// that stage on this flag (internal/providers/registry.go). The test reads the
// persisted XML and decodes it as library.LibraryOptions, asserting the
// configured media path and the internet-providers flag — the stored contract,
// not the source literal.
func TestRunAddLibrarySeedsInternetProviders(t *testing.T) {
	programData := t.TempDir()
	mediaDir := t.TempDir()

	const name = "Movies"
	const kind = "movies"
	if err := runAddLibrary(context.Background(), programData, "", name, kind, mediaDir); err != nil {
		t.Fatalf("runAddLibrary: %v", err)
	}

	optionsPath := filepath.Join(programData, "root", "default", name, "options.xml")
	contents, err := os.ReadFile(optionsPath)
	if err != nil {
		t.Fatalf("read options.xml: %v", err)
	}

	var options library.LibraryOptions
	if err := xml.Unmarshal(contents, &options); err != nil {
		t.Fatalf("decode options.xml: %v", err)
	}

	if !options.EnableInternetProviders {
		t.Fatalf("EnableInternetProviders = false, want true (CLI must seed remote-metadata eligibility)")
	}

	wantPaths := []library.MediaPathInfo{{Path: mediaDir}}
	if len(options.PathInfos) != len(wantPaths) {
		t.Fatalf("PathInfos count = %d, want %d", len(options.PathInfos), len(wantPaths))
	}
	for i, want := range wantPaths {
		if options.PathInfos[i].Path != want.Path {
			t.Fatalf("PathInfos[%d].Path = %q, want %q", i, options.PathInfos[i].Path, want.Path)
		}
	}
}
