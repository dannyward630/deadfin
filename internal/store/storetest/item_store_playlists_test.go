// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func playlistBackends() []struct {
	name string
	open func(*testing.T) *store.ItemStore
} {
	return []struct {
		name string
		open func(*testing.T) *store.ItemStore
	}{
		{name: "sqlite", open: openItemStore},
		{name: "postgres", open: openPostgresItemStore},
	}
}

// TestItemStorePlaylistLifecycle pins the playlist persistence contract:
// CreatePlaylist writes the item, shares, and ordered children in one
// transaction; MutatePlaylist rewrites them; Playlist reads them back.
func TestItemStorePlaylistLifecycle(t *testing.T) {
	for _, backend := range playlistBackends() {
		t.Run(backend.name, func(t *testing.T) {
			items := backend.open(t)
			ctx := t.Context()
			folder := hierarchyItem(t, 16000, item.KindPlaylistsFolder, nil)
			movieA := hierarchyItem(t, 16001, item.KindMovie, nil)
			movieB := hierarchyItem(t, 16002, item.KindMovie, nil)
			movieC := hierarchyItem(t, 16003, item.KindMovie, nil)
			for _, value := range []item.Item{folder, movieA, movieB, movieC} {
				if err := items.Upsert(ctx, value); err != nil {
					t.Fatalf("seed %s: %v", value.ID, err)
				}
			}
			owner := contractUUID(t, 16010)
			friend := contractUUID(t, 16011)
			order := 0
			playlist := hierarchyItem(t, 16020, item.KindPlaylist, &folder.ID)
			playlist.PlaylistOwnerUserID = owner
			playlist.IsFolder = true
			playlist.LinkedChildren = []item.LinkedChildRef{{ItemID: movieB.ID, Type: item.LinkedChildTypeManual, SortOrder: &order}}
			if err := items.CreatePlaylist(ctx, playlist, []store.ItemShare{{UserID: friend, CanEdit: true}}); err != nil {
				t.Fatalf("create playlist: %v", err)
			}

			state, err := items.Playlist(ctx, playlist.ID)
			if err != nil {
				t.Fatalf("read playlist: %v", err)
			}
			if state.OwnerUserID != owner || state.Name != *playlist.Name || state.OpenAccess {
				t.Fatalf("state = %+v", state)
			}
			if !reflect.DeepEqual(state.Children, []dto.UUID{movieB.ID}) {
				t.Fatalf("children = %v, want [%s]", state.Children, movieB.ID)
			}
			if !reflect.DeepEqual(state.Shares, []store.ItemShare{{UserID: friend, CanEdit: true}}) {
				t.Fatalf("shares = %+v", state.Shares)
			}

			sortName := "renamed"
			written, err := items.MutatePlaylist(ctx, playlist.ID, func(state *store.PlaylistState) error {
				state.Name = "Renamed"
				state.SortName = &sortName
				state.OpenAccess = true
				state.Children = []dto.UUID{movieC.ID, movieA.ID, movieB.ID, movieA.ID}
				state.Shares = nil
				return nil
			})
			if err != nil {
				t.Fatalf("mutate playlist: %v", err)
			}
			if !reflect.DeepEqual(written.Children, []dto.UUID{movieC.ID, movieA.ID, movieB.ID}) {
				t.Fatalf("children = %v, want C, A, B with the duplicate dropped", written.Children)
			}
			if !written.OpenAccess || written.Name != "Renamed" || written.SortName == nil || *written.SortName != "renamed" || len(written.Shares) != 0 {
				t.Fatalf("written = %+v", written)
			}
			got, err := items.Get(ctx, playlist.ID)
			if err != nil || got.CleanName == nil || *got.CleanName != item.CleanName("Renamed") {
				t.Fatalf("clean name after rename = %v, %v", got.CleanName, err)
			}

			// A direct Playlist browse returns its entries in entry order.
			parentID := playlist.ID
			page, err := items.Query(ctx, store.ItemQuery{ParentID: &parentID, EnableTotalRecordCount: true})
			if err != nil {
				t.Fatalf("query playlist children: %v", err)
			}
			if ids := pageIDs(page.Items); !reflect.DeepEqual(ids, []dto.UUID{movieC.ID, movieA.ID, movieB.ID}) || page.TotalRecordCount != 3 {
				t.Fatalf("playlist browse = %v (total %d), want C, A, B", ids, page.TotalRecordCount)
			}
			sorted, err := items.Query(ctx, store.ItemQuery{ParentID: &parentID, SortBy: []store.ItemSortBy{store.ItemSortBySortName}})
			if err != nil {
				t.Fatalf("query sorted playlist children: %v", err)
			}
			if ids := pageIDs(sorted.Items); !reflect.DeepEqual(ids, []dto.UUID{movieA.ID, movieB.ID, movieC.ID}) {
				t.Fatalf("sorted playlist browse = %v, want A, B, C", ids)
			}
		})
	}
}

// TestItemStorePlaylistRejectsOtherKinds proves the playlist methods address
// only Playlist items.
func TestItemStorePlaylistRejectsOtherKinds(t *testing.T) {
	for _, backend := range playlistBackends() {
		t.Run(backend.name, func(t *testing.T) {
			items := backend.open(t)
			boxSet := hierarchyItem(t, 16100, item.KindBoxSet, nil)
			if err := items.Upsert(t.Context(), boxSet); err != nil {
				t.Fatalf("seed: %v", err)
			}
			for _, id := range []dto.UUID{boxSet.ID, contractUUID(t, 16101)} {
				if _, err := items.Playlist(t.Context(), id); !errors.Is(err, store.ErrNotPlaylist) || !store.IsNotFound(err) {
					t.Fatalf("Playlist(%s) error = %v, want ErrNotPlaylist", id, err)
				}
				if _, err := items.MutatePlaylist(t.Context(), id, func(*store.PlaylistState) error { return nil }); !errors.Is(err, store.ErrNotPlaylist) {
					t.Fatalf("MutatePlaylist(%s) error = %v, want ErrNotPlaylist", id, err)
				}
			}
		})
	}
}

// TestItemStorePlaylistConcurrentAppends proves MutatePlaylist serializes
// read-modify-write callers: no concurrent append is lost.
func TestItemStorePlaylistConcurrentAppends(t *testing.T) {
	for _, backend := range playlistBackends() {
		t.Run(backend.name, func(t *testing.T) {
			items := backend.open(t)
			ctx := t.Context()
			folder := hierarchyItem(t, 16200, item.KindPlaylistsFolder, nil)
			if err := items.Upsert(ctx, folder); err != nil {
				t.Fatalf("seed folder: %v", err)
			}
			playlist := hierarchyItem(t, 16201, item.KindPlaylist, &folder.ID)
			playlist.PlaylistOwnerUserID = contractUUID(t, 16202)
			if err := items.CreatePlaylist(ctx, playlist, nil); err != nil {
				t.Fatalf("create playlist: %v", err)
			}
			const writers = 8
			var wg sync.WaitGroup
			errs := make(chan error, writers)
			for index := range writers {
				child := contractUUID(t, 16300+index)
				wg.Go(func() {
					_, err := items.MutatePlaylist(ctx, playlist.ID, func(state *store.PlaylistState) error {
						state.Children = append(state.Children, child)
						return nil
					})
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent append: %v", err)
				}
			}
			state, err := items.Playlist(ctx, playlist.ID)
			if err != nil || len(state.Children) != writers {
				t.Fatalf("children after %d appends = %d, %v", writers, len(state.Children), err)
			}
		})
	}
}

// TestItemStoreBoxSetDirectBrowseReturnsLinkedChildren ports
// BoxSet.GetChildren: a direct BoxSet browse returns its linked children,
// and RemoveFromCollection unlinks them.
func TestItemStoreBoxSetDirectBrowseReturnsLinkedChildren(t *testing.T) {
	for _, backend := range playlistBackends() {
		t.Run(backend.name, func(t *testing.T) {
			items := backend.open(t)
			ctx := t.Context()
			boxSet := hierarchyItem(t, 16400, item.KindBoxSet, nil)
			movieA := hierarchyItem(t, 16401, item.KindMovie, nil)
			movieB := hierarchyItem(t, 16402, item.KindMovie, nil)
			for _, value := range []item.Item{boxSet, movieA, movieB} {
				if err := items.Upsert(ctx, value); err != nil {
					t.Fatalf("seed %s: %v", value.ID, err)
				}
			}
			if err := items.AddToCollection(ctx, boxSet.ID, []dto.UUID{movieB.ID, movieA.ID}); err != nil {
				t.Fatalf("add to collection: %v", err)
			}
			parentID := boxSet.ID
			page, err := items.Query(ctx, store.ItemQuery{ParentID: &parentID})
			if err != nil {
				t.Fatalf("query boxset children: %v", err)
			}
			if ids := pageIDs(page.Items); !reflect.DeepEqual(ids, []dto.UUID{movieA.ID, movieB.ID}) {
				t.Fatalf("boxset browse = %v, want both movies in sort-name order", ids)
			}

			// RemoveFromCollection unlinks members and skips non-members.
			if err := items.RemoveFromCollection(ctx, boxSet.ID, []dto.UUID{movieA.ID, contractUUID(t, 16403)}); err != nil {
				t.Fatalf("remove from collection: %v", err)
			}
			page, err = items.Query(ctx, store.ItemQuery{ParentID: &parentID})
			if err != nil {
				t.Fatalf("query boxset children after remove: %v", err)
			}
			if ids := pageIDs(page.Items); !reflect.DeepEqual(ids, []dto.UUID{movieB.ID}) {
				t.Fatalf("boxset browse after remove = %v, want only movie B", ids)
			}
			if err := items.RemoveFromCollection(ctx, movieB.ID, []dto.UUID{movieA.ID}); !errors.Is(err, store.ErrInvalidReference) {
				t.Fatalf("remove from a movie error = %v, want ErrInvalidReference", err)
			}
		})
	}
}

func pageIDs(values []item.Item) []dto.UUID {
	out := make([]dto.UUID, 0, len(values))
	for _, value := range values {
		out = append(out, value.ID)
	}
	return out
}
