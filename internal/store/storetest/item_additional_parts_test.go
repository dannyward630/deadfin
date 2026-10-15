// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// Video.AdditionalParts is an ordered persisted source-path relation, distinct
// from linked alternate versions and from the primary-version relationship.
func TestItemStorePersistsOrderedVideoAdditionalParts(t *testing.T) {
	for _, open := range []struct {
		name string
		open func(*testing.T) *store.ItemStore
	}{
		{name: "sqlite", open: openItemStore},
		{name: "postgres", open: openPostgresItemStore},
	} {
		t.Run(open.name, func(t *testing.T) {
			items := open.open(t)
			video := hierarchyItem(t, 15001, item.KindMovie, nil)
			video.AdditionalParts = []string{"/media/film-part2.mkv", "/media/film-part3.mkv"}
			if err := items.Upsert(t.Context(), video); err != nil {
				t.Fatalf("upserting video parts: %v", err)
			}
			got, err := items.Get(t.Context(), video.ID)
			if err != nil || !reflect.DeepEqual(got.AdditionalParts, video.AdditionalParts) {
				t.Fatalf("stored additional paths = %#v, %v; want %#v", got.AdditionalParts, err, video.AdditionalParts)
			}

			video.AdditionalParts = []string{"/media/replacement-part2.mkv"}
			if err := items.Upsert(t.Context(), video); err != nil {
				t.Fatalf("replacing video parts: %v", err)
			}
			got, err = items.Get(t.Context(), video.ID)
			if err != nil || !reflect.DeepEqual(got.AdditionalParts, video.AdditionalParts) {
				t.Fatalf("replaced additional paths = %#v, %v; want %#v", got.AdditionalParts, err, video.AdditionalParts)
			}
		})
	}
}

func TestItemStoreAdditionalPartsDeletionContract(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			items, stores := backend.open(t)

			t.Run("deletes direct owner without touching retained owners", func(t *testing.T) {
				deleted := additionalPartsItem(t, 15010, nil, "/media/deleted-part2.mkv", "/media/deleted-part3.mkv")
				retained := additionalPartsItem(t, 15011, nil, "/media/retained-part2.mkv")
				for _, it := range []item.Item{deleted, retained} {
					if err := items.Upsert(t.Context(), it); err != nil {
						t.Fatalf("seed %s: %v", it.ID, err)
					}
				}
				assertAdditionalPartsCount(t, stores, deleted.ID, len(deleted.AdditionalParts))
				assertAdditionalPartsCount(t, stores, retained.ID, len(retained.AdditionalParts))

				if err := items.Delete(t.Context(), deleted.ID); err != nil {
					t.Fatalf("delete direct additional-parts owner: %v", err)
				}
				assertAdditionalPartsCount(t, stores, deleted.ID, 0)
				assertAdditionalPartsCount(t, stores, retained.ID, len(retained.AdditionalParts))
				assertNoAdditionalPartOrphans(t, stores)
			})

			t.Run("deletes descendant owners with their ancestor subtree", func(t *testing.T) {
				root := hierarchyItem(t, 15020, item.KindFolder, nil)
				branch := hierarchyItem(t, 15021, item.KindFolder, &root.ID)
				descendant := additionalPartsItem(t, 15022, &branch.ID, "/media/branch-part2.mkv", "/media/branch-part3.mkv")
				retained := additionalPartsItem(t, 15023, &root.ID, "/media/retained-subtree-part2.mkv")
				for _, it := range []item.Item{root, branch, descendant, retained} {
					if err := items.Upsert(t.Context(), it); err != nil {
						t.Fatalf("seed subtree item %s: %v", it.ID, err)
					}
				}
				assertAdditionalPartsCount(t, stores, descendant.ID, len(descendant.AdditionalParts))
				assertAdditionalPartsCount(t, stores, retained.ID, len(retained.AdditionalParts))

				if err := items.Delete(t.Context(), branch.ID); err != nil {
					t.Fatalf("delete additional-parts ancestor subtree: %v", err)
				}
				assertAdditionalPartsCount(t, stores, descendant.ID, 0)
				assertAdditionalPartsCount(t, stores, retained.ID, len(retained.AdditionalParts))
				assertNoAdditionalPartOrphans(t, stores)
			})

			t.Run("database foreign key cascades raw owner deletion", func(t *testing.T) {
				owner := additionalPartsItem(t, 15030, nil, "/media/raw-delete-part2.mkv")
				if err := items.Upsert(t.Context(), owner); err != nil {
					t.Fatalf("seed raw deletion owner: %v", err)
				}
				assertAdditionalPartsCount(t, stores, owner.ID, len(owner.AdditionalParts))

				query := "DELETE FROM items WHERE id = " + stores.Dialect().Placeholder(1)
				if _, err := stores.Writer().ExecContext(t.Context(), query, owner.ID[:]); err != nil {
					t.Fatalf("raw delete additional-parts owner: %v", err)
				}
				assertAdditionalPartsCount(t, stores, owner.ID, 0)
				assertNoAdditionalPartOrphans(t, stores)
			})
		})
	}
}

func additionalPartsItem(t *testing.T, value int, parentID *dto.UUID, paths ...string) item.Item {
	t.Helper()
	it := hierarchyItem(t, value, item.KindMovie, parentID)
	it.AdditionalParts = paths
	return it
}

func assertAdditionalPartsCount(t *testing.T, stores store.Stores, itemID dto.UUID, want int) {
	t.Helper()
	storetest.AssertAdditionalPartsCount(t, stores, itemID, want)
}

func assertNoAdditionalPartOrphans(t *testing.T, stores store.Stores) {
	t.Helper()
	storetest.AssertNoAdditionalPartOrphans(t, stores)
}
