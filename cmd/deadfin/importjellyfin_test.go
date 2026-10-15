// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLibrary(t *testing.T, root, name, mediaPath string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, contents := range map[string]string{"options.xml": "<LibraryOptions />", "movies.collection": "", "movies.mblink": mediaPath, "unrelated.txt": "x", "._movies.mblink": "\x00\x05\x16\x07AppleDouble"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopyJellyfinLibraries(t *testing.T) {
	jellyfin, deadfin, media := t.TempDir(), t.TempDir(), t.TempDir()
	writeLibrary(t, jellyfin, "Music", media)
	writeLibrary(t, jellyfin, "Fresh Movies", "/media/not-mounted-here")
	writeLibrary(t, jellyfin, "Movies", media)
	if err := os.MkdirAll(filepath.Join(deadfin, "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := copyJellyfinLibraries(&out, jellyfin, deadfin, false); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"options.xml", "movies.collection", "movies.mblink"} {
		if _, err := os.Stat(filepath.Join(deadfin, "Music", file)); err != nil {
			t.Errorf("Music/%s not copied: %v", file, err)
		}
	}
	for _, junk := range []string{"unrelated.txt", "._movies.mblink"} {
		if _, err := os.Stat(filepath.Join(deadfin, "Music", junk)); err == nil {
			t.Errorf("copied %s, which is not a library definition", junk)
		}
	}
	if _, err := os.Stat(filepath.Join(deadfin, "Fresh Movies")); err == nil {
		t.Error("copied a library whose media path does not exist here")
	}
	for _, want := range []string{`"Movies": already present`, `"Fresh Movies": skipped`, `"Music": copied`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q lacks %q", out.String(), want)
		}
	}

	// A dry run reports and writes nothing.
	fresh := t.TempDir()
	if err := copyJellyfinLibraries(&out, jellyfin, fresh, true); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(fresh); len(entries) != 0 {
		t.Errorf("dry run wrote %d entries", len(entries))
	}
}
