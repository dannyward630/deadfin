// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
)

// TestScannerPreservesInitialDateCreatedAcrossRescan proves DateCreated is the
// item creation fact, not a fresh filesystem observation on every scan. The
// ReadDir seam changes only the source-created timestamp between scans, so a
// passing result must have come from persisted scanner state.
func TestScannerPreservesInitialDateCreatedAcrossRescan(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "created-once.mkv")
	writeScannerFile(t, media, "fixture")
	initial := time.Date(2026, time.July, 27, 6, 44, 50, 0, time.UTC)
	rescanObservation := initial.Add(24 * time.Hour)
	created := map[string]time.Time{media: initial}

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     scannerReplayReadDir(created),
	})
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	first := scannerItemAtPath(t, harness.items, media)
	if first.DateCreated == nil || !first.DateCreated.Equal(initial) {
		t.Fatalf("initial persisted DateCreated = %v, want source timestamp %s", first.DateCreated, initial)
	}

	created[media] = rescanObservation
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("rescan after source timestamp observation changed: %v", err)
	}
	second := scannerItemAtPath(t, harness.items, media)
	if second.DateCreated == nil || !second.DateCreated.Equal(initial) {
		t.Fatalf("rescanned DateCreated = %v, want original persisted timestamp %s rather than new source observation %s", second.DateCreated, initial, rescanObservation)
	}
}

// TestScannerPersistsDateLastSavedFromLifecycleClock prevents DateLastSaved
// from being substituted with a filesystem timestamp. It is the metadata-save
// lifecycle time in upstream BaseItem, so the injected scanner clock must be
// what survives the persistence boundary. Source: BaseItem.cs:433-434 at
// upstream/jellyfin.
func TestScannerPersistsDateLastSavedFromLifecycleClock(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "saved-by-lifecycle.mkv")
	writeScannerFile(t, media, "fixture")
	created := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	modified := created.Add(time.Hour)
	saved := time.Date(2026, time.July, 27, 6, 44, 50, 123456789, time.FixedZone("source-offset", 3600))

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return saved },
		ReadDir: func(path string) ([]library.Entry, error) {
			entries, err := scannerReadDir(path)
			if err != nil {
				return nil, err
			}
			for index := range entries {
				if entries[index].Path == media {
					entries[index].Created = created
					entries[index].Modified = modified
				}
			}
			return entries, nil
		},
	})
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan lifecycle timestamp fixture: %v", err)
	}
	persisted := scannerItemAtPath(t, harness.items, media)
	want := saved.UTC().Truncate(time.Microsecond)
	if persisted.DateLastSaved == nil || !persisted.DateLastSaved.Equal(want) {
		t.Fatalf("persisted DateLastSaved = %v, want scanner lifecycle UTC time %s", persisted.DateLastSaved, want)
	}
	if persisted.DateLastSaved.Equal(modified) {
		t.Fatalf("persisted DateLastSaved = filesystem modified time %s, want lifecycle save time %s", modified, want)
	}
}
