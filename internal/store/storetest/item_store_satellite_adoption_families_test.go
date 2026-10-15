// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// adoptChild stores previous under one parent, then reconciles desired under
// another parent, and returns the stored child. The reconcile adopts the
// child.
func adoptChild(t *testing.T, previous, desired item.Item) item.Item {
	t.Helper()
	ctx := t.Context()
	stores, err := storesqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open adoption store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close adoption store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	parentA, parentB := dto.UUID{0x11}, dto.UUID{0x21}
	for _, parent := range []dto.UUID{parentA, parentB} {
		if err := items.Upsert(ctx, folderItem(parent, nil)); err != nil {
			t.Fatalf("seed parent %s: %v", parent, err)
		}
	}
	previous.ID, desired.ID = dto.UUID{0x31}, dto.UUID{0x31}
	previous.Kind, desired.Kind = item.KindMovie, item.KindMovie
	previous.ParentID, desired.ParentID = &parentA, &parentB
	previous.Name, desired.Name = strPtr("Adopted Movie"), strPtr("Adopted Movie")
	if err := items.Upsert(ctx, previous); err != nil {
		t.Fatalf("seed previous child: %v", err)
	}
	if _, err := items.ReconcileScannedChildren(ctx, parentB, []item.Item{desired}, nil); err != nil {
		t.Fatalf("reconcile adoption: %v", err)
	}
	stored, err := items.Get(ctx, desired.ID)
	if err != nil {
		t.Fatalf("get adopted child: %v", err)
	}
	return stored
}

// TestReconcileAdoptionUnionsLockedFields proves that a partial desired lock
// set keeps the previous locks. Upstream MergeBaseItemData merges LockedFields
// with Concat.Distinct.
func TestReconcileAdoptionUnionsLockedFields(t *testing.T) {
	stored := adoptChild(t,
		item.Item{LockedFields: []item.MetadataField{item.MetadataFieldOverview, item.MetadataFieldGenres}},
		item.Item{LockedFields: []item.MetadataField{item.MetadataFieldCast, item.MetadataFieldOverview}},
	)
	got := slices.Clone(stored.LockedFields)
	slices.Sort(got)
	want := []item.MetadataField{item.MetadataFieldCast, item.MetadataFieldOverview, item.MetadataFieldGenres}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adopted LockedFields = %v, want the union %v", got, want)
	}
}

// TestReconcileAdoptionReplacesAdditionalParts proves that the resolver's
// complete part list replaces the stored one, and that an empty list removes
// stale parts.
func TestReconcileAdoptionReplacesAdditionalParts(t *testing.T) {
	previous := item.Item{AdditionalParts: []string{"/media/movie-cd2.mkv", "/media/movie-cd3.mkv"}}
	if stored := adoptChild(t, previous, item.Item{AdditionalParts: []string{"/media/movie-part2.mkv"}}); !reflect.DeepEqual(stored.AdditionalParts, []string{"/media/movie-part2.mkv"}) {
		t.Fatalf("adopted AdditionalParts = %v, want the desired list only", stored.AdditionalParts)
	}
	if stored := adoptChild(t, previous, item.Item{}); len(stored.AdditionalParts) != 0 {
		t.Fatalf("adopted AdditionalParts = %v, want none: the resolver found no more parts", stored.AdditionalParts)
	}
}

// TestReconcileAdoptionDropsInheritedTags proves that stale InheritedTags do
// not carry: no scan derives them, and a direct child write drops them too.
func TestReconcileAdoptionDropsInheritedTags(t *testing.T) {
	stored := adoptChild(t, item.Item{Values: item.ItemValues{InheritedTags: []string{"stale"}}}, item.Item{})
	if len(stored.Values.InheritedTags) != 0 {
		t.Fatalf("adopted InheritedTags = %v, want none", stored.Values.InheritedTags)
	}
}

// TestReconcileAdoptionReplacesPresentValueFamilies is the negative control
// for the value families: the scanner already merged the stored values into
// the desired item, so a present desired family replaces the stored one
// exactly, and an absent one carries it.
func TestReconcileAdoptionReplacesPresentValueFamilies(t *testing.T) {
	previous := item.Item{
		Values: item.ItemValues{
			Genres:  []string{"Drama", "Action"},
			Studios: []string{"Warner Bros."},
			Tags:    []string{"stored-tag"},
		},
		People: []item.PersonCredit{{PersonID: dto.UUID{0x41}, Name: "Stored Actor", Type: item.PersonKindActor}},
	}
	stored := adoptChild(t, previous, item.Item{Values: item.ItemValues{Genres: []string{"Comedy"}}})
	if !reflect.DeepEqual(stored.Values.Genres, []string{"Comedy"}) {
		t.Fatalf("adopted Genres = %v, want the desired family [Comedy] exactly", stored.Values.Genres)
	}
	if !reflect.DeepEqual(stored.Values.Studios, []string{"Warner Bros."}) || !reflect.DeepEqual(stored.Values.Tags, []string{"stored-tag"}) {
		t.Fatalf("adopted Studios = %v, Tags = %v; want the stored families carried", stored.Values.Studios, stored.Values.Tags)
	}
	if len(stored.People) != 1 || stored.People[0].Name != "Stored Actor" {
		t.Fatalf("adopted People = %#v, want the stored credit carried", stored.People)
	}
}
