// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestStoreEnricherFlattensCollectionFolderPhysicalRootsForChildCount locks
// the public /Items count semantics: a CollectionFolder displays the children
// of each configured media root, never the physical-root implementation
// wrappers. Ordinary folders retain their immediate-child count.
func TestStoreEnricherFlattensCollectionFolderPhysicalRootsForChildCount(t *testing.T) {
	items := openBuilderItemStore(t)
	collectionID := builderUUID(t, 8_100)
	firstRootID := builderUUID(t, 8_101)
	secondRootID := builderUUID(t, 8_102)
	ordinaryID := builderUUID(t, 8_103)

	collection := item.Item{ID: collectionID, Kind: item.KindCollectionFolder, IsFolder: true, Name: builderString("Movies")}
	firstRoot := collectionFolderCountItem(firstRootID, "first physical source", &collectionID)
	secondRoot := collectionFolderCountItem(secondRootID, "second physical source", &collectionID)
	ordinary := collectionFolderCountItem(ordinaryID, "ordinary folder", nil)
	fixture := []item.Item{
		collection,
		firstRoot,
		secondRoot,
		{ID: builderUUID(t, 8_109), Kind: item.KindMovie, Name: builderString("direct collection movie"), ParentID: &collectionID},
		ordinary,
		collectionFolderCountItem(builderUUID(t, 8_104), "first source one", &firstRootID),
		collectionFolderCountItem(builderUUID(t, 8_105), "first source two", &firstRootID),
		collectionFolderCountItem(builderUUID(t, 8_106), "second source only", &secondRootID),
		collectionFolderCountItem(builderUUID(t, 8_107), "ordinary one", &ordinaryID),
		collectionFolderCountItem(builderUUID(t, 8_108), "ordinary two", &ordinaryID),
	}
	for _, value := range fixture {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert count fixture %q: %v", *value.Name, err)
		}
	}

	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil))
	result, err := builder.Build(t.Context(), []item.Item{
		collectionFolderCountLoad(t, items, collectionID),
		collectionFolderCountLoad(t, items, ordinaryID),
	}, store.ItemQueryDTOOptions{Fields: []string{"ChildCount"}}, nil)
	if err != nil {
		t.Fatalf("build collection and ordinary folder counts: %v", err)
	}

	counts := make(map[dto.UUID]int, len(result.Items))
	for _, projected := range result.Items {
		if projected.ChildCount == nil {
			t.Fatalf("projected folder %s omitted requested ChildCount", projected.ID)
		}
		counts[projected.ID] = *projected.ChildCount
	}
	if got, want := counts[collectionID], 4; got != want {
		t.Errorf("CollectionFolder ChildCount = %d, want %d visible children across physical roots plus a direct child", got, want)
	}
	if got, want := counts[ordinaryID], 2; got != want {
		t.Errorf("ordinary Folder ChildCount = %d, want %d immediate children", got, want)
	}
}

// TestStoreEnricherBatchesCollectionFolderChildCounts prevents a page of
// CollectionFolders from adding one count query per visible library.
func TestStoreEnricherBatchesCollectionFolderChildCounts(t *testing.T) {
	items, counting := openTaxonomyCountingItemStore(t)
	values := make([]item.Item, 3)
	for index := range values {
		id := builderUUID(t, 8_200+index)
		values[index] = item.Item{ID: id, Kind: item.KindCollectionFolder, IsFolder: true, Name: builderString("counted collection")}
		if err := items.Upsert(t.Context(), values[index]); err != nil {
			t.Fatalf("upsert collection count page item %d: %v", index, err)
		}
	}
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil))
	build := func(t *testing.T, page []item.Item) int64 {
		t.Helper()
		counting.ResetReaderQueries()
		if _, err := builder.Build(t.Context(), page, store.ItemQueryDTOOptions{Fields: []string{"ChildCount"}}, nil); err != nil {
			t.Fatalf("build %d CollectionFolder child counts: %v", len(page), err)
		}
		return counting.ReaderQueries()
	}
	one := build(t, values[:1])
	three := build(t, values)
	if three != one {
		t.Fatalf("CollectionFolder ChildCount reader queries = %d for three roots, want page-batched %d for one root", three, one)
	}
}

func collectionFolderCountItem(id dto.UUID, name string, parent *dto.UUID) item.Item {
	return item.Item{ID: id, Kind: item.KindFolder, IsFolder: true, Name: &name, ParentID: parent}
}

func collectionFolderCountLoad(t *testing.T, items *store.ItemStore, id dto.UUID) item.Item {
	t.Helper()
	value, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load count fixture %s: %v", id, err)
	}
	return value
}
