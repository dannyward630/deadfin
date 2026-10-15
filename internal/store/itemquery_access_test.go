// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestTranslateItemQueryUsesNormalizedPlaylistVisibilitySQL pins divergence
// D-03 (docs/divergences.md). A playlist is visible to the current user only
// through its dedicated public, playlist-owner, or normalized share facts;
// generic owner_id is deliberately not a playlist-owner alias.
func TestTranslateItemQueryUsesNormalizedPlaylistVisibilitySQL(t *testing.T) {
	viewer := groupingUUID(t, 601)
	query := store.ItemQuery{
		User:             &store.User{ID: viewer},
		IncludeItemTypes: []item.Kind{item.KindPlaylist},
	}

	for _, dialect := range groupingDialects() {
		t.Run(dialect.name, func(t *testing.T) {
			translated, err := store.Translate(query, dialect.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				for _, fragment := range []string{"kind IN", "open_access", "playlist_owner_user_id", "item_shares"} {
					if !strings.Contains(sql, fragment) {
						t.Fatalf("playlist visibility SQL = %s\nwant normalized visibility fragment %q", sql, fragment)
					}
				}
				if strings.Contains(sql, "owner_id =") {
					t.Fatalf("playlist visibility must not alias generic owner_id: %s", sql)
				}
				if strings.Contains(sql, viewer.String()) {
					t.Fatalf("playlist visibility SQL interpolated viewer %q: %s", viewer, sql)
				}
				if !strings.Contains(sql, dialect.placeholder) {
					t.Fatalf("playlist visibility SQL = %s, want %s placeholder", sql, dialect.placeholder)
				}
			}
			if len(translated.CountArgs) < 2 {
				t.Fatalf("playlist count args = %#v, want kind and bound viewer", translated.CountArgs)
			}
			if len(translated.Args) < len(translated.CountArgs) {
				t.Fatalf("page args = %#v, want count-argument prefix %#v", translated.Args, translated.CountArgs)
			}
		})
	}
}

// TestTranslateItemQueryUsesBoxSetLibraryFolderRelation pins the collection
// half of D-03: a BoxSet's library visibility comes from
// boxset_library_folders, not playlist facts or a serialized blob. The same
// relation must constrain the count before pagination.
func TestTranslateItemQueryUsesBoxSetLibraryFolderRelation(t *testing.T) {
	folder := groupingUUID(t, 602)
	query := store.ItemQuery{
		IncludeItemTypes:     []item.Kind{item.KindBoxSet},
		BoxSetLibraryFolders: []dto.UUID{folder},
	}
	for _, dialect := range groupingDialects() {
		t.Run(dialect.name, func(t *testing.T) {
			translated, err := store.Translate(query, dialect.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				if !strings.Contains(sql, "boxset_library_folders") || !strings.Contains(sql, "library_folder_id") {
					t.Fatalf("BoxSet library relation SQL = %s", sql)
				}
				if strings.Contains(sql, folder.String()) {
					t.Fatalf("BoxSet library SQL interpolated folder %q: %s", folder, sql)
				}
				if !strings.Contains(sql, dialect.placeholder) {
					t.Fatalf("BoxSet library SQL = %s, want %s placeholder", sql, dialect.placeholder)
				}
			}
			if len(translated.CountArgs) != 2 {
				t.Fatalf("BoxSet count args = %#v, want kind and folder", translated.CountArgs)
			}
		})
	}
}
