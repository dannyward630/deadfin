// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerMovieDirectoryFixtureConventions defends the scanner boundary
// that the stock goldenrec inventory records. MovieResolver.ResolveVideos
// ignores a file named with the word "sample", so "HLG Sample (2024)" and
// "Subtitle Sample (2024)" stay generic Folders, while a directory whose
// videos form one movie becomes that Movie. Resolution must consume the one
// frozen listing for each directory rather than traversing it again.
func TestScannerMovieDirectoryFixtureConventions(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})

	hlgDirectory := scannerPath(t, harness.source, "HLG Sample (2024)")
	hlgMedia := scannerPath(t, hlgDirectory, "HLG Sample (2024).mkv")
	writeScannerFile(t, hlgMedia, "hlg")
	writeScannerFile(t, scannerPath(t, hlgDirectory, "HLG Sample (2024).nfo"), `<movie><title>HLG Sample</title></movie>`)
	writeScannerFile(t, scannerPath(t, hlgDirectory, "HLG Sample (2024).png"), "image")

	subtitleDirectory := scannerPath(t, harness.source, "Subtitle Sample (2024)")
	subtitleMedia := scannerPath(t, subtitleDirectory, "Subtitle Sample (2024).mkv")
	writeScannerFile(t, subtitleMedia, "subtitle")
	writeScannerFile(t, scannerPath(t, subtitleDirectory, "Subtitle Sample (2024).nfo"), `<movie><title>Subtitle Sample</title></movie>`)
	writeScannerFile(t, scannerPath(t, subtitleDirectory, "Subtitle Sample (2024).webp"), "image")
	writeScannerFile(t, scannerPath(t, subtitleDirectory, "Subtitle Sample (2024).en.srt"), "subtitle")

	syntheticDirectory := scannerPath(t, harness.source, "Synthetic Feature (2024)")
	synthetic1080 := scannerPath(t, syntheticDirectory, "Synthetic Feature (2024) - 1080p.mkv")
	synthetic720 := scannerPath(t, syntheticDirectory, "Synthetic Feature (2024) - 720p.mkv")
	writeScannerFile(t, synthetic1080, "1080")
	writeScannerFile(t, synthetic720, "720")
	writeScannerFile(t, scannerPath(t, syntheticDirectory, "movie.nfo"), `<movie><title>Synthetic Feature</title></movie>`)

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
		t.Fatalf("scan fixture movie directories: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan fixture movie directories errors = %v, want none", result.Errors)
	}

	for _, fixture := range []struct {
		name      string
		directory string
		media     string
	}{
		{name: "HLG", directory: hlgDirectory, media: hlgMedia},
		{name: "Subtitle", directory: subtitleDirectory, media: subtitleMedia},
	} {
		folder := scannerItemAtPath(t, harness.items, fixture.directory)
		if folder.Kind != item.KindFolder || !folder.IsFolder {
			t.Fatalf("%s directory item = %#v, want a generic Folder", fixture.name, folder)
		}
		movie := scannerItemAtPath(t, harness.items, fixture.media)
		if movie.Kind != item.KindMovie || movie.IsFolder || movie.ParentID == nil || *movie.ParentID != folder.ID {
			t.Fatalf("%s directory media = %#v, folder = %#v; want Movie retained under its generic Folder", fixture.name, movie, folder)
		}
	}

	assertScannerNoItemAtPath(t, harness.items, syntheticDirectory)
	collection := scannerCollectionFolder(t, harness)
	primary := scannerItemAtPath(t, harness.items, synthetic1080)
	assertScannerMovieVisible(t, harness, synthetic1080, primary.ID)
	if primary.Kind != item.KindMovie || primary.IsFolder || primary.ParentID == nil {
		t.Fatalf("synthetic resolver primary = %#v; want visible Movie", primary)
	}
	primaryRoot, err := harness.items.Get(t.Context(), *primary.ParentID)
	if err != nil {
		t.Fatalf("load primary root parent: %v", err)
	}
	if primaryRoot.Kind != item.KindFolder || primaryRoot.ParentID == nil || *primaryRoot.ParentID != collection.ID {
		t.Fatalf("synthetic resolver primary parent chain = %s -> %s, want Folder -> CollectionFolder %s", primaryRoot.Kind, primaryRoot.ParentID, collection.ID)
	}
	alternate := scannerStoredMovieAtPath(t, harness.items, synthetic720)
	if alternate.Kind != item.KindMovie || alternate.IsFolder || alternate.ParentID == nil {
		t.Fatalf("hidden synthetic alternate = %#v; want Movie", alternate)
	}
	if *alternate.ParentID != *primary.ParentID {
		t.Fatalf("hidden synthetic alternate parent = %s, want same root Folder as primary %s", *alternate.ParentID, *primary.ParentID)
	}
	assertScannerAlternateGroup(t, primary, alternate)
	hidden, err := harness.items.Query(t.Context(), store.ItemQuery{Path: synthetic720, IncludeItemTypes: []item.Kind{item.KindMovie}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query ordinary synthetic 720p item: %v", err)
	}
	if len(hidden.Items) != 0 || hidden.TotalRecordCount != 0 {
		t.Fatalf("ordinary scanner query returned hidden 720p alternate = %#v (total %d), want no row", hidden.Items, hidden.TotalRecordCount)
	}

	readsMu.Lock()
	defer readsMu.Unlock()
	for _, directory := range []string{harness.source, hlgDirectory, subtitleDirectory, syntheticDirectory} {
		if reads[directory] != 1 {
			t.Fatalf("ReadDir(%q) calls = %d, want one frozen directory listing", directory, reads[directory])
		}
	}
	if len(reads) != 4 {
		t.Fatalf("ReadDir paths = %#v, want one enumeration each for source and the three fixture directories", reads)
	}
}
