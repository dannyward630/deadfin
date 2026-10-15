// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTopParentStopsAtCollectionFolder proves the persisted store writes the
// CollectionFolder ID as top_parent_id, not the AggregateFolder ID. The
// AggregateFolder is the application root; the CollectionFolder under it is the
// library boundary and the top parent for its descendants, per upstream
// BaseItem.IsTopParent (BaseItem.cs:755-758: GetParent() is AggregateFolder).
//
// ItemStore.Upsert calls repairDerivedTx, which recomputes top_parent_id by
// walking the parent chain. Before the fix, the walk stopped only at the
// parentless AggregateFolder and wrote that ID, so a TopParentIDs query for a
// CollectionFolder returned zero rows. This test persists the
// full chain through Upsert and asserts the post-repair value and the query.
func TestTopParentStopsAtCollectionFolder(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite stores: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	aggregate := topParentTestItem(t, 1, item.KindAggregateFolder, true, nil, "default")
	collection := topParentTestItem(t, 2, item.KindCollectionFolder, true, &aggregate.ID, "Movies")
	source := topParentTestItem(t, 3, item.KindFolder, true, &collection.ID, "Source")
	movie := topParentTestItem(t, 4, item.KindMovie, false, &source.ID, "Film (2024)")
	movie.MediaType = mediaType(item.MediaTypeVideo)

	for _, value := range []item.Item{aggregate, collection, source, movie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting %s %s: %v", value.Kind, value.ID, err)
		}
	}

	// The stored Movie TopParentID must be the CollectionFolder ID, not the
	// AggregateFolder ID. repairDerivedTx recomputes this value, so this reads
	// the post-repair fact, not the value the resolver set.
	storedMovie, err := items.Get(t.Context(), movie.ID)
	if err != nil {
		t.Fatalf("Get(movie) error = %v", err)
	}
	if storedMovie.TopParentID == nil {
		t.Fatalf("stored Movie TopParentID is nil, want CollectionFolder ID %v", collection.ID)
	}
	if *storedMovie.TopParentID == aggregate.ID {
		t.Fatalf("stored Movie TopParentID = AggregateFolder %v, want CollectionFolder %v", aggregate.ID, collection.ID)
	}
	if *storedMovie.TopParentID != collection.ID {
		t.Fatalf("stored Movie TopParentID = %v, want CollectionFolder %v", *storedMovie.TopParentID, collection.ID)
	}

	// A TopParentIDs query for the CollectionFolder ID must return the Movie.
	// This is the /Items/Latest path: items_latest.go sets
	// query.TopParentIDs = allowedLatestRoots(...), which holds CollectionFolder
	// IDs, and itemquery_filters.go turns that into top_parent_id IN (...).
	page, err := items.Query(t.Context(), store.ItemQuery{
		TopParentIDs:           []dto.UUID{collection.ID},
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("Query(TopParentIDs=[collection]) error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("TopParentIDs query returned %d items, want 1", len(page.Items))
	}
	if page.Items[0].ID != movie.ID {
		t.Fatalf("TopParentIDs query returned %v, want Movie %v", page.Items[0].ID, movie.ID)
	}

	// A TopParentIDs query for the AggregateFolder ID must NOT return the
	// Movie. This is the regression: before the fix, every descendant pointed
	// at the AggregateFolder, so this query returned the Movie.
	aggregatePage, err := items.Query(t.Context(), store.ItemQuery{
		TopParentIDs:           []dto.UUID{aggregate.ID},
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("Query(TopParentIDs=[aggregate]) error = %v", err)
	}
	if len(aggregatePage.Items) != 0 {
		t.Fatalf("TopParentIDs query for AggregateFolder returned %d items, want 0", len(aggregatePage.Items))
	}

	// The ancestors closure must still include the AggregateFolder and the
	// CollectionFolder, so recursive browse keeps working. Querying by
	// AncestorIDs exercises the maintained closure, not top_parent_id.
	ancestorPage, err := items.Query(t.Context(), store.ItemQuery{
		AncestorIDs:            []dto.UUID{aggregate.ID},
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("Query(AncestorIDs=[aggregate]) error = %v", err)
	}
	if len(ancestorPage.Items) != 1 || ancestorPage.Items[0].ID != movie.ID {
		t.Fatalf("AncestorIDs query for AggregateFolder returned %v, want [Movie %v]", ids(ancestorPage.Items), movie.ID)
	}
}

func topParentTestItem(t *testing.T, value int, kind item.Kind, isFolder bool, parentID *dto.UUID, name string) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return item.Item{
		ID:       id,
		Kind:     kind,
		IsFolder: isFolder,
		ParentID: parentID,
		Name:     &name,
		SortName: &name,
	}
}

func ids(values []item.Item) []dto.UUID {
	out := make([]dto.UUID, len(values))
	for i, value := range values {
		out[i] = value.ID
	}
	return out
}

func mediaType(value item.MediaType) *item.MediaType { return &value }
