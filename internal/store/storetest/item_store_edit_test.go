// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStoreEditItemsSQLite(t *testing.T) {
	runItemStoreEditItems(t, openItemStore)
}

func TestItemStoreEditItemsPostgres(t *testing.T) {
	runItemStoreEditItems(t, openPostgresItemStore)
}

// runItemStoreEditItems checks the metadata-edit write and that a later
// provider backfill honors the locks the edit sets.
func runItemStoreEditItems(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	ctx := t.Context()
	items := open(t)

	parent := hierarchyItem(t, 37400, item.KindFolder, nil)
	parent.IsFolder = true
	if err := items.Upsert(ctx, parent); err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	movie := reconciliationChild(t, 37401, item.KindMovie, parent.ID, "Scanned Name")
	movie.Path = stringPtr("/library/scanned.mkv")
	movie.Overview = stringPtr("Scanned overview")
	saved := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	if _, err := items.ReconcileScannedPlanAt(ctx, parent.ID, []item.Item{movie}, nil, nil, saved); err != nil {
		t.Fatalf("seed movie: %v", err)
	}

	stored, err := items.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("get seeded movie: %v", err)
	}
	topParent := stored.TopParentID

	person := item.Item{ID: contractUUID(t, 37402), Kind: item.KindPerson, Name: stringPtr("Edited Person")}
	order := 0
	role := "Lead"
	stored.Name = stringPtr("Edited Name")
	stored.Overview = stringPtr("Edited overview")
	stored.LockedFields = []item.MetadataField{item.MetadataFieldName}
	stored.Values.Genres = []string{"Edited Genre"}
	stored.People = []item.PersonCredit{{PersonID: person.ID, Name: "Edited Person", Role: &role, Type: item.PersonKindActor, SortOrder: &order}}
	stored.TopParentID = nil
	if err := items.EditItems(ctx, []item.Item{stored}, []item.Item{person}); err != nil {
		t.Fatalf("edit movie: %v", err)
	}

	edited, err := items.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("get edited movie: %v", err)
	}
	if edited.Name == nil || *edited.Name != "Edited Name" || edited.CleanName == nil || *edited.CleanName != item.CleanName("Edited Name") {
		t.Fatalf("edited name = %v clean %v; want Edited Name", edited.Name, edited.CleanName)
	}
	if len(edited.People) != 1 || edited.People[0].Name != "Edited Person" {
		t.Fatalf("edited people = %+v; want Edited Person", edited.People)
	}
	if len(edited.Values.Genres) != 1 || edited.Values.Genres[0] != "Edited Genre" {
		t.Fatalf("edited genres = %v; want Edited Genre", edited.Values.Genres)
	}
	if !sameUUIDPointer(edited.TopParentID, topParent) {
		t.Fatalf("edited TopParentID = %v; want stored %v kept", edited.TopParentID, topParent)
	}
	if _, err := items.Get(ctx, person.ID); err != nil {
		t.Fatalf("get edit-referenced person entity: %v", err)
	}

	// A provider refresh replaces the unlocked overview and keeps the locked name.
	desired := edited
	desired.Name = stringPtr("Provider Name")
	desired.Overview = stringPtr("Provider overview")
	if _, err := items.MetadataBackfill(ctx, parent.ID, []item.Item{desired}, nil, saved.Add(time.Hour), store.MetadataBackfillOptions{}); err != nil {
		t.Fatalf("metadata backfill after field lock: %v", err)
	}
	refreshed, err := items.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("get refreshed movie: %v", err)
	}
	if refreshed.Name == nil || *refreshed.Name != "Edited Name" {
		t.Fatalf("locked name after refresh = %v; want Edited Name", refreshed.Name)
	}
	if refreshed.Overview == nil || *refreshed.Overview != "Provider overview" {
		t.Fatalf("unlocked overview after refresh = %v; want Provider overview", refreshed.Overview)
	}

	// LockData protects every field.
	refreshed.IsLocked = true
	refreshed.Overview = stringPtr("Locked overview")
	if err := items.EditItems(ctx, []item.Item{refreshed}, nil); err != nil {
		t.Fatalf("lock movie: %v", err)
	}
	desired.Overview = stringPtr("Second provider overview")
	if _, err := items.MetadataBackfill(ctx, parent.ID, []item.Item{desired}, nil, saved.Add(2*time.Hour), store.MetadataBackfillOptions{}); err != nil {
		t.Fatalf("metadata backfill after item lock: %v", err)
	}
	locked, err := items.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("get locked movie: %v", err)
	}
	if !locked.IsLocked || locked.Overview == nil || *locked.Overview != "Locked overview" {
		t.Fatalf("locked movie = IsLocked %t overview %v; want true, Locked overview", locked.IsLocked, locked.Overview)
	}

	moved := locked
	moved.Path = stringPtr("/library/elsewhere.mkv")
	if err := items.EditItems(ctx, []item.Item{moved}, nil); err == nil {
		t.Fatal("EditItems accepted a path change")
	}
	missing := locked
	missing.ID = contractUUID(t, 37499)
	if err := items.EditItems(ctx, []item.Item{missing}, nil); !errors.Is(err, store.ErrEditItemNotFound) {
		t.Fatalf("EditItems(missing) = %v; want ErrEditItemNotFound", err)
	}
}

func sameUUIDPointer(left, right *dto.UUID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
