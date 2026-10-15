// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStorePostgresSerializesConcurrentRootUpserts(t *testing.T) {
	items, _ := openPostgresReviewerItemStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	const roots = 8
	operations := make([]func(context.Context) error, 0, roots)
	ids := make([]dto.UUID, 0, roots)
	for index := range roots {
		id := contractUUID(t, 14000+index)
		ids = append(ids, id)
		it := item.Item{ID: id, Kind: item.KindFolder, Name: stringPtr("independent root")}
		operations = append(operations, func(ctx context.Context) error { return items.Upsert(ctx, it) })
	}
	runConcurrentItemStoreOperations(t, ctx, operations)
	for _, id := range ids {
		got, err := items.Get(ctx, id)
		if err != nil || got.ID != id {
			t.Errorf("root %s after concurrent upserts = %#v, %v", id, got, err)
		}
	}
}

func TestItemStorePostgresReplacementTransactionsDoNotMerge(t *testing.T) {
	items, _ := openPostgresReviewerItemStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	t.Run("shares", func(t *testing.T) {
		id := contractUUID(t, 14100)
		playlist := item.Item{ID: id, Kind: item.KindPlaylist, Name: stringPtr("concurrent shares"), PlaylistOwnerUserID: contractUUID(t, 14101)}
		if err := items.Upsert(ctx, playlist); err != nil {
			t.Fatalf("upsert playlist: %v", err)
		}
		first := []store.ItemShare{{UserID: contractUUID(t, 14102), CanEdit: false}, {UserID: contractUUID(t, 14103), CanEdit: true}}
		second := []store.ItemShare{{UserID: contractUUID(t, 14104), CanEdit: true}, {UserID: contractUUID(t, 14105), CanEdit: false}}
		runConcurrentItemStoreOperations(t, ctx, []func(context.Context) error{
			func(ctx context.Context) error { return items.ReplaceShares(ctx, id, first) },
			func(ctx context.Context) error { return items.ReplaceShares(ctx, id, second) },
		})
		got, err := items.Shares(ctx, id)
		if err != nil || (!reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second)) {
			t.Fatalf("shares after concurrent replacement = %#v, %v; want one complete replacement %#v or %#v", got, err, first, second)
		}
	})

	t.Run("boxset folders", func(t *testing.T) {
		id := contractUUID(t, 14200)
		if err := items.Upsert(ctx, item.Item{ID: id, Kind: item.KindBoxSet, Name: stringPtr("concurrent folders")}); err != nil {
			t.Fatalf("upsert boxset: %v", err)
		}
		first := []dto.UUID{contractUUID(t, 14201), contractUUID(t, 14202)}
		second := []dto.UUID{contractUUID(t, 14203), contractUUID(t, 14204)}
		runConcurrentItemStoreOperations(t, ctx, []func(context.Context) error{
			func(ctx context.Context) error { return items.ReplaceBoxSetLibraryFolders(ctx, id, first) },
			func(ctx context.Context) error { return items.ReplaceBoxSetLibraryFolders(ctx, id, second) },
		})
		got, err := items.BoxSetLibraryFolders(ctx, id)
		if err != nil || (!reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second)) {
			t.Fatalf("boxset folders after concurrent replacement = %#v, %v; want one complete replacement %#v or %#v", got, err, first, second)
		}
	})

	t.Run("media streams", func(t *testing.T) {
		id := concurrencyMediaItem(t, items, ctx, 14300)
		first := []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}, {Index: 1, Type: playback.StreamTypeAudio, Codec: "aac"}}
		second := []playback.MediaStream{{Index: 2, Type: playback.StreamTypeVideo, Codec: "hevc"}, {Index: 3, Type: playback.StreamTypeAudio, Codec: "opus"}}
		runConcurrentItemStoreOperations(t, ctx, []func(context.Context) error{
			func(ctx context.Context) error { return items.ReplaceStreams(ctx, id, first) },
			func(ctx context.Context) error { return items.ReplaceStreams(ctx, id, second) },
		})
		got, err := items.Streams(ctx, id)
		if err != nil || (!reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second)) {
			t.Fatalf("streams after concurrent replacement = %#v, %v; want one complete replacement %#v or %#v", got, err, first, second)
		}
	})

	t.Run("chapters", func(t *testing.T) {
		id := concurrencyMediaItem(t, items, ctx, 14400)
		first := []store.Chapter{{Index: 0, StartPositionTicks: 1, Extra: []byte(`{"marker":"first"}`)}, {Index: 1, StartPositionTicks: 2, Extra: []byte(`{"marker":"first"}`)}}
		second := []store.Chapter{{Index: 2, StartPositionTicks: 3, Extra: []byte(`{"marker":"second"}`)}, {Index: 3, StartPositionTicks: 4, Extra: []byte(`{"marker":"second"}`)}}
		runConcurrentItemStoreOperations(t, ctx, []func(context.Context) error{
			func(ctx context.Context) error { return items.ReplaceChapters(ctx, id, first) },
			func(ctx context.Context) error { return items.ReplaceChapters(ctx, id, second) },
		})
		got, err := items.Chapters(ctx, id)
		if err != nil || (!equalChapters(got, first) && !equalChapters(got, second)) {
			t.Fatalf("chapters after concurrent replacement = %#v, %v; want one complete replacement", got, err)
		}
	})

	t.Run("attachments", func(t *testing.T) {
		id := concurrencyMediaItem(t, items, ctx, 14500)
		first := []playback.MediaAttachment{{Index: 0, FileName: "first.jpg"}, {Index: 1, FileName: "first-2.jpg"}}
		second := []playback.MediaAttachment{{Index: 2, FileName: "second.jpg"}, {Index: 3, FileName: "second-2.jpg"}}
		runConcurrentItemStoreOperations(t, ctx, []func(context.Context) error{
			func(ctx context.Context) error { return items.ReplaceAttachments(ctx, id, first) },
			func(ctx context.Context) error { return items.ReplaceAttachments(ctx, id, second) },
		})
		got, err := items.Attachments(ctx, id)
		if err != nil || (!reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second)) {
			t.Fatalf("attachments after concurrent replacement = %#v, %v; want one complete replacement %#v or %#v", got, err, first, second)
		}
	})
}

func concurrencyMediaItem(t *testing.T, items *store.ItemStore, ctx context.Context, value int) dto.UUID {
	t.Helper()
	id := contractUUID(t, value)
	if err := items.Upsert(ctx, item.Item{ID: id, Kind: item.KindMovie, Name: stringPtr("concurrent media")}); err != nil {
		t.Fatalf("upsert concurrent media item: %v", err)
	}
	return id
}

func runConcurrentItemStoreOperations(t *testing.T, ctx context.Context, operations []func(context.Context) error) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, len(operations))
	var group sync.WaitGroup
	group.Add(len(operations))
	for _, operation := range operations {
		go func(operation func(context.Context) error) {
			defer group.Done()
			<-start
			errs <- operation(ctx)
		}(operation)
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replacement operation: %v", err)
		}
	}
}
