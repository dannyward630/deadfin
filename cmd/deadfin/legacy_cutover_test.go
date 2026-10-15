// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationLegacyFlattenedCutoverPreservesUserData proves that a
// legacy flattened database — one where media items are parented directly
// under the CollectionFolder with no physical root Folder and no
// PhysicalFolderIDs marker — converts safely on the first scan. The movie
// is reparented under the root Folder while its user_data row survives.
func TestApplicationLegacyFlattenedCutoverPreservesUserData(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	source := t.TempDir()
	moviePath := filepath.Join(source, "legacy-cutover.mkv")
	if err := os.WriteFile(moviePath, []byte("movie fixture"), 0o600); err != nil {
		t.Fatalf("write cutover fixture %q: %v", moviePath, err)
	}

	app := openRootTopologyApplication(t, programData)
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	if _, err := app.topology.AddVirtualFolder(t.Context(), "Legacy Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add legacy collection: %v", err)
	}

	// Run a first scan to let the scanner create the deterministic IDs
	// for the collection, root Folder, and movie. This also persists the
	// PhysicalFolderIDs marker on the collection.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("seed scan errors = %#v, want none", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	collections, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindCollectionFolder}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	if len(collections.Items) != 1 {
		t.Fatalf("collection count = %d, want 1", len(collections.Items))
	}
	collection := collections.Items[0]

	movie := applicationTopologyItemAtPath(t, items, moviePath)
	if movie.ParentID == nil {
		t.Fatalf("seed movie has no parent")
	}
	rootFolderID := *movie.ParentID

	// Set user_data on the movie before simulating the legacy state.
	userID := dto.UUID{0x99}
	if _, err := items.MutateUserDataForItem(t.Context(), userID, movie.ID, func(data *store.UserData) error {
		data.Played = true
		data.PlayCount = 3
		data.PositionTicks = 5_000_000
		return nil
	}); err != nil {
		t.Fatalf("seed user data: %v", err)
	}

	// Seed provider IDs, images, and streams on the movie. These
	// represent external metadata that the scanner's local-source
	// refresher does not re-derive. They must survive the cutover reparent.
	movie.ProviderIDs.Set("tmdb", "legacy-42")
	movie.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: filepath.Join(source, "poster.jpg")}}
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("seed provider IDs and images: %v", err)
	}
	seededStreams := []playback.MediaStream{{Codec: "h264", Index: 0, Type: playback.StreamTypeVideo, IsDefault: true}}
	if err := items.ReplaceStreams(t.Context(), movie.ID, seededStreams); err != nil {
		t.Fatalf("seed media streams: %v", err)
	}

	// Simulate a legacy flattened database: clear the marker, delete the
	// root Folder, and reparent the movie directly under the collection.
	// This matches a legacy database where media items are direct children
	// of the CollectionFolder.
	collection.Extra.CollectionFolder = &item.CollectionFolderExtra{
		CollectionType: collection.Extra.CollectionFolder.CollectionType,
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("clear collection marker: %v", err)
	}
	// Reparent the movie directly under the collection before deleting
	// the root Folder, so the movie's user_data keys survive.
	movie.ParentID = &collection.ID
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("reparent movie to collection: %v", err)
	}
	// Delete the root Folder row. The movie stays under the collection
	// as a legacy flattened child. A legacy database has no physical
	// root Folder, so the conversion scan must recreate it by its
	// deterministic ID.
	if err := items.Delete(t.Context(), rootFolderID); err != nil {
		t.Fatalf("delete root folder: %v", err)
	}
	// Make sure the root Folder is gone before the conversion scan.
	if _, err := items.Get(t.Context(), rootFolderID); !store.IsNotFound(err) {
		t.Fatalf("Get(rootFolderID) = %v, want not found", err)
	}

	// Verify the legacy state: movie is directly under the collection.
	legacy := applicationTopologyItemAtPath(t, items, moviePath)
	if legacy.ParentID == nil || *legacy.ParentID != collection.ID {
		t.Fatalf("legacy movie parent = %v, want collection %s", legacy.ParentID, collection.ID)
	}

	// Verify user_data still exists on the legacy movie.
	legacyData, err := items.MutateUserDataForItem(t.Context(), userID, legacy.ID, func(data *store.UserData) error { return nil })
	if err != nil {
		t.Fatalf("read legacy user data: %v", err)
	}
	if !legacyData.Played || legacyData.PlayCount != 3 || legacyData.PositionTicks != 5_000_000 {
		t.Fatalf("legacy user data = %#v, want played=true count=3 position=5000000", legacyData)
	}

	// Run the cutover scan. The scanner creates a root Folder, reparents
	// the movie under it, and preserves the movie row and its user_data.
	result, err := app.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("cutover scan: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("cutover scan errors = %#v, want none", result.Errors)
	}

	// The movie's parent is the root Folder, not the CollectionFolder.
	scannedMovie := applicationTopologyItemAtPath(t, items, moviePath)
	if scannedMovie.ParentID == nil {
		t.Fatalf("cutover movie has no parent")
	}
	if *scannedMovie.ParentID == collection.ID {
		t.Fatalf("cutover movie parent = collection %s, want root Folder", collection.ID)
	}
	// The new root Folder has the same ID as the original — the IDs
	// are deterministic. Assert equality unconditionally: a different
	// non-nil root ID is a topology defect, not a pass.
	if *scannedMovie.ParentID != rootFolderID {
		t.Fatalf("cutover movie parent = %s, want root Folder %s", scannedMovie.ParentID, rootFolderID)
	}

	rootFolder, err := items.Get(t.Context(), *scannedMovie.ParentID)
	if err != nil {
		t.Fatalf("load root folder: %v", err)
	}
	if rootFolder.Kind != item.KindFolder || rootFolder.ParentID == nil || *rootFolder.ParentID != collection.ID {
		t.Fatalf("root folder hierarchy = %#v, want Folder child of CollectionFolder %s", rootFolder, collection.ID)
	}

	// The user_data row survived the reparent.
	stored, err := items.MutateUserDataForItem(t.Context(), userID, scannedMovie.ID, func(data *store.UserData) error { return nil })
	if err != nil {
		t.Fatalf("read cutover user data: %v", err)
	}
	if !stored.Played || stored.PlayCount != 3 || stored.PositionTicks != 5_000_000 {
		t.Fatalf("cutover user data = %#v, want played=true count=3 position=5000000", stored)
	}

	// Provider IDs survived the reparent.
	if v, ok := scannedMovie.ProviderIDs.Get("tmdb"); !ok || v != "legacy-42" {
		t.Fatalf("cutover provider IDs = %v, want tmdb legacy-42", scannedMovie.ProviderIDs.Snapshot())
	}

	// Images survived the reparent.
	if len(scannedMovie.ImageInfos) == 0 {
		t.Fatalf("cutover images lost, want the seeded primary poster")
	}

	// Streams survived the reparent.
	cutoverStreams, err := items.Streams(t.Context(), scannedMovie.ID)
	if err != nil {
		t.Fatalf("read cutover streams: %v", err)
	}
	if len(cutoverStreams) != 1 || cutoverStreams[0].Codec != "h264" {
		t.Fatalf("cutover streams = %#v, want one h264 video stream", cutoverStreams)
	}
}

// TestApplicationLegacyCutoverRemovesStaleChildWhenSourceIsGone proves that the
// PreserveChildIDs cutover mechanism does not retain a legacy flattened child
// whose source the resolver can no longer find. The test seeds a flattened
// child under an accessible root, removes the source file before the first
// conversion scan, and runs two scans. Storage and /Items must omit the stale
// child after each scan. This is the P1 regression for the
// cutover/stale-legacy-child-retained finding.
func TestApplicationLegacyCutoverRemovesStaleChildWhenSourceIsGone(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	source := t.TempDir()
	moviePath := filepath.Join(source, "stale-cutover.mkv")
	if err := os.WriteFile(moviePath, []byte("stale movie fixture"), 0o600); err != nil {
		t.Fatalf("write stale fixture %q: %v", moviePath, err)
	}

	app := openRootTopologyApplication(t, programData)
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	if _, err := app.topology.AddVirtualFolder(t.Context(), "Stale Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add stale collection: %v", err)
	}

	// Run a seed scan so the scanner creates the deterministic IDs for the
	// collection, root Folder, and movie. This also persists the
	// PhysicalFolderIDs marker on the collection.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("seed scan errors = %#v, want none", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	collections, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindCollectionFolder}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	if len(collections.Items) != 1 {
		t.Fatalf("collection count = %d, want 1", len(collections.Items))
	}
	collection := collections.Items[0]

	movie := applicationTopologyItemAtPath(t, items, moviePath)
	if movie.ParentID == nil {
		t.Fatalf("seed movie has no parent")
	}

	// Set user_data on the movie so it is a legacy child with retained
	// state that a broken preserve path would keep alive.
	userID := dto.UUID{0x88}
	if _, err := items.MutateUserDataForItem(t.Context(), userID, movie.ID, func(data *store.UserData) error {
		data.Played = true
		return nil
	}); err != nil {
		t.Fatalf("seed user data: %v", err)
	}

	// Simulate a legacy flattened database: clear the marker, reparent the
	// movie directly under the collection, and delete the root Folder. The
	// movie becomes a direct child of the CollectionFolder, exactly like a
	// legacy database.
	collection.Extra.CollectionFolder = &item.CollectionFolderExtra{
		CollectionType: collection.Extra.CollectionFolder.CollectionType,
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("clear collection marker: %v", err)
	}
	originalRootFolderID := *movie.ParentID
	movie.ParentID = &collection.ID
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("reparent movie to collection: %v", err)
	}
	// Delete the original root Folder row. The movie stays under the
	// collection as a legacy flattened child. A legacy database has no
	// physical root Folder, so the conversion scan must recreate it by
	// its deterministic ID.
	if err := items.Delete(t.Context(), originalRootFolderID); err != nil {
		t.Fatalf("delete root folder: %v", err)
	}
	// Make sure the root Folder is gone before the conversion scan.
	if _, err := items.Get(t.Context(), originalRootFolderID); !store.IsNotFound(err) {
		t.Fatalf("Get(originalRootFolderID) = %v, want not found", err)
	}

	// Verify the legacy state: the movie is a direct child of the collection.
	legacy := applicationTopologyItemAtPath(t, items, moviePath)
	if legacy.ParentID == nil || *legacy.ParentID != collection.ID {
		t.Fatalf("legacy movie parent = %v, want collection %s", legacy.ParentID, collection.ID)
	}

	// Remove the source file before the conversion scan. The resolver will
	// not produce the movie, so no per-root reconcile can adopt it. A
	// correct PreserveChildIDs computation must not protect a child whose
	// source is gone, so the collection-level reconcile deletes it.
	if err := os.Remove(moviePath); err != nil {
		t.Fatalf("remove stale source %q: %v", moviePath, err)
	}

	// Configure the administrator and log in so the test can prove the
	// /Items projection omits the stale child through the HTTP API.
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())

	// assertStaleChildOmitted verifies that storage and /Items omit the
	// stale movie after a scan. Storage: no item row at the movie path or
	// under the collection with that ID. /Items: a non-recursive browse of
	// the collection returns zero rows whose path equals the removed file.
	assertStaleChildOmitted := func(t *testing.T, items *store.ItemStore, app *application, token string, collectionID dto.UUID, wantPath string) {
		t.Helper()
		// Storage: a path query returns zero items.
		pathResult, err := items.Query(t.Context(), store.ItemQuery{Path: wantPath})
		if err != nil {
			t.Fatalf("query stale child by path: %v", err)
		}
		if len(pathResult.Items) != 0 {
			t.Fatalf("storage still contains stale child at %q: %#v", wantPath, pathResult.Items)
		}
		// Storage: a recursive query under the collection returns no
		// item whose path equals the removed source file.
		recursive, err := items.Query(t.Context(), store.ItemQuery{ParentID: &collectionID, Recursive: true})
		if err != nil {
			t.Fatalf("query collection subtree: %v", err)
		}
		for _, child := range recursive.Items {
			if child.Path != nil && *child.Path == wantPath {
				t.Fatalf("collection subtree still contains stale child at %q: %#v", wantPath, child)
			}
		}
		// /Items: a non-recursive browse of the collection omits the
		// stale child. The flatten projection returns the media items
		// under the root Folders, never a removed item.
		browse := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Items?ParentId="+collectionID.Dashed()+"&EnableTotalRecordCount=true", nil, token)
		if browse.Code != http.StatusOK {
			t.Fatalf("GET /Items status = %d, want 200; body=%s", browse.Code, browse.Body.String())
		}
		var page struct {
			Items            []map[string]any `json:"Items"`
			TotalRecordCount int              `json:"TotalRecordCount"`
		}
		if err := json.Unmarshal(browse.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode /Items page: %v; body=%s", err, browse.Body.String())
		}
		for _, row := range page.Items {
			if rowPath, _ := row["Path"].(string); rowPath == wantPath {
				t.Fatalf("/Items returned stale child at %q: %#v", wantPath, row)
			}
		}
		if page.TotalRecordCount != 0 {
			t.Fatalf("/Items TotalRecordCount = %d, want 0 (no media under the collection)", page.TotalRecordCount)
		}
	}

	// First conversion scan: the source is gone, so the scanner must not
	// preserve or adopt the stale child. The collection-level reconcile
	// deletes it.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("first cutover scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("first cutover scan errors = %#v, want none", result.Errors)
	}
	assertStaleChildOmitted(t, items, app, token, collection.ID, moviePath)

	// Second scan: the deletion is idempotent. The stale child stays gone.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("second cutover scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("second cutover scan errors = %#v, want none", result.Errors)
	}
	assertStaleChildOmitted(t, items, app, token, collection.ID, moviePath)

	// The root Folder and collection still exist; only the stale movie is gone.
	rootFolder, err := items.Get(t.Context(), originalRootFolderID)
	if err != nil {
		t.Fatalf("load root folder after scan: %v", err)
	}
	if rootFolder.Kind != item.KindFolder {
		t.Fatalf("root folder kind = %v, want Folder", rootFolder.Kind)
	}
}

// TestApplicationLegacyCutoverRetainsFlattenedChildWhenRootIsInaccessible
// proves that a legacy flattened child under an inaccessible root survives
// the cutover. The test seeds a flattened child under an accessible root,
// simulates a legacy database by deleting the root Folder row, makes the
// source directory inaccessible so ReadDir fails, and runs two scans. The
// movie row, user_data, provider IDs, images, and streams must remain after
// each scan. This is the P1 regression for the
// cutover/inaccessible-legacy-child-deleted finding.
func TestApplicationLegacyCutoverRetainsFlattenedChildWhenRootIsInaccessible(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	source := t.TempDir()
	moviePath := filepath.Join(source, "inaccessible-cutover.mkv")
	if err := os.WriteFile(moviePath, []byte("movie fixture"), 0o600); err != nil {
		t.Fatalf("write cutover fixture %q: %v", moviePath, err)
	}

	app := openRootTopologyApplication(t, programData)
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	if _, err := app.topology.AddVirtualFolder(t.Context(), "Inaccessible Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add collection: %v", err)
	}

	// Run a seed scan so the scanner creates the deterministic IDs for the
	// collection, root Folder, and movie. This also persists the
	// PhysicalFolderIDs marker on the collection.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("seed scan errors = %#v, want none", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	collections, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindCollectionFolder}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	if len(collections.Items) != 1 {
		t.Fatalf("collection count = %d, want 1", len(collections.Items))
	}
	collection := collections.Items[0]

	movie := applicationTopologyItemAtPath(t, items, moviePath)
	if movie.ParentID == nil {
		t.Fatalf("seed movie has no parent")
	}
	rootFolderID := *movie.ParentID

	// Set user_data, provider IDs, images, and streams on the movie. These
	// must survive the cutover even though the root is inaccessible and no
	// per-root reconcile runs.
	userID := dto.UUID{0x77}
	if _, err := items.MutateUserDataForItem(t.Context(), userID, movie.ID, func(data *store.UserData) error {
		data.Played = true
		data.PlayCount = 2
		data.PositionTicks = 3_000_000
		return nil
	}); err != nil {
		t.Fatalf("seed user data: %v", err)
	}
	movie.ProviderIDs.Set("tmdb", "inaccessible-99")
	movie.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: filepath.Join(source, "poster.jpg")}}
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("seed provider IDs and images: %v", err)
	}
	seededStreams := []playback.MediaStream{{Codec: "h264", Index: 0, Type: playback.StreamTypeVideo, IsDefault: true}}
	if err := items.ReplaceStreams(t.Context(), movie.ID, seededStreams); err != nil {
		t.Fatalf("seed media streams: %v", err)
	}

	// Simulate a legacy flattened database: clear the marker, reparent the
	// movie directly under the collection, and delete the root Folder row.
	collection.Extra.CollectionFolder = &item.CollectionFolderExtra{
		CollectionType: collection.Extra.CollectionFolder.CollectionType,
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("clear collection marker: %v", err)
	}
	movie.ParentID = &collection.ID
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("reparent movie to collection: %v", err)
	}
	// Delete the root Folder row. A legacy database has no physical root
	// Folder, so the conversion scan must recreate it by its deterministic
	// ID.
	if err := items.Delete(t.Context(), rootFolderID); err != nil {
		t.Fatalf("delete root folder: %v", err)
	}
	// Make sure the root Folder is gone before the conversion scan.
	if _, err := items.Get(t.Context(), rootFolderID); !store.IsNotFound(err) {
		t.Fatalf("Get(rootFolderID) = %v, want not found", err)
	}

	// Verify the legacy state: the movie is a direct child of the collection.
	legacy := applicationTopologyItemAtPath(t, items, moviePath)
	if legacy.ParentID == nil || *legacy.ParentID != collection.ID {
		t.Fatalf("legacy movie parent = %v, want collection %s", legacy.ParentID, collection.ID)
	}

	// Make the source directory inaccessible so ReadDir fails. The root
	// Folder still enters the collection plan because its construction
	// needs no directory enumeration, but no per-root reconcile or scan
	// task runs for it. The legacy flattened child must survive via
	// PreserveChildIDs.
	if err := os.RemoveAll(source); err != nil {
		t.Fatalf("remove source directory: %v", err)
	}

	// assertChildRetained verifies that the movie row, user_data, provider
	// IDs, images, and streams all survive after a scan.
	assertChildRetained := func(t *testing.T, items *store.ItemStore, moviePath string, userID dto.UUID, rootFolderID, collectionID dto.UUID) {
		t.Helper()
		// The movie row survives.
		retained := applicationTopologyItemAtPath(t, items, moviePath)
		// The movie stays under the collection. The root Folder is
		// inaccessible, so no per-root reconcile reparents it.
		if retained.ParentID == nil || *retained.ParentID != collectionID {
			t.Fatalf("retained movie parent = %v, want collection %s", retained.ParentID, collectionID)
		}
		// user_data survives.
		data, err := items.MutateUserDataForItem(t.Context(), userID, retained.ID, func(data *store.UserData) error { return nil })
		if err != nil {
			t.Fatalf("read retained user data: %v", err)
		}
		if !data.Played || data.PlayCount != 2 || data.PositionTicks != 3_000_000 {
			t.Fatalf("retained user data = %#v, want played=true count=2 position=3000000", data)
		}
		// Provider IDs survive.
		if v, ok := retained.ProviderIDs.Get("tmdb"); !ok || v != "inaccessible-99" {
			t.Fatalf("retained provider IDs = %v, want tmdb inaccessible-99", retained.ProviderIDs.Snapshot())
		}
		// Images survive.
		if len(retained.ImageInfos) == 0 {
			t.Fatalf("retained images lost, want the seeded primary poster")
		}
		// Streams survive.
		streams, err := items.Streams(t.Context(), retained.ID)
		if err != nil {
			t.Fatalf("read retained streams: %v", err)
		}
		if len(streams) != 1 || streams[0].Codec != "h264" {
			t.Fatalf("retained streams = %#v, want one h264 video stream", streams)
		}
		// The root Folder survives as a desired item even though it is
		// inaccessible.
		rootFolder, err := items.Get(t.Context(), rootFolderID)
		if err != nil {
			t.Fatalf("load root folder: %v", err)
		}
		if rootFolder.Kind != item.KindFolder {
			t.Fatalf("root folder kind = %v, want Folder", rootFolder.Kind)
		}
	}

	// First scan: the root is inaccessible, so the scanner must preserve
	// the legacy flattened child via PreserveChildIDs. An inaccessible root
	// reports its status in the progress result, not as a scan error.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("first cutover scan: %v", err)
	} else if result.Inaccessible < 1 {
		t.Fatalf("first cutover scan inaccessible = %d, want at least 1", result.Inaccessible)
	}
	assertChildRetained(t, items, moviePath, userID, rootFolderID, collection.ID)

	// Second scan: the preservation is idempotent. The child stays.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("second cutover scan: %v", err)
	} else if result.Inaccessible < 1 {
		t.Fatalf("second cutover scan inaccessible = %d, want at least 1", result.Inaccessible)
	}
	assertChildRetained(t, items, moviePath, userID, rootFolderID, collection.ID)
}

// quarantineCutoverRefresher injects a duplicate image type and index into the
// movie at the given path. This triggers the quarantine validator inside
// resolveFolder: duplicate image types are a scanner bug that Part 1
// normalization does not fix, so quarantine must contain them. Every other
// item gets a single primary image so the seed scan stores clean metadata.
type quarantineCutoverRefresher struct {
	invalidPath string
}

func (r quarantineCutoverRefresher) Refresh(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
	if *candidate.Path == r.invalidPath {
		candidate.ImageInfos = []item.ImageInfo{
			{Type: item.ImageTypePrimary, Path: *candidate.Path + "-a.jpg"},
			{Type: item.ImageTypePrimary, Path: *candidate.Path + "-b.jpg"},
		}
		return nil
	}
	if candidate.Kind == item.KindMovie || candidate.Kind == item.KindFolder {
		candidate.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: *candidate.Path + ".jpg"}}
	}
	return nil
}

// TestApplicationLegacyCutoverRetainsQuarantinedChild proves that a legacy
// flattened child the quarantine validator drops during the first conversion
// scan survives the cutover. The test seeds a movie under an accessible root,
// sets user_data, provider IDs, images, and streams, then simulates a legacy
// database by clearing the marker, reparenting the movie to the collection, and
// deleting the root Folder row. A custom refresher injects a duplicate image
// type so the quarantine validator drops the candidate from the root plan
// desired set. Without preservation, the collection-level reconcile would
// delete the legacy row with its user_data and satellites. The fix preserves
// the quarantined ID so the row, user_data, satellites, and skipped progress
// record survive across two scans. This is the P1 regression for the
// cutover/quarantined-legacy-child-deleted finding.
func TestApplicationLegacyCutoverRetainsQuarantinedChild(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	source := t.TempDir()
	moviePath := filepath.Join(source, "quarantine-cutover.mkv")
	if err := os.WriteFile(moviePath, []byte("movie fixture"), 0o600); err != nil {
		t.Fatalf("write cutover fixture %q: %v", moviePath, err)
	}

	app := openRootTopologyApplication(t, programData)
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	if _, err := app.topology.AddVirtualFolder(t.Context(), "Quarantine Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add quarantine collection: %v", err)
	}

	// Run a seed scan with the default refresher so the scanner creates the
	// deterministic IDs for the collection, root Folder, and movie. This
	// also persists the PhysicalFolderIDs marker on the collection.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("seed scan errors = %#v, want none", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	collections, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindCollectionFolder}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query collections: %v", err)
	}
	if len(collections.Items) != 1 {
		t.Fatalf("collection count = %d, want 1", len(collections.Items))
	}
	collection := collections.Items[0]

	movie := applicationTopologyItemAtPath(t, items, moviePath)
	if movie.ParentID == nil {
		t.Fatalf("seed movie has no parent")
	}
	rootFolderID := *movie.ParentID

	// Set user_data, provider IDs, images, and streams on the movie. These
	// must survive the cutover even though the quarantine validator drops
	// the candidate.
	userID := dto.UUID{0x66}
	if _, err := items.MutateUserDataForItem(t.Context(), userID, movie.ID, func(data *store.UserData) error {
		data.Played = true
		data.PlayCount = 4
		data.PositionTicks = 7_000_000
		return nil
	}); err != nil {
		t.Fatalf("seed user data: %v", err)
	}
	movie.ProviderIDs.Set("tmdb", "quarantine-77")
	movie.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: filepath.Join(source, "poster.jpg")}}
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("seed provider IDs and images: %v", err)
	}
	seededStreams := []playback.MediaStream{{Codec: "h264", Index: 0, Type: playback.StreamTypeVideo, IsDefault: true}}
	if err := items.ReplaceStreams(t.Context(), movie.ID, seededStreams); err != nil {
		t.Fatalf("seed media streams: %v", err)
	}

	// Simulate a legacy flattened database: clear the marker, reparent the
	// movie directly under the collection, and delete the root Folder row.
	collection.Extra.CollectionFolder = &item.CollectionFolderExtra{
		CollectionType: collection.Extra.CollectionFolder.CollectionType,
	}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("clear collection marker: %v", err)
	}
	movie.ParentID = &collection.ID
	if err := items.Upsert(t.Context(), movie); err != nil {
		t.Fatalf("reparent movie to collection: %v", err)
	}
	if err := items.Delete(t.Context(), rootFolderID); err != nil {
		t.Fatalf("delete root folder: %v", err)
	}
	if _, err := items.Get(t.Context(), rootFolderID); !store.IsNotFound(err) {
		t.Fatalf("Get(rootFolderID) = %v, want not found", err)
	}

	// Verify the legacy state: the movie is a direct child of the collection.
	legacy := applicationTopologyItemAtPath(t, items, moviePath)
	if legacy.ParentID == nil || *legacy.ParentID != collection.ID {
		t.Fatalf("legacy movie parent = %v, want collection %s", legacy.ParentID, collection.ID)
	}

	// Replace the scanner with one whose refresher injects a duplicate
	// image type for the movie path. The quarantine validator inside
	// resolveFolder drops the candidate from the root plan desired set
	// because duplicate image types fail validateReconcileSatellites.
	if err := app.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	quarantineScanner, err := library.NewScanner(
		app.topology,
		store.NewItemStore(app.stores),
		library.NewDefaultChain(naming.DefaultOptions()),
		library.ScannerOptions{
			Concurrency: 1,
			Clock:       clock.NewReal(),
			Refresher:   quarantineCutoverRefresher{invalidPath: moviePath},
		},
	)
	if err != nil {
		t.Fatalf("create quarantine scanner: %v", err)
	}
	app.scanner = quarantineScanner

	// assertQuarantinedChildRetained verifies that the movie row, user_data,
	// provider IDs, images, and streams all survive after a scan.
	assertQuarantinedChildRetained := func(t *testing.T, items *store.ItemStore, moviePath string, userID dto.UUID, rootFolderID, collectionID dto.UUID) {
		t.Helper()
		// The movie row survives.
		retained := applicationTopologyItemAtPath(t, items, moviePath)
		// The movie stays under the collection. The quarantine validator
		// dropped the candidate, so no per-root reconcile reparents it.
		if retained.ParentID == nil || *retained.ParentID != collectionID {
			t.Fatalf("retained movie parent = %v, want collection %s", retained.ParentID, collectionID)
		}
		// user_data survives.
		data, err := items.MutateUserDataForItem(t.Context(), userID, retained.ID, func(data *store.UserData) error { return nil })
		if err != nil {
			t.Fatalf("read retained user data: %v", err)
		}
		if !data.Played || data.PlayCount != 4 || data.PositionTicks != 7_000_000 {
			t.Fatalf("retained user data = %#v, want played=true count=4 position=7000000", data)
		}
		// Provider IDs survive.
		if v, ok := retained.ProviderIDs.Get("tmdb"); !ok || v != "quarantine-77" {
			t.Fatalf("retained provider IDs = %v, want tmdb quarantine-77", retained.ProviderIDs.Snapshot())
		}
		// Images survive.
		if len(retained.ImageInfos) == 0 {
			t.Fatalf("retained images lost, want the seeded primary poster")
		}
		// Streams survive.
		streams, err := items.Streams(t.Context(), retained.ID)
		if err != nil {
			t.Fatalf("read retained streams: %v", err)
		}
		if len(streams) != 1 || streams[0].Codec != "h264" {
			t.Fatalf("retained streams = %#v, want one h264 video stream", streams)
		}
		// The root Folder survives as a desired item.
		rootFolder, err := items.Get(t.Context(), rootFolderID)
		if err != nil {
			t.Fatalf("load root folder: %v", err)
		}
		if rootFolder.Kind != item.KindFolder {
			t.Fatalf("root folder kind = %v, want Folder", rootFolder.Kind)
		}
	}

	// First scan: the quarantine validator drops the movie candidate, but
	// the preservation set includes the quarantined ID, so the
	// collection-level reconcile does not delete the legacy row. The
	// skipped progress record appears in the result errors.
	result, err := app.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("first cutover scan: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("first cutover scan errors = %#v, want exactly one quarantine error", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), moviePath) {
		t.Fatalf("first cutover scan error = %q, want it to name the movie path %q", result.Errors[0], moviePath)
	}
	assertQuarantinedChildRetained(t, items, moviePath, userID, rootFolderID, collection.ID)

	// Second scan: the preservation is idempotent. The quarantined child
	// and its user_data, satellites, and skipped progress record stay.
	result, err = app.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("second cutover scan: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("second cutover scan errors = %#v, want exactly one quarantine error", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), moviePath) {
		t.Fatalf("second cutover scan error = %q, want it to name the movie path %q", result.Errors[0], moviePath)
	}
	assertQuarantinedChildRetained(t, items, moviePath, userID, rootFolderID, collection.ID)
}
