// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerDeletesRemovedRootFolderAndSubtree proves that a media root
// removed from the library configuration is deleted on the next scan,
// including the physical root Folder and its media subtree.
func TestScannerDeletesRemovedRootFolderAndSubtree(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})

	// Add a second root with a movie.
	secondRoot := scannerPath(t, t.TempDir(), "second-media")
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatalf("create second root: %v", err)
	}
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, secondRoot); err != nil {
		t.Fatalf("add second media root: %v", err)
	}
	secondMovie := scannerPath(t, secondRoot, "removed-movie.mkv")
	writeScannerFile(t, secondMovie, "fixture")

	// Scan both roots.
	result := scannerScan(t, harness.scanner, "two-root scan")
	if len(result.Errors) != 0 {
		t.Fatalf("two-root scan errors = %#v", result.Errors)
	}

	// Verify the second-root movie exists.
	secondMovieItem := scannerItemAtPath(t, harness.items, secondMovie)
	if secondMovieItem.ID.IsNil() {
		t.Fatalf("second-root movie not found after two-root scan")
	}

	// Capture the physical root Folder ID before removal. The movie's
	// direct parent is the root Folder the scanner created for the
	// second media root. After the root is removed and the scan runs,
	// this Folder row must not exist.
	if secondMovieItem.ParentID == nil {
		t.Fatalf("second-root movie has no parent, want the root Folder")
	}
	rootFolderID := *secondMovieItem.ParentID

	// Remove the second root from the configuration.
	if err := harness.topology.RemoveMediaPath(t.Context(), harness.virtualID, secondRoot); err != nil {
		t.Fatalf("remove second media root: %v", err)
	}

	// Rescan. The removed root's Folder and movie subtree delete.
	result = scannerScan(t, harness.scanner, "removed-root scan")
	if len(result.Errors) != 0 {
		t.Fatalf("removed-root scan errors = %#v", result.Errors)
	}
	if result.Deleted == 0 {
		t.Fatalf("removed-root scan deleted = 0, want the removed root Folder and its subtree")
	}

	// The physical root Folder row is gone. A test that checks only the
	// deleted count and the movie absence cannot prove the Folder row
	// itself was deleted, so require ItemStore.Get to report not found.
	if _, err := harness.items.Get(t.Context(), rootFolderID); !store.IsNotFound(err) {
		t.Fatalf("Get(removed root Folder %s) = %v, want not found", rootFolderID, err)
	}

	// The movie under the removed root is gone.
	after, err := harness.items.Query(t.Context(), store.ItemQuery{Path: secondMovie})
	if err != nil {
		t.Fatalf("query removed-root movie: %v", err)
	}
	if len(after.Items) != 0 {
		t.Fatalf("removed-root movie still exists, want it deleted")
	}
}
