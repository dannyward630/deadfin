// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestReconcileAdoptionPreservesPartialProviderIDs proves that an adopted
// child keeps both its desired provider IDs and the previous provider IDs
// the desired item omits. The scanner's refresher produces local-source
// metadata only: an NFO carrying one tmdb id yields a non-nil ProviderIDs
// with one entry. Without per-key preservation, the prior row's imdb and
// tvdb ids are dropped on adoption. This is the P1 regression for
// cutover/partial-satellite-family-loss at the provider-IDs granularity.
func TestReconcileAdoptionPreservesPartialProviderIDs(t *testing.T) {
	ctx := t.Context()
	stores, err := storesqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open satellite adoption store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close satellite adoption store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	parentA := dto.UUID{0x10}
	parentB := dto.UUID{0x20}
	childID := dto.UUID{0x30}

	// Seed the parent folders so the ancestors table and cycle checks
	// resolve.
	if err := items.Upsert(ctx, folderItem(parentA, nil)); err != nil {
		t.Fatalf("seed parentA: %v", err)
	}
	if err := items.Upsert(ctx, folderItem(parentB, nil)); err != nil {
		t.Fatalf("seed parentB: %v", err)
	}

	// Seed the child under parentA with two provider ids: tmdb and imdb.
	previous := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentA,
		Name:     strPtr("Adopted Movie"),
	}
	previous.ProviderIDs.Set("tmdb", "prior-tmdb-42")
	previous.ProviderIDs.Set("imdb", "prior-imdb-99")
	if err := items.Upsert(ctx, previous); err != nil {
		t.Fatalf("seed previous item: %v", err)
	}

	// Build a desired item under parentB with only one provider id (tmdb).
	// This simulates a refresher that re-derived the local NFO source but
	// not the remote imdb lookup the prior row carried.
	desired := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentB,
		Name:     strPtr("Adopted Movie"),
	}
	desired.ProviderIDs.Set("tmdb", "desired-tmdb-42")

	if _, err := items.ReconcileScannedChildren(ctx, parentB, []item.Item{desired}, nil); err != nil {
		t.Fatalf("reconcile adoption: %v", err)
	}

	stored, err := items.Get(ctx, childID)
	if err != nil {
		t.Fatalf("get adopted child: %v", err)
	}

	// The desired tmdb id survived.
	if v, ok := stored.ProviderIDs.Get("tmdb"); !ok || v != "desired-tmdb-42" {
		t.Fatalf("tmdb id = %v, want desired-tmdb-42", stored.ProviderIDs.Snapshot())
	}

	// The previous imdb id survived even though the desired item omitted it.
	if v, ok := stored.ProviderIDs.Get("imdb"); !ok || v != "prior-imdb-99" {
		t.Fatalf("imdb id lost during adoption = %v, want prior-imdb-99", stored.ProviderIDs.Snapshot())
	}
}

// TestReconcileAdoptionPreservesPartialImages proves that an adopted child
// keeps both its desired images and the previous images the desired item
// omits. A locally-found poster yields one ImageInfo, so without per-image
// preservation the prior row's backdrop and logo are dropped on adoption.
// This is the P1 regression for cutover/partial-satellite-family-loss at
// the image granularity.
func TestReconcileAdoptionPreservesPartialImages(t *testing.T) {
	ctx := t.Context()
	stores, err := storesqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open image adoption store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close image adoption store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	parentA := dto.UUID{0x10}
	parentB := dto.UUID{0x20}
	childID := dto.UUID{0x30}

	// Seed the parent folders so the ancestors table and cycle checks
	// resolve.
	if err := items.Upsert(ctx, folderItem(parentA, nil)); err != nil {
		t.Fatalf("seed parentA: %v", err)
	}
	if err := items.Upsert(ctx, folderItem(parentB, nil)); err != nil {
		t.Fatalf("seed parentB: %v", err)
	}

	// Seed the child under parentA with three images: primary, backdrop, logo.
	previous := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentA,
		Name:     strPtr("Adopted Movie"),
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Index: 0, Path: "/images/primary.jpg"},
			{Type: item.ImageTypeBackdrop, Index: 0, Path: "/images/backdrop.jpg"},
			{Type: item.ImageTypeLogo, Index: 0, Path: "/images/logo.png"},
		},
	}
	if err := items.Upsert(ctx, previous); err != nil {
		t.Fatalf("seed previous item: %v", err)
	}

	// Build a desired item under parentB with only one image (primary).
	// This simulates a refresher that found the local poster but not the
	// remote backdrop and logo the prior row carried.
	desired := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentB,
		Name:     strPtr("Adopted Movie"),
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Index: 0, Path: "/images/primary.jpg"},
		},
	}

	if _, err := items.ReconcileScannedChildren(ctx, parentB, []item.Item{desired}, nil); err != nil {
		t.Fatalf("reconcile adoption: %v", err)
	}

	stored, err := items.Get(ctx, childID)
	if err != nil {
		t.Fatalf("get adopted child: %v", err)
	}

	// Build a lookup of stored images by type so order does not matter.
	storedByType := make(map[item.ImageType]item.ImageInfo, len(stored.ImageInfos))
	for _, img := range stored.ImageInfos {
		storedByType[img.Type] = img
	}

	// The desired primary image survived.
	if img, ok := storedByType[item.ImageTypePrimary]; !ok || img.Path != "/images/primary.jpg" {
		t.Fatalf("primary image lost during adoption = %v, want /images/primary.jpg", stored.ImageInfos)
	}

	// The previous backdrop survived even though the desired item omitted it.
	if img, ok := storedByType[item.ImageTypeBackdrop]; !ok || img.Path != "/images/backdrop.jpg" {
		t.Fatalf("backdrop image lost during adoption = %v, want /images/backdrop.jpg", stored.ImageInfos)
	}

	// The previous logo survived even though the desired item omitted it.
	if img, ok := storedByType[item.ImageTypeLogo]; !ok || img.Path != "/images/logo.png" {
		t.Fatalf("logo image lost during adoption = %v, want /images/logo.png", stored.ImageInfos)
	}
}

// TestReconcileAdoptionOverwritesConflictingProviderID proves that a desired
// provider id with the same key as a previous one overwrites the previous
// value. The per-key merge carries previous keys the desired item omits,
// but a key the desired item sets wins. This guards the boundary between
// preservation and replacement.
func TestReconcileAdoptionOverwritesConflictingProviderID(t *testing.T) {
	ctx := t.Context()
	stores, err := storesqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open conflict adoption store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close conflict adoption store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	parentA := dto.UUID{0x10}
	parentB := dto.UUID{0x20}
	childID := dto.UUID{0x30}

	if err := items.Upsert(ctx, folderItem(parentA, nil)); err != nil {
		t.Fatalf("seed parentA: %v", err)
	}
	if err := items.Upsert(ctx, folderItem(parentB, nil)); err != nil {
		t.Fatalf("seed parentB: %v", err)
	}

	// Seed the child with tmdb and imdb.
	previous := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentA,
		Name:     strPtr("Adopted Movie"),
	}
	previous.ProviderIDs.Set("tmdb", "prior-tmdb-42")
	previous.ProviderIDs.Set("imdb", "prior-imdb-99")
	if err := items.Upsert(ctx, previous); err != nil {
		t.Fatalf("seed previous item: %v", err)
	}

	// The desired item sets tmdb to a new value and omits imdb.
	desired := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentB,
		Name:     strPtr("Adopted Movie"),
	}
	desired.ProviderIDs.Set("tmdb", "desired-tmdb-7")

	if _, err := items.ReconcileScannedChildren(ctx, parentB, []item.Item{desired}, nil); err != nil {
		t.Fatalf("reconcile adoption: %v", err)
	}

	stored, err := items.Get(ctx, childID)
	if err != nil {
		t.Fatalf("get adopted child: %v", err)
	}

	// The desired tmdb value won.
	if v, ok := stored.ProviderIDs.Get("tmdb"); !ok || v != "desired-tmdb-7" {
		t.Fatalf("tmdb id = %v, want desired-tmdb-7", stored.ProviderIDs.Snapshot())
	}

	// The previous imdb id survived because the desired item omitted it.
	if v, ok := stored.ProviderIDs.Get("imdb"); !ok || v != "prior-imdb-99" {
		t.Fatalf("imdb id lost = %v, want prior-imdb-99", stored.ProviderIDs.Snapshot())
	}
}

// TestReconcileAdoptionOverwritesConflictingImage proves that a desired
// image with the same Type and Index as a previous one overwrites the
// previous image. The per-image merge carries previous images the desired
// item omits, but an image with the same Type/Index wins. This guards the
// boundary between preservation and replacement.
func TestReconcileAdoptionOverwritesConflictingImage(t *testing.T) {
	ctx := t.Context()
	stores, err := storesqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open image conflict store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close image conflict store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	parentA := dto.UUID{0x10}
	parentB := dto.UUID{0x20}
	childID := dto.UUID{0x30}

	if err := items.Upsert(ctx, folderItem(parentA, nil)); err != nil {
		t.Fatalf("seed parentA: %v", err)
	}
	if err := items.Upsert(ctx, folderItem(parentB, nil)); err != nil {
		t.Fatalf("seed parentB: %v", err)
	}

	// Seed the child with a primary and a backdrop.
	previous := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentA,
		Name:     strPtr("Adopted Movie"),
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Index: 0, Path: "/images/old-primary.jpg"},
			{Type: item.ImageTypeBackdrop, Index: 0, Path: "/images/backdrop.jpg"},
		},
	}
	if err := items.Upsert(ctx, previous); err != nil {
		t.Fatalf("seed previous item: %v", err)
	}

	// The desired item supplies a new primary and omits the backdrop.
	desired := item.Item{
		ID:       childID,
		Kind:     item.KindMovie,
		ParentID: &parentB,
		Name:     strPtr("Adopted Movie"),
		ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Index: 0, Path: "/images/new-primary.jpg"},
		},
	}

	if _, err := items.ReconcileScannedChildren(ctx, parentB, []item.Item{desired}, nil); err != nil {
		t.Fatalf("reconcile adoption: %v", err)
	}

	stored, err := items.Get(ctx, childID)
	if err != nil {
		t.Fatalf("get adopted child: %v", err)
	}

	storedByType := make(map[item.ImageType]item.ImageInfo, len(stored.ImageInfos))
	for _, img := range stored.ImageInfos {
		storedByType[img.Type] = img
	}

	// The desired primary image won.
	if img, ok := storedByType[item.ImageTypePrimary]; !ok || img.Path != "/images/new-primary.jpg" {
		t.Fatalf("primary image = %v, want /images/new-primary.jpg", stored.ImageInfos)
	}

	// The previous backdrop survived because the desired item omitted it.
	if img, ok := storedByType[item.ImageTypeBackdrop]; !ok || img.Path != "/images/backdrop.jpg" {
		t.Fatalf("backdrop image lost = %v, want /images/backdrop.jpg", stored.ImageInfos)
	}
}

func strPtr(s string) *string {
	return &s
}

func folderItem(id dto.UUID, parentID *dto.UUID) item.Item {
	n := "Folder"
	return item.Item{
		ID:       id,
		Kind:     item.KindFolder,
		ParentID: parentID,
		Name:     &n,
		SortName: &n,
		IsFolder: true,
	}
}
