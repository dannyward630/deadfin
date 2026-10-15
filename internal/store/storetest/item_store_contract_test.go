// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestItemStoreContractSQLite(t *testing.T) {
	runItemStoreContract(t, openItemStore)
}

func TestItemStoreContractPostgres(t *testing.T) {
	runItemStoreContract(t, openPostgresItemStore)
}

func runItemStoreContract(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Run("CRUD every kind and batch retrieval", func(t *testing.T) {
		items := open(t)
		created := make([]item.Item, 0, len(allKinds))
		for index, kind := range allKinds {
			it := item.Item{ID: contractUUID(t, 100+index), Kind: kind, Name: stringPtr(kind.String() + " initial")}
			if kind == item.KindPlaylist {
				it.PlaylistOwnerUserID = contractUUID(t, 10000+index)
			}
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upsert %s: %v", kind, err)
			}

			got, err := items.Get(t.Context(), it.ID)
			if err != nil {
				t.Fatalf("get %s: %v", kind, err)
			}
			assertItemIdentity(t, got, it.ID, kind, *it.Name)

			it.Name = stringPtr(kind.String() + " replacement")
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("replace %s: %v", kind, err)
			}
			got, err = items.Get(t.Context(), it.ID)
			if err != nil {
				t.Fatalf("get replacement %s: %v", kind, err)
			}
			assertItemIdentity(t, got, it.ID, kind, *it.Name)
			created = append(created, it)
		}

		batchIDs := make([]dto.UUID, 0, len(created))
		for index := len(created) - 1; index >= 0; index-- {
			batchIDs = append(batchIDs, created[index].ID)
		}
		batch, err := items.GetBatch(t.Context(), batchIDs)
		if err != nil {
			t.Fatalf("get batch: %v", err)
		}
		if len(batch) != len(created) {
			t.Fatalf("batch count = %d, want %d", len(batch), len(created))
		}
		byID := make(map[dto.UUID]item.Item, len(batch))
		for _, got := range batch {
			byID[got.ID] = got
		}
		for _, want := range created {
			got, ok := byID[want.ID]
			if !ok {
				t.Errorf("batch omitted %s %s", want.Kind, want.ID)
				continue
			}
			assertItemIdentity(t, got, want.ID, want.Kind, *want.Name)
		}

		for _, it := range created {
			if err := items.Delete(t.Context(), it.ID); err != nil {
				t.Fatalf("delete %s: %v", it.Kind, err)
			}
			if _, err := items.Get(t.Context(), it.ID); err == nil {
				t.Errorf("get deleted %s succeeded", it.Kind)
			}
		}
	})

	t.Run("replaces all normalized item satellites transactionally", func(t *testing.T) {
		items := open(t)
		id := contractUUID(t, 1000)
		first := item.Item{
			ID:           id,
			Kind:         item.KindMovie,
			Name:         stringPtr("satellite original"),
			ProviderIDs:  providerIDs("Tmdb", "10", "Imdb", "tt10"),
			ImageInfos:   []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "original.jpg", Width: 100}},
			LockedFields: []item.MetadataField{item.MetadataFieldName},
			Values: item.ItemValues{
				Genres: []string{"Drama"}, Tags: []string{"old-tag"}, Artists: []string{"Original Artist"},
			},
			People: []item.PersonCredit{{PersonID: contractUUID(t, 1001), Name: "Original Person", Type: item.PersonKindActor}},
		}
		if err := items.Upsert(t.Context(), first); err != nil {
			t.Fatalf("upsert original satellites: %v", err)
		}

		replacement := item.Item{
			ID:           id,
			Kind:         item.KindMovie,
			Name:         stringPtr("satellite replacement"),
			ProviderIDs:  providerIDs("Tmdb", "20"),
			ImageInfos:   []item.ImageInfo{{Type: item.ImageTypeBackdrop, Path: "replacement.jpg", Width: 200}},
			LockedFields: []item.MetadataField{item.MetadataFieldOverview},
			Values: item.ItemValues{
				Genres: []string{"Comedy"}, Tags: []string{"new-tag"}, Artists: []string{"Replacement Artist"},
			},
			People: []item.PersonCredit{{PersonID: contractUUID(t, 1002), Name: "Replacement Person", Type: item.PersonKindDirector}},
		}
		if err := items.Upsert(t.Context(), replacement); err != nil {
			t.Fatalf("replace satellites: %v", err)
		}
		got, err := items.Get(t.Context(), id)
		if err != nil {
			t.Fatalf("get replacement satellites: %v", err)
		}
		if !reflect.DeepEqual(got.ProviderIDs.Snapshot(), replacement.ProviderIDs.Snapshot()) ||
			!reflect.DeepEqual(got.ImageInfos, replacement.ImageInfos) ||
			!reflect.DeepEqual(got.LockedFields, replacement.LockedFields) ||
			!reflect.DeepEqual(got.Values, replacement.Values) ||
			!reflect.DeepEqual(got.People, replacement.People) {
			t.Fatalf("replacement satellites = %#v, want %#v", got, replacement)
		}
		images, err := items.Images(t.Context(), id)
		if err != nil || !reflect.DeepEqual(images, replacement.ImageInfos) {
			t.Fatalf("images accessor = %#v, %v; want %#v, nil", images, err, replacement.ImageInfos)
		}
		people, err := items.People(t.Context(), id)
		if err != nil || !reflect.DeepEqual(people, replacement.People) {
			t.Fatalf("people accessor = %#v, %v; want %#v, nil", people, err, replacement.People)
		}
	})

	t.Run("maintains recursive closure after insert move and delete", func(t *testing.T) {
		items := open(t)
		root := hierarchyItem(t, 2000, item.KindFolder, nil)
		oldParent := hierarchyItem(t, 2001, item.KindFolder, &root.ID)
		newParent := hierarchyItem(t, 2002, item.KindFolder, &root.ID)
		branch := hierarchyItem(t, 2003, item.KindFolder, &oldParent.ID)
		leaf := hierarchyItem(t, 2004, item.KindMovie, &branch.ID)
		series := hierarchyItem(t, 2005, item.KindSeries, &root.ID)
		season := hierarchyItem(t, 2006, item.KindSeason, &series.ID)
		episode := hierarchyItem(t, 2007, item.KindEpisode, &season.ID)
		for _, it := range []item.Item{root, oldParent, newParent, branch, leaf, series, season, episode} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upsert hierarchy %s: %v", it.ID, err)
			}
		}
		assertAncestorSet(t, items, leaf.ID, root.ID, oldParent.ID, branch.ID)
		assertAncestorSet(t, items, episode.ID, root.ID, series.ID, season.ID)
		storedEpisode, err := items.Get(t.Context(), episode.ID)
		if err != nil {
			t.Fatalf("get episode derived columns: %v", err)
		}
		assertUUIDPointer(t, "episode top parent", storedEpisode.TopParentID, root.ID)
		assertUUIDPointer(t, "episode series", storedEpisode.SeriesID, series.ID)
		assertUUIDPointer(t, "episode season", storedEpisode.SeasonID, season.ID)

		if err := items.MoveSubtree(t.Context(), branch.ID, &newParent.ID); err != nil {
			t.Fatalf("move branch: %v", err)
		}
		assertAncestorSet(t, items, branch.ID, root.ID, newParent.ID)
		assertAncestorSet(t, items, leaf.ID, root.ID, newParent.ID, branch.ID)
		if err := items.Delete(t.Context(), branch.ID); err != nil {
			t.Fatalf("delete branch: %v", err)
		}
		if _, err := items.Get(t.Context(), branch.ID); err == nil {
			t.Error("deleted branch remained readable")
		}
		if _, err := items.Get(t.Context(), leaf.ID); err == nil {
			t.Error("descendant of deleted branch remained readable")
		}
		assertAncestorSet(t, items, newParent.ID, root.ID)
	})

	t.Run("interns ordered values and preserves first artist", func(t *testing.T) {
		items := open(t)
		first := item.Item{ID: contractUUID(t, 3000), Kind: item.KindAudio, Name: stringPtr("first audio"), Values: item.ItemValues{
			Genres: []string{"Rock", "Alternative"}, Tags: []string{"Driving", "Live"}, Artists: []string{"First Artist", "Second Artist"},
		}}
		second := item.Item{ID: contractUUID(t, 3001), Kind: item.KindAudio, Name: stringPtr("second audio"), Values: item.ItemValues{
			Genres: []string{"Rock"}, Tags: []string{"Live"}, Artists: []string{"First Artist"},
		}}
		for _, it := range []item.Item{first, second} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upsert values %s: %v", it.ID, err)
			}
		}
		got, err := items.Get(t.Context(), first.ID)
		if err != nil {
			t.Fatalf("get ordered values: %v", err)
		}
		if !reflect.DeepEqual(got.Values, first.Values) {
			t.Fatalf("ordered values = %#v, want %#v", got.Values, first.Values)
		}
		if len(got.Values.Artists) == 0 || got.Values.Artists[0] != "First Artist" {
			t.Fatalf("first artist = %#v, want First Artist at position zero", got.Values.Artists)
		}
		for _, query := range []struct {
			kind string
			want []string
		}{{"genre", []string{"Alternative", "Rock"}}, {"tag", []string{"Driving", "Live"}}, {"artist", []string{"First Artist", "Second Artist"}}} {
			values, err := items.Values(t.Context(), query.kind)
			if err != nil {
				t.Fatalf("list %s values: %v", query.kind, err)
			}
			if !reflect.DeepEqual(values, query.want) {
				t.Errorf("%s values = %#v, want deduped %#v", query.kind, values, query.want)
			}
		}
	})

	t.Run("recomputes clean values idempotently", func(t *testing.T) {
		items := open(t)
		name := "  Älpha—Béta!  "
		it := item.Item{ID: contractUUID(t, 4000), Kind: item.KindMovie, Name: &name}
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert clean-name item: %v", err)
		}
		if err := items.RepairDerived(t.Context()); err != nil {
			t.Fatalf("first derived repair: %v", err)
		}
		first, err := items.Get(t.Context(), it.ID)
		if err != nil {
			t.Fatalf("get after first repair: %v", err)
		}
		if first.CleanName == nil || *first.CleanName != item.CleanName(name) {
			t.Fatalf("clean name = %v, want %q", first.CleanName, item.CleanName(name))
		}
		if err := items.RepairDerived(t.Context()); err != nil {
			t.Fatalf("second derived repair: %v", err)
		}
		second, err := items.Get(t.Context(), it.ID)
		if err != nil {
			t.Fatalf("get after second repair: %v", err)
		}
		if !reflect.DeepEqual(second, first) {
			t.Fatalf("second repair changed item:\n got %#v\nwant %#v", second, first)
		}
	})

	t.Run("upserts user data by user and custom key", func(t *testing.T) {
		items := open(t)
		userID := contractUUID(t, 5000)
		key := "shared-watch-state"
		first := store.UserData{UserID: userID, CustomKey: key, ItemID: contractUUID(t, 5001), PositionTicks: 10, PlayCount: 1}
		rating := float32(8.5)
		likes := 3
		audio := 1
		subtitle := 2
		lastPlayed := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
		replacement := store.UserData{UserID: userID, CustomKey: key, ItemID: contractUUID(t, 5002), Played: true, PositionTicks: 20, PlayCount: 2, IsFavorite: true, Rating: &rating, Likes: &likes, LastPlayed: &lastPlayed, AudioStreamIndex: &audio, SubtitleStreamIndex: &subtitle}
		for _, data := range []store.UserData{first, replacement} {
			if err := items.UpsertUserData(t.Context(), data); err != nil {
				t.Fatalf("upsert user data: %v", err)
			}
		}
		got, err := items.GetUserData(t.Context(), userID, key)
		if err != nil {
			t.Fatalf("get user data: %v", err)
		}
		if !reflect.DeepEqual(got, replacement) {
			t.Fatalf("user data = %#v, want replacement %#v", got, replacement)
		}
	})

	t.Run("round trips mutable linked child identity and nullable order", func(t *testing.T) {
		items := open(t)
		parentID := contractUUID(t, 6000)
		childID := contractUUID(t, 6001)
		initial := item.Item{ID: parentID, Kind: item.KindBoxSet, Name: stringPtr("parent"), LinkedChildren: []item.LinkedChildRef{{ItemID: childID, Type: item.LinkedChildTypeManual}}}
		if err := items.Upsert(t.Context(), initial); err != nil {
			t.Fatalf("upsert initial linked child: %v", err)
		}
		order := 7
		updated := initial
		updated.LinkedChildren = []item.LinkedChildRef{{ItemID: childID, Type: item.LinkedChildTypeLinkedAlternateVersion, SortOrder: &order}}
		if err := items.Upsert(t.Context(), updated); err != nil {
			t.Fatalf("update linked child: %v", err)
		}
		children, err := items.LinkedChildren(t.Context(), parentID)
		if err != nil {
			t.Fatalf("linked children accessor: %v", err)
		}
		if !reflect.DeepEqual(children, updated.LinkedChildren) {
			t.Fatalf("linked children = %#v, want parent+child replacement %#v", children, updated.LinkedChildren)
		}
	})

	t.Run("round trips stream attachment and chapter satellites", func(t *testing.T) {
		items := open(t)
		id := contractUUID(t, 7000)
		if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("satellite holder")}); err != nil {
			t.Fatalf("upsert satellite holder: %v", err)
		}
		chapterName := "Opening"
		streams := []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Language: stringPtr("en"), IsDefault: true, Profile: stringPtr("high")}}
		attachments := []playback.MediaAttachment{{Index: 0, FileName: "cover.jpg", Codec: "mjpeg", MimeType: "image/jpeg"}}
		chapters := []store.Chapter{{Index: 0, StartPositionTicks: 123, Name: &chapterName, Extra: []byte(`{"marker":"intro"}`)}}
		if err := items.ReplaceStreams(t.Context(), id, streams); err != nil {
			t.Fatalf("replace streams: %v", err)
		}
		if err := items.ReplaceAttachments(t.Context(), id, attachments); err != nil {
			t.Fatalf("replace attachments: %v", err)
		}
		if err := items.ReplaceChapters(t.Context(), id, chapters); err != nil {
			t.Fatalf("replace chapters: %v", err)
		}
		gotStreams, streamsErr := items.Streams(t.Context(), id)
		gotAttachments, attachmentsErr := items.Attachments(t.Context(), id)
		gotChapters, chaptersErr := items.Chapters(t.Context(), id)
		if streamsErr != nil || !reflect.DeepEqual(gotStreams, streams) {
			t.Errorf("streams = %#v, %v; want %#v, nil", gotStreams, streamsErr, streams)
		}
		if attachmentsErr != nil || !reflect.DeepEqual(gotAttachments, attachments) {
			t.Errorf("attachments = %#v, %v; want %#v, nil", gotAttachments, attachmentsErr, attachments)
		}
		if chaptersErr != nil || !equalChapters(gotChapters, chapters) {
			t.Errorf("chapters = %#v, %v; want semantic JSON equal to %#v, nil", gotChapters, chaptersErr, chapters)
		}
	})

	t.Run("keeps playlist visibility facts separate from generic ownership", func(t *testing.T) {
		items := open(t)
		playlistID := contractUUID(t, 8000)
		genericOwner := contractUUID(t, 8001)
		playlistOwner := contractUUID(t, 8002)
		playlist := item.Item{ID: playlistID, Kind: item.KindPlaylist, Name: stringPtr("shared playlist"), OwnerID: &genericOwner, PlaylistOwnerUserID: playlistOwner, OpenAccess: true}
		if err := items.Upsert(t.Context(), playlist); err != nil {
			t.Fatalf("upsert playlist: %v", err)
		}
		shares := []store.ItemShare{{UserID: contractUUID(t, 8003), CanEdit: false}, {UserID: contractUUID(t, 8004), CanEdit: true}}
		if err := items.ReplaceShares(t.Context(), playlistID, shares); err != nil {
			t.Fatalf("replace playlist shares: %v", err)
		}
		got, err := items.Get(t.Context(), playlistID)
		if err != nil {
			t.Fatalf("get playlist: %v", err)
		}
		assertUUIDPointer(t, "generic owner", got.OwnerID, genericOwner)
		if got.PlaylistOwnerUserID != playlistOwner || !got.OpenAccess {
			t.Fatalf("playlist visibility facts = owner %s open %v, want owner %s open true", got.PlaylistOwnerUserID, got.OpenAccess, playlistOwner)
		}
		gotShares, err := items.Shares(t.Context(), playlistID)
		if err != nil || !reflect.DeepEqual(gotShares, shares) {
			t.Fatalf("playlist shares = %#v, %v; want %#v, nil", gotShares, err, shares)
		}
	})

	t.Run("keeps boxset library folders independent from playlist facts", func(t *testing.T) {
		items := open(t)
		playlistID := contractUUID(t, 9000)
		boxsetID := contractUUID(t, 9001)
		playlist := item.Item{ID: playlistID, Kind: item.KindPlaylist, Name: stringPtr("playlist facts"), PlaylistOwnerUserID: contractUUID(t, 9005)}
		boxset := item.Item{ID: boxsetID, Kind: item.KindBoxSet, Name: stringPtr("boxset facts")}
		for _, it := range []item.Item{playlist, boxset} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upsert visibility item %s: %v", it.ID, err)
			}
		}
		shares := []store.ItemShare{{UserID: contractUUID(t, 9002), CanEdit: true}}
		folders := []dto.UUID{contractUUID(t, 9003), contractUUID(t, 9004)}
		if err := items.ReplaceShares(t.Context(), playlistID, shares); err != nil {
			t.Fatalf("replace playlist shares: %v", err)
		}
		if err := items.ReplaceBoxSetLibraryFolders(t.Context(), boxsetID, folders); err != nil {
			t.Fatalf("replace boxset folders: %v", err)
		}
		gotFolders, err := items.BoxSetLibraryFolders(t.Context(), boxsetID)
		if err != nil || !reflect.DeepEqual(gotFolders, folders) {
			t.Fatalf("boxset folders = %#v, %v; want %#v, nil", gotFolders, err, folders)
		}
		gotShares, err := items.Shares(t.Context(), playlistID)
		if err != nil || !reflect.DeepEqual(gotShares, shares) {
			t.Fatalf("playlist shares after boxset update = %#v, %v; want %#v, nil", gotShares, err, shares)
		}
		gotBoxset, err := items.Get(t.Context(), boxsetID)
		if err != nil {
			t.Fatalf("get boxset: %v", err)
		}
		if !gotBoxset.PlaylistOwnerUserID.IsNil() || gotBoxset.OpenAccess {
			t.Fatalf("boxset acquired playlist facts: owner %s open %v", gotBoxset.PlaylistOwnerUserID, gotBoxset.OpenAccess)
		}
	})
}

func TestItemStoreSchemaHasDialectTwins(t *testing.T) {
	if err := migrate.ValidateTwins(); err != nil {
		t.Fatalf("item-store schema dialect twins: %v", err)
	}
}

// TestImageInfoDimensionsRoundTrip checks that the SQLite item store keeps
// distinct nonzero image Width and Height through the single-item reader
// (Images, GetBatch) and the batch reader (Query).
func TestImageInfoDimensionsRoundTrip(t *testing.T) {
	items := openItemStore(t)
	seeded := []item.Item{
		{ID: contractUUID(t, 9001), Kind: item.KindMovie, Name: stringPtr("dimensions a"), ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Path: "a-primary.jpg", Width: 1000, Height: 1500},
			{Type: item.ImageTypeBackdrop, Path: "a-backdrop.jpg", Width: 3840, Height: 2160},
		}},
		{ID: contractUUID(t, 9002), Kind: item.KindMovie, Name: stringPtr("dimensions b"), ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypePrimary, Path: "b-primary.jpg", Width: 680, Height: 1000},
		}},
		{ID: contractUUID(t, 9003), Kind: item.KindMovie, Name: stringPtr("dimensions c"), ImageInfos: []item.ImageInfo{
			{Type: item.ImageTypeThumb, Index: 1, Path: "c-thumb.jpg", Width: 1920, Height: 1080},
		}},
	}
	want := make(map[dto.UUID][]item.ImageInfo, len(seeded))
	for _, it := range seeded {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert %s: %v", it.ID, err)
		}
		want[it.ID] = it.ImageInfos
	}

	for id, wantImages := range want {
		got, err := items.Images(t.Context(), id)
		if err != nil {
			t.Fatalf("Images %s: %v", id, err)
		}
		assertImageDimensions(t, "Images", id, got, wantImages)
	}

	batch, err := items.GetBatch(t.Context(), []dto.UUID{seeded[2].ID, seeded[0].ID, seeded[1].ID})
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	assertImageDimensionsForItems(t, "GetBatch", batch, want)

	result, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{seeded[0].ID, seeded[1].ID, seeded[2].ID}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertImageDimensionsForItems(t, "Query", result.Items, want)
}

func assertImageDimensionsForItems(t *testing.T, path string, got []item.Item, want map[dto.UUID][]item.ImageInfo) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s returned %d items, want %d", path, len(got), len(want))
	}
	for _, it := range got {
		wantImages, ok := want[it.ID]
		if !ok {
			t.Fatalf("%s returned unexpected item %s", path, it.ID)
		}
		assertImageDimensions(t, path, it.ID, it.ImageInfos, wantImages)
	}
}

// assertImageDimensions matches each expected image by (Type, Index, Path)
// so the readers' `ORDER BY type,idx` (a string sort) does not have to agree
// with the seed order, then asserts the exact Width and Height.
func assertImageDimensions(t *testing.T, path string, id dto.UUID, got, want []item.ImageInfo) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s item %s: %d images, want %d", path, id, len(got), len(want))
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g.Type != w.Type || g.Index != w.Index || g.Path != w.Path {
				continue
			}
			found = true
			if g.Width != w.Width || g.Height != w.Height {
				t.Fatalf("%s item %s image %s/%d: dimensions %dx%d, want %dx%d", path, id, w.Type, w.Index, g.Width, g.Height, w.Width, w.Height)
			}
		}
		if !found {
			t.Fatalf("%s item %s: image %s/%d/%q missing from %v", path, id, w.Type, w.Index, w.Path, got)
		}
	}
}

func openItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	var stores store.Stores
	storetest.Run(t, func(t *testing.T) store.Stores {
		opened, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("opening ItemStore SQLite fixture: %v", err)
		}
		stores = opened
		return opened
	})
	return store.NewItemStore(stores)
}

func openPostgresItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	return store.NewItemStore(storetest.OpenPostgres(t))
}

func hierarchyItem(t *testing.T, value int, kind item.Kind, parentID *dto.UUID) item.Item {
	t.Helper()
	return item.Item{ID: contractUUID(t, value), Kind: kind, Name: stringPtr(fmt.Sprintf("%s %d", kind, value)), ParentID: parentID}
}

func equalChapters(got, want []store.Chapter) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		gotChapter, wantChapter := got[index], want[index]
		gotChapter.Extra = nil
		wantChapter.Extra = nil
		if !reflect.DeepEqual(gotChapter, wantChapter) || !equalJSON(got[index].Extra, want[index].Extra) {
			return false
		}
	}
	return true
}

func equalJSON(got, want []byte) bool {
	var gotValue, wantValue any
	return json.Unmarshal(got, &gotValue) == nil && json.Unmarshal(want, &wantValue) == nil && reflect.DeepEqual(gotValue, wantValue)
}

func contractUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func providerIDs(values ...string) item.ProviderIDs {
	var ids item.ProviderIDs
	for index := 0; index < len(values); index += 2 {
		ids.Set(values[index], values[index+1])
	}
	return ids
}

func stringPtr(value string) *string { return &value }

func assertItemIdentity(t *testing.T, got item.Item, wantID dto.UUID, wantKind item.Kind, wantName string) {
	t.Helper()
	if got.ID != wantID || got.Kind != wantKind || got.Name == nil || *got.Name != wantName {
		t.Errorf("item identity = %#v, want ID %s Kind %s Name %q", got, wantID, wantKind, wantName)
	}
}

func assertAncestorSet(t *testing.T, items *store.ItemStore, id dto.UUID, want ...dto.UUID) {
	t.Helper()
	got, err := items.Ancestors(t.Context(), id)
	if err != nil {
		t.Fatalf("ancestors for %s: %v", id, err)
	}
	if !sameUUIDSet(got, want) {
		t.Fatalf("ancestors for %s = %v, want closure %v", id, got, want)
	}
}

func sameUUIDSet(got, want []dto.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	gotCopy := append([]dto.UUID(nil), got...)
	wantCopy := append([]dto.UUID(nil), want...)
	sort.Slice(gotCopy, func(i, j int) bool { return gotCopy[i].String() < gotCopy[j].String() })
	sort.Slice(wantCopy, func(i, j int) bool { return wantCopy[i].String() < wantCopy[j].String() })
	return reflect.DeepEqual(gotCopy, wantCopy)
}

func assertUUIDPointer(t *testing.T, name string, got *dto.UUID, want dto.UUID) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %s", name, got, want)
	}
}

var allKinds = []item.Kind{
	item.KindAggregateFolder,
	item.KindAudio,
	item.KindAudioBook,
	item.KindBasePluginFolder,
	item.KindBook,
	item.KindBoxSet,
	item.KindChannel,
	item.KindChannelFolderItem,
	item.KindCollectionFolder,
	item.KindEpisode,
	item.KindFolder,
	item.KindGenre,
	item.KindManualPlaylistsFolder,
	item.KindMovie,
	item.KindLiveTvChannel,
	item.KindLiveTvProgram,
	item.KindMusicAlbum,
	item.KindMusicArtist,
	item.KindMusicGenre,
	item.KindMusicVideo,
	item.KindPerson,
	item.KindPhoto,
	item.KindPhotoAlbum,
	item.KindPlaylist,
	item.KindPlaylistsFolder,
	item.KindProgram,
	item.KindRecording,
	item.KindSeason,
	item.KindSeries,
	item.KindStudio,
	item.KindTrailer,
	item.KindTvChannel,
	item.KindTvProgram,
	item.KindUserRootFolder,
	item.KindUserView,
	item.KindVideo,
	item.KindYear,
}
