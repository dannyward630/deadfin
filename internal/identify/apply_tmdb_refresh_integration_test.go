// SPDX-License-Identifier: GPL-2.0-only

package identify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplyRefreshesTMDbMetadataFromCassette proves the complete refresh leg
// of one Identify Apply operation. Apply replaces the stale provider IDs,
// runs the real TMDb provider through providers.Registry, and saves the
// refreshed item. The test uses the recorded cassette and makes no live HTTP
// request.
//
// The test composes two tested mechanisms: the Apply service flow and the
// TMDb cassette provider. It reloads the saved item from SQLite before it
// makes the final assertions, so a missing store write fails the test.
func TestApplyRefreshesTMDbMetadataFromCassette(t *testing.T) {
	items, stores := setupTestItemsAndStores(t)

	fakeClock := clock.NewFake(time.Unix(0, 0).UTC())

	// A real Topology with one Movies virtual folder that owns the media root.
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("new topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	mediaRoot := t.TempDir()
	virtual, err := topology.AddVirtualFolder(context.Background(), "Movies", "movies", library.LibraryOptions{
		PathInfos:                 []library.MediaPathInfo{{Path: mediaRoot}},
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
	})
	if err != nil {
		t.Fatalf("add virtual folder: %v", err)
	}
	collectionFolderID := virtual.CollectionFolderID

	// Store a CollectionFolder whose ID equals the virtual folder's
	// CollectionFolderID. The movie stores beneath this collection folder, so
	// the store derives the movie TopParentID as the collection-folder ID and
	// resolveLibraryOptions matches the owning folder.
	collectionFolder := &item.Item{
		ID:       collectionFolderID,
		Kind:     item.KindCollectionFolder,
		Name:     ptr("Movies"),
		Path:     &mediaRoot,
		IsFolder: true,
	}
	if err := items.Upsert(context.Background(), *collectionFolder); err != nil {
		t.Fatalf("upsert collection folder: %v", err)
	}

	// Store a movie beneath the collection folder with the stale IDs Tmdb=999
	// and Tvdb=stale. Apply replaces these IDs wholesale with the result IDs.
	moviePath := filepath.Join(mediaRoot, "movie.mp4")
	if err := os.WriteFile(moviePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write movie file: %v", err)
	}
	movieID := mustUUID("aabbccddeeff00112233445566778899")
	movie := &item.Item{
		ID:          movieID,
		Kind:        item.KindMovie,
		Name:        ptr("Stale Movie"),
		Path:        &moviePath,
		ProviderIDs: newProviderIDs("Tmdb", "999", "Tvdb", "stale"),
		ParentID:    &collectionFolderID,
	}
	if err := items.Upsert(context.Background(), *movie); err != nil {
		t.Fatalf("upsert movie: %v", err)
	}

	// Load the recorded TMDb cassette. The cassette contains the
	// /configuration and /movie/603 responses. The cassette rejects every
	// unmatched request, so the test makes no live HTTP request and fails
	// loudly on any unexpected call.
	cassette, err := remotetest.Load("../providers/tmdb/testdata/the-matrix.json")
	if err != nil {
		t.Fatalf("load cassette: %v", err)
	}

	// Construct the remote.Client with the cassette as its only Doer. The
	// client adds a bounded response cache so the /configuration fetch reuses
	// the cached entry on the later image-base lookup.
	httpClient := remote.NewClient(remote.Config{Doer: cassette, CacheEntries: 32})

	// Construct the real TMDb provider with the cassette client and a test
	// API key. The cassette matcher ignores the api_key parameter, so the
	// recorded responses match the test-key requests.
	tmdbConfig := tmdb.DefaultConfig()
	tmdbConfig.APIKey = "test-key"
	tmdbProvider := tmdb.NewProvider(tmdb.Options{
		HTTP:   httpClient,
		Config: tmdbConfig,
	})

	// Put the real TMDb provider in a real providers.Registry. The registry
	// runs the remote stage for the full replacement that Apply triggers.
	// The TMDb provider assigns a durable PersonID to each credit from
	// library.PersonEntity, so the credits persist directly through
	// ItemStore.Upsert without a test-only adapter.
	registry := providers.NewRegistry(tmdbProvider)

	// Construct the identify.Service with the real item store, the real
	// Topology, the real registry, and a fake clock. No Changes bus is set, so
	// Apply publishes nothing; this test covers the refresh leg only.
	svc := New(Options{
		Items:     items,
		Topology:  topology,
		Refresher: registry,
		Clock:     fakeClock,
	})

	// Call Service.Apply with only Tmdb=603 in the result IDs. The result name
	// and year are stale so the cassette refresh must replace them. The
	// replaceAllImages flag is true, which matches the handler default.
	result := providers.RemoteSearchResult{
		Name:           ptr("Stale Search Result"),
		ProviderIDs:    map[string]string{"Tmdb": "603"},
		ProductionYear: intptr(1988),
	}
	if err := svc.Apply(context.Background(), movieID, result, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Reload the movie through ItemStore.GetBatch after Apply returns. The
	// assertions read the persisted state, not the in-memory work copy, so a
	// missing store write fails the test.
	loaded, err := items.GetBatch(context.Background(), []dto.UUID{movieID})
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if len(loaded) == 0 {
		t.Fatal("movie not found after Apply")
	}
	saved := loaded[0]

	// Assert that the stale Tvdb ID is absent. Apply replaces ProviderIDs
	// wholesale, so the Tvdb=stale value must be gone.
	if _, ok := saved.ProviderIDs.Get("Tvdb"); ok {
		t.Error("ProviderIDs[Tvdb] still present, want removed by wholesale replacement")
	}

	// Assert that Tmdb equals 603. The result carried only Tmdb=603, and the
	// TMDb provider set it again from the cassette movie id.
	if v, ok := saved.ProviderIDs.Get("Tmdb"); !ok || v != "603" {
		t.Errorf("ProviderIDs[Tmdb] = %q (%t), want 603", v, ok)
	}

	// Assert that Imdb equals tt0133093. The cassette movie carries the IMDb
	// id, and the provider maps it onto the item.
	if v, ok := saved.ProviderIDs.Get("Imdb"); !ok || v != "tt0133093" {
		t.Errorf("ProviderIDs[Imdb] = %q (%t), want tt0133093", v, ok)
	}

	// Assert that TmdbCollection equals 2344. The cassette movie belongs to
	// the collection with id 2344.
	if v, ok := saved.ProviderIDs.Get("TmdbCollection"); !ok || v != "2344" {
		t.Errorf("ProviderIDs[TmdbCollection] = %q (%t), want 2344", v, ok)
	}

	// Assert that the saved name is The Matrix. The stale result name was
	// "Stale Search Result", and the cassette refresh replaced it with the
	// TMDb title.
	if saved.Name == nil || *saved.Name != "The Matrix" {
		t.Errorf("Name = %v, want The Matrix", saved.Name)
	}

	// Assert that the saved production year is 1999. The stale result year
	// was 1988, and the cassette refresh replaced it with the TMDb release
	// year.
	if saved.ProductionYear == nil || *saved.ProductionYear != 1999 {
		t.Errorf("ProductionYear = %v, want 1999", saved.ProductionYear)
	}

	// Assert that the saved overview equals the cassette text. The provider
	// collapses the paragraph break to a single newline.
	wantOverview := "A computer hacker learns from mysterious rebels.\nHe joins the fight."
	if saved.Overview == nil || *saved.Overview != wantOverview {
		t.Errorf("Overview = %v, want %q", saved.Overview, wantOverview)
	}

	// Assert that the saved primary image path equals the cassette poster
	// URL. The provider builds the URL from the /configuration base, the
	// "original" size, and the first poster file path.
	wantPrimaryImage := "https://image.tmdb.org/t/p/original/best-poster.jpg"
	foundPrimary := false
	for _, image := range saved.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == wantPrimaryImage {
			foundPrimary = true
			break
		}
	}
	if !foundPrimary {
		t.Errorf("primary image not found, want %q in %#v", wantPrimaryImage, saved.ImageInfos)
	}

	// Assert that the reloaded movie carries the five credits that the TMDb
	// provider produced. The provider assigns each PersonID through
	// library.PersonEntity, and ItemStore.Upsert persists the people_map rows
	// keyed on (item_id, person_id, type). A nil PersonID violates the
	// primary key, so the comparison proves that the provider output persists
	// unchanged through the store round-trip.
	//
	// The store reloads credits in sort_order order with NULLS LAST, so the
	// three cast credits keep their billing order and the two crew credits
	// follow. Each expected PersonID derives from its expected name through
	// library.PersonEntity, the same identity the provider and the scanner
	// use.
	cast := func(name, role string, order int) item.PersonCredit {
		sortOrder := order
		return item.PersonCredit{
			PersonID:  library.PersonEntity(name).ID,
			Name:      name,
			Role:      &role,
			Type:      item.PersonKindActor,
			SortOrder: &sortOrder,
		}
	}
	crew := func(name, role string, kind item.PersonKind) item.PersonCredit {
		return item.PersonCredit{
			PersonID: library.PersonEntity(name).ID,
			Name:     name,
			Role:     &role,
			Type:     kind,
		}
	}
	wantPeople := []item.PersonCredit{
		cast("Keanu Reeves", "Neo", 0),
		cast("Laurence Fishburne", "Morpheus", 1),
		cast("Carrie-Anne Moss", "Trinity", 2),
		crew("Lana Wachowski", "Director", item.PersonKindDirector),
		crew("Lilly Wachowski", "Writer", item.PersonKindWriter),
	}
	if len(saved.People) != len(wantPeople) {
		t.Fatalf("People count = %d, want %d", len(saved.People), len(wantPeople))
	}
	for index, want := range wantPeople {
		got := saved.People[index]
		if got.PersonID != want.PersonID {
			t.Errorf("credit[%d] PersonID = %s, want %s", index, got.PersonID, want.PersonID)
		}
		if got.PersonID.IsNil() {
			t.Errorf("credit[%d] has nil PersonID", index)
		}
		if got.Name != want.Name {
			t.Errorf("credit[%d] Name = %q, want %q", index, got.Name, want.Name)
		}
		if !reflect.DeepEqual(got.Role, want.Role) {
			t.Errorf("credit[%d] Role = %v, want %v", index, got.Role, want.Role)
		}
		if got.Type != want.Type {
			t.Errorf("credit[%d] Type = %s, want %s", index, got.Type, want.Type)
		}
		if !reflect.DeepEqual(got.SortOrder, want.SortOrder) {
			t.Errorf("credit[%d] SortOrder = %v, want %v", index, got.SortOrder, want.SortOrder)
		}
	}
}
