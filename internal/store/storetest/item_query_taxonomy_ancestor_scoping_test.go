// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryExcludesTaxonomyWithoutAncestorsFromRecursiveInventory
// verifies that an unscoped recursive /Items query excludes entities whose
// SupportsAncestors capability is false (Studio, Genre, Person) while keeping
// MusicArtist, which inherits ancestor support. The fixture deliberately seeds
// taxonomy rows so the test fails non-vacuously: without the exclusion, the
// recursive query returns the Studio, Genre, and Person rows.
func TestItemStoreQueryExcludesTaxonomyWithoutAncestorsFromRecursiveInventory(t *testing.T) {
	items := openQueryItemStore(t)

	studio := taxonomyFixtureItem(t, 100, item.KindStudio, "Case Studio", nil)
	genre := taxonomyFixtureItem(t, 101, item.KindGenre, "Drama", nil)
	person := taxonomyFixtureItem(t, 102, item.KindPerson, "Ada Director", nil)
	artist := taxonomyFixtureItem(t, 103, item.KindMusicArtist, "Reference Artist", nil)
	movie := taxonomyFixtureItem(t, 104, item.KindMovie, "Feature Film", &studio.ID)
	_ = movie

	for _, it := range []item.Item{studio, genre, person, artist, movie} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	// The unscoped recursive query is the default item inventory: no parent,
	// no item IDs, and no type filter. It must hide the SupportsAncestors-false
	// taxonomy entities but keep MusicArtist and ordinary media items.
	result, err := items.Query(t.Context(), store.ItemQuery{
		Recursive:              true,
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("recursive query: %v", err)
	}

	kinds := make(map[item.Kind]bool, len(result.Items))
	for _, it := range result.Items {
		kinds[it.Kind] = true
	}

	if kinds[item.KindStudio] {
		t.Errorf("recursive inventory contains a Studio item; SupportsAncestors-false entities must be excluded")
	}
	if kinds[item.KindGenre] {
		t.Errorf("recursive inventory contains a Genre item; SupportsAncestors-false entities must be excluded")
	}
	if kinds[item.KindPerson] {
		t.Errorf("recursive inventory contains a Person item; SupportsAncestors-false entities must be excluded")
	}

	if !kinds[item.KindMusicArtist] {
		t.Errorf("recursive inventory omits MusicArtist; it inherits ancestor support and must remain")
	}
	if !kinds[item.KindMovie] {
		t.Errorf("recursive inventory omits the Movie; ordinary media items must remain")
	}
}

// TestItemStoreQueryTaxonomyRemainsAddressableByRouteAndItemID verifies that
// the exclusion is scoped to the unscoped recursive inventory only. The
// taxonomy routes query the store with an IncludeItemTypes filter, and the
// direct item route queries by ItemIDs. Both paths must still return the
// persisted Studio and Genre rows, so the entities stay materialised and
// addressable by their own routes even though they are hidden from the
// default listing.
func TestItemStoreQueryTaxonomyRemainsAddressableByRouteAndItemID(t *testing.T) {
	items := openQueryItemStore(t)

	studio := taxonomyFixtureItem(t, 200, item.KindStudio, "North Star", nil)
	genre := taxonomyFixtureItem(t, 201, item.KindGenre, "Comedy", nil)
	person := taxonomyFixtureItem(t, 202, item.KindPerson, "Bob Actor", nil)

	for _, it := range []item.Item{studio, genre, person} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}

	// The taxonomy routes (/Studios, /Genres, /Persons) query the store with an
	// IncludeItemTypes filter. This path must return the persisted rows.
	t.Run("IncludeItemTypes returns Studio rows", func(t *testing.T) {
		result, err := items.Query(t.Context(), store.ItemQuery{
			IncludeItemTypes:       []item.Kind{item.KindStudio},
			EnableTotalRecordCount: true,
		})
		if err != nil {
			t.Fatalf("studio route query: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != studio.ID {
			t.Fatalf("studio route query returned %v, want [Studio %v]", queryIDs(result.Items), studio.ID)
		}
	})

	t.Run("IncludeItemTypes returns Genre rows", func(t *testing.T) {
		result, err := items.Query(t.Context(), store.ItemQuery{
			IncludeItemTypes:       []item.Kind{item.KindGenre},
			EnableTotalRecordCount: true,
		})
		if err != nil {
			t.Fatalf("genre route query: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != genre.ID {
			t.Fatalf("genre route query returned %v, want [Genre %v]", queryIDs(result.Items), genre.ID)
		}
	})

	t.Run("IncludeItemTypes returns Person rows", func(t *testing.T) {
		result, err := items.Query(t.Context(), store.ItemQuery{
			IncludeItemTypes:       []item.Kind{item.KindPerson},
			EnableTotalRecordCount: true,
		})
		if err != nil {
			t.Fatalf("person route query: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != person.ID {
			t.Fatalf("person route query returned %v, want [Person %v]", queryIDs(result.Items), person.ID)
		}
	})

	// The direct item route (/Items/{id}) queries by ItemIDs. A Studio must
	// remain addressable by its own ID.
	t.Run("ItemIDs returns Studio by direct item ID", func(t *testing.T) {
		result, err := items.Query(t.Context(), store.ItemQuery{
			ItemIDs:                []dto.UUID{studio.ID},
			EnableTotalRecordCount: true,
		})
		if err != nil {
			t.Fatalf("direct item query: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != studio.ID {
			t.Fatalf("direct item query returned %v, want [Studio %v]", queryIDs(result.Items), studio.ID)
		}
	})
}

// taxonomyFixtureItem builds a minimal item of the given kind for store-level
// scoping tests. Taxonomy entities carry no parent; the movie attaches to a
// fixture parent so it is not an orphan root.
func taxonomyFixtureItem(t *testing.T, value int, kind item.Kind, name string, parentID *dto.UUID) item.Item {
	t.Helper()
	return item.Item{ID: queryUUID(t, value), Kind: kind, Name: &name, SortName: &name, ParentID: parentID}
}
