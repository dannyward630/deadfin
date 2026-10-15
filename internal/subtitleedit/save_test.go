// SPDX-License-Identifier: GPL-2.0-only

package subtitleedit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAllowedFormatReturnsListedExtension(t *testing.T) {
	if got, ok := allowedFormat("srt"); !ok || got != "srt" {
		t.Fatalf("allowedFormat(srt) = %q, %v", got, ok)
	}
	for _, format := range []string{"", "exe", "srt/../x"} {
		if got, ok := allowedFormat(format); ok {
			t.Fatalf("allowedFormat(%q) = %q, true; want rejection", format, got)
		}
	}
}

func TestSaveSubtitleStaysInDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"..", "../x", "a/b", "/abs", ""} {
		if _, err := saveSubtitle(dir, name, "srt", []byte("1")); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("saveSubtitle(%q) error = %v, want ErrInvalidRequest", name, err)
		}
	}
	path, err := saveSubtitle(dir, "movie.en", "srt", []byte("1"))
	if err != nil || path != filepath.Join(dir, "movie.en.srt") {
		t.Fatalf("saveSubtitle = %q, %v", path, err)
	}
	if path, err = saveSubtitle(dir, "movie.en", "srt", []byte("2")); err != nil || path != filepath.Join(dir, "movie.en.0.srt") {
		t.Fatalf("second saveSubtitle = %q, %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
