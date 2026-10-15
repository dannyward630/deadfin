// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"crypto/md5" // #nosec G501 -- source-compatible persisted item identities, not security.
	"encoding/binary"
	"path/filepath"
	"testing"
	"unicode"
	"unicode/utf16"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Source: upstream/jellyfin/Emby.Server.Implementations/Library/LibraryManager.cs:777-803,
// :1135-1165; ServerApplicationPaths.cs:33-36; and PlaylistsFolder.cs:18-21.
func TestApplicationStartupPersistsSourceIdentifiedUserRootAndPlaylistsFoldersAcrossRestart(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	rootPath := filepath.Join(programData, "root", "default")
	playlistsPath := filepath.Join(programData, "data", "playlists")
	aggregateID := sourceBootstrapItemID("MediaBrowser.Controller.Entities.AggregateFolder", rootPath, programData)
	userRootID := sourceBootstrapItemID("MediaBrowser.Controller.Entities.UserRootFolder", rootPath, programData)
	playlistsID := sourceBootstrapItemID("Emby.Server.Implementations.Playlists.PlaylistsFolder", playlistsPath, programData)

	app := openRootBootstrapApplication(t, programData)
	assertRootBootstrap(t, store.NewItemStore(app.stores), aggregateID, userRootID, rootPath, playlistsID, playlistsPath)
	closeRootBootstrapApplication(t, app, t.Context())

	app = openRootBootstrapApplication(t, programData)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		closeRootBootstrapApplication(t, app, ctx)
	})
	assertRootBootstrap(t, store.NewItemStore(app.stores), aggregateID, userRootID, rootPath, playlistsID, playlistsPath)
}

func openRootBootstrapApplication(t *testing.T, programData string) *application {
	t.Helper()

	app, err := newApplication(t.Context(), programData)
	if err != nil {
		t.Fatalf("start application for root bootstrap: %v", err)
	}
	return app
}

func closeRootBootstrapApplication(t *testing.T, app *application, ctx context.Context) {
	t.Helper()

	if err := app.Close(ctx); err != nil {
		t.Fatalf("close application for root bootstrap restart: %v", err)
	}
}

func assertRootBootstrap(t *testing.T, items *store.ItemStore, aggregateID, userRootID dto.UUID, rootPath string, playlistsID dto.UUID, playlistsPath string) {
	t.Helper()

	roots, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindUserRootFolder}})
	if err != nil {
		t.Fatalf("query bootstrapped user roots: %v", err)
	}
	if len(roots.Items) != 1 {
		t.Fatalf("bootstrapped user root count = %d, want exactly 1", len(roots.Items))
	}
	if roots.Items[0].ID != userRootID {
		t.Fatalf("queried user root ID = %s, want source-derived %s", roots.Items[0].ID, userRootID)
	}

	root, err := items.Get(t.Context(), userRootID)
	if err != nil {
		t.Fatalf("get source-derived user root %s: %v", userRootID, err)
	}
	assertBootstrappedFolder(t, root, userRootID, item.KindUserRootFolder, "default", rootPath, nil, &userRootID)

	aggregate, err := items.Get(t.Context(), aggregateID)
	if err != nil {
		t.Fatalf("get source-derived aggregate root %s: %v", aggregateID, err)
	}
	assertBootstrappedFolder(t, aggregate, aggregateID, item.KindAggregateFolder, "default", rootPath, nil, &aggregateID)

	allPlaylists, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindPlaylistsFolder}})
	if err != nil {
		t.Fatalf("query bootstrapped playlists folders: %v", err)
	}
	if len(allPlaylists.Items) != 1 {
		t.Fatalf("bootstrapped playlists folder count = %d, want exactly 1", len(allPlaylists.Items))
	}
	if allPlaylists.Items[0].ID != playlistsID {
		t.Fatalf("queried playlists ID = %s, want source-derived %s", allPlaylists.Items[0].ID, playlistsID)
	}

	playlists, err := items.Query(t.Context(), store.ItemQuery{ParentID: &aggregateID, IncludeItemTypes: []item.Kind{item.KindPlaylistsFolder}})
	if err != nil {
		t.Fatalf("query bootstrapped playlists child: %v", err)
	}
	if len(playlists.Items) != 1 {
		t.Fatalf("bootstrapped playlists child count = %d, want exactly 1", len(playlists.Items))
	}
	if playlists.Items[0].ID != playlistsID {
		t.Fatalf("queried playlists ID = %s, want source-derived %s", playlists.Items[0].ID, playlistsID)
	}

	playlist, err := items.Get(t.Context(), playlistsID)
	if err != nil {
		t.Fatalf("get source-derived playlists folder %s: %v", playlistsID, err)
	}
	assertBootstrappedFolder(t, playlist, playlistsID, item.KindPlaylistsFolder, "Playlists", playlistsPath, &aggregateID, &playlistsID)
}

func assertBootstrappedFolder(t *testing.T, got item.Item, wantID dto.UUID, wantKind item.Kind, wantName, wantPath string, wantParent, wantTopParent *dto.UUID) {
	t.Helper()

	if got.ID != wantID || got.Kind != wantKind || !got.IsFolder {
		t.Fatalf("bootstrapped folder identity = %#v, want ID %s kind %s folder=true", got, wantID, wantKind)
	}
	if got.Name == nil || *got.Name != wantName {
		t.Fatalf("bootstrapped %s name = %v, want %q", wantKind, got.Name, wantName)
	}
	if got.Path == nil || *got.Path != wantPath {
		t.Fatalf("bootstrapped %s path = %v, want %q", wantKind, got.Path, wantPath)
	}
	if wantParent == nil {
		if got.ParentID != nil {
			t.Fatalf("bootstrapped %s parent = %s, want none", wantKind, *got.ParentID)
		}
	} else if got.ParentID == nil || *got.ParentID != *wantParent {
		t.Fatalf("bootstrapped %s parent = %v, want %s", wantKind, got.ParentID, *wantParent)
	}
	// A folder whose parent is the AggregateFolder is its own top parent, per
	// upstream BaseItem.IsTopParent. A parentless root is also its own top
	// parent, since the store walks the parent chain to a nil parent and
	// writes that item's own ID.
	if wantTopParent == nil {
		t.Fatalf("bootstrapped %s wantTopParent is nil", wantKind)
	}
	if got.TopParentID == nil || *got.TopParentID != *wantTopParent {
		t.Fatalf("bootstrapped %s top parent = %v, want %s", wantKind, got.TopParentID, *wantTopParent)
	}
}

func sourceBootstrapItemID(clrType, path, programData string) dto.UUID {
	key := path
	if len(key) >= len(programData) && key[:len(programData)] == programData {
		key = key[len(programData):]
		for len(key) > 0 && (key[0] == '/' || key[0] == '\\') {
			key = key[1:]
		}
		for index := range key {
			if key[index] == '/' {
				key = key[:index] + "\\" + key[index+1:]
			}
		}
	}

	units := utf16.Encode([]rune(clrType + stringLowerInvariant(key)))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	sum := md5.Sum(encoded) // #nosec G401 -- Jellyfin's persisted item identity algorithm.
	return dto.UUID{sum[3], sum[2], sum[1], sum[0], sum[5], sum[4], sum[7], sum[6], sum[8], sum[9], sum[10], sum[11], sum[12], sum[13], sum[14], sum[15]}
}

func stringLowerInvariant(value string) string {
	result := []rune(value)
	for index, character := range result {
		if character != '\u0130' {
			result[index] = unicodeSimpleLower(character)
		}
	}
	return string(result)
}

func unicodeSimpleLower(character rune) rune {
	return unicode.ToLower(character)
}
