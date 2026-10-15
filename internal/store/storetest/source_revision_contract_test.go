// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStoreDerivedSourceSnapshotRevisionContractSQLite(t *testing.T) {
	runDerivedSourceSnapshotRevisionContract(t, openReviewerItemStore)
}

func TestItemStoreDerivedSourceSnapshotRevisionContractPostgres(t *testing.T) {
	runDerivedSourceSnapshotRevisionContract(t, openPostgresReviewerItemStore)
}

func TestItemStoreDerivedSourceSnapshotFileFactsContractSQLite(t *testing.T) {
	runDerivedSourceSnapshotFileFactsContract(t, openReviewerItemStore)
}

func TestItemStoreDerivedSourceSnapshotFileFactsContractPostgres(t *testing.T) {
	runDerivedSourceSnapshotFileFactsContract(t, openPostgresReviewerItemStore)
}

func TestItemStoreRejectsDerivedWritesWithoutCompleteIdentitySQLite(t *testing.T) {
	runDerivedWritesRequireCompleteIdentity(t, openReviewerItemStore)
}

func TestItemStoreRejectsDerivedWritesWithoutCompleteIdentityPostgres(t *testing.T) {
	runDerivedWritesRequireCompleteIdentity(t, openPostgresReviewerItemStore)
}

func runDerivedWritesRequireCompleteIdentity(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	for offset, tc := range []struct {
		name   string
		mutate func(*item.Item)
	}{
		{
			name: "both inode and changed-at absent",
			mutate: func(source *item.Item) {
				source.FileInode = nil
				source.FileChangedAt = nil
			},
		},
		{
			name: "inode only",
			mutate: func(source *item.Item) {
				source.FileChangedAt = nil
			},
		},
		{
			name: "changed-at only",
			mutate: func(source *item.Item) {
				source.FileInode = nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _ := open(t)
			parent, source, media := revisionSourceFixture(t, 17950+offset*10)
			tc.mutate(&source)
			reconcileRevisionSource(t, items, parent, source, media)

			snapshot := derivedSourceSnapshot(t, items, source.ID)
			if snapshot.SourceRevision != "" {
				t.Fatalf("derived source revision with %s = %q, want empty", tc.name, snapshot.SourceRevision)
			}
			record, err := items.MediaSourceRecord(t.Context(), source.ID)
			if err != nil {
				t.Fatalf("read media source record with %s: %v", tc.name, err)
			}
			if record.SourceRevision != "" {
				t.Fatalf("media source record revision with %s = %q, want empty", tc.name, record.SourceRevision)
			}

			keyframes := store.KeyframeInfo{
				ItemID:             source.ID,
				SourceRevision:     "forged-incomplete-identity-revision",
				TotalDurationTicks: *source.RunTimeTicks,
				KeyframeTicks:      []int64{0, 45_000_000},
			}
			if err := items.ReplaceKeyframes(t.Context(), keyframes); !errors.Is(err, store.ErrStaleSourceRevision) {
				t.Fatalf("replace keyframes with %s error = %v, want ErrStaleSourceRevision", tc.name, err)
			}
			if _, err := items.Keyframes(t.Context(), source.ID); !store.IsNotFound(err) {
				t.Fatalf("keyframes after rejected %s write error = %v, want no row", tc.name, err)
			}

			trickplay := store.TrickplayInfo{
				ItemID:          source.ID,
				SourceRevision:  "forged-incomplete-identity-revision",
				Width:           640,
				Height:          360,
				TileWidth:       160,
				TileHeight:      90,
				ThumbnailCount:  12,
				IntervalMS:      2_500,
				Bandwidth:       84_000,
				GenerationToken: "incomplete-identity-rejection",
			}
			if err := items.ReplaceTrickplay(t.Context(), trickplay); !errors.Is(err, store.ErrStaleSourceRevision) {
				t.Fatalf("replace trickplay with %s error = %v, want ErrStaleSourceRevision", tc.name, err)
			}
			if values, err := items.Trickplays(t.Context(), source.ID); err != nil || values != nil {
				t.Fatalf("trickplays after rejected %s write = %#v, %v; want no rows", tc.name, values, err)
			}
		})
	}

	t.Run("complete identity publishes derived writes", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := revisionSourceFixture(t, 17990)
		reconcileRevisionSource(t, items, parent, source, media)
		snapshot := derivedSourceSnapshot(t, items, source.ID)
		if snapshot.SourceRevision == "" {
			t.Fatal("derived source revision with complete identity is empty")
		}

		if err := items.ReplaceKeyframes(t.Context(), store.KeyframeInfo{
			ItemID:             source.ID,
			SourceRevision:     snapshot.SourceRevision,
			TotalDurationTicks: *source.RunTimeTicks,
			KeyframeTicks:      []int64{0, 45_000_000},
		}); err != nil {
			t.Fatalf("replace keyframes with complete identity: %v", err)
		}
		if err := items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{
			ItemID:          source.ID,
			SourceRevision:  snapshot.SourceRevision,
			Width:           640,
			Height:          360,
			TileWidth:       160,
			TileHeight:      90,
			ThumbnailCount:  12,
			IntervalMS:      2_500,
			Bandwidth:       84_000,
			GenerationToken: "complete-identity-control",
		}); err != nil {
			t.Fatalf("replace trickplay with complete identity: %v", err)
		}
		if _, err := items.Keyframes(t.Context(), source.ID); err != nil {
			t.Fatalf("read keyframes published with complete identity: %v", err)
		}
		if _, err := items.Trickplay(t.Context(), source.ID, 640); err != nil {
			t.Fatalf("read trickplay published with complete identity: %v", err)
		}
	})
}

func runDerivedSourceSnapshotFileFactsContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	items, _ := open(t)
	parent, source, media := revisionSourceFixture(t, 17900)
	size := int64(123_456_789)
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 654_321_000, time.UTC)
	source.Size = &size
	source.DateModified = &modified
	reconcileRevisionSource(t, items, parent, source, media)

	first := derivedSourceSnapshot(t, items, source.ID)
	if first.Size == nil || *first.Size != size {
		t.Fatalf("snapshot size = %v, want %d", first.Size, size)
	}
	if first.DateModified == nil || !first.DateModified.Equal(modified) {
		t.Fatalf("snapshot date modified = %v, want %s", first.DateModified, modified)
	}
	if first.SourceRevision == "" {
		t.Fatal("snapshot source revision is empty")
	}

	*first.Size = 1
	*first.DateModified = first.DateModified.Add(time.Hour)
	second := derivedSourceSnapshot(t, items, source.ID)
	if second.Size == nil || *second.Size != size {
		t.Fatalf("snapshot size after caller mutation = %v, want %d", second.Size, size)
	}
	if second.DateModified == nil || !second.DateModified.Equal(modified) {
		t.Fatalf("snapshot date modified after caller mutation = %v, want %s", second.DateModified, modified)
	}
	if second.SourceRevision != first.SourceRevision {
		t.Fatalf("snapshot source revision after caller mutation = %q, want canonical %q", second.SourceRevision, first.SourceRevision)
	}

	keyframes := store.KeyframeInfo{
		ItemID:             source.ID,
		SourceRevision:     second.SourceRevision,
		TotalDurationTicks: *source.RunTimeTicks,
		KeyframeTicks:      []int64{0, 45_000_000},
	}
	if err := items.ReplaceKeyframes(t.Context(), keyframes); err != nil {
		t.Fatalf("replace keyframes with canonical source revision: %v", err)
	}
	committed, err := items.Keyframes(t.Context(), source.ID)
	if err != nil {
		t.Fatalf("read committed keyframes: %v", err)
	}
	if committed.SourceRevision != "" {
		t.Fatalf("committed keyframes source revision = %q, want write-side only", committed.SourceRevision)
	}
}

func runDerivedSourceSnapshotRevisionContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	t.Run("is non-empty and stable across an identical source refresh", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := revisionSourceFixture(t, 18000)
		reconcileRevisionSource(t, items, parent, source, media)
		first := derivedSourceSnapshot(t, items, source.ID)
		reconcileRevisionSource(t, items, parent, source, media)
		second := derivedSourceSnapshot(t, items, source.ID)
		if first.SourceRevision == "" {
			t.Fatal("derived source revision is empty")
		}
		if second.SourceRevision != first.SourceRevision {
			t.Fatalf("revision after identical refresh = %q, want stable %q", second.SourceRevision, first.SourceRevision)
		}
		if second.ItemID != source.ID || !reflect.DeepEqual(second.Path, source.Path) || !reflect.DeepEqual(second.RunTimeTicks, source.RunTimeTicks) || !reflect.DeepEqual(second.Width, source.Width) || !reflect.DeepEqual(second.Height, source.Height) || !reflect.DeepEqual(second.Container, source.Container) {
			t.Fatalf("snapshot source facts = %#v, want current source facts for %s", second, source.ID)
		}
	})

	itemFields := []struct {
		name   string
		mutate func(*item.Item)
	}{
		{name: "path", mutate: func(value *item.Item) { value.Path = stringPtr("/library/revision-replaced.mkv") }},
		{name: "size", mutate: func(value *item.Item) { changed := int64(222_222); value.Size = &changed }},
		{name: "date modified", mutate: func(value *item.Item) { changed := value.DateModified.Add(time.Hour); value.DateModified = &changed }},
		{name: "runtime", mutate: func(value *item.Item) { changed := int64(120_000_000); value.RunTimeTicks = &changed }},
		{name: "container", mutate: func(value *item.Item) { value.Container = stringPtr("mp4") }},
		{name: "media source container", mutate: func(value *item.Item) { value.MediaSourceContainer = stringPtr("mov") }},
		{name: "width", mutate: func(value *item.Item) { changed := 1280; value.Width = &changed }},
		{name: "height", mutate: func(value *item.Item) { changed := 720; value.Height = &changed }},
		{name: "total bitrate", mutate: func(value *item.Item) { changed := 3_000_000; value.TotalBitrate = &changed }},
	}
	for offset, field := range itemFields {
		t.Run("changes when "+field.name+" changes", func(t *testing.T) {
			items, _ := open(t)
			parent, source, media := revisionSourceFixture(t, 18100+offset)
			reconcileRevisionSource(t, items, parent, source, media)
			before := derivedSourceSnapshot(t, items, source.ID)
			field.mutate(&source)
			reconcileRevisionSource(t, items, parent, source, media)
			assertChangedSourceRevision(t, before.SourceRevision, derivedSourceSnapshot(t, items, source.ID).SourceRevision, field.name)
		})
	}

	t.Run("changes when any stream description changes", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := revisionSourceFixture(t, 18200)
		reconcileRevisionSource(t, items, parent, source, media)
		before := derivedSourceSnapshot(t, items, source.ID)
		changed := append([]playback.MediaStream(nil), media[0].Streams...)
		changed[1].Title = stringPtr("director commentary")
		media[0].Streams = changed
		reconcileRevisionSource(t, items, parent, source, media)
		assertChangedSourceRevision(t, before.SourceRevision, derivedSourceSnapshot(t, items, source.ID).SourceRevision, "media stream description")
	})

	t.Run("changes when media stream order changes", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := revisionSourceFixture(t, 18201)
		reconcileRevisionSource(t, items, parent, source, media)
		before := derivedSourceSnapshot(t, items, source.ID)
		media[0].Streams[0], media[0].Streams[1] = media[0].Streams[1], media[0].Streams[0]
		media[0].Streams[0].Index, media[0].Streams[1].Index = 0, 1
		reconcileRevisionSource(t, items, parent, source, media)
		assertChangedSourceRevision(t, before.SourceRevision, derivedSourceSnapshot(t, items, source.ID).SourceRevision, "media stream order")
	})
}

func TestItemStoreDerivedSourceConditionalWritesContractSQLite(t *testing.T) {
	runDerivedSourceConditionalWritesContract(t, openReviewerItemStore)
}

func TestItemStoreDerivedSourceConditionalWritesContractPostgres(t *testing.T) {
	runDerivedSourceConditionalWritesContract(t, openPostgresReviewerItemStore)
}

func runDerivedSourceConditionalWritesContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()
	items, _ := open(t)
	parent, source, media := revisionSourceFixture(t, 18300)
	reconcileRevisionSource(t, items, parent, source, media)
	initial := derivedSourceSnapshot(t, items, source.ID)

	keyframes := store.KeyframeInfo{
		ItemID:             source.ID,
		SourceRevision:     initial.SourceRevision,
		TotalDurationTicks: *source.RunTimeTicks,
		KeyframeTicks:      []int64{0, 45_000_000},
	}
	trickplay := store.TrickplayInfo{
		ItemID:          source.ID,
		SourceRevision:  initial.SourceRevision,
		Width:           640,
		Height:          360,
		TileWidth:       160,
		TileHeight:      90,
		ThumbnailCount:  12,
		IntervalMS:      2_500,
		Bandwidth:       84_000,
		GenerationToken: "revision-contract-initial",
	}
	if err := items.ReplaceKeyframes(t.Context(), keyframes); err != nil {
		t.Fatalf("replace current-revision keyframes: %v", err)
	}
	if err := items.ReplaceTrickplay(t.Context(), trickplay); err != nil {
		t.Fatalf("replace current-revision trickplay: %v", err)
	}
	gotKeyframes, err := items.Keyframes(t.Context(), source.ID)
	if err != nil {
		t.Fatalf("read committed keyframes: %v", err)
	}
	wantKeyframes := keyframes
	wantKeyframes.SourceRevision = ""
	if !reflect.DeepEqual(gotKeyframes, wantKeyframes) {
		t.Fatalf("committed keyframes = %#v, want write-side revision omitted %#v", gotKeyframes, wantKeyframes)
	}
	gotTrickplay, err := items.Trickplay(t.Context(), source.ID, trickplay.Width)
	if err != nil {
		t.Fatalf("read committed trickplay: %v", err)
	}
	wantTrickplay := trickplay
	wantTrickplay.SourceRevision = ""
	if gotTrickplay != wantTrickplay {
		t.Fatalf("committed trickplay = %#v, want write-side revision omitted %#v", gotTrickplay, wantTrickplay)
	}

	changedPath := "/library/revision-cas-replaced.mkv"
	source.Path = &changedPath
	reconcileRevisionSource(t, items, parent, source, media)
	current := derivedSourceSnapshot(t, items, source.ID)
	assertChangedSourceRevision(t, initial.SourceRevision, current.SourceRevision, "source before conditional replacement")

	staleKeyframes := keyframes
	staleKeyframes.TotalDurationTicks = 77_000_000
	if err := items.ReplaceKeyframes(t.Context(), staleKeyframes); !errors.Is(err, store.ErrStaleSourceRevision) {
		t.Fatalf("replace stale-revision keyframes error = %v, want ErrStaleSourceRevision", err)
	}
	if _, err := items.Keyframes(t.Context(), source.ID); !store.IsNotFound(err) {
		t.Fatalf("keyframes after stale replacement error = %v, want no row", err)
	}

	staleTrickplay := trickplay
	staleTrickplay.GenerationToken = "revision-contract-stale"
	if err := items.ReplaceTrickplay(t.Context(), staleTrickplay); !errors.Is(err, store.ErrStaleSourceRevision) {
		t.Fatalf("replace stale-revision trickplay error = %v, want ErrStaleSourceRevision", err)
	}
	if got, err := items.Trickplays(t.Context(), source.ID); err != nil || got != nil {
		t.Fatalf("trickplays after stale replacement = %#v, %v; want no rows", got, err)
	}

	keyframes.SourceRevision = current.SourceRevision
	keyframes.TotalDurationTicks = 77_000_000
	if err := items.ReplaceKeyframes(t.Context(), keyframes); err != nil {
		t.Fatalf("replace fresh-revision keyframes: %v", err)
	}
	trickplay.SourceRevision = current.SourceRevision
	trickplay.GenerationToken = "revision-contract-current"
	if err := items.ReplaceTrickplay(t.Context(), trickplay); err != nil {
		t.Fatalf("replace fresh-revision trickplay: %v", err)
	}
	if _, err := items.Keyframes(t.Context(), source.ID); err != nil {
		t.Fatalf("fresh-revision keyframes did not commit: %v", err)
	}
	if _, err := items.Trickplay(t.Context(), source.ID, trickplay.Width); err != nil {
		t.Fatalf("fresh-revision trickplay did not commit: %v", err)
	}
}

func revisionSourceFixture(t *testing.T, value int) (item.Item, item.Item, []store.ScannedChildMedia) {
	t.Helper()
	parentID := contractUUID(t, value)
	parent := item.Item{ID: parentID, Kind: item.KindFolder, IsFolder: true, Name: stringPtr("revision parent")}
	childID := contractUUID(t, value+1)
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	size, runtime := int64(100_000), int64(90_000_000)
	width, height, bitrate := 1920, 1080, 2_000_000
	source := item.Item{
		ID:                   childID,
		Kind:                 item.KindMovie,
		ParentID:             &parentID,
		Name:                 stringPtr("revision source"),
		Path:                 stringPtr("/library/revision-source.mkv"),
		Size:                 &size,
		DateModified:         &modified,
		RunTimeTicks:         &runtime,
		Container:            stringPtr("matroska"),
		MediaSourceContainer: stringPtr("mkv"),
		Width:                &width,
		Height:               &height,
		TotalBitrate:         &bitrate,
	}
	device, inode := int64(0x1000), int64(value+0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, value*1_000, time.UTC)
	source.FileDevice = &device
	source.FileInode = &inode
	source.FileChangedAt = &changed
	language := "eng"
	return parent, source, []store.ScannedChildMedia{{
		ItemID: childID,
		Streams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Language: &language, Width: &width, Height: &height},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: &language, Channels: intPointer(2)},
		},
	}}
}

func reconcileRevisionSource(t *testing.T, items *store.ItemStore, parent, source item.Item, media []store.ScannedChildMedia) {
	t.Helper()
	if err := items.Upsert(t.Context(), parent); err != nil {
		t.Fatalf("upsert source parent: %v", err)
	}
	if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{source}, media, nil); err != nil {
		t.Fatalf("reconcile source: %v", err)
	}
}

func derivedSourceSnapshot(t *testing.T, items *store.ItemStore, itemID dto.UUID) store.DerivedSource {
	t.Helper()
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("read derived source snapshot for %s: %v", itemID, err)
	}
	return snapshot
}

func assertChangedSourceRevision(t *testing.T, before, after string, changed string) {
	t.Helper()
	if after == "" {
		t.Fatalf("revision after %s change is empty", changed)
	}
	if after == before {
		t.Fatalf("revision did not change after %s changed: %q", changed, after)
	}
}

func intPointer(value int) *int { return &value }
