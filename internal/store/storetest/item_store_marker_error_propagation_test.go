// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreQueryPropagatesMarkerJSONError proves that a corrupt extra
// column on a CollectionFolder propagates a JSON error from the marker read.
// The query must fail rather than silently fall back to the legacy parent
// filter. This is the P2 regression for store/marker-error-propagation.
func TestItemStoreQueryPropagatesMarkerJSONError(t *testing.T) {
	stores, err := storesqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open marker error store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close marker error store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	collectionID := dto.UUID{0x01}
	collectionPath := "/library/movies"
	collectionName := "Movies"
	collection := item.Item{
		ID:       collectionID,
		Kind:     item.KindCollectionFolder,
		IsFolder: true,
		Path:     &collectionPath,
		Name:     &collectionName,
		SortName: &collectionName,
		Extra: item.ExtraFields{
			CollectionFolder: &item.CollectionFolderExtra{
				PhysicalFolderIDs: []dto.UUID{{0x03}},
			},
		},
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed collection folder: %v", err)
	}

	// Corrupt the extra column so the marker read fails to unmarshal.
	corruptJSON := []byte("{not valid json")
	if _, err := stores.Writer().ExecContext(t.Context(),
		"UPDATE items SET extra = "+stores.Dialect().Placeholder(1)+" WHERE id = "+stores.Dialect().Placeholder(2),
		corruptJSON, collectionID[:]); err != nil {
		t.Fatalf("corrupt collection extra: %v", err)
	}

	if _, err := items.Query(t.Context(), store.ItemQuery{ParentID: &collectionID}); err == nil {
		t.Fatalf("query with corrupt marker: error = nil, want a JSON unmarshal error")
	}
}

// TestItemStoreQueryPropagatesMarkerDatabaseError proves that a database
// error from the marker read propagates rather than returning the
// absent-marker state. The marker read shares the page's reader
// transaction, so the test closes the reader pool and the read fails with a
// connection error.
func TestItemStoreQueryPropagatesMarkerDatabaseError(t *testing.T) {
	stores, err := storesqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open marker db error store: %v", err)
	}
	items := store.NewItemStore(stores)

	collectionID := dto.UUID{0x01}
	collectionPath := "/library/movies"
	collectionName := "Movies"
	collection := item.Item{
		ID:       collectionID,
		Kind:     item.KindCollectionFolder,
		IsFolder: true,
		Path:     &collectionPath,
		Name:     &collectionName,
		SortName: &collectionName,
		Extra: item.ExtraFields{
			CollectionFolder: &item.CollectionFolderExtra{
				PhysicalFolderIDs: []dto.UUID{{0x03}},
			},
		},
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed collection folder: %v", err)
	}

	// Close the reader so the marker read fails with a database error.
	if err := stores.Reader().Close(); err != nil {
		t.Fatalf("close reader pool: %v", err)
	}

	if _, err := items.Query(t.Context(), store.ItemQuery{ParentID: &collectionID}); err == nil {
		t.Fatalf("query after reader close: error = nil, want a database error")
	}
}

// TestItemStoreQueryMarkerAbsentIsNotAnError proves that a missing parent
// row (sql.ErrNoRows) is not an error: the query falls back to the strict
// parent filter. This guards the boundary between the propagated error and
// the absent-marker state.
func TestItemStoreQueryMarkerAbsentIsNotAnError(t *testing.T) {
	stores, err := storesqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open marker absent store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close marker absent store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	missingParentID := dto.UUID{0x0a}
	page, err := items.Query(t.Context(), store.ItemQuery{ParentID: &missingParentID})
	if err != nil {
		t.Fatalf("query with missing parent: error = %v, want nil (absent marker is not an error)", err)
	}
	if page.TotalRecordCount != 0 {
		t.Fatalf("query total = %d, want 0 for a missing parent", page.TotalRecordCount)
	}
}
