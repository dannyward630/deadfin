// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestAddToCollectionCreatesManualMembership(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	boxSet := collectionBoxSetItem(t, 800, "Test BoxSet")
	movie := collectionMovieItem(t, 801, "Test Movie")

	for _, value := range []item.Item{boxSet, movie} {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	if err := items.AddToCollection(ctx, boxSet.ID, []dto.UUID{movie.ID}); err != nil {
		t.Fatalf("AddToCollection: %v", err)
	}

	links, err := items.LinkedChildren(ctx, boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("LinkedChildren = %d links, want 1", len(links))
	}
	if links[0].ItemID != movie.ID {
		t.Fatalf("LinkedChildren[0].ItemID = %s, want %s", links[0].ItemID, movie.ID)
	}
	if links[0].Type != item.LinkedChildTypeManual {
		t.Fatalf("LinkedChildren[0].Type = %s, want Manual", links[0].Type)
	}
	if links[0].SortOrder != nil {
		t.Fatalf("LinkedChildren[0].SortOrder = %v, want nil", *links[0].SortOrder)
	}
}

func TestAddToCollectionIsIdempotent(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	boxSet := collectionBoxSetItem(t, 810, "Idempotent BoxSet")
	movie := collectionMovieItem(t, 811, "Idempotent Movie")

	for _, value := range []item.Item{boxSet, movie} {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	if err := items.AddToCollection(ctx, boxSet.ID, []dto.UUID{movie.ID}); err != nil {
		t.Fatalf("first AddToCollection: %v", err)
	}
	if err := items.AddToCollection(ctx, boxSet.ID, []dto.UUID{movie.ID}); err != nil {
		t.Fatalf("second AddToCollection: %v", err)
	}

	links, err := items.LinkedChildren(ctx, boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("LinkedChildren = %d links after repeated POST, want 1", len(links))
	}
}

func TestAddToCollectionRejectsNonBoxSetParent(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	folder := collectionFolderItem(t, 820)
	movie := collectionMovieItem(t, 821, "NonBoxSet Movie")

	for _, value := range []item.Item{folder, movie} {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	err = items.AddToCollection(ctx, folder.ID, []dto.UUID{movie.ID})
	if err == nil {
		t.Fatalf("AddToCollection to non-BoxSet parent succeeded, want error")
	}

	links, err := items.LinkedChildren(ctx, folder.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("LinkedChildren = %d links after rejected write, want 0", len(links))
	}
}

func TestAddToCollectionRejectsMissingChild(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	boxSet := collectionBoxSetItem(t, 830, "Missing Child BoxSet")
	missing := collectionUUID(t, 831)

	if err := items.Upsert(ctx, boxSet); err != nil {
		t.Fatalf("upsert %s: %v", boxSet.ID, err)
	}

	err = items.AddToCollection(ctx, boxSet.ID, []dto.UUID{missing})
	if err == nil {
		t.Fatalf("AddToCollection with missing child succeeded, want error")
	}

	links, err := items.LinkedChildren(ctx, boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("LinkedChildren = %d links after rejected write, want 0", len(links))
	}
}

func TestAddToCollectionRollsBackOnPartialFailure(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	boxSet := collectionBoxSetItem(t, 840, "Rollback BoxSet")
	validMovie := collectionMovieItem(t, 841, "Valid Movie")
	missingMovie := collectionUUID(t, 842)

	for _, value := range []item.Item{boxSet, validMovie} {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	err = items.AddToCollection(ctx, boxSet.ID, []dto.UUID{validMovie.ID, missingMovie})
	if err == nil {
		t.Fatalf("AddToCollection with mixed valid/missing children succeeded, want error")
	}

	links, err := items.LinkedChildren(ctx, boxSet.ID)
	if err != nil {
		t.Fatalf("LinkedChildren: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("LinkedChildren = %d links after rollback, want 0", len(links))
	}
}

func TestAddToCollectionCollapseBehavior(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open item store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	boxSet := collectionBoxSetItem(t, 850, "Collapse BoxSet")
	linkedMovie := collectionMovieItem(t, 851, "Linked Movie")
	unlinkedMovie := collectionMovieItem(t, 852, "Unlinked Movie")

	for _, value := range []item.Item{boxSet, linkedMovie, unlinkedMovie} {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}

	// Pre-link one movie via Upsert (the scanner's path).
	boxSetWithChild := boxSet
	boxSetWithChild.LinkedChildren = []item.LinkedChildRef{
		{ItemID: linkedMovie.ID, Type: item.LinkedChildTypeManual},
	}
	if err := items.Upsert(ctx, boxSetWithChild); err != nil {
		t.Fatalf("upsert BoxSet with child: %v", err)
	}

	// AddToCollection writes the unlinked movie.
	if err := items.AddToCollection(ctx, boxSet.ID, []dto.UUID{unlinkedMovie.ID}); err != nil {
		t.Fatalf("AddToCollection: %v", err)
	}

	collapseTrue := true
	collapseFalse := false

	// CollapseBoxSetItems=true returns the BoxSet instead of the linked movies.
	result, err := items.Query(ctx, store.ItemQuery{
		IncludeItemTypes:    []item.Kind{item.KindMovie},
		CollapseBoxSetItems: &collapseTrue,
	})
	if err != nil {
		t.Fatalf("Query collapsed: %v", err)
	}
	collapsedIDs := collectionQueryIDs(result)
	if len(collapsedIDs) != 1 || collapsedIDs[0] != boxSet.ID {
		t.Fatalf("collapsed IDs = %v, want [%s]", collapsedIDs, boxSet.ID)
	}

	// CollapseBoxSetItems=false returns the linked movies.
	result, err = items.Query(ctx, store.ItemQuery{
		IncludeItemTypes:    []item.Kind{item.KindMovie},
		CollapseBoxSetItems: &collapseFalse,
	})
	if err != nil {
		t.Fatalf("Query uncollapsed: %v", err)
	}
	uncollapsedIDs := collectionQueryIDs(result)
	wantUncollapsed := []dto.UUID{linkedMovie.ID, unlinkedMovie.ID}
	if !reflect.DeepEqual(uncollapsedIDs, wantUncollapsed) {
		t.Fatalf("uncollapsed IDs = %v, want %v", uncollapsedIDs, wantUncollapsed)
	}
}

func collectionBoxSetItem(t *testing.T, value int, name string) item.Item {
	t.Helper()
	n := name
	return item.Item{
		ID:       collectionUUID(t, value),
		Kind:     item.KindBoxSet,
		Name:     &n,
		SortName: &n,
		IsFolder: true,
	}
}

func collectionMovieItem(t *testing.T, value int, name string) item.Item {
	t.Helper()
	n := name
	return item.Item{
		ID:       collectionUUID(t, value),
		Kind:     item.KindMovie,
		Name:     &n,
		SortName: &n,
	}
}

func collectionFolderItem(t *testing.T, value int) item.Item {
	t.Helper()
	n := "Test Folder"
	return item.Item{
		ID:       collectionUUID(t, value),
		Kind:     item.KindFolder,
		Name:     &n,
		SortName: &n,
		IsFolder: true,
	}
}

func collectionUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing collection UUID %d: %v", value, err)
	}
	return id
}

func collectionQueryIDs(result store.ItemQueryResult) []dto.UUID {
	ids := make([]dto.UUID, len(result.Items))
	for i, it := range result.Items {
		ids[i] = it.ID
	}
	return ids
}
