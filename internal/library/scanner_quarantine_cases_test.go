// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// quarantineRefresher injects a duplicate image type and index into one
// named movie. This is a shape that Part 1 normalization does not fix:
// duplicate images are a scanner bug, not a value-list duplicate, so
// quarantine must contain them. Every other movie gets clean metadata.
type quarantineRefresher struct {
	invalidPath string
	genre       string
}

func (r quarantineRefresher) Refresh(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
	if *candidate.Path == r.invalidPath {
		candidate.ImageInfos = []item.ImageInfo{
			{Type: item.ImageTypePrimary, Path: *candidate.Path + "-a.jpg"},
			{Type: item.ImageTypePrimary, Path: *candidate.Path + "-b.jpg"},
		}
		if r.genre != "" {
			candidate.Values.Genres = []string{r.genre}
		}
		return nil
	}
	if candidate.Kind == item.KindMovie || candidate.Kind == item.KindFolder {
		candidate.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: *candidate.Path + ".jpg"}}
	}
	return nil
}

// quarantineMediaProbe supplies different media streams and attachments on
// each probe so a test can prove that quarantine drops the candidate payload
// and preserves the prior stored media. The stream codec and attachment
// filename differ on every call for the same path.
type quarantineMediaProbe struct {
	calls map[string]int
}

func (p *quarantineMediaProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	if p.calls == nil {
		p.calls = make(map[string]int)
	}
	path := *input.Item.Path
	p.calls[path]++
	sequence := p.calls[path]
	return library.MediaProbeResult{
		Container: "mkv",
		Streams: []playback.MediaStream{{
			Index: 0,
			Type:  playback.StreamTypeVideo,
			Codec: "h264-v" + strings.Repeat("x", sequence),
		}},
		Attachments: []playback.MediaAttachment{{
			Index:    0,
			FileName: "cover-v" + strings.Repeat("x", sequence) + ".jpg",
			MimeType: "image/jpeg",
		}},
	}, nil
}

// TestQuarantineInvalidLeafWithoutPrior proves that one invalid leaf
// without a stored prior is quarantined while every sibling is stored.
// The invalid leaf carries a duplicate image type and index, which Part 1
// normalization does not fix. RefreshResult.Errors contains exactly one
// error naming the leaf's path.
func TestQuarantineInvalidLeafWithoutPrior(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	good := scannerPath(t, harness.source, "Good Movie (2024).mkv")
	bad := scannerPath(t, harness.source, "Bad Movie (2024).mkv")
	writeScannerFile(t, good, "good")
	writeScannerFile(t, bad, "bad")

	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: bad},
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with quarantined leaf: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("scan errors = %#v, want exactly one", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), bad) {
		t.Fatalf("scan error = %q, want it to name the bad leaf path %q", result.Errors[0], bad)
	}

	scannerItemAtPath(t, harness.items, good)
	assertScannerNoItemAtPath(t, harness.items, bad)
}

// TestQuarantineInvalidLeafWithPrior proves that when the same invalid
// leaf has a stored prior, the prior row, its streams, and its
// attachments survive the rescan unchanged. The candidate media payload
// (fresh probe streams) is dropped by quarantine so the prior media
// satellites are not overwritten.
func TestQuarantineInvalidLeafWithPrior(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	good := scannerPath(t, harness.source, "Good Movie (2024).mkv")
	bad := scannerPath(t, harness.source, "Bad Movie (2024).mkv")
	writeScannerFile(t, good, "good")
	writeScannerFile(t, bad, "bad")

	// First scan with clean metadata so the bad movie gets stored.
	probe := &quarantineMediaProbe{}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{},
		MediaProbe:  probe,
	})
	scannerScan(t, harness.scanner, "initial clean scan")
	prior := scannerItemAtPath(t, harness.items, bad)
	priorStreams, err := harness.items.Streams(t.Context(), prior.ID)
	if err != nil {
		t.Fatalf("read prior streams: %v", err)
	}
	priorAttachments, err := harness.items.Attachments(t.Context(), prior.ID)
	if err != nil {
		t.Fatalf("read prior attachments: %v", err)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close clean scanner: %v", err)
	}

	// Rewrite the file so its size changes. This defeats reusableMediaSource
	// and forces probeResolvedMedia to call the probe again, which returns
	// different streams and attachments. Without quarantine dropping the
	// candidate payload, replaceMediaTx would overwrite the prior media.
	writeScannerFile(t, bad, "bad-rescan-different-size")

	// Second scan with the bad movie now invalid.
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: bad},
		MediaProbe:  probe,
	})
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("rescan with quarantined leaf: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("rescan errors = %#v, want exactly one", result.Errors)
	}
	stored := scannerItemAtPath(t, harness.items, bad)
	if !reflect.DeepEqual(stored, prior) {
		t.Fatalf("stored item = %#v, want prior item = %#v (complete row equality required)", stored, prior)
	}
	storedStreams, err := harness.items.Streams(t.Context(), stored.ID)
	if err != nil {
		t.Fatalf("read stored streams: %v", err)
	}
	if !reflect.DeepEqual(storedStreams, priorStreams) {
		t.Fatalf("stored streams = %#v, want prior streams = %#v (quarantine must drop candidate media)", storedStreams, priorStreams)
	}
	storedAttachments, err := harness.items.Attachments(t.Context(), stored.ID)
	if err != nil {
		t.Fatalf("read stored attachments: %v", err)
	}
	if !reflect.DeepEqual(storedAttachments, priorAttachments) {
		t.Fatalf("stored attachments = %#v, want prior attachments = %#v", storedAttachments, priorAttachments)
	}
	scannerItemAtPath(t, harness.items, good)
}

// TestQuarantineInvalidFolderWithPriorSubtree proves that an invalid folder
// with a persisted prior subtree survives verbatim: the folder row, a
// child folder, and a grandchild file are all present after the rescan,
// and no reconcile ran below the folder.
func TestQuarantineInvalidFolderWithPriorSubtree(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	folder := scannerPath(t, harness.source, "Series Folder")
	if err := mkdirAll(t, folder); err != nil {
		t.Fatalf("create series folder: %v", err)
	}
	childFile := scannerPath(t, harness.source, "Series Folder", "episode.mkv")
	writeScannerFile(t, childFile, "episode")
	childFolder := scannerPath(t, harness.source, "Series Folder", "Season 01")
	if err := mkdirAll(t, childFolder); err != nil {
		t.Fatalf("create child folder: %v", err)
	}
	grandchild := scannerPath(t, harness.source, "Series Folder", "Season 01", "s01e01.mkv")
	writeScannerFile(t, grandchild, "s01e01")

	// First scan: clean metadata stores the folder, the child file, the
	// child folder, and the grandchild.
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{},
	})
	scannerScan(t, harness.scanner, "initial folder scan")
	folderItem := scannerItemAtPath(t, harness.items, folder)
	childFileItem := scannerItemAtPath(t, harness.items, childFile)
	childFolderItem := scannerItemAtPath(t, harness.items, childFolder)
	grandchildItem := scannerItemAtPath(t, harness.items, grandchild)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close clean scanner: %v", err)
	}

	// Remove the child entries from the filesystem before the rescan. A
	// reconcile below the folder would delete the child file, the child
	// folder, and the grandchild. The folder prior is substituted, no
	// child task is derived, and the entire subtree survives untouched.
	if err := os.RemoveAll(folder); err != nil {
		t.Fatalf("remove folder contents: %v", err)
	}
	if err := mkdirAll(t, folder); err != nil {
		t.Fatalf("recreate empty series folder: %v", err)
	}

	// Second scan: the folder is now invalid (duplicate image) and empty.
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: folder},
	})
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("rescan with quarantined folder: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("rescan errors = %#v, want exactly one", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), folder) {
		t.Fatalf("rescan error = %q, want it to name the folder path %q", result.Errors[0], folder)
	}

	storedFolder := scannerItemAtPath(t, harness.items, folder)
	if !reflect.DeepEqual(storedFolder, folderItem) {
		t.Fatalf("stored folder = %#v, want prior folder = %#v (complete row equality required)", storedFolder, folderItem)
	}
	storedChildFile := scannerItemAtPath(t, harness.items, childFile)
	if !reflect.DeepEqual(storedChildFile, childFileItem) {
		t.Fatalf("stored child file = %#v, want prior child file = %#v (complete row equality required)", storedChildFile, childFileItem)
	}
	storedChildFolder := scannerItemAtPath(t, harness.items, childFolder)
	if !reflect.DeepEqual(storedChildFolder, childFolderItem) {
		t.Fatalf("stored child folder = %#v, want prior child folder = %#v (complete row equality required)", storedChildFolder, childFolderItem)
	}
	storedGrandchild := scannerItemAtPath(t, harness.items, grandchild)
	if !reflect.DeepEqual(storedGrandchild, grandchildItem) {
		t.Fatalf("stored grandchild = %#v, want prior grandchild = %#v (complete row equality required)", storedGrandchild, grandchildItem)
	}
}

// TestQuarantineInvalidFolderWithoutPrior proves that an invalid folder
// without a prior stores nothing for it, and a genre unique to it does not
// appear as a virtual entity.
func TestQuarantineInvalidFolderWithoutPrior(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	good := scannerPath(t, harness.source, "Good Movie (2024).mkv")
	writeScannerFile(t, good, "good")
	badFolder := scannerPath(t, harness.source, "Bad Folder")
	if err := mkdirAll(t, badFolder); err != nil {
		t.Fatalf("create bad folder: %v", err)
	}
	badChild := scannerPath(t, harness.source, "Bad Folder", "child.mkv")
	writeScannerFile(t, badChild, "child")

	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: badFolder, genre: "UniqueBadGenre"},
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with quarantined folder: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("scan errors = %#v, want exactly one", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), badFolder) {
		t.Fatalf("scan error = %q, want it to name the folder path %q", result.Errors[0], badFolder)
	}

	assertScannerNoItemAtPath(t, harness.items, badFolder)
	assertScannerNoItemAtPath(t, harness.items, badChild)
	scannerItemAtPath(t, harness.items, good)

	genres, err := harness.items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindGenre},
		Recursive:        true,
	})
	if err != nil {
		t.Fatalf("query genres: %v", err)
	}
	for _, genre := range genres.Items {
		if genre.Name != nil && *genre.Name == "UniqueBadGenre" {
			t.Fatalf("quarantined folder genre UniqueBadGenre leaked as virtual entity")
		}
	}
}

// TestQuarantineDeepSubtreeSkip proves that when an invalid item sits deep
// in a subtree task (not the root plan), the skip still reaches
// RefreshResult.Errors and the containing folder's "scanned" progress
// record keeps its counts. The progress callback captures every
// RefreshProgress record so the parent folder's scanned record is
// observable alongside the skip record.
func TestQuarantineDeepSubtreeSkip(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	parent := scannerPath(t, harness.source, "Season Folder")
	if err := mkdirAll(t, parent); err != nil {
		t.Fatalf("create season folder: %v", err)
	}
	good := scannerPath(t, harness.source, "Season Folder", "good.mkv")
	bad := scannerPath(t, harness.source, "Season Folder", "bad.mkv")
	writeScannerFile(t, good, "good")
	writeScannerFile(t, bad, "bad")

	var progressRecords []library.RefreshProgress
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: bad},
		Progress: func(p library.RefreshProgress) {
			progressRecords = append(progressRecords, p)
		},
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with deep quarantined item: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("scan errors = %#v, want exactly one", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), bad) {
		t.Fatalf("scan error = %q, want it to name the bad item path %q", result.Errors[0], bad)
	}

	// The parent folder's scanned progress record must keep its counts:
	// two entries discovered, one inserted, zero updated, zero deleted.
	// The skip record does not suppress the parent's scanned record.
	var parentRecord *library.RefreshProgress
	for i := range progressRecords {
		if progressRecords[i].Status == "scanned" && progressRecords[i].Path == parent {
			parentRecord = &progressRecords[i]
			break
		}
	}
	if parentRecord == nil {
		t.Fatalf("no scanned progress record for parent folder %q in %#v", parent, progressRecords)
	}
	if parentRecord.Discovered != 2 {
		t.Fatalf("parent Discovered = %d, want 2", parentRecord.Discovered)
	}
	if parentRecord.Inserted != 1 {
		t.Fatalf("parent Inserted = %d, want 1", parentRecord.Inserted)
	}
	if parentRecord.Updated != 0 {
		t.Fatalf("parent Updated = %d, want 0", parentRecord.Updated)
	}
	if parentRecord.Deleted != 0 {
		t.Fatalf("parent Deleted = %d, want 0", parentRecord.Deleted)
	}

	scannerItemAtPath(t, harness.items, good)
	assertScannerNoItemAtPath(t, harness.items, bad)
	scannerItemAtPath(t, harness.items, parent)
}

// TestQuarantineRootSkipPropagation proves that a skip from one media root
// reaches RefreshResult.Errors even when a later root fails. prepareRoot
// must preserve skipped entries from earlier roots before it records the
// later root failure, so both errors appear in the result.
func TestQuarantineRootSkipPropagation(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open scanner SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close scanner SQLite stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open scanner topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close scanner topology: %v", err)
		}
	})
	rootOne := filepath.Join(t.TempDir(), "media-one")
	if err := os.Mkdir(rootOne, 0o755); err != nil {
		t.Fatalf("create media root one: %v", err)
	}
	rootTwo := filepath.Join(t.TempDir(), "media-two")
	if err := os.Mkdir(rootTwo, 0o755); err != nil {
		t.Fatalf("create media root two: %v", err)
	}
	bad := filepath.Join(rootOne, "Bad Movie (2024).mkv")
	writeScannerFile(t, bad, "bad")
	good := filepath.Join(rootTwo, "Good Movie (2024).mkv")
	writeScannerFile(t, good, "good")
	// Make root two's good movie resolve to a malformed item so the
	// resolver rejects it and resolveFolder returns an error. The
	// skip from root one must survive this later root failure.
	resolver.setMalformed(good)

	folder, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: rootOne}, {Path: rootTwo}}})
	_ = folder
	if err != nil {
		t.Fatalf("add scanner virtual folder: %v", err)
	}
	chain := library.NewChain(resolver)
	items := store.NewItemStore(stores)
	scanner := newScanner(t, topology, items, chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   quarantineRefresher{invalidPath: bad},
	})
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})

	result, err := scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with root skip and root failure: %v", err)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("scan errors = %#v, want exactly two (one skip, one root failure)", result.Errors)
	}
	var sawSkip, sawFailure bool
	for _, scanErr := range result.Errors {
		text := scanErr.Error()
		if strings.Contains(text, bad) && strings.Contains(text, "quarantine") {
			sawSkip = true
		}
		if strings.Contains(text, good) || strings.Contains(text, rootTwo) {
			sawFailure = true
		}
	}
	if !sawSkip {
		t.Fatalf("no skip error naming the bad movie %q in %#v", bad, result.Errors)
	}
	if !sawFailure {
		t.Fatalf("no root failure error naming root two %q in %#v", rootTwo, result.Errors)
	}
}

func mkdirAll(t *testing.T, path string) error {
	t.Helper()
	return os.MkdirAll(path, 0o755)
}
