// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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

func TestItemStoreReviewerRegressionsSQLite(t *testing.T) {
	runItemStoreReviewerRegressions(t, openReviewerItemStore)
}

func TestItemStoreReviewerRegressionsPostgres(t *testing.T) {
	runItemStoreReviewerRegressions(t, openPostgresReviewerItemStore)
}

func runItemStoreReviewerRegressions(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Run("round trips trailer types", func(t *testing.T) {
		items, _ := open(t)
		want := []item.TrailerType{item.TrailerTypeComingSoonToStreaming, item.TrailerTypeLocalTrailer}
		it := item.Item{ID: contractUUID(t, 11000), Kind: item.KindMovie, Name: stringPtr("trailer types"), TrailerTypes: want}
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert trailer types: %v", err)
		}
		got, err := items.Get(t.Context(), it.ID)
		if err != nil {
			t.Fatalf("get trailer types: %v", err)
		}
		if !reflect.DeepEqual(got.TrailerTypes, want) {
			t.Fatalf("trailer types = %#v, want %#v", got.TrailerTypes, want)
		}
	})

	t.Run("repair recomputes value and people clean fields idempotently", func(t *testing.T) {
		items, stores := open(t)
		personID := contractUUID(t, 11101)
		it := item.Item{
			ID:     contractUUID(t, 11100),
			Kind:   item.KindMovie,
			Name:   stringPtr("repair clean fields"),
			Values: item.ItemValues{Genres: []string{"Zulu", "Åardvark"}},
			People: []item.PersonCredit{{PersonID: personID, Name: "Éclair", Type: item.PersonKindActor}},
		}
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert clean field fixture: %v", err)
		}
		if _, err := stores.Writer().ExecContext(t.Context(), "UPDATE item_values SET clean_value = 'stale' WHERE kind = 'genre'"); err != nil {
			t.Fatalf("poison item value clean field: %v", err)
		}
		if _, err := stores.Writer().ExecContext(t.Context(), "UPDATE people SET clean_name = 'stale' WHERE id = "+stores.Dialect().Placeholder(1), personID[:]); err != nil {
			t.Fatalf("poison person clean field: %v", err)
		}
		if err := items.RepairDerived(t.Context()); err != nil {
			t.Fatalf("repair derived clean fields: %v", err)
		}
		values, err := items.Values(t.Context(), "genre")
		if err != nil {
			t.Fatalf("list repaired genres: %v", err)
		}
		if want := []string{"Åardvark", "Zulu"}; !reflect.DeepEqual(values, want) {
			t.Fatalf("repaired genre ordering = %#v, want %#v", values, want)
		}
		var clean string
		if err := stores.Reader().QueryRowContext(t.Context(), "SELECT clean_name FROM people WHERE id = "+stores.Dialect().Placeholder(1), personID[:]).Scan(&clean); err != nil {
			t.Fatalf("read repaired person clean name: %v", err)
		}
		if want := item.CleanName("Éclair"); clean != want {
			t.Fatalf("person clean name = %q, want %q", clean, want)
		}
		if err := items.RepairDerived(t.Context()); err != nil {
			t.Fatalf("second derived repair: %v", err)
		}
		valuesAfterSecondRepair, err := items.Values(t.Context(), "genre")
		if err != nil {
			t.Fatalf("list genres after second repair: %v", err)
		}
		if !reflect.DeepEqual(valuesAfterSecondRepair, values) {
			t.Fatalf("second repair changed value ordering: got %#v, want %#v", valuesAfterSecondRepair, values)
		}
	})

	t.Run("stores media canonical fields once outside extra", func(t *testing.T) {
		items, stores := open(t)
		id := contractUUID(t, 11200)
		if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("typed media")}); err != nil {
			t.Fatalf("upsert typed media item: %v", err)
		}
		bitRate, width, height, channels, sampleRate := 9000000, 3840, 2160, 8, 48000
		streams := []playback.MediaStream{{
			Index: 4, Type: playback.StreamTypeVideo, Codec: "hevc", Language: stringPtr("en"), Title: stringPtr("Main video"), IsDefault: true,
			IsForced: true, IsExternal: true, BitRate: &bitRate, Width: &width, Height: &height, Channels: &channels, SampleRate: &sampleRate, Profile: stringPtr("Main 10"),
		}}
		attachments := []playback.MediaAttachment{{Index: 3, FileName: "cover.jpg", MimeType: "image/jpeg", Codec: "mjpeg", CodecTag: "MJPG", Comment: "cover"}}
		if err := items.ReplaceStreams(t.Context(), id, streams); err != nil {
			t.Fatalf("replace typed stream: %v", err)
		}
		if err := items.ReplaceAttachments(t.Context(), id, attachments); err != nil {
			t.Fatalf("replace typed attachment: %v", err)
		}
		gotStreams, err := items.Streams(t.Context(), id)
		if err != nil || !reflect.DeepEqual(gotStreams, streams) {
			t.Fatalf("streams = %#v, %v; want %#v, nil", gotStreams, err, streams)
		}
		gotAttachments, err := items.Attachments(t.Context(), id)
		if err != nil || !reflect.DeepEqual(gotAttachments, attachments) {
			t.Fatalf("attachments = %#v, %v; want %#v, nil", gotAttachments, err, attachments)
		}
		assertNoMediaCanonicalFieldsInExtra(t, stores, "media_streams", "stream_index", id, []string{"Index", "Type", "Codec", "Language", "Title", "IsDefault", "IsForced", "IsExternal", "BitRate", "Width", "Height", "Channels", "SampleRate"})
		assertNoMediaCanonicalFieldsInExtra(t, stores, "media_attachments", "attachment_index", id, []string{"Index", "FileName", "MimeType"})
	})

	t.Run("uses nearest nested series and season deterministically", func(t *testing.T) {
		items, _ := open(t)
		root := hierarchyItem(t, 11300, item.KindFolder, nil)
		outerSeries := hierarchyItem(t, 11304, item.KindSeries, &root.ID)
		outerSeason := hierarchyItem(t, 11303, item.KindSeason, &outerSeries.ID)
		innerSeries := hierarchyItem(t, 11302, item.KindSeries, &outerSeason.ID)
		innerSeason := hierarchyItem(t, 11301, item.KindSeason, &innerSeries.ID)
		episode := hierarchyItem(t, 11305, item.KindEpisode, &innerSeason.ID)
		for _, it := range []item.Item{root, outerSeries, outerSeason, innerSeries, innerSeason, episode} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upsert nested hierarchy %s: %v", it.ID, err)
			}
		}
		got, err := items.Get(t.Context(), episode.ID)
		if err != nil {
			t.Fatalf("get nested episode: %v", err)
		}
		assertUUIDPointer(t, "nearest series", got.SeriesID, innerSeries.ID)
		assertUUIDPointer(t, "nearest season", got.SeasonID, innerSeason.ID)
	})

	t.Run("rejects invalid playlist and visibility associations", func(t *testing.T) {
		items, _ := open(t)
		playlistWithoutOwner := item.Item{ID: contractUUID(t, 11400), Kind: item.KindPlaylist, Name: stringPtr("owner required")}
		if err := items.Upsert(t.Context(), playlistWithoutOwner); err == nil {
			t.Fatal("upsert Playlist with zero PlaylistOwnerUserID succeeded")
		}
		nonPlaylistWithOwner := item.Item{ID: contractUUID(t, 11401), Kind: item.KindMovie, Name: stringPtr("not playlist"), PlaylistOwnerUserID: contractUUID(t, 11402)}
		if err := items.Upsert(t.Context(), nonPlaylistWithOwner); err == nil {
			t.Fatal("upsert non-Playlist with PlaylistOwnerUserID succeeded")
		}
		movie := item.Item{ID: contractUUID(t, 11403), Kind: item.KindMovie, Name: stringPtr("wrong visibility target")}
		if err := items.Upsert(t.Context(), movie); err != nil {
			t.Fatalf("upsert movie visibility target: %v", err)
		}
		share := []store.ItemShare{{UserID: contractUUID(t, 11404), CanEdit: true}}
		if err := items.ReplaceShares(t.Context(), movie.ID, share); err == nil {
			t.Fatal("replace shares for non-Playlist succeeded")
		}
		if err := items.ReplaceShares(t.Context(), contractUUID(t, 11405), share); err == nil {
			t.Fatal("replace shares for missing Playlist succeeded")
		}
		folders := []dto.UUID{contractUUID(t, 11406)}
		if err := items.ReplaceBoxSetLibraryFolders(t.Context(), movie.ID, folders); err == nil {
			t.Fatal("replace BoxSet folders for non-BoxSet succeeded")
		}
		if err := items.ReplaceBoxSetLibraryFolders(t.Context(), contractUUID(t, 11407), folders); err == nil {
			t.Fatal("replace BoxSet folders for missing BoxSet succeeded")
		}
	})

	t.Run("removes unreferenced values after replacement and delete", func(t *testing.T) {
		items, _ := open(t)
		id := contractUUID(t, 11500)
		first := item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("value owner"), Values: item.ItemValues{Tags: []string{"Legacy"}}}
		if err := items.Upsert(t.Context(), first); err != nil {
			t.Fatalf("upsert first tag: %v", err)
		}
		replacement := first
		replacement.Values.Tags = []string{"Current"}
		if err := items.Upsert(t.Context(), replacement); err != nil {
			t.Fatalf("replace tag: %v", err)
		}
		values, err := items.Values(t.Context(), "tag")
		if err != nil {
			t.Fatalf("list replaced tags: %v", err)
		}
		if want := []string{"Current"}; !reflect.DeepEqual(values, want) {
			t.Fatalf("values after replacement = %#v, want %#v", values, want)
		}
		if err := items.Delete(t.Context(), id); err != nil {
			t.Fatalf("delete tag owner: %v", err)
		}
		values, err = items.Values(t.Context(), "tag")
		if err != nil {
			t.Fatalf("list deleted tags: %v", err)
		}
		if len(values) != 0 {
			t.Fatalf("values after delete = %#v, want no unreferenced values", values)
		}
	})

	t.Run("rejects self and descendant cycles without hanging recursive closure", func(t *testing.T) {
		items, _ := open(t)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		root := hierarchyItem(t, 11600, item.KindFolder, nil)
		child := hierarchyItem(t, 11601, item.KindFolder, &root.ID)
		grandchild := hierarchyItem(t, 11602, item.KindMovie, &child.ID)
		for _, it := range []item.Item{root, child, grandchild} {
			if err := items.Upsert(ctx, it); err != nil {
				t.Fatalf("upsert cycle fixture: %v", err)
			}
		}
		if err := items.MoveSubtree(ctx, root.ID, &root.ID); err == nil {
			t.Fatal("self-parent move succeeded")
		}
		if err := items.MoveSubtree(ctx, root.ID, &grandchild.ID); err == nil {
			t.Fatal("move under descendant succeeded")
		}
		assertAncestorSet(t, items, grandchild.ID, root.ID, child.ID)
	})

	t.Run("returns a core and satellite snapshot from one item version", func(t *testing.T) {
		items, _ := open(t)
		id := contractUUID(t, 11700)
		versions := []item.Item{
			{ID: id, Kind: item.KindMovie, Name: stringPtr("version-a"), ImageInfos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "version-a"}}},
			{ID: id, Kind: item.KindMovie, Name: stringPtr("version-b"), ImageInfos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "version-b"}}},
		}
		if err := items.Upsert(t.Context(), versions[0]); err != nil {
			t.Fatalf("seed concurrent snapshot fixture: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		writerReady := make(chan error)
		readerStart := make(chan struct{})
		writerDone := make(chan error, 1)
		go func() {
			if err := items.Upsert(ctx, versions[0]); err != nil {
				writerReady <- err
				writerDone <- err
				return
			}
			writerReady <- nil
			<-readerStart
			for index := 1; index < 128; index++ {
				if err := items.Upsert(ctx, versions[index%len(versions)]); err != nil {
					writerDone <- err
					return
				}
			}
			writerDone <- nil
		}()
		if err := <-writerReady; err != nil {
			t.Fatalf("start concurrent writer: %v", err)
		}
		close(readerStart)
		for index := range 128 {
			got, err := items.Get(ctx, id)
			if err != nil {
				t.Fatalf("concurrent get %d: %v", index, err)
			}
			if got.Name == nil || len(got.ImageInfos) != 1 || got.ImageInfos[0].Path != *got.Name {
				t.Fatalf("torn item read %d: name=%v images=%#v", index, got.Name, got.ImageInfos)
			}
			batch, err := items.GetBatch(ctx, []dto.UUID{id})
			if err != nil {
				t.Fatalf("concurrent get batch %d: %v", index, err)
			}
			if len(batch) != 1 || batch[0].Name == nil || len(batch[0].ImageInfos) != 1 || batch[0].ImageInfos[0].Path != *batch[0].Name {
				t.Fatalf("torn item batch %d: %#v", index, batch)
			}
		}
		if err := <-writerDone; err != nil {
			t.Fatalf("concurrent writer: %v", err)
		}
	})
}

func TestItemStoreSatelliteAndPersonRegressionsSQLite(t *testing.T) {
	runItemStoreSatelliteAndPersonRegressions(t, openReviewerItemStore)
}

func TestItemStoreSatelliteAndPersonRegressionsPostgres(t *testing.T) {
	runItemStoreSatelliteAndPersonRegressions(t, openPostgresReviewerItemStore)
}

func runItemStoreSatelliteAndPersonRegressions(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Run("renames shared people globally without a people map name mirror", func(t *testing.T) {
		items, _ := open(t)
		personID := contractUUID(t, 11900)
		firstID := contractUUID(t, 11901)
		secondID := contractUUID(t, 11902)
		first := item.Item{ID: firstID, Kind: item.KindMovie, Name: stringPtr("first"), People: []item.PersonCredit{{PersonID: personID, Name: "Original", Type: item.PersonKindActor}}}
		second := item.Item{ID: secondID, Kind: item.KindMovie, Name: stringPtr("second"), People: []item.PersonCredit{{PersonID: personID, Name: "Renamed", Type: item.PersonKindActor}}}
		if err := items.Upsert(t.Context(), first); err != nil {
			t.Fatalf("upsert first shared person: %v", err)
		}
		if err := items.Upsert(t.Context(), second); err != nil {
			t.Fatalf("upsert renamed shared person: %v", err)
		}
		for _, id := range []dto.UUID{firstID, secondID} {
			people, err := items.People(t.Context(), id)
			if err != nil || len(people) != 1 || people[0].Name != "Renamed" {
				t.Errorf("people for %s = %#v, %v; want globally renamed person", id, people, err)
			}
		}
	})

	t.Run("rejects satellite replacement for missing and deleted items without orphans", func(t *testing.T) {
		items, _ := open(t)
		missingID := contractUUID(t, 12000)
		chapter := store.Chapter{Index: 0, StartPositionTicks: 1, Extra: []byte(`{"marker":"valid"}`)}
		stream := playback.MediaStream{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}
		attachment := playback.MediaAttachment{Index: 0, FileName: "cover.jpg"}
		if err := items.ReplaceStreams(t.Context(), missingID, []playback.MediaStream{stream}); err == nil {
			t.Error("replace streams for missing item succeeded")
		}
		if err := items.ReplaceChapters(t.Context(), missingID, []store.Chapter{chapter}); err == nil {
			t.Error("replace chapters for missing item succeeded")
		}
		if err := items.ReplaceAttachments(t.Context(), missingID, []playback.MediaAttachment{attachment}); err == nil {
			t.Error("replace attachments for missing item succeeded")
		}
		id := contractUUID(t, 12001)
		if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("delete satellite owner")}); err != nil {
			t.Fatalf("upsert satellite owner: %v", err)
		}
		if err := items.Delete(t.Context(), id); err != nil {
			t.Fatalf("delete satellite owner: %v", err)
		}
		if err := items.ReplaceStreams(t.Context(), id, []playback.MediaStream{stream}); err == nil {
			t.Error("replace streams after delete succeeded")
		}
		if err := items.ReplaceChapters(t.Context(), id, []store.Chapter{chapter}); err == nil {
			t.Error("replace chapters after delete succeeded")
		}
		if err := items.ReplaceAttachments(t.Context(), id, []playback.MediaAttachment{attachment}); err == nil {
			t.Error("replace attachments after delete succeeded")
		}
	})

	t.Run("rejects malformed chapter extra without replacing existing chapters", func(t *testing.T) {
		items, _ := open(t)
		id := contractUUID(t, 12100)
		if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("chapter owner")}); err != nil {
			t.Fatalf("upsert chapter owner: %v", err)
		}
		original := []store.Chapter{{Index: 0, StartPositionTicks: 1, Extra: []byte(`{"marker":"valid"}`)}}
		if err := items.ReplaceChapters(t.Context(), id, original); err != nil {
			t.Fatalf("replace valid chapters: %v", err)
		}
		invalid := []store.Chapter{{Index: 0, StartPositionTicks: 2, Extra: []byte(`{`)}}
		if err := items.ReplaceChapters(t.Context(), id, invalid); err == nil {
			t.Fatal("replace malformed chapter extra succeeded")
		}
		got, err := items.Chapters(t.Context(), id)
		if err != nil {
			t.Fatalf("read chapters after malformed replacement: %v", err)
		}
		if !equalChapters(got, original) {
			t.Fatalf("chapters after malformed replacement = %#v, want semantic JSON equal to preserved %#v", got, original)
		}
	})
}

func TestItemsMigrationRoundTripSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite migration fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite migration fixture: %v", err)
		}
	})
	assertItemsMigrationRoundTrip(t, stores.Writer(), migrate.SQLite)
}

func TestItemsMigrationRoundTripPostgres(t *testing.T) {
	stores := storetest.OpenPostgres(t)
	assertItemsMigrationRoundTrip(t, stores.Writer(), migrate.Postgres)
}

func assertItemsMigrationRoundTrip(t *testing.T, db *sql.DB, dialect migrate.Dialect) {
	t.Helper()
	if err := migrate.DownTo(t.Context(), db, dialect, 6); err != nil {
		t.Fatalf("rollback to 00006 after 00007 for %s: %v", dialect, err)
	}
	assertItemUserDataKeysPresent(t, db, dialect, "00006")
	assertItemUserDataKeysIndex(t, db, dialect)
	if err := migrate.DownTo(t.Context(), db, dialect, 5); err != nil {
		t.Fatalf("rollback to 00005 after 00006 for %s: %v", dialect, err)
	}
	assertItemUserDataKeysPresent(t, db, dialect, "00005")
	assertItemUserDataKeysIndex(t, db, dialect)
	if err := migrate.DownTo(t.Context(), db, dialect, 4); err != nil {
		t.Fatalf("rollback to 00004 after 00005 for %s: %v", dialect, err)
	}
	assertItemUserDataKeysAbsent(t, db, dialect, "00005 down")
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM locked_fields").Scan(new(int)); err != nil {
		t.Fatalf("locked_fields absent after 00005 down for %s: %v", dialect, err)
	}
	if err := migrate.Up(t.Context(), db, dialect); err != nil {
		t.Fatalf("migrations up after rollback for %s: %v", dialect, err)
	}
	assertItemUserDataKeysPresent(t, db, dialect, "current")
	assertItemUserDataKeysIndex(t, db, dialect)
	if err := migrate.DownTo(t.Context(), db, dialect, 4); err != nil {
		t.Fatalf("rollback to 00004 for %s: %v", dialect, err)
	}
	assertItemUserDataKeysAbsent(t, db, dialect, "second 00005 down")
	if err := migrate.DownTo(t.Context(), db, dialect, 3); err != nil {
		t.Fatalf("rollback to 00003 after 00004 for %s: %v", dialect, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM locked_fields").Scan(new(int)); err == nil {
		t.Fatalf("locked_fields survived 00004 down for %s", dialect)
	}
	if err := migrate.Up(t.Context(), db, dialect); err != nil {
		t.Fatalf("items migrations re-up for %s: %v", dialect, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM locked_fields").Scan(new(int)); err != nil {
		t.Fatalf("locked_fields absent after items migrations re-up for %s: %v", dialect, err)
	}
}

func assertItemUserDataKeysPresent(t *testing.T, db *sql.DB, dialect migrate.Dialect, version string) {
	t.Helper()
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM item_user_data_keys").Scan(new(int)); err != nil {
		t.Fatalf("item_user_data_keys absent at %s for %s: %v", version, dialect, err)
	}
}

func assertItemUserDataKeysAbsent(t *testing.T, db *sql.DB, dialect migrate.Dialect, version string) {
	t.Helper()
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM item_user_data_keys").Scan(new(int)); err == nil {
		t.Fatalf("item_user_data_keys survived %s for %s", version, dialect)
	}
}

func assertItemUserDataKeysIndex(t *testing.T, db *sql.DB, dialect migrate.Dialect) {
	t.Helper()
	for _, index := range []string{"ix_item_user_data_keys_lookup", "ix_item_user_data_keys_key"} {
		var count int
		switch dialect {
		case migrate.SQLite:
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_index_list('item_user_data_keys') WHERE name = ?", index).Scan(&count); err != nil {
				t.Fatalf("reading SQLite user-data key indexes: %v", err)
			}
		case migrate.Postgres:
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1", index).Scan(&count); err != nil {
				t.Fatalf("reading PostgreSQL user-data key indexes: %v", err)
			}
		default:
			t.Fatalf("unsupported migration dialect %q", dialect)
		}
		if count != 1 {
			t.Fatalf("user-data key index %q count for %s = %d, want 1", index, dialect, count)
		}
	}
}

func TestItemStoreFileBackedSQLiteContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "item-store.db")
	stores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open file-backed SQLite store: %v", err)
	}
	items := store.NewItemStore(stores)
	id := contractUUID(t, 11800)
	it := item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("file-backed"), Values: item.ItemValues{Genres: []string{"Drama"}}, ImageInfos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "file-backed.jpg"}}}
	if err := items.Upsert(t.Context(), it); err != nil {
		t.Fatalf("upsert file-backed item: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("close file-backed SQLite store: %v", err)
	}
	reopened, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen file-backed SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened file-backed SQLite store: %v", err)
		}
	})
	got, err := store.NewItemStore(reopened).Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get file-backed item after reopen: %v", err)
	}
	if got.Name == nil || *got.Name != *it.Name || !reflect.DeepEqual(got.Values, it.Values) || !reflect.DeepEqual(got.ImageInfos, it.ImageInfos) {
		t.Fatalf("file-backed round trip = %#v, want persisted core and satellites %#v", got, it)
	}
}

func openReviewerItemStore(t *testing.T) (*store.ItemStore, store.Stores) {
	t.Helper()
	var stores store.Stores
	storetest.Run(t, func(t *testing.T) store.Stores {
		opened, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("open reviewer SQLite fixture: %v", err)
		}
		stores = opened
		return opened
	})
	return store.NewItemStore(stores), stores
}

func openPostgresReviewerItemStore(t *testing.T) (*store.ItemStore, store.Stores) {
	t.Helper()
	stores := storetest.OpenPostgres(t)
	return store.NewItemStore(stores), stores
}

func assertNoMediaCanonicalFieldsInExtra(t *testing.T, stores store.Stores, table, indexColumn string, itemID dto.UUID, fields []string) {
	t.Helper()
	var raw string
	query := fmt.Sprintf("SELECT extra FROM %s WHERE item_id = %s ORDER BY %s LIMIT 1", table, stores.Dialect().Placeholder(1), indexColumn)
	if err := stores.Reader().QueryRowContext(t.Context(), query, itemID[:]).Scan(&raw); err != nil {
		t.Fatalf("read %s extra: %v", table, err)
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		t.Fatalf("decode %s extra: %v", table, err)
	}
	for _, field := range fields {
		if _, ok := extra[field]; ok {
			t.Errorf("%s.extra duplicated canonical field %q: %s", table, field, raw)
		}
	}
}
