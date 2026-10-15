// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// TestScannerMaterializesAndPreservesCollectionRootLifecycleDates keeps the
// scanner's virtual topology on the same lifecycle boundary as scanned
// folders. CollectionFolder and AggregateFolder are created from the injected
// clock, while a scanned file retains its independently injected ReadDir
// creation fact; neither root lifecycle fact may be rewritten on a rescan.
func TestScannerMaterializesAndPreservesCollectionRootLifecycleDates(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	media := scannerPath(t, harness.source, "root-lifecycle.mkv")
	writeScannerFile(t, media, "fixture")
	firstClock := time.Date(2026, time.July, 27, 18, 41, 53, 0, time.UTC)
	secondClock := firstClock.Add(72 * time.Hour)
	firstReadCreated := time.Date(2003, time.February, 4, 5, 6, 7, 0, time.UTC)
	secondReadCreated := firstReadCreated.Add(48 * time.Hour)
	clockNow, readCreated := firstClock, firstReadCreated

	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return clockNow },
		ReadDir: func(path string) ([]library.Entry, error) {
			entries, err := scannerReadDir(path)
			if err != nil {
				return nil, err
			}
			for index := range entries {
				if entries[index].Path == media {
					entries[index].Created = readCreated
				}
			}
			return entries, nil
		},
	})

	scannerScan(t, harness.scanner, "initial collection root lifecycle scan")
	collection, aggregate := collectionRootLifecycleItems(t, harness)
	collectionRootLifecycleAssert(t, "initial CollectionFolder", collection.DateCreated, collection.DateLastSaved, firstClock)
	collectionRootLifecycleAssert(t, "initial AggregateFolder", aggregate.DateCreated, aggregate.DateLastSaved, firstClock)
	storedMedia := scannerItemAtPath(t, harness.items, media)
	if storedMedia.DateCreated == nil || !storedMedia.DateCreated.Equal(firstReadCreated) {
		t.Fatalf("scanned media DateCreated = %v, want injected ReadDir fact %s", storedMedia.DateCreated, firstReadCreated)
	}

	clockNow, readCreated = secondClock, secondReadCreated
	scannerScan(t, harness.scanner, "rescan collection root lifecycle fixture")
	collection, aggregate = collectionRootLifecycleItems(t, harness)
	collectionRootLifecycleAssert(t, "rescanned CollectionFolder", collection.DateCreated, collection.DateLastSaved, firstClock)
	collectionRootLifecycleAssert(t, "rescanned AggregateFolder", aggregate.DateCreated, aggregate.DateLastSaved, firstClock)
	storedMedia = scannerItemAtPath(t, harness.items, media)
	if storedMedia.DateCreated == nil || !storedMedia.DateCreated.Equal(firstReadCreated) {
		t.Fatalf("rescanned media DateCreated = %v, want original injected ReadDir fact %s", storedMedia.DateCreated, firstReadCreated)
	}
}

func collectionRootLifecycleItems(t *testing.T, harness *scannerHarness) (collection, aggregate item.Item) {
	t.Helper()
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("scanner virtual folders = %#v, %v; want one collection", folders, err)
	}
	collection = scannerItemAtPath(t, harness.items, folders[0].Path)
	if collection.ParentID == nil {
		t.Fatalf("CollectionFolder missing AggregateFolder parent: %#v", collection)
	}
	aggregate, err = harness.items.Get(t.Context(), *collection.ParentID)
	if err != nil {
		t.Fatalf("load CollectionFolder aggregate parent %s: %v", *collection.ParentID, err)
	}
	if collection.Kind != item.KindCollectionFolder || aggregate.Kind != item.KindAggregateFolder {
		t.Fatalf("root kinds = %s/%s, want CollectionFolder/AggregateFolder", collection.Kind, aggregate.Kind)
	}
	return collection, aggregate
}

func collectionRootLifecycleAssert(t *testing.T, label string, created, saved *time.Time, want time.Time) {
	t.Helper()
	if created == nil || !created.Equal(want) {
		t.Errorf("%s DateCreated = %v, want lifecycle clock %s", label, created, want)
	}
	if saved == nil || !saved.Equal(want) {
		t.Errorf("%s DateLastSaved = %v, want lifecycle clock %s", label, saved, want)
	}
}
