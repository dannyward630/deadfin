// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Source: upstream/jellyfin/Emby.Server.Implementations/Library/LibraryManager.cs:1075-1124,
// :1127-1171, and :777-804. The production composition must use the one portable
// root/default topology for both source roots and system-folder relationships.
func TestApplicationBootstrapPersistsScannedTopologyAcrossRestart(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	rootPath := filepath.Join(programData, "root", "default")
	legacyDataRootPath := filepath.Join(programData, "data", "root", "default")
	source := t.TempDir()
	moviePath := filepath.Join(source, "topology-rooted.mkv")
	posterPath := filepath.Join(source, "poster.jpg")
	nfoPath := filepath.Join(source, "topology-rooted.nfo")
	for path, contents := range map[string]string{
		moviePath:  "movie fixture",
		posterPath: "poster fixture",
		nfoPath:    `<movie><title>Topology Enriched Movie</title><year>2024</year><uniqueid type="tmdb">topology-42</uniqueid></movie>`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write topology fixture %q: %v", path, err)
		}
	}

	app := openRootTopologyApplication(t, programData)
	assertApplicationTopologyRootPath(t, rootPath, legacyDataRootPath)
	items := store.NewItemStore(app.stores)
	aggregate := assertApplicationSystemRootHierarchy(t, items, programData, rootPath)
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Topology Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add topology collection: %v", err)
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan topology collection: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan topology collection errors = %#v, want none", result.Errors)
	}
	before := assertApplicationScannedTopology(t, items, aggregate.ID, moviePath, posterPath)
	closeRootTopologyApplication(t, app, t.Context())

	app = openRootTopologyApplication(t, programData)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		closeRootTopologyApplication(t, app, ctx)
	})
	items = store.NewItemStore(app.stores)
	aggregate = assertApplicationSystemRootHierarchy(t, items, programData, rootPath)
	after := assertApplicationScannedTopology(t, items, aggregate.ID, moviePath, posterPath)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("restarted root topology = %#v, want unchanged source-connected topology and enrichment %#v", after, before)
	}
}

type applicationScannedTopology struct {
	collection item.Item
	movie      item.Item
}

func openRootTopologyApplication(t *testing.T, programData string) *application {
	t.Helper()

	app, err := newApplication(t.Context(), programData)
	if err != nil {
		t.Fatalf("start application for root topology: %v", err)
	}
	return app
}

func closeRootTopologyApplication(t *testing.T, app *application, ctx context.Context) {
	t.Helper()
	if err := app.Close(ctx); err != nil {
		t.Fatalf("close application for root topology restart: %v", err)
	}
}

func assertApplicationTopologyRootPath(t *testing.T, rootPath, legacyDataRootPath string) {
	t.Helper()
	if info, err := os.Stat(rootPath); err != nil || !info.IsDir() {
		t.Fatalf("production topology root %q = (%#v, %v), want directory", rootPath, info, err)
	}
	if _, err := os.Stat(legacyDataRootPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("production topology created legacy data-root path %q: %v", legacyDataRootPath, err)
	}
}

func assertApplicationSystemRootHierarchy(t *testing.T, items *store.ItemStore, programData, rootPath string) item.Item {
	t.Helper()

	aggregateID := sourceBootstrapItemID("MediaBrowser.Controller.Entities.AggregateFolder", rootPath, programData)
	userRootID := sourceBootstrapItemID("MediaBrowser.Controller.Entities.UserRootFolder", rootPath, programData)
	playlistsPath := filepath.Join(programData, "data", "playlists")
	playlistsID := sourceBootstrapItemID("Emby.Server.Implementations.Playlists.PlaylistsFolder", playlistsPath, programData)
	aggregate := applicationTopologyItem(t, items, aggregateID)
	userRoot := applicationTopologyItem(t, items, userRootID)
	playlists := applicationTopologyItem(t, items, playlistsID)

	assertApplicationTopologyFolder(t, aggregate, item.KindAggregateFolder, rootPath, nil, &aggregateID)
	// GetUserRootFolder is a separate persisted root. It shares the source path
	// but is not made a child of the aggregate root.
	assertApplicationTopologyFolder(t, userRoot, item.KindUserRootFolder, rootPath, nil, &userRootID)
	assertApplicationTopologyFolder(t, playlists, item.KindPlaylistsFolder, playlistsPath, &aggregateID, &playlistsID)
	return aggregate
}

func applicationTopologyItem(t *testing.T, items *store.ItemStore, id dto.UUID) item.Item {
	t.Helper()
	got, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get source-identified topology item %s: %v", id, err)
	}
	return got
}

func assertApplicationTopologyFolder(t *testing.T, got item.Item, wantKind item.Kind, wantPath string, wantParent, wantTopParent *dto.UUID) {
	t.Helper()
	if got.Kind != wantKind || !got.IsFolder || got.Path == nil || *got.Path != wantPath {
		t.Fatalf("topology folder = %#v, want kind %s folder=true path %q", got, wantKind, wantPath)
	}
	if wantParent == nil {
		if got.ParentID != nil {
			t.Fatalf("topology %s parent = %v, want none", wantKind, got.ParentID)
		}
	} else if got.ParentID == nil || *got.ParentID != *wantParent {
		t.Fatalf("topology %s parent = %v, want %s", wantKind, got.ParentID, *wantParent)
	}
	// A folder whose parent is the AggregateFolder is its own top parent, per
	// upstream BaseItem.IsTopParent. A parentless root is also its own top
	// parent, since the store walks the parent chain to a nil parent and
	// writes that item's own ID.
	if wantTopParent == nil {
		t.Fatalf("topology %s wantTopParent is nil", wantKind)
	}
	if got.TopParentID == nil || *got.TopParentID != *wantTopParent {
		t.Fatalf("topology %s top parent = %v, want %s", wantKind, got.TopParentID, *wantTopParent)
	}
}

func assertApplicationScannedTopology(t *testing.T, items *store.ItemStore, aggregateID dto.UUID, moviePath, posterPath string) applicationScannedTopology {
	var collection item.Item

	collections, err := items.Query(t.Context(), store.ItemQuery{ParentID: &aggregateID, IncludeItemTypes: []item.Kind{item.KindCollectionFolder}})
	if err != nil {
		t.Fatalf("query collection under aggregate: %v", err)
	}
	if len(collections.Items) != 1 {
		t.Fatalf("aggregate collection children = %#v, want exactly one", collections.Items)
	}
	collection = collections.Items[0]
	if collection.ParentID == nil || *collection.ParentID != aggregateID || collection.TopParentID == nil || *collection.TopParentID != collection.ID {
		t.Fatalf("collection hierarchy = %#v, want aggregate parent %s and own top parent %s", collection, aggregateID, collection.ID)
	}

	movie := applicationTopologyItemAtPath(t, items, moviePath)
	if movie.ParentID == nil || movie.TopParentID == nil || *movie.TopParentID != collection.ID {
		t.Fatalf("scanned movie hierarchy = %#v, want collection top parent %s", movie, collection.ID)
	}
	rootFolder, err := items.Get(t.Context(), *movie.ParentID)
	if err != nil {
		t.Fatalf("load movie parent root Folder: %v", err)
	}
	if rootFolder.Kind != item.KindFolder || rootFolder.ParentID == nil || *rootFolder.ParentID != collection.ID {
		t.Fatalf("root Folder hierarchy = %#v, want collection parent %s", rootFolder, collection.ID)
	}
	if rootFolder.TopParentID == nil || *rootFolder.TopParentID != collection.ID {
		t.Fatalf("root Folder top parent = %v, want collection ID %s", rootFolder.TopParentID, collection.ID)
	}
	if movie.Name == nil || *movie.Name != "Topology Enriched Movie" || movie.ProductionYear == nil || *movie.ProductionYear != 2024 {
		t.Fatalf("scanned movie enrichment = %#v, want NFO title and year", movie)
	}
	if tmdb, ok := movie.ProviderIDs.Get("tmdb"); !ok || tmdb != "topology-42" {
		t.Fatalf("scanned movie provider IDs = %#v, want tmdb topology-42", movie.ProviderIDs.Snapshot())
	}
	for _, image := range movie.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == posterPath {
			return applicationScannedTopology{collection: collection, movie: movie}
		}
	}
	t.Fatalf("scanned movie images = %#v, want primary poster %q", movie.ImageInfos, posterPath)
	return applicationScannedTopology{}
}

func applicationTopologyItemAtPath(t *testing.T, items *store.ItemStore, path string) item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{Path: path})
	if err != nil {
		t.Fatalf("query topology item at %q: %v", path, err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("topology items at %q = %#v, want exactly one", path, result.Items)
	}
	return result.Items[0]
}
