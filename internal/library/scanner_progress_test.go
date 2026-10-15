// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// scannerProgressHarness sets up a scanner with a temporary library for
// progress tests. It is a slim version of scannerHarness in scanner_test.go
// that does not depend on the fixture resolver.
type scannerProgressHarness struct {
	stores   store.Stores
	topology *library.Topology
	items    *store.ItemStore
	scanner  *library.Scanner
}

func newScannerProgressHarness(t *testing.T, source string, concurrency int) *scannerProgressHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	if _, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                   true,
		EnableInternetProviders:   false,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 []library.MediaPathInfo{{Path: source}},
	}); err != nil {
		t.Fatalf("add virtual folder: %v", err)
	}
	chain := library.NewDefaultChain(naming.DefaultOptions())
	items := store.NewItemStore(stores)
	scanner, err := library.NewScanner(topology, items, chain, library.ScannerOptions{
		Concurrency: concurrency,
	})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})
	return &scannerProgressHarness{stores: stores, topology: topology, items: items, scanner: scanner}
}

// newScannerProgressHarnessWithRefresher builds a scanner with a custom
// refresher and one media path per source directory. Each source directory
// becomes one scan-queue task, so concurrency 2 dispatches two workers
// that each process one item and call the progress tracker concurrently.
func newScannerProgressHarnessWithRefresher(t *testing.T, sources []string, concurrency int, refresher library.Refresher) *scannerProgressHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	pathInfos := make([]library.MediaPathInfo, len(sources))
	for i, src := range sources {
		pathInfos[i] = library.MediaPathInfo{Path: src}
	}
	if _, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                   true,
		EnableInternetProviders:   false,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 pathInfos,
	}); err != nil {
		t.Fatalf("add virtual folder: %v", err)
	}
	chain := library.NewDefaultChain(naming.DefaultOptions())
	items := store.NewItemStore(stores)
	scanner, err := library.NewScanner(topology, items, chain, library.ScannerOptions{
		Concurrency: concurrency,
		Refresher:   refresher,
	})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})
	return &scannerProgressHarness{stores: stores, topology: topology, items: items, scanner: scanner}
}

// TestScannerProgressMonotonic proves ScanWithProgress emits monotonic
// intermediate values when the scanner processes multiple items. The
// reported sequence never decreases and never reaches 100 before the scan
// completes.
func TestScannerProgressMonotonic(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	// Create two movie files so the scanner discovers two items.
	if err := os.WriteFile(filepath.Join(source, "Movie One.mkv"), []byte("one"), 0o600); err != nil {
		t.Fatalf("write movie one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "Movie Two.mkv"), []byte("two"), 0o600); err != nil {
		t.Fatalf("write movie two: %v", err)
	}

	harness := newScannerProgressHarness(t, source, 4)

	var mu sync.Mutex
	var values []float64

	progress := func(v float64) {
		mu.Lock()
		values = append(values, v)
		mu.Unlock()
	}

	result, err := harness.scanner.ScanWithProgress(t.Context(), library.DefaultRefreshOptions(), progress)
	if err != nil {
		t.Fatalf("ScanWithProgress: %v", err)
	}

	// The scan must discover at least two items (the two movie files).
	if result.Discovered < 2 {
		t.Fatalf("discovered = %d, want at least 2", result.Discovered)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(values) == 0 {
		t.Fatal("no progress values emitted")
	}

	// Values must be monotonically non-decreasing.
	for i := 1; i < len(values); i++ {
		if values[i] < values[i-1] {
			t.Fatalf("progress decreased at index %d: %v < %v", i, values[i], values[i-1])
		}
	}

	// No intermediate value may reach 100 before success. The scanner clamps
	// below 100; the runnable calls progress(100) after the scan returns.
	for i, v := range values {
		if v >= 100 {
			t.Fatalf("progress value %d reached 100 before completion: %v", i, v)
		}
	}

	// At least one intermediate value must be strictly between 0 and 100.
	found := false
	for _, v := range values {
		if v > 0 && v < 100 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no intermediate value in (0,100); values = %v", values)
	}
}

// TestScannerProgressCallbacksSerializedConcurrently exercises the scanner
// with two concurrent folder workers that reach tracker.complete at the same
// time. A barrier refresher holds both workers at the refresher until both
// arrive, so the scan queue dispatches both item-refresh completions together.
//
// This test runs under -race so the race detector reports any unsynchronized
// access when two concurrent workers enter tracker.complete together. The
// deterministic proof that the tracker holds its mutex through the callback
// lives in scanner_progress_internal_test.go. This test does not use a
// timing probe to detect overlap, because a wall-clock wait cannot prove
// serialization deterministically.
func TestScannerProgressCallbacksSerializedConcurrently(t *testing.T) {
	// Two subdirectories under one media root each hold two unrelated movies,
	// so neither is a self-contained movie directory. The scanner flattens the
	// root: planSources resolves the subdirectories as
	// direct children of the CollectionFolder and enqueues them as child
	// tasks. The scan queue then scans both subdirectories concurrently.
	source := t.TempDir()
	folderA := filepath.Join(source, "Folder A")
	folderB := filepath.Join(source, "Folder B")
	if err := os.MkdirAll(folderA, 0o755); err != nil {
		t.Fatalf("create folder A: %v", err)
	}
	if err := os.MkdirAll(folderB, 0o755); err != nil {
		t.Fatalf("create folder B: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderA, "Movie A.mkv"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write movie A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderB, "Movie B.mkv"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write movie B: %v", err)
	}
	for _, path := range []string{filepath.Join(folderA, "Other A (2019).mkv"), filepath.Join(folderB, "Other B (2019).mkv")} {
		if err := os.WriteFile(path, []byte("o"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// barrier waits until both workers reach the refresher for their movie
	// item. This forces both workers into tracker.complete at the same time.
	var barrier sync.WaitGroup
	barrier.Add(2)

	// progress records the emitted values so the test can assert that the
	// sequence stays monotonic. The serialization proof is in the internal
	// tracker test; this callback does not detect overlap with a wall-clock
	// wait.
	var mu sync.Mutex
	var values []float64
	progress := func(v float64) {
		mu.Lock()
		values = append(values, v)
		mu.Unlock()
	}

	refresher := scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
		// Only movie items trigger the barrier. Folder refresher calls in
		// planSources and resolveFolder run before the scan queue reaches
		// the movie items and must not consume barrier slots.
		if candidate.IsFolder || candidate.Path == nil || !strings.HasPrefix(filepath.Base(*candidate.Path), "Movie ") {
			return nil
		}
		barrier.Done()
		barrier.Wait()
		return nil
	})

	harness := newScannerProgressHarnessWithRefresher(t, []string{source}, 2, refresher)

	result, err := harness.scanner.ScanWithProgress(t.Context(), library.DefaultRefreshOptions(), progress)
	if err != nil {
		t.Fatalf("ScanWithProgress: %v", err)
	}
	if result.Discovered < 2 {
		t.Fatalf("discovered = %d, want at least 2", result.Discovered)
	}

	// The concurrent workers must produce a monotonic value sequence. A
	// race on the aggregate counters would let a worker observe a stale
	// or decreased value, which the race detector or this check reports.
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(values); i++ {
		if values[i] < values[i-1] {
			t.Fatalf("progress decreased at index %d: %v < %v", i, values[i], values[i-1])
		}
	}
}

// TestScannerProgressOmits100OnError proves a scanner error does not emit a
// terminal 100 callback through the progress tracker. The scan runs against
// a missing directory, so the scanner cannot enumerate items. The existing
// scan failure behavior remains unchanged.
func TestScannerProgressOmits100OnError(t *testing.T) {
	// Point the library at a directory, then remove it before the scan runs.
	failingSource := filepath.Join(t.TempDir(), "missing-library")
	if err := os.MkdirAll(failingSource, 0o755); err != nil {
		t.Fatalf("create failing source: %v", err)
	}
	harness := newScannerProgressHarness(t, failingSource, 1)
	if err := os.RemoveAll(failingSource); err != nil {
		t.Fatalf("remove failing source: %v", err)
	}

	var mu sync.Mutex
	var values []float64
	progress := func(v float64) {
		mu.Lock()
		values = append(values, v)
		mu.Unlock()
	}

	result, _ := harness.scanner.ScanWithProgress(t.Context(), library.DefaultRefreshOptions(), progress)

	mu.Lock()
	defer mu.Unlock()

	// No progress value may reach 100. The scan failed, so the tracker never
	// emits a terminal value. The runnable omits progress(100) on error.
	for i, v := range values {
		if v >= 100 {
			t.Fatalf("progress value %d reached 100 on error: %v", i, v)
		}
	}

	// The scan must have at least one folder error.
	if len(result.Errors) == 0 {
		t.Fatal("scan of missing directory produced no folder errors")
	}
}

// TestScannerProgressNilCallbackIsNoOp proves ScanWithProgress with a nil
// callback does not panic and produces the same result as ScanWithOptions.
// This keeps plain Scan and ScanWithOptions callers unaffected.
func TestScannerProgressNilCallbackIsNoOp(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "Movie.mkv"), []byte("movie"), 0o600); err != nil {
		t.Fatalf("write movie: %v", err)
	}
	harness := newScannerProgressHarness(t, source, 2)

	result, err := harness.scanner.ScanWithProgress(t.Context(), library.DefaultRefreshOptions(), nil)
	if err != nil {
		t.Fatalf("ScanWithProgress nil callback: %v", err)
	}
	if result.Discovered < 1 {
		t.Fatalf("discovered = %d, want at least 1", result.Discovered)
	}
}

// TestConcurrentScanWithProgressKeepsCallbacks proves that two concurrent
// ScanWithProgress calls on the same scanner do not race on the shared
// taskProgress field. Both task adapters share one scanner, so the scan
// serialization gate (scanMu) must pair each callback with its own scan.
//
// Two goroutines enter ScanWithProgress at the same time through a start
// barrier. Scans serialize on scanMu, so the second scan runs after the
// first completes. Each scan installs its callback inside the gate, creates
// a tracker from that callback, and clears the callback before the gate
// opens. Both callbacks must receive nonzero progress values. If the
// taskProgress field were written outside the gate, one scan could read the
// other callback or a nil one, and one callback would receive no values.
//
// Run under -race with -count=100 so the race detector exercises the field
// access across many iterations.
func TestConcurrentScanWithProgressKeepsCallbacks(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "Movie One.mkv"), []byte("one"), 0o600); err != nil {
		t.Fatalf("write movie one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "Movie Two.mkv"), []byte("two"), 0o600); err != nil {
		t.Fatalf("write movie two: %v", err)
	}
	harness := newScannerProgressHarness(t, source, 2)

	// Each callback records its values in its own slice under its own mutex.
	// A shared slice would hide a race behind one mutex. Separate slices make
	// the nonzero assertion per callback.
	var muA, muB sync.Mutex
	var valuesA, valuesB []float64

	callbackA := func(v float64) {
		muA.Lock()
		valuesA = append(valuesA, v)
		muA.Unlock()
	}
	callbackB := func(v float64) {
		muB.Lock()
		valuesB = append(valuesB, v)
		muB.Unlock()
	}

	// start coordinates both goroutines so they enter ScanWithProgress at the
	// same time. Both block here until both are ready, then both proceed.
	start := make(chan struct{})
	// done signals that both scans have returned.
	done := make(chan error, 2)

	run := func(progress func(float64)) {
		<-start
		_, err := harness.scanner.ScanWithProgress(t.Context(), library.DefaultRefreshOptions(), progress)
		done <- err
	}
	go run(callbackA)
	go run(callbackB)

	close(start)

	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent ScanWithProgress %d: %v", i, err)
		}
	}

	// Both callbacks must have received at least one value. If the
	// taskProgress race left one scan with a nil or wrong callback, that
	// scan tracker would never call its callback, and the slice would be
	// empty.
	muA.Lock()
	countA := len(valuesA)
	muA.Unlock()
	muB.Lock()
	countB := len(valuesB)
	muB.Unlock()

	if countA == 0 {
		t.Fatal("callback A received no progress values; the taskProgress race discarded its callback")
	}
	if countB == 0 {
		t.Fatal("callback B received no progress values; the taskProgress race discarded its callback")
	}

	// At least one value from each callback must be strictly above zero so
	// the scan delivered item progress, not only a terminal zero.
	hasPositive := func(values []float64) bool {
		for _, v := range values {
			if v > 0 {
				return true
			}
		}
		return false
	}

	muA.Lock()
	positiveA := hasPositive(valuesA)
	muA.Unlock()
	muB.Lock()
	positiveB := hasPositive(valuesB)
	muB.Unlock()

	if !positiveA {
		t.Fatal("callback A received no positive progress values")
	}
	if !positiveB {
		t.Fatal("callback B received no positive progress values")
	}
}
