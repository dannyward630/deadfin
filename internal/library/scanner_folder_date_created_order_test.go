// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// TestScannerAssignsFolderDateCreatedInSourceEnumerationOrder verifies the
// lifecycle distinction stock makes during a fresh scan: files retain their
// filesystem creation fact, while every newly materialized directory (including
// a directory transformed into a BoxSet) is clocked immediately before its own
// metadata refresh. The deliberately non-alphabetic ReadDir order is the
// observable source order. Sorting by fixture path reverses the persisted
// DateCreated page.
func TestScannerAssignsFolderDateCreatedInSourceEnumerationOrder(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}

	firstFolder := scannerPath(t, harness.source, "Zulu first")
	boxset := scannerPath(t, harness.source, "Middle Collection [boxset]")
	lastFolder := scannerPath(t, harness.source, "Alpha last")
	file := scannerPath(t, harness.source, "filesystem movie (2024).mkv")
	for _, path := range []string{
		// Two unrelated movies keep each directory a generic Folder. One movie
		// alone would make the directory the Movie (MovieResolver.FindMovie).
		scannerPath(t, firstFolder, "first movie (2024).mkv"),
		scannerPath(t, firstFolder, "other first movie (2019).mkv"),
		scannerPath(t, boxset, "collection movie (2024).mkv"),
		scannerPath(t, lastFolder, "last movie (2024).mkv"),
		scannerPath(t, lastFolder, "other last movie (2019).mkv"),
		file,
	} {
		writeScannerFile(t, path, "fixture")
	}

	filesystemDirectoryCreated := time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)
	filesystemFileCreated := time.Date(2002, time.February, 3, 4, 5, 6, 0, time.UTC)
	clockStart := time.Date(2026, time.July, 27, 8, 26, 40, 0, time.UTC)
	nfoDateAdded := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	fakeClock := newFolderDateCreatedClock(clockStart)
	order := []string{firstFolder, boxset, lastFolder, file}
	readDir := newFolderDateCreatedReadDir(t, harness.source, order, filesystemDirectoryCreated, filesystemFileCreated)
	var refreshTimes []time.Time
	var refreshClockCalls []int
	refresher := scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
		if candidate.Path == nil || !candidate.IsFolder || *candidate.Path == harness.source {
			return nil
		}
		observed, calls := fakeClock.Snapshot()
		refreshTimes = append(refreshTimes, observed)
		refreshClockCalls = append(refreshClockCalls, calls)
		if *candidate.Path == firstFolder {
			// This models a directory NFO dateadded value. It must win over the
			// lifecycle timestamp sampled before this same provider call.
			candidate.DateCreated = &nfoDateAdded
		}
		// Metadata work advances the fake wall clock. A scanner that batches all
		// Now calls after refresh will assign the same later instant to each
		// remaining directory instead of this read-directory sequence.
		fakeClock.Advance(time.Second)
		return nil
	})
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         fakeClock.Now,
		ReadDir:     readDir.ReadDir,
		Refresher:   refresher,
	})

	scannerScan(t, harness.scanner, "scan source-ordered folder creation fixture")
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, firstFolder), item.KindFolder, nfoDateAdded)
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, boxset), item.KindBoxSet, clockStart.Add(time.Second))
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, lastFolder), item.KindFolder, clockStart.Add(2*time.Second))
	storedFile := scannerItemAtPath(t, harness.items, file)
	if storedFile.DateCreated == nil || !storedFile.DateCreated.Equal(filesystemFileCreated) {
		t.Fatalf("file DateCreated = %v, want filesystem creation time %s", storedFile.DateCreated, filesystemFileCreated)
	}
	if got, want := refreshTimes, []time.Time{clockStart, clockStart.Add(time.Second), clockStart.Add(2 * time.Second)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folder refresh times = %v, want DateCreated clock samples in ReadDir order %v", got, want)
	}
	if got, want := refreshClockCalls, []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folder refresh clock call counts = %v, want each folder DateCreated sample in ReadDir order %v", got, want)
	}

	page := folderDateCreatedPage(t, harness, 0, 2)
	if page.TotalRecordCount != 3 {
		t.Fatalf("recursive folder DateCreated total = %d, want 3", page.TotalRecordCount)
	}
	assertFolderDateCreatedPagePaths(t, page, []string{firstFolder, lastFolder})
	secondPage := folderDateCreatedPage(t, harness, 2, 2)
	assertFolderDateCreatedPagePaths(t, secondPage, []string{boxset})

	// A later filesystem observation and a later lifecycle clock must not rewrite
	// creation timestamps already persisted for any folder transformation.
	readDir.created = time.Date(2037, time.March, 4, 5, 6, 7, 0, time.UTC)
	scannerScan(t, harness.scanner, "rescan persisted folder creation fixture")
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, firstFolder), item.KindFolder, nfoDateAdded)
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, boxset), item.KindBoxSet, clockStart.Add(time.Second))
	assertFolderDateCreated(t, scannerItemAtPath(t, harness.items, lastFolder), item.KindFolder, clockStart.Add(2*time.Second))
	storedFile = scannerItemAtPath(t, harness.items, file)
	if storedFile.DateCreated == nil || !storedFile.DateCreated.Equal(filesystemFileCreated) {
		t.Fatalf("rescanned file DateCreated = %v, want original filesystem creation time %s", storedFile.DateCreated, filesystemFileCreated)
	}
}

type folderDateCreatedClock struct {
	mu    sync.Mutex
	now   time.Time
	calls int
}

func newFolderDateCreatedClock(start time.Time) *folderDateCreatedClock {
	return &folderDateCreatedClock{now: start}
}

func (clock *folderDateCreatedClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.calls++
	return clock.now
}

func (clock *folderDateCreatedClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func (clock *folderDateCreatedClock) Snapshot() (time.Time, int) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now, clock.calls
}

type folderDateCreatedDirectoryReader struct {
	t           *testing.T
	source      string
	order       []string
	created     time.Time
	fileCreated time.Time
}

func newFolderDateCreatedReadDir(t *testing.T, source string, order []string, created, fileCreated time.Time) *folderDateCreatedDirectoryReader {
	t.Helper()
	return &folderDateCreatedDirectoryReader{t: t, source: source, order: order, created: created, fileCreated: fileCreated}
}

func (reader *folderDateCreatedDirectoryReader) ReadDir(path string) ([]library.Entry, error) {
	entries, err := scannerReadDir(path)
	if err != nil {
		return nil, err
	}
	for index := range entries {
		entries[index].Created = reader.created
		if !entries[index].IsDir {
			entries[index].Created = reader.fileCreated
		}
	}
	if path != reader.source {
		return entries, nil
	}
	byPath := make(map[string]library.Entry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	ordered := make([]library.Entry, 0, len(entries))
	for _, want := range reader.order {
		entry, found := byPath[want]
		if !found {
			reader.t.Fatalf("source ReadDir missing ordered entry %q in %#v", want, entries)
		}
		ordered = append(ordered, entry)
		delete(byPath, want)
	}
	for _, entry := range entries {
		if _, remaining := byPath[entry.Path]; remaining {
			ordered = append(ordered, entry)
		}
	}
	return ordered, nil
}

type folderDateCreatedResponse struct {
	Items            []folderDateCreatedItem `json:"Items"`
	TotalRecordCount int                     `json:"TotalRecordCount"`
	StartIndex       int                     `json:"StartIndex"`
}

type folderDateCreatedItem struct {
	Path        string     `json:"Path"`
	DateCreated *time.Time `json:"DateCreated"`
}

func folderDateCreatedPage(t *testing.T, harness *scannerHarness, startIndex, limit int) folderDateCreatedResponse {
	t.Helper()
	routes := router.New()
	handlers.RegisterItems(routes, harness.items, dtobuild.NewBuilder(harness.items, dtobuild.NewStoreEnricher(harness.items, nil, nil)), clock.NewReal())
	request := httptest.NewRequest(http.MethodGet, "/Items?ParentId="+scannerCollectionFolder(t, harness).ID.Dashed()+"&Recursive=true&IncludeItemTypes=Folder,BoxSet&Fields=DateCreated,Path&EnableTotalRecordCount=true&SortBy=DateCreated&SortOrder=Descending&StartIndex="+strconv.Itoa(startIndex)+"&Limit="+strconv.Itoa(limit), nil)
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET recursive folder DateCreated page status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var page folderDateCreatedResponse
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode recursive folder DateCreated page: %v; body=%s", err, record.Body.String())
	}
	if page.StartIndex != startIndex {
		t.Fatalf("recursive folder DateCreated page StartIndex = %d, want %d", page.StartIndex, startIndex)
	}
	return page
}

func assertFolderDateCreated(t *testing.T, got item.Item, kind item.Kind, want time.Time) {
	t.Helper()
	if got.Kind != kind {
		t.Fatalf("folder kind = %s, want %s; item=%#v", got.Kind, kind, got)
	}
	if got.DateCreated == nil || !got.DateCreated.Equal(want) {
		t.Fatalf("%s DateCreated = %v, want lifecycle timestamp %s", kind, got.DateCreated, want)
	}
}

func assertFolderDateCreatedPagePaths(t *testing.T, page folderDateCreatedResponse, want []string) {
	t.Helper()
	got := make([]string, len(page.Items))
	for index, value := range page.Items {
		got[index] = value.Path
		if value.DateCreated == nil {
			t.Fatalf("recursive folder DateCreated row %q omitted requested DateCreated", value.Path)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive folder DateCreated descending paths = %v, want source-lifecycle order %v", got, want)
	}
}
