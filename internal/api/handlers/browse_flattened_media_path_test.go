// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestBrowseFlattensPhysicalRootFolderAndReturnsMedia is the browse
// regression test for physical root folders. The scanner persists a
// physical root Folder per configured media root, parented under the
// CollectionFolder.
// The store projection flattens that root Folder from a non-recursive
// browse, so a client browsing the CollectionFolder sees the typed media
// items, never the intermediate root Folder. The flatten clause uses the
// CollectionFolder's PhysicalFolderIDs marker to decide which Folders to
// suppress.
func TestBrowseFlattensPhysicalRootFolderAndReturnsMedia(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open browse regression store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close browse regression store: %v", err)
		}
	})
	items := store.NewItemStore(stores)

	collectionID := dto.UUID{0x01}
	collectionPath := "/library/movies"
	collectionName := "Movies"
	rootFolderID := dto.UUID{0x03}
	rootFolderPath := "/media/movies"
	rootFolderName := "movies"
	collection := item.Item{
		ID:       collectionID,
		Kind:     item.KindCollectionFolder,
		IsFolder: true,
		Path:     &collectionPath,
		Name:     &collectionName,
		SortName: &collectionName,
		Extra: item.ExtraFields{
			CollectionFolder: &item.CollectionFolderExtra{
				PhysicalFolderIDs: []dto.UUID{rootFolderID},
			},
		},
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed collection folder: %v", err)
	}

	rootFolder := item.Item{
		ID:       rootFolderID,
		Kind:     item.KindFolder,
		IsFolder: true,
		ParentID: &collectionID,
		Path:     &rootFolderPath,
		Name:     &rootFolderName,
		SortName: &rootFolderName,
	}
	if err := items.Upsert(t.Context(), rootFolder); err != nil {
		t.Fatalf("seed root folder: %v", err)
	}

	movieID := dto.UUID{0x02}
	moviePath := "/media/movies/Movie (2024).mkv"
	movieName := "Movie (2024)"
	movie := item.Item{
		ID:       movieID,
		Kind:     item.KindMovie,
		ParentID: &rootFolderID,
		Path:     &moviePath,
		Name:     &movieName,
		SortName: &movieName,
	}
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("seed movie: %v", err)
	}

	rt := router.New()
	handlers.RegisterItems(rt, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())

	request := httptest.NewRequest(http.MethodGet, "/Items?ParentId="+collectionID.Dashed()+"&EnableTotalRecordCount=true", nil)
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
	}

	var page struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int              `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode browse page: %v; body=%s", err, record.Body.String())
	}

	if page.TotalRecordCount != 1 {
		t.Fatalf("browse TotalRecordCount = %d, want 1 (the Movie, not the root Folder)", page.TotalRecordCount)
	}
	if len(page.Items) != 1 {
		t.Fatalf("browse Items = %d, want 1", len(page.Items))
	}
	gotKind, _ := page.Items[0]["Type"].(string)
	if gotKind != "Movie" {
		t.Fatalf("browse child Type = %q, want \"Movie\" (flattened media child, not a physical-root Folder)", gotKind)
	}
}

// TestBrowseReturnsDirectlyParentedLegacyMovieWhenMarkerIsAbsent proves that
// a legacy flattened database — one where the marker is empty or absent —
// browses exactly as before the cutover. A movie parented directly under the
// CollectionFolder stays visible because the flatten clause reduces to the
// strict parent filter when the marker is empty.
func TestBrowseReturnsDirectlyParentedLegacyMovieWhenMarkerIsAbsent(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open browse legacy store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close browse legacy store: %v", err)
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
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed collection folder: %v", err)
	}

	movieID := dto.UUID{0x02}
	moviePath := "/media/movies/Movie (2024).mkv"
	movieName := "Movie (2024)"
	movie := item.Item{
		ID:       movieID,
		Kind:     item.KindMovie,
		ParentID: &collectionID,
		Path:     &moviePath,
		Name:     &movieName,
		SortName: &movieName,
	}
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("seed movie: %v", err)
	}

	rt := router.New()
	handlers.RegisterItems(rt, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())

	request := httptest.NewRequest(http.MethodGet, "/Items?ParentId="+collectionID.Dashed()+"&EnableTotalRecordCount=true", nil)
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
	}

	var page struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int              `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode browse page: %v; body=%s", err, record.Body.String())
	}

	if page.TotalRecordCount != 1 {
		t.Fatalf("legacy browse TotalRecordCount = %d, want 1 (the directly-parented Movie)", page.TotalRecordCount)
	}
	if len(page.Items) != 1 {
		t.Fatalf("legacy browse Items = %d, want 1", len(page.Items))
	}
	gotKind, _ := page.Items[0]["Type"].(string)
	if gotKind != "Movie" {
		t.Fatalf("legacy browse child Type = %q, want \"Movie\"", gotKind)
	}
}

// seedFlattenTopology seeds a collection with a physical root Folder marker,
// the root Folder, and two movies under it. It returns the IDs the browse
// regression tests need.
func seedFlattenTopology(t *testing.T, items *store.ItemStore) (collectionID, rootFolderID, movie1ID, movie2ID dto.UUID) {
	t.Helper()
	collectionID = dto.UUID{0x01}
	rootFolderID = dto.UUID{0x03}
	movie1ID = dto.UUID{0x02}
	movie2ID = dto.UUID{0x04}
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
				PhysicalFolderIDs: []dto.UUID{rootFolderID},
			},
		},
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	rootFolderPath := "/media/movies"
	rootFolderName := "movies"
	rootFolder := item.Item{
		ID:       rootFolderID,
		Kind:     item.KindFolder,
		IsFolder: true,
		ParentID: &collectionID,
		Path:     &rootFolderPath,
		Name:     &rootFolderName,
		SortName: &rootFolderName,
	}
	if err := items.Upsert(t.Context(), rootFolder); err != nil {
		t.Fatalf("seed root folder: %v", err)
	}
	for _, m := range []struct {
		id   dto.UUID
		path string
		name string
	}{
		{movie1ID, "/media/movies/Alpha.mkv", "Alpha"},
		{movie2ID, "/media/movies/Beta.mkv", "Beta"},
	} {
		name := m.name
		path := m.path
		if err := items.Upsert(t.Context(), item.Item{
			ID:       m.id,
			Kind:     item.KindMovie,
			ParentID: &rootFolderID,
			Path:     &path,
			Name:     &name,
			SortName: &name,
		}); err != nil {
			t.Fatalf("seed movie %s: %v", m.name, err)
		}
	}
	return collectionID, rootFolderID, movie1ID, movie2ID
}

func openFlattenTestStore(t *testing.T) *store.ItemStore {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open flatten test store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close flatten test store: %v", err)
		}
	})
	return store.NewItemStore(stores)
}

func flattenBrowse(t *testing.T, items *store.ItemStore, queryString string) (itemsOut []map[string]any, total int) {
	t.Helper()
	rt := router.New()
	handlers.RegisterItems(rt, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())
	request := httptest.NewRequest(http.MethodGet, "/Items?"+queryString, nil)
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items?%s status = %d, want 200; body=%s", queryString, record.Code, record.Body.String())
	}
	var page struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int              `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode browse page: %v; body=%s", err, record.Body.String())
	}
	return page.Items, page.TotalRecordCount
}

// TestBrowseFlattenHonorsPagingAndTotals proves that paging and
// TotalRecordCount apply to the flattened media set, not to the
// intermediate root Folder. A limit of one returns one movie while the
// total stays two.
func TestBrowseFlattenHonorsPagingAndTotals(t *testing.T) {
	items := openFlattenTestStore(t)
	collectionID, _, _, _ := seedFlattenTopology(t, items)

	gotItems, total := flattenBrowse(t, items,
		"ParentId="+collectionID.Dashed()+"&EnableTotalRecordCount=true&Limit=1&StartIndex=0")
	if total != 2 {
		t.Fatalf("flatten browse total = %d, want 2 (both movies, not the root Folder)", total)
	}
	if len(gotItems) != 1 {
		t.Fatalf("flatten browse page length = %d, want 1 (the limit)", len(gotItems))
	}
	gotKind, _ := gotItems[0]["Type"].(string)
	if gotKind != "Movie" {
		t.Fatalf("flatten browse child Type = %q, want Movie", gotKind)
	}
}

// TestBrowseFlattenFiltersByItemType proves that IncludeItemTypes filters
// the flattened media set. When the filter asks for movies only, the root
// Folder never appears even though it is a child of the collection.
func TestBrowseFlattenFiltersByItemType(t *testing.T) {
	items := openFlattenTestStore(t)
	collectionID, _, _, _ := seedFlattenTopology(t, items)

	gotItems, total := flattenBrowse(t, items,
		"ParentId="+collectionID.Dashed()+"&EnableTotalRecordCount=true&IncludeItemTypes=Movie")
	if total != 2 {
		t.Fatalf("filter browse total = %d, want 2 movies", total)
	}
	if len(gotItems) != 2 {
		t.Fatalf("filter browse items = %d, want 2", len(gotItems))
	}
	for _, it := range gotItems {
		if k, _ := it["Type"].(string); k != "Movie" {
			t.Fatalf("filter browse child Type = %q, want Movie", k)
		}
	}
}

// TestBrowseRecursiveExcludesPhysicalRootFolders proves that a recursive
// browse of a collection excludes the physical root Folder. The root
// Folder's top_parent_id is the collection, so the recursive scope would
// surface it without the exclusion. The marker drives the exclusion.
func TestBrowseRecursiveExcludesPhysicalRootFolders(t *testing.T) {
	items := openFlattenTestStore(t)
	collectionID, rootFolderID, _, _ := seedFlattenTopology(t, items)

	gotItems, total := flattenBrowse(t, items,
		"ParentId="+collectionID.Dashed()+"&Recursive=true&EnableTotalRecordCount=true")
	if total < 2 {
		t.Fatalf("recursive browse total = %d, want at least 2 movies", total)
	}
	for _, it := range gotItems {
		if k, _ := it["Type"].(string); k == "Folder" {
			id, _ := dto.ParseUUID(it["Id"].(string))
			if id == rootFolderID {
				t.Fatalf("recursive browse returned the physical root Folder %s, want it excluded by the marker", rootFolderID)
			}
		}
	}
}

// TestUnscopedInventoryIncludesRootFolders proves that an unscoped
// recursive inventory — one with no ParentId — includes the physical root
// Folders because itemQueryHidesDefaultInventoryItems does not hide kind
// Folder.
func TestUnscopedInventoryIncludesRootFolders(t *testing.T) {
	items := openFlattenTestStore(t)
	_, rootFolderID, _, _ := seedFlattenTopology(t, items)

	gotItems, _ := flattenBrowse(t, items, "Recursive=true")
	found := false
	for _, it := range gotItems {
		if k, _ := it["Type"].(string); k == "Folder" {
			id, _ := dto.ParseUUID(it["Id"].(string))
			if id == rootFolderID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("unscoped inventory does not include the physical root Folder %s", rootFolderID)
	}
}

// TestDirectRootLookupReturnsRootChildren proves that browsing into a
// physical root Folder (ParentId=<rootFolderID>) lists the folder's direct
// children through the strict parent filter. The flatten clause does not
// apply because the parent is a Folder, not a CollectionFolder.
func TestDirectRootLookupReturnsRootChildren(t *testing.T) {
	items := openFlattenTestStore(t)
	_, rootFolderID, _, _ := seedFlattenTopology(t, items)

	gotItems, total := flattenBrowse(t, items,
		"ParentId="+rootFolderID.Dashed()+"&EnableTotalRecordCount=true")
	if total != 2 {
		t.Fatalf("direct root lookup total = %d, want 2 movies", total)
	}
	if len(gotItems) != 2 {
		t.Fatalf("direct root lookup items = %d, want 2", len(gotItems))
	}
	for _, it := range gotItems {
		if k, _ := it["Type"].(string); k != "Movie" {
			t.Fatalf("direct root lookup child Type = %q, want Movie", k)
		}
	}
}
