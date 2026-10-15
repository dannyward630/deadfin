// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreRawVirtualPathStorage makes sure that Upsert stores the raw
// virtual-path token and a direct SQL read returns it unchanged. The store
// must not expand or rewrite the token because it is part of the item
// identity contract.
func TestItemStoreRawVirtualPathStorage(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)

	id := dto.UUID{0x01}
	name := "metadata studio"
	stored := "%MetadataPath%/Studio/Case Studio"
	storedImage := "%MetadataPath%/library/backdrop.jpg"
	if err := items.Upsert(ctx, item.Item{
		ID: id, Kind: item.KindMovie, Name: &name, Path: &stored,
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypeBackdrop, Path: storedImage, Index: 0},
		},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Direct SQL read of items.path proves the raw token survives storage.
	var itemPath string
	if err := stores.Reader().QueryRowContext(ctx, "SELECT path FROM items WHERE id = ?", id[:]).Scan(&itemPath); err != nil {
		t.Fatalf("direct SQL read items.path: %v", err)
	}
	if itemPath != stored {
		t.Fatalf("items.path = %q, want raw %q", itemPath, stored)
	}

	// Direct SQL read of item_images.path proves the raw token survives
	// satellite storage.
	var imagePath string
	if err := stores.Reader().QueryRowContext(ctx, "SELECT path FROM item_images WHERE item_id = ? AND type = ? AND idx = ?", id[:], item.ImageTypeBackdrop.String(), 0).Scan(&imagePath); err != nil {
		t.Fatalf("direct SQL read item_images.path: %v", err)
	}
	if imagePath != storedImage {
		t.Fatalf("item_images.path = %q, want raw %q", imagePath, storedImage)
	}

	// The typed Get API must also return the raw token, not an expanded path.
	got, err := items.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Path == nil {
		t.Fatal("item path is nil after upsert")
	}
	if *got.Path != stored {
		t.Fatalf("Get path = %q, want raw %q", *got.Path, stored)
	}
}

// TestItemStoreVirtualPathReadContract makes sure that the store returns raw
// tokens for both %MetadataPath% and %AppDataPath% and that a path with no
// token is returned unchanged. Direct SQL reads prove the raw values persist.
func TestItemStoreVirtualPathReadContract(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)

	for _, tc := range []struct {
		name   string
		id     dto.UUID
		kind   item.Kind
		stored string
	}{
		{name: "metadata path token", id: dto.UUID{0x02}, kind: item.KindMovie, stored: "%MetadataPath%/People/G/Generator"},
		{name: "appdata path token", id: dto.UUID{0x03}, kind: item.KindMovie, stored: "%AppDataPath%/library/poster.jpg"},
		{name: "both tokens", id: dto.UUID{0x04}, kind: item.KindMovie, stored: "%AppDataPath%/ref/%MetadataPath%/meta"},
		{name: "no token", id: dto.UUID{0x05}, kind: item.KindMovie, stored: "/media/movies/feature.mkv"},
		{name: "literal percent", id: dto.UUID{0x06}, kind: item.KindMovie, stored: "/metadata/Caf%C3%A9%20A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := tc.name
			path := tc.stored
			if err := items.Upsert(ctx, item.Item{ID: tc.id, Kind: tc.kind, Name: &n, Path: &path}); err != nil {
				t.Fatalf("upsert %s: %v", tc.name, err)
			}
			// Direct SQL read proves the exact stored value persists.
			var raw string
			if err := stores.Reader().QueryRowContext(ctx, "SELECT path FROM items WHERE id = ?", tc.id[:]).Scan(&raw); err != nil {
				t.Fatalf("direct SQL read %s: %v", tc.name, err)
			}
			if raw != tc.stored {
				t.Fatalf("%s: items.path = %q, want raw %q", tc.name, raw, tc.stored)
			}
			// The typed API must agree.
			got, err := items.Get(ctx, tc.id)
			if err != nil {
				t.Fatalf("get %s: %v", tc.name, err)
			}
			if got.Path == nil {
				t.Fatalf("%s: path is nil", tc.name)
			}
			if *got.Path != tc.stored {
				t.Fatalf("%s: Get path = %q, want raw %q", tc.name, *got.Path, tc.stored)
			}
		})
	}
}

// TestItemStoreRawIdentityReconciliation makes sure that SQL path predicates
// match the raw token and that ReconcileChildren preserves raw-token paths.
// The store uses raw paths for identity, predicates, and reconciliation, so
// a predicate that queries the stored path must match the raw value.
func TestItemStoreRawIdentityReconciliation(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)

	parentID := dto.UUID{0x10}
	parentName := "library root"
	parentPath := "%MetadataPath%/library"
	if err := items.Upsert(ctx, item.Item{
		ID: parentID, Kind: item.KindFolder, Name: &parentName, Path: &parentPath,
	}); err != nil {
		t.Fatalf("upsert parent: %v", err)
	}

	// A SQL path predicate must match the raw stored path, not an expanded one.
	// If the store expanded on write, a predicate against the raw token would
	// fail.
	var predicateMatch int
	if err := stores.Reader().QueryRowContext(ctx,
		"SELECT count(*) FROM items WHERE path = ?", parentPath,
	).Scan(&predicateMatch); err != nil {
		t.Fatalf("predicate query: %v", err)
	}
	if predicateMatch != 1 {
		t.Fatalf("path predicate matched %d rows, want 1 (raw token must match)", predicateMatch)
	}

	// A LIKE predicate must also match the raw token prefix.
	var likeMatch int
	if err := stores.Reader().QueryRowContext(ctx,
		"SELECT count(*) FROM items WHERE path LIKE ?", "%MetadataPath%/library%",
	).Scan(&likeMatch); err != nil {
		t.Fatalf("like predicate query: %v", err)
	}
	if likeMatch < 1 {
		t.Fatalf("path LIKE predicate matched %d rows, want at least 1", likeMatch)
	}

	// Reconcile children with raw-token paths. The reconciled children must
	// keep their raw tokens in storage.
	childID := dto.UUID{0x11}
	childName := "feature movie"
	childPath := "%MetadataPath%/library/movies/feature.mkv"
	result, err := items.ReconcileChildren(ctx, parentID, []item.Item{
		{ID: childID, Kind: item.KindMovie, Name: &childName, Path: &childPath, ParentID: &parentID},
	})
	if err != nil {
		t.Fatalf("reconcile children: %v", err)
	}
	if len(result.Inserted) != 1 {
		t.Fatalf("reconcile inserted %d, want 1", len(result.Inserted))
	}

	// Direct SQL read proves the raw token survived reconciliation.
	var storedChildPath string
	if err := stores.Reader().QueryRowContext(ctx,
		"SELECT path FROM items WHERE id = ?", childID[:],
	).Scan(&storedChildPath); err != nil {
		t.Fatalf("direct SQL read child path: %v", err)
	}
	if storedChildPath != childPath {
		t.Fatalf("child path after reconcile = %q, want raw %q", storedChildPath, childPath)
	}

	// The typed Get API must also return the raw token.
	got, err := items.Get(ctx, childID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if got.Path == nil || *got.Path != childPath {
		t.Fatalf("Get child path = %v, want raw %q", got.Path, childPath)
	}
}
