// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/library"
)

func TestScannerPublishesCommittedLibraryChangesAndRefreshProgressOnFakeClock(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Clock: fake})
	changes := harness.scanner.Changes().Subscribe(1)
	progress := harness.scanner.Progress().Subscribe(events.SubscriptionOptions{Buffer: 16})
	moviePath := scannerPath(t, harness.source, "new movie.mkv")
	writeScannerFile(t, moviePath, "movie")

	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan library change fixture: %v", err)
	}
	var sawRootProgress bool
	for {
		event := <-progress.Events()
		// planSources publishes a scanned progress event for the
		// CollectionFolder after resolving the source root entries.
		if event.Status == "scanned" && event.Progress == 100 {
			sawRootProgress = true
		}
		if event.Status == "completed" {
			break
		}
	}
	if !sawRootProgress {
		t.Fatal("scan progress did not publish the root 100% event")
	}

	fake.Advance(30 * time.Second)
	update := receiveScannerLibraryUpdate(t, changes.Events())
	if !scannerUUIDSliceContains(update.ItemsAdded, scannerID(moviePath)) {
		t.Fatalf("LibraryChanged ItemsAdded = %v, want scanned movie %s", update.ItemsAdded, scannerID(moviePath))
	}
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("load collection folder identity: folders=%+v err=%v", folders, err)
	}
	collectionFolderID := scannerID(folders[0].Path)
	if !scannerUUIDSliceContains(update.FoldersAddedTo, collectionFolderID) {
		t.Fatalf("LibraryChanged FoldersAddedTo = %v, want collection folder %s", update.FoldersAddedTo, collectionFolderID)
	}
	if !scannerUUIDSliceContains(update.CollectionFolders, collectionFolderID) {
		t.Fatalf("LibraryChanged CollectionFolders = %v, want resolved collection folder %s", update.CollectionFolders, collectionFolderID)
	}
}

func receiveScannerLibraryUpdate(t *testing.T, values <-chan library.UpdateInfo) library.UpdateInfo {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("scanner LibraryChanged subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("scanner LibraryChanged update was not delivered")
		return library.UpdateInfo{}
	}
}

func scannerUUIDSliceContains(values []dto.UUID, want dto.UUID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
