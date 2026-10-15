// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// TestScannerResolvesSelfContainedMovieDirectoryAtMediaPath defends the
// self-contained movie-directory resolution seam: the directory's frozen child
// listing authorizes the default Movie resolver to persist its direct media
// file, rather than a generic Folder for the directory itself.
func TestScannerResolvesSelfContainedMovieDirectoryAtMediaPath(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	directory := scannerPath(t, harness.source, "Standalone Movie (2023)")
	media := scannerPath(t, directory, "Standalone Movie (2023).mkv")
	writeScannerFile(t, media, "feature")
	writeScannerFile(t, scannerPath(t, directory, "movie.nfo"), `<movie><title>Standalone Movie</title></movie>`)
	writeScannerFile(t, scannerPath(t, directory, "Standalone Movie (2023)-poster.jpg"), "poster")

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	var readsMu sync.Mutex
	reads := make(map[string]int)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		ReadDir: func(path string) ([]library.Entry, error) {
			readsMu.Lock()
			reads[path]++
			readsMu.Unlock()
			return scannerReadDir(path)
		},
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan self-contained movie directory: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan self-contained movie directory errors = %v, want none", result.Errors)
	}

	movie := scannerItemAtPath(t, harness.items, media)
	if movie.Kind != item.KindMovie || movie.Path == nil || *movie.Path != media {
		t.Fatalf("persisted item = %#v, want Movie at direct media path %q", movie, media)
	}
	assertScannerNoItemAtPath(t, harness.items, directory)

	readsMu.Lock()
	defer readsMu.Unlock()
	if reads[directory] != 1 {
		t.Fatalf("ReadDir(%q) calls = %d, want one frozen directory listing", directory, reads[directory])
	}
	if reads[harness.source] != 1 || len(reads) != 2 {
		t.Fatalf("ReadDir calls = %#v, want exactly one listing each for source and movie directory", reads)
	}
}
