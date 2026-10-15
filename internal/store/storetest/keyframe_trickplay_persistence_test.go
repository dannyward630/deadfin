// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
)

// KeyframeInfo is one item-ID keyed cache. TotalDurationTicks and every entry
// in KeyframeTicks use 100ns ticks; source order is significant to keyframe
// index seeking. A persisted nil tick collection is distinct from an empty one.
func TestItemStoreKeyframePersistenceContractSQLite(t *testing.T) {
	runItemStoreKeyframePersistenceContract(t, openReviewerItemStore)
}

func TestItemStoreKeyframePersistenceContractPostgres(t *testing.T) {
	runItemStoreKeyframePersistenceContract(t, openPostgresReviewerItemStore)
}

func runItemStoreKeyframePersistenceContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	t.Run("replaces one item keyed cache with exact ordered 100ns ticks", func(t *testing.T) {
		items, _ := open(t)
		ownerID := keyframeTrickplayOwner(t, items, 16100)
		otherID := keyframeTrickplayOwner(t, items, 16101)
		first := store.KeyframeInfo{
			ItemID:             ownerID,
			TotalDurationTicks: 100_000_000,
			KeyframeTicks:      []int64{20_000_000, 0, 10_000_000},
		}
		other := store.KeyframeInfo{
			ItemID:             otherID,
			TotalDurationTicks: 30_000_000,
			KeyframeTicks:      []int64{30_000_000},
		}
		for _, value := range []store.KeyframeInfo{first, other} {
			if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), value); err != nil {
				t.Fatalf("replace keyframe cache for %s: %v", value.ItemID, err)
			}
		}
		got, err := items.Keyframes(t.Context(), ownerID)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("keyframe cache = %#v, %v; want exact ordered tick record %#v, nil", got, err, first)
		}

		replacement := store.KeyframeInfo{
			ItemID:             ownerID,
			TotalDurationTicks: 120_000_000,
			KeyframeTicks:      []int64{90_000_000, 40_000_000},
		}
		if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), replacement); err != nil {
			t.Fatalf("replace keyframe cache: %v", err)
		}
		got, err = items.Keyframes(t.Context(), ownerID)
		if err != nil || !reflect.DeepEqual(got, replacement) {
			t.Fatalf("replacement keyframe cache = %#v, %v; want %#v, nil", got, err, replacement)
		}
		otherGot, err := items.Keyframes(t.Context(), otherID)
		if err != nil || !reflect.DeepEqual(otherGot, other) {
			t.Fatalf("other item keyframe cache = %#v, %v; want independent %#v", otherGot, err, other)
		}
	})

	t.Run("round trips nullable and empty tick collections distinctly", func(t *testing.T) {
		items, _ := open(t)
		nilID := keyframeTrickplayOwner(t, items, 16110)
		emptyID := keyframeTrickplayOwner(t, items, 16111)
		nilTicks := store.KeyframeInfo{ItemID: nilID, TotalDurationTicks: 10_000_000, KeyframeTicks: nil}
		emptyTicks := store.KeyframeInfo{ItemID: emptyID, TotalDurationTicks: 10_000_000, KeyframeTicks: []int64{}}
		for _, value := range []store.KeyframeInfo{nilTicks, emptyTicks} {
			if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), value); err != nil {
				t.Fatalf("persist %s tick collection: %v", value.ItemID, err)
			}
		}
		for _, want := range []store.KeyframeInfo{nilTicks, emptyTicks} {
			got, err := items.Keyframes(t.Context(), want.ItemID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("keyframe cache for %s = %#v, %v; want nullable-empty-preserving %#v, nil", want.ItemID, got, err, want)
			}
		}
	})

	t.Run("delete makes the cache not found", func(t *testing.T) {
		items, _ := open(t)
		ownerID := keyframeTrickplayOwner(t, items, 16112)
		if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: 10_000_000, KeyframeTicks: []int64{1}}); err != nil {
			t.Fatalf("seed keyframe cache: %v", err)
		}
		if err := items.DeleteKeyframes(t.Context(), ownerID); err != nil {
			t.Fatalf("delete keyframe cache: %v", err)
		}
		if _, err := items.Keyframes(t.Context(), ownerID); !store.IsNotFound(err) {
			t.Fatalf("read deleted keyframe cache error = %v, want not found", err)
		}
	})
}

// The write is a remove-and-replace transaction: a failed replacement must
// leave the prior complete cache visible, rather than no cache or new metadata.
func TestItemStoreKeyframeReplacementIsTransactionalSQLite(t *testing.T) {
	items, stores := openReviewerItemStore(t)
	ownerID := keyframeTrickplayOwner(t, items, 16120)
	original := store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: 20_000_000, KeyframeTicks: []int64{0, 10_000_000}}
	if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), original); err != nil {
		t.Fatalf("seed keyframe cache: %v", err)
	}

	for _, trigger := range []string{
		`CREATE TRIGGER fail_keyframe_insert BEFORE INSERT ON keyframes WHEN NEW.total_duration_ticks = 777000000 BEGIN SELECT RAISE(ABORT, 'injected keyframe insert failure'); END`,
		`CREATE TRIGGER fail_keyframe_update BEFORE UPDATE ON keyframes WHEN NEW.total_duration_ticks = 777000000 BEGIN SELECT RAISE(ABORT, 'injected keyframe update failure'); END`,
	} {
		if _, err := stores.Writer().ExecContext(t.Context(), trigger); err != nil {
			t.Fatalf("install keyframe replacement failure trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, trigger := range []string{"fail_keyframe_insert", "fail_keyframe_update"} {
			if _, err := stores.Writer().ExecContext(context.Background(), "DROP TRIGGER IF EXISTS "+trigger); err != nil {
				t.Errorf("remove keyframe replacement failure trigger %s: %v", trigger, err)
			}
		}
	})

	failed := store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: 777_000_000, KeyframeTicks: []int64{20_000_000}}
	if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), failed); err == nil {
		t.Fatal("replace keyframe cache with injected write failure succeeded")
	}
	got, err := items.Keyframes(t.Context(), ownerID)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("keyframe cache after failed replacement = %#v, %v; want preserved %#v", got, err, original)
	}
}

func TestItemStoreKeyframeForeignKeyCleanupContract(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			t.Run("item store delete clears cache without touching retained owner", func(t *testing.T) {
				items, stores := backend.open(t)
				deletedID := keyframeTrickplayOwner(t, items, 16130)
				retainedID := keyframeTrickplayOwner(t, items, 16131)
				for _, value := range []store.KeyframeInfo{
					{ItemID: deletedID, TotalDurationTicks: 20_000_000, KeyframeTicks: []int64{0, 10_000_000}},
					{ItemID: retainedID, TotalDurationTicks: 20_000_000, KeyframeTicks: []int64{20_000_000}},
				} {
					if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), value); err != nil {
						t.Fatalf("seed keyframe cache for %s: %v", value.ItemID, err)
					}
				}
				if err := items.Delete(t.Context(), deletedID); err != nil {
					t.Fatalf("delete keyframe owner: %v", err)
				}
				assertKeyframeCount(t, stores, deletedID, 0)
				assertKeyframeCount(t, stores, retainedID, 1)
				assertNoKeyframeOrphans(t, stores)
			})

			t.Run("raw owner deletion cascades cache row", func(t *testing.T) {
				items, stores := backend.open(t)
				ownerID := keyframeTrickplayOwner(t, items, 16132)
				if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: 20_000_000, KeyframeTicks: []int64{0, 10_000_000}}); err != nil {
					t.Fatalf("seed raw keyframe owner: %v", err)
				}
				query := "DELETE FROM items WHERE id = " + stores.Dialect().Placeholder(1)
				if _, err := stores.Writer().ExecContext(t.Context(), query, ownerID[:]); err != nil {
					t.Fatalf("raw delete keyframe owner: %v", err)
				}
				assertKeyframeCount(t, stores, ownerID, 0)
				assertNoKeyframeOrphans(t, stores)
			})
		})
	}
}

// TrickplayInfo exactly mirrors the upstream required metadata fields. Its
// IntervalMS unit is milliseconds, unlike keyframe positions and duration.
func TestItemStoreTrickplayPersistenceContractSQLite(t *testing.T) {
	runItemStoreTrickplayPersistenceContract(t, openReviewerItemStore)
}

func TestItemStoreTrickplayPersistenceContractPostgres(t *testing.T) {
	runItemStoreTrickplayPersistenceContract(t, openPostgresReviewerItemStore)
}

func runItemStoreTrickplayPersistenceContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	t.Run("replaces complete item width metadata and generation tokens deterministically", func(t *testing.T) {
		items, _ := open(t)
		ownerID := keyframeTrickplayOwner(t, items, 16200)
		otherID := keyframeTrickplayOwner(t, items, 16201)
		wide := store.TrickplayInfo{ItemID: ownerID, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 12, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "wide-generation-original"}
		narrow := store.TrickplayInfo{ItemID: ownerID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 18, IntervalMS: 1_000, Bandwidth: 32_000, GenerationToken: "narrow-generation"}
		other := store.TrickplayInfo{ItemID: otherID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 4, IntervalMS: 500, Bandwidth: 16_000, GenerationToken: "other-generation"}
		for _, value := range []store.TrickplayInfo{wide, narrow, other} {
			if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), value); err != nil {
				t.Fatalf("replace trickplay %s/%d: %v", value.ItemID, value.Width, err)
			}
		}

		listed, err := items.Trickplays(t.Context(), ownerID)
		if err != nil || !reflect.DeepEqual(listed, []store.TrickplayInfo{narrow, wide}) {
			t.Fatalf("listed trickplay metadata = %#v, %v; want deterministic width order %#v", listed, err, []store.TrickplayInfo{narrow, wide})
		}
		got, err := items.Trickplay(t.Context(), ownerID, wide.Width)
		if err != nil || got != wide {
			t.Fatalf("read wide trickplay metadata = %#v, %v; want %#v, nil", got, err, wide)
		}

		replacement := wide
		replacement.Height = 352
		replacement.TileHeight = 88
		replacement.ThumbnailCount = 15
		replacement.IntervalMS = 3_000
		replacement.Bandwidth = 96_000
		replacement.GenerationToken = "wide-generation-replacement"
		if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), replacement); err != nil {
			t.Fatalf("replace same-width trickplay metadata: %v", err)
		}
		listed, err = items.Trickplays(t.Context(), ownerID)
		if err != nil || !reflect.DeepEqual(listed, []store.TrickplayInfo{narrow, replacement}) {
			t.Fatalf("trickplay metadata after same-width replacement = %#v, %v; want one complete replacement", listed, err)
		}
	})

	t.Run("returns nil empty list and not found point read after delete", func(t *testing.T) {
		items, _ := open(t)
		ownerID := keyframeTrickplayOwner(t, items, 16210)
		empty, err := items.Trickplays(t.Context(), ownerID)
		if err != nil {
			t.Fatalf("list absent trickplay metadata: %v", err)
		}
		if empty != nil {
			t.Fatalf("absent trickplay list = %#v, want nil empty list", empty)
		}
		if _, err := items.Trickplay(t.Context(), ownerID, 320); !store.IsNotFound(err) {
			t.Fatalf("read absent trickplay metadata error = %v, want not found", err)
		}

		value := store.TrickplayInfo{ItemID: ownerID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 4, IntervalMS: 500, Bandwidth: 16_000, GenerationToken: "delete-generation"}
		if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), value); err != nil {
			t.Fatalf("seed trickplay metadata: %v", err)
		}
		if err := items.DeleteTrickplay(t.Context(), ownerID, value.Width); err != nil {
			t.Fatalf("delete trickplay metadata: %v", err)
		}
		empty, err = items.Trickplays(t.Context(), ownerID)
		if err != nil || empty != nil {
			t.Fatalf("trickplay list after delete = %#v, %v; want nil empty list", empty, err)
		}
	})

	t.Run("rejects blank generation token before database access", func(t *testing.T) {
		items, _ := open(t)
		ownerID := keyframeTrickplayOwner(t, items, 16211)
		blank := store.TrickplayInfo{ItemID: ownerID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 4, IntervalMS: 500, Bandwidth: 16_000, GenerationToken: " \t "}
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := items.ReplaceTrickplay(cancelled, blank); err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("replace trickplay with blank generation token error = %v, want validation rejection before database access", err)
		}
	})
}

func TestItemStoreTrickplayForeignKeyCleanupContract(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			t.Run("item store delete clears metadata without touching retained owner", func(t *testing.T) {
				items, stores := backend.open(t)
				deletedID := keyframeTrickplayOwner(t, items, 16220)
				retainedID := keyframeTrickplayOwner(t, items, 16221)
				for _, value := range []store.TrickplayInfo{
					{ItemID: deletedID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 4, IntervalMS: 500, Bandwidth: 16_000, GenerationToken: "deleted-generation"},
					{ItemID: retainedID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 8, IntervalMS: 1_000, Bandwidth: 32_000, GenerationToken: "retained-generation"},
				} {
					if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), value); err != nil {
						t.Fatalf("seed trickplay metadata for %s: %v", value.ItemID, err)
					}
				}
				if err := items.Delete(t.Context(), deletedID); err != nil {
					t.Fatalf("delete trickplay owner: %v", err)
				}
				assertTrickplayCount(t, stores, deletedID, 0)
				assertTrickplayCount(t, stores, retainedID, 1)
				assertNoTrickplayOrphans(t, stores)
			})

			t.Run("raw owner deletion cascades metadata row", func(t *testing.T) {
				items, stores := backend.open(t)
				ownerID := keyframeTrickplayOwner(t, items, 16222)
				value := store.TrickplayInfo{ItemID: ownerID, Width: 320, Height: 180, TileWidth: 80, TileHeight: 45, ThumbnailCount: 4, IntervalMS: 500, Bandwidth: 16_000, GenerationToken: "raw-delete-generation"}
				if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), value); err != nil {
					t.Fatalf("seed raw trickplay owner: %v", err)
				}
				query := "DELETE FROM items WHERE id = " + stores.Dialect().Placeholder(1)
				if _, err := stores.Writer().ExecContext(t.Context(), query, ownerID[:]); err != nil {
					t.Fatalf("raw delete trickplay owner: %v", err)
				}
				assertTrickplayCount(t, stores, ownerID, 0)
				assertNoTrickplayOrphans(t, stores)
			})
		})
	}
}

func keyframeTrickplayOwner(t *testing.T, items *store.ItemStore, value int) dto.UUID {
	t.Helper()
	id := contractUUID(t, value)
	device := int64(0x1000)
	inode := int64(value + 0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, value*1_000, time.UTC)
	if err := items.Upsert(t.Context(), item.Item{
		ID:            id,
		Kind:          item.KindMovie,
		Name:          stringPtr("keyframe and trickplay owner"),
		FileDevice:    &device,
		FileInode:     &inode,
		FileChangedAt: &changed,
	}); err != nil {
		t.Fatalf("upsert keyframe/trickplay owner %s: %v", id, err)
	}
	return id
}

func assertKeyframeCount(t *testing.T, stores store.Stores, itemID dto.UUID, want int) {
	t.Helper()
	query := "SELECT count(*) FROM keyframes WHERE item_id = " + stores.Dialect().Placeholder(1)
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query, itemID[:]).Scan(&got); err != nil {
		t.Fatalf("count keyframes for %s: %v", itemID, err)
	}
	if got != want {
		t.Fatalf("keyframe rows for %s = %d, want %d", itemID, got, want)
	}
}

func assertNoKeyframeOrphans(t *testing.T, stores store.Stores) {
	t.Helper()
	const query = `
SELECT count(*)
FROM keyframes
LEFT JOIN items ON items.id = keyframes.item_id
WHERE items.id IS NULL`
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatalf("count orphaned keyframes: %v", err)
	}
	if got != 0 {
		t.Fatalf("orphaned keyframe rows = %d, want 0", got)
	}
}

func assertTrickplayCount(t *testing.T, stores store.Stores, itemID dto.UUID, want int) {
	t.Helper()
	query := "SELECT count(*) FROM trickplay_infos WHERE item_id = " + stores.Dialect().Placeholder(1)
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query, itemID[:]).Scan(&got); err != nil {
		t.Fatalf("count trickplay metadata for %s: %v", itemID, err)
	}
	if got != want {
		t.Fatalf("trickplay metadata rows for %s = %d, want %d", itemID, got, want)
	}
}

func assertNoTrickplayOrphans(t *testing.T, stores store.Stores) {
	t.Helper()
	const query = `
SELECT count(*)
FROM trickplay_infos
LEFT JOIN items ON items.id = trickplay_infos.item_id
WHERE items.id IS NULL`
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatalf("count orphaned trickplay metadata: %v", err)
	}
	if got != 0 {
		t.Fatalf("orphaned trickplay metadata rows = %d, want 0", got)
	}
}

// These stores use separate PostgreSQL writer pools over one schema. The
// client-side barrier releases both replace operations together; bounded
// repetitions cover the insert window for which the public store exposes no
// injectable database hook.
func TestItemStoreKeyframeFirstWriteConcurrentReplacementPostgres(t *testing.T) {
	firstStore, secondStore, _ := openPostgresKeyframeTrickplayStorePair(t)
	for attempt := range 16 {
		ownerID := keyframeTrickplayOwner(t, firstStore, 16300+attempt)
		first := store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: int64(10_000_000 + attempt), KeyframeTicks: []int64{0, int64(attempt + 1)}}
		second := store.KeyframeInfo{ItemID: ownerID, TotalDurationTicks: int64(20_000_000 + attempt), KeyframeTicks: []int64{int64(attempt + 2), 30_000_000}}

		errs := runTwoStoreReplacements(t.Context(), func(ctx context.Context) error {
			return replaceKeyframesWithCurrentRevision(t, firstStore, ctx, first)
		}, func(ctx context.Context) error {
			return replaceKeyframesWithCurrentRevision(t, secondStore, ctx, second)
		})
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("first-write keyframe replacements on attempt %d = (%v, %v), want both successful", attempt, errs[0], errs[1])
		}

		got, err := firstStore.Keyframes(t.Context(), ownerID)
		if err != nil || (!reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second)) {
			t.Fatalf("keyframes after concurrent first write on attempt %d = %#v, %v; want one complete candidate %#v or %#v", attempt, got, err, first, second)
		}
	}
}

func TestItemStoreTrickplaySameWidthConcurrentReplacementPostgres(t *testing.T) {
	firstStore, secondStore, _ := openPostgresKeyframeTrickplayStorePair(t)
	for attempt := range 16 {
		ownerID := keyframeTrickplayOwner(t, firstStore, 16400+attempt)
		original := store.TrickplayInfo{ItemID: ownerID, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 1, IntervalMS: 1_000, Bandwidth: 32_000, GenerationToken: "concurrent-generation"}
		if err := replaceTrickplayWithCurrentRevision(t, firstStore, t.Context(), original); err != nil {
			t.Fatalf("seed same-width trickplay replacement on attempt %d: %v", attempt, err)
		}
		first := original
		first.Height = 352
		first.TileHeight = 88
		first.ThumbnailCount = 12 + attempt
		first.IntervalMS = 2_000
		first.Bandwidth = 64_000
		second := original
		second.Height = 348
		second.TileWidth = 158
		second.ThumbnailCount = 24 + attempt
		second.IntervalMS = 3_000
		second.Bandwidth = 96_000

		errs := runTwoStoreReplacements(t.Context(), func(ctx context.Context) error {
			return replaceTrickplayWithCurrentRevision(t, firstStore, ctx, first)
		}, func(ctx context.Context) error {
			return replaceTrickplayWithCurrentRevision(t, secondStore, ctx, second)
		})
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("same-width trickplay replacements on attempt %d = (%v, %v), want both successful", attempt, errs[0], errs[1])
		}

		got, err := firstStore.Trickplay(t.Context(), ownerID, original.Width)
		if err != nil || (got != first && got != second) {
			t.Fatalf("trickplay after concurrent same-width replacement on attempt %d = %#v, %v; want one complete candidate %#v or %#v", attempt, got, err, first, second)
		}
	}
}

func TestItemStoreTrickplayInt32RangeParitySQLite(t *testing.T) {
	runItemStoreTrickplayInt32RangeParity(t, openReviewerItemStore)
}

func TestItemStoreTrickplayInt32RangeParityPostgres(t *testing.T) {
	runItemStoreTrickplayInt32RangeParity(t, openPostgresReviewerItemStore)
}

func runItemStoreTrickplayInt32RangeParity(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()
	const maxInt32 = int64(1<<31 - 1)
	for index, field := range []struct {
		name       string
		setMaximum func(*store.TrickplayInfo)
		setTooBig  func(*store.TrickplayInfo)
	}{
		{name: "width", setMaximum: func(value *store.TrickplayInfo) { value.Width = int(maxInt32) }, setTooBig: func(value *store.TrickplayInfo) { value.Width = int(maxInt32 + 1) }},
		{name: "height", setMaximum: func(value *store.TrickplayInfo) { value.Height = int(maxInt32) }, setTooBig: func(value *store.TrickplayInfo) { value.Height = int(maxInt32 + 1) }},
		{name: "tile width", setMaximum: func(value *store.TrickplayInfo) { value.TileWidth = int(maxInt32) }, setTooBig: func(value *store.TrickplayInfo) { value.TileWidth = int(maxInt32 + 1) }},
		{name: "tile height", setMaximum: func(value *store.TrickplayInfo) { value.TileHeight = int(maxInt32) }, setTooBig: func(value *store.TrickplayInfo) { value.TileHeight = int(maxInt32 + 1) }},
		{name: "thumbnail count", setMaximum: func(value *store.TrickplayInfo) { value.ThumbnailCount = int(maxInt32) }, setTooBig: func(value *store.TrickplayInfo) { value.ThumbnailCount = int(maxInt32 + 1) }},
		{name: "interval milliseconds", setMaximum: func(value *store.TrickplayInfo) { value.IntervalMS = maxInt32 }, setTooBig: func(value *store.TrickplayInfo) { value.IntervalMS = maxInt32 + 1 }},
		{name: "bandwidth", setMaximum: func(value *store.TrickplayInfo) { value.Bandwidth = maxInt32 }, setTooBig: func(value *store.TrickplayInfo) { value.Bandwidth = maxInt32 + 1 }},
	} {
		t.Run(field.name, func(t *testing.T) {
			items, _ := open(t)
			ownerID := keyframeTrickplayOwner(t, items, 16500+index)
			maximum := store.TrickplayInfo{ItemID: ownerID, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 12, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "range-generation"}
			field.setMaximum(&maximum)
			if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), maximum); err != nil {
				t.Fatalf("replace trickplay with maximum Int32 %s: %v", field.name, err)
			}
			got, err := items.Trickplay(t.Context(), ownerID, maximum.Width)
			if err != nil || got != maximum {
				t.Fatalf("maximum Int32 %s trickplay = %#v, %v; want %#v, nil", field.name, got, err, maximum)
			}

			overflow := maximum
			field.setTooBig(&overflow)
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if err := items.ReplaceTrickplay(cancelled, overflow); err == nil || errors.Is(err, context.Canceled) {
				t.Fatalf("replace trickplay with Int32-overflow %s error = %v, want validation rejection before database access", field.name, err)
			}
		})
	}

	items, _ := open(t)
	itemID := contractUUID(t, 16520)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := items.Trickplay(cancelled, itemID, int(maxInt32+1)); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("read trickplay with Int32-overflow width error = %v, want validation rejection before query", err)
	}
	if err := items.DeleteTrickplay(cancelled, itemID, int(maxInt32+1)); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("delete trickplay with Int32-overflow width error = %v, want validation rejection before query", err)
	}
}

func openPostgresKeyframeTrickplayStorePair(t *testing.T) (*store.ItemStore, *store.ItemStore, store.Stores) {
	t.Helper()
	firstStore, stores := openPostgresReviewerItemStore(t)
	var schema string
	if err := stores.Writer().QueryRowContext(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("discover PostgreSQL fixture schema: %v", err)
	}
	secondStores, err := postgres.Open(t.Context(), postgres.Config{DSN: os.Getenv("DEADFIN_TEST_POSTGRES_DSN"), Schema: schema})
	if err != nil {
		t.Fatalf("open second PostgreSQL store for replacement race: %v", err)
	}
	t.Cleanup(func() {
		if err := secondStores.Close(); err != nil {
			t.Errorf("close second PostgreSQL store for replacement race: %v", err)
		}
	})
	return firstStore, store.NewItemStore(secondStores), stores
}

func runTwoStoreReplacements(ctx context.Context, first, second func(context.Context) error) [2]error {
	var (
		ready sync.WaitGroup
		done  sync.WaitGroup
		errs  [2]error
	)
	start := make(chan struct{})
	ready.Add(2)
	done.Add(2)
	for index, replace := range []func(context.Context) error{first, second} {
		go func(index int, replace func(context.Context) error) {
			defer done.Done()
			ready.Done()
			<-start
			errs[index] = replace(ctx)
		}(index, replace)
	}
	ready.Wait()
	close(start)
	done.Wait()
	return errs
}

// A scanner reusing the same path-derived item ID must keep derived seek and
// thumbnail artifacts only while the source revision is unchanged. The source
// facts below are the persisted scanner inputs that make existing derivatives
// stale: location, size, modification time, runtime, and stream description.
func TestReconcileScannedPlanInvalidatesKeyframesAndTrickplayOnSourceRevisionChange(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			items, _ := backend.open(t)
			parent := hierarchyItem(t, 16600, item.KindFolder, nil)
			if err := items.Upsert(t.Context(), parent); err != nil {
				t.Fatalf("upsert scanned-plan parent: %v", err)
			}

			modified := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
			size, runtime := int64(100_000), int64(90_000_000)
			device, inode := int64(0x1000), int64(0x2000)
			changedAt := time.Date(2026, time.July, 28, 12, 0, 0, 1_000, time.UTC)
			child := reconciliationChild(t, 16601, item.KindMovie, parent.ID, "source revision movie")
			child.Path = stringPtr("/library/source-revision-original.mkv")
			child.Size = &size
			child.DateModified = &modified
			child.RunTimeTicks = &runtime
			child.FileDevice = &device
			child.FileInode = &inode
			child.FileChangedAt = &changedAt
			media := []store.ScannedChildMedia{{ItemID: child.ID, Streams: []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}}}}
			if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{child}, media, nil); err != nil {
				t.Fatalf("reconcile initial scanned source: %v", err)
			}

			keyframes := store.KeyframeInfo{ItemID: child.ID, TotalDurationTicks: runtime, KeyframeTicks: []int64{0, 45_000_000}}
			trickplay := store.TrickplayInfo{ItemID: child.ID, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 12, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "source-revision-generation"}
			if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), keyframes); err != nil {
				t.Fatalf("seed derived keyframes: %v", err)
			}
			if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), trickplay); err != nil {
				t.Fatalf("seed derived trickplay: %v", err)
			}

			if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{child}, media, nil); err != nil {
				t.Fatalf("reconcile unchanged scanned source: %v", err)
			}
			if got, err := items.Keyframes(t.Context(), child.ID); err != nil || !reflect.DeepEqual(got, keyframes) {
				t.Fatalf("keyframes after unchanged source scan = %#v, %v; want retained %#v", got, err, keyframes)
			}
			if got, err := items.Trickplay(t.Context(), child.ID, trickplay.Width); err != nil || got != trickplay {
				t.Fatalf("trickplay after unchanged source scan = %#v, %v; want retained %#v", got, err, trickplay)
			}

			changed := child
			changed.Path = stringPtr("/library/source-revision-replaced.mkv")
			changedSize, changedRuntime := int64(200_000), int64(120_000_000)
			changedModified := modified.Add(time.Minute)
			changed.Size = &changedSize
			changed.DateModified = &changedModified
			changed.RunTimeTicks = &changedRuntime
			changedMedia := []store.ScannedChildMedia{{ItemID: child.ID, Streams: []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc"}}}}
			if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{changed}, changedMedia, nil); err != nil {
				t.Fatalf("reconcile changed scanned source: %v", err)
			}
			if _, err := items.Keyframes(t.Context(), child.ID); !store.IsNotFound(err) {
				t.Fatalf("keyframes after source revision change error = %v, want not found", err)
			}
			if got, err := items.Trickplays(t.Context(), child.ID); err != nil || got != nil {
				t.Fatalf("trickplays after source revision change = %#v, %v; want nil empty list", got, err)
			}
		})
	}
}

func TestItemStorePublicSourceMutationsInvalidateDerivedMetadata(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			for offset, field := range []struct {
				name   string
				mutate func(*item.Item)
			}{
				{name: "path", mutate: func(value *item.Item) { value.Path = stringPtr("/library/upsert-replaced.mkv") }},
				{name: "size", mutate: func(value *item.Item) { changed := int64(222_222); value.Size = &changed }},
				{name: "date modified", mutate: func(value *item.Item) { changed := value.DateModified.Add(time.Hour); value.DateModified = &changed }},
				{name: "runtime", mutate: func(value *item.Item) { changed := int64(120_000_000); value.RunTimeTicks = &changed }},
				{name: "container", mutate: func(value *item.Item) { value.Container = stringPtr("mp4") }},
				{name: "media source container", mutate: func(value *item.Item) { value.MediaSourceContainer = stringPtr("mov") }},
				{name: "width", mutate: func(value *item.Item) { changed := 1280; value.Width = &changed }},
				{name: "height", mutate: func(value *item.Item) { changed := 720; value.Height = &changed }},
				{name: "total bitrate", mutate: func(value *item.Item) { changed := 3_000_000; value.TotalBitrate = &changed }},
			} {
				t.Run("upsert invalidates when "+field.name+" changes", func(t *testing.T) {
					items, _ := backend.open(t)
					parent, source, media := revisionSourceFixture(t, 16650+offset*10)
					reconcileRevisionSource(t, items, parent, source, media)
					seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)
					field.mutate(&source)
					if err := items.Upsert(t.Context(), source); err != nil {
						t.Fatalf("upsert changed %s source: %v", field.name, err)
					}
					assertNoDerivedMetadata(t, items, source.ID)
				})
			}

			t.Run("unchanged upsert preserves derived metadata", func(t *testing.T) {
				items, _ := backend.open(t)
				parent, source, media := revisionSourceFixture(t, 16750)
				reconcileRevisionSource(t, items, parent, source, media)
				seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)
				if err := items.Upsert(t.Context(), source); err != nil {
					t.Fatalf("upsert unchanged source: %v", err)
				}
				assertDerivedMetadata(t, items, source.ID)
			})

			for offset, change := range []struct {
				name   string
				mutate func([]playback.MediaStream) []playback.MediaStream
			}{
				{name: "stream description", mutate: func(streams []playback.MediaStream) []playback.MediaStream {
					changed := append([]playback.MediaStream(nil), streams...)
					changed[1].Title = stringPtr("updated commentary")
					return changed
				}},
				{name: "stream order", mutate: func(streams []playback.MediaStream) []playback.MediaStream {
					changed := append([]playback.MediaStream(nil), streams...)
					changed[0], changed[1] = changed[1], changed[0]
					changed[0].Index, changed[1].Index = 0, 1
					return changed
				}},
			} {
				t.Run("replace streams invalidates when "+change.name+" changes", func(t *testing.T) {
					items, _ := backend.open(t)
					parent, source, media := revisionSourceFixture(t, 16800+offset*10)
					reconcileRevisionSource(t, items, parent, source, media)
					seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)
					if err := items.ReplaceStreams(t.Context(), source.ID, change.mutate(media[0].Streams)); err != nil {
						t.Fatalf("replace changed %s streams: %v", change.name, err)
					}
					assertNoDerivedMetadata(t, items, source.ID)
				})
			}

			t.Run("unchanged replace streams preserves derived metadata", func(t *testing.T) {
				items, _ := backend.open(t)
				parent, source, media := revisionSourceFixture(t, 16850)
				reconcileRevisionSource(t, items, parent, source, media)
				seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)
				if err := items.ReplaceStreams(t.Context(), source.ID, media[0].Streams); err != nil {
					t.Fatalf("replace unchanged streams: %v", err)
				}
				assertDerivedMetadata(t, items, source.ID)
			})
		})
	}
}

func TestItemStorePublicSourceMutationRacesLeaveNoStaleDerivedMetadata(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, *store.ItemStore)
	}{
		{name: "sqlite", open: func(t *testing.T) (*store.ItemStore, *store.ItemStore) {
			items, _ := openReviewerItemStore(t)
			return items, items
		}},
		{name: "postgres", open: func(t *testing.T) (*store.ItemStore, *store.ItemStore) {
			first, second, _ := openPostgresKeyframeTrickplayStorePair(t)
			return first, second
		}},
	} {
		t.Run(backend.name, func(t *testing.T) {
			for offset, mutation := range []struct {
				name  string
				apply func(context.Context, *store.ItemStore, item.Item, []store.ScannedChildMedia) error
			}{
				{name: "upsert", apply: func(ctx context.Context, items *store.ItemStore, source item.Item, _ []store.ScannedChildMedia) error {
					source.Path = stringPtr("/library/race-upsert-replaced.mkv")
					return items.Upsert(ctx, source)
				}},
				{name: "replace streams", apply: func(ctx context.Context, items *store.ItemStore, source item.Item, media []store.ScannedChildMedia) error {
					changed := append([]playback.MediaStream(nil), media[0].Streams...)
					changed[1].Codec = "ac3"
					return items.ReplaceStreams(ctx, source.ID, changed)
				}},
			} {
				t.Run("CAS versus "+mutation.name, func(t *testing.T) {
					first, second := backend.open(t)
					for attempt := range 8 {
						parent, source, media := revisionSourceFixture(t, 16900+offset*100+attempt*10)
						reconcileRevisionSource(t, first, parent, source, media)
						seedDerivedMetadata(t, first, source.ID, *source.RunTimeTicks)
						snapshot := derivedSourceSnapshot(t, first, source.ID)
						keyframes := store.KeyframeInfo{ItemID: source.ID, SourceRevision: snapshot.SourceRevision, TotalDurationTicks: *source.RunTimeTicks, KeyframeTicks: []int64{0, 45_000_000}}
						trickplay := store.TrickplayInfo{ItemID: source.ID, SourceRevision: snapshot.SourceRevision, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 12, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "race-replacement"}
						errs := runTwoStoreReplacements(t.Context(), func(ctx context.Context) error {
							if err := first.ReplaceKeyframes(ctx, keyframes); err != nil && !errors.Is(err, store.ErrStaleSourceRevision) {
								return err
							}
							if err := first.ReplaceTrickplay(ctx, trickplay); err != nil && !errors.Is(err, store.ErrStaleSourceRevision) {
								return err
							}
							return nil
						}, func(ctx context.Context) error {
							return mutation.apply(ctx, second, source, media)
						})
						if errs[0] != nil || errs[1] != nil {
							t.Fatalf("CAS versus %s race attempt %d = (%v, %v)", mutation.name, attempt, errs[0], errs[1])
						}
						assertNoDerivedMetadata(t, first, source.ID)
					}
				})
			}
		})
	}
}

func TestReconcileScannedPlanMovedSourceInvalidatesDerivedMetadata(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) (*store.ItemStore, store.Stores)
	}{
		{name: "sqlite", open: openReviewerItemStore},
		{name: "postgres", open: openPostgresReviewerItemStore},
	} {
		t.Run(backend.name, func(t *testing.T) {
			for offset, change := range []struct {
				name  string
				apply func(*item.Item, []store.ScannedChildMedia) []store.ScannedChildMedia
			}{
				{name: "item facts", apply: func(source *item.Item, media []store.ScannedChildMedia) []store.ScannedChildMedia {
					source.Path = stringPtr("/library/promoted-replaced.mkv")
					return media
				}},
				{name: "streams", apply: func(_ *item.Item, media []store.ScannedChildMedia) []store.ScannedChildMedia {
					changed := append([]playback.MediaStream(nil), media[0].Streams...)
					changed[0].Codec = "hevc"
					media[0].Streams = changed
					return media
				}},
			} {
				t.Run("promoted source with changed "+change.name, func(t *testing.T) {
					items, _ := backend.open(t)
					oldParent, source, media := revisionSourceFixture(t, 17100+offset*20)
					newParent := hierarchyItem(t, 17110+offset*20, item.KindFolder, nil)
					if err := items.Upsert(t.Context(), newParent); err != nil {
						t.Fatalf("upsert new reconciliation parent: %v", err)
					}
					reconcileRevisionSource(t, items, oldParent, source, media)
					seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)
					promoted := source
					promoted.ParentID = &newParent.ID
					changedMedia := change.apply(&promoted, media)
					if _, err := items.ReconcileScannedPlan(t.Context(), newParent.ID, []item.Item{promoted}, changedMedia, nil); err != nil {
						t.Fatalf("reconcile promoted source with changed %s: %v", change.name, err)
					}
					assertNoDerivedMetadata(t, items, source.ID)
				})
			}
		})
	}
}

func seedDerivedMetadata(t *testing.T, items *store.ItemStore, itemID dto.UUID, runtime int64) {
	t.Helper()
	if err := replaceKeyframesWithCurrentRevision(t, items, t.Context(), store.KeyframeInfo{ItemID: itemID, TotalDurationTicks: runtime, KeyframeTicks: []int64{0, runtime / 2}}); err != nil {
		t.Fatalf("seed derived keyframes: %v", err)
	}
	if err := replaceTrickplayWithCurrentRevision(t, items, t.Context(), store.TrickplayInfo{ItemID: itemID, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90, ThumbnailCount: 12, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "derived-invalidation-seed"}); err != nil {
		t.Fatalf("seed derived trickplay: %v", err)
	}
}

func assertDerivedMetadata(t *testing.T, items *store.ItemStore, itemID dto.UUID) {
	t.Helper()
	if _, err := items.Keyframes(t.Context(), itemID); err != nil {
		t.Fatalf("read retained keyframes: %v", err)
	}
	if values, err := items.Trickplays(t.Context(), itemID); err != nil || len(values) == 0 {
		t.Fatalf("read retained trickplay rows = %#v, %v; want seeded row", values, err)
	}
}

func assertNoDerivedMetadata(t *testing.T, items *store.ItemStore, itemID dto.UUID) {
	t.Helper()
	if _, err := items.Keyframes(t.Context(), itemID); !store.IsNotFound(err) {
		t.Fatalf("keyframes after source mutation error = %v, want not found", err)
	}
	if values, err := items.Trickplays(t.Context(), itemID); err != nil || values != nil {
		t.Fatalf("trickplay after source mutation = %#v, %v; want nil empty list", values, err)
	}
}

// Existing persistence contracts exercise payload storage; derived writes now
// require the source snapshot taken immediately before each write.
func replaceKeyframesWithCurrentRevision(t *testing.T, items *store.ItemStore, ctx context.Context, value store.KeyframeInfo) error {
	t.Helper()
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), value.ItemID)
	if err != nil {
		return err
	}
	value.SourceRevision = snapshot.SourceRevision
	return items.ReplaceKeyframes(ctx, value)
}

func replaceTrickplayWithCurrentRevision(t *testing.T, items *store.ItemStore, ctx context.Context, value store.TrickplayInfo) error {
	t.Helper()
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), value.ItemID)
	if err != nil {
		return err
	}
	value.SourceRevision = snapshot.SourceRevision
	return items.ReplaceTrickplay(ctx, value)
}
