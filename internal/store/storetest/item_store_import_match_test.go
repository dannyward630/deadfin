// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestItemStoreImportMatchIndexSQLite proves the importer's match index maps
// an item's user-data keys and its path to the item, and a library name to
// its CollectionFolder.
func TestItemStoreImportMatchIndexSQLite(t *testing.T) {
	items := openItemStore(t)
	library := hierarchyItem(t, 12900, item.KindCollectionFolder, nil)
	library.Name = stringPtr("Movies")
	if err := items.Upsert(t.Context(), library); err != nil {
		t.Fatalf("upsert library: %v", err)
	}
	movie := reconciliationChild(t, 12901, item.KindMovie, library.ID, "Heat")
	movie.Path = stringPtr("/media/movies/Heat (1995)/Heat.mkv")
	movie.ProviderIDs = providerIDs("Tmdb", "949")
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("upsert movie: %v", err)
	}

	index, err := items.ImportMatchIndex(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := index.ByPath[*movie.Path]; got != movie.ID {
		t.Errorf("ByPath[%q] = %v, want %v", *movie.Path, got, movie.ID)
	}
	if got := index.LibrariesByName["Movies"]; got != library.ID {
		t.Errorf("LibrariesByName[Movies] = %v, want %v", got, library.ID)
	}
	keyed := false
	for key, id := range index.ByUserDataKey {
		if id == movie.ID {
			keyed = true
			t.Logf("movie user-data key %q", key)
		}
	}
	if !keyed {
		t.Errorf("no user-data key maps to the movie: %v", index.ByUserDataKey)
	}
}
