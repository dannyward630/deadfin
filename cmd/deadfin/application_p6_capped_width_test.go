// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestP6TrickplayTaskSkipsPersistedCappedEffectiveWidth locks the boundary
// where an odd-width source caps a requested rendition. The application source
// must expose the persisted effective width used by tasks, once only, so the
// real scheduled task does not regenerate an existing tile sheet.
func TestP6TrickplayTaskSkipsPersistedCappedEffectiveWidth(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	library := p6AddTrickplayLibrary(t, app, "capped-width", true)
	itemID := p6AdapterUUID(t, 0x6a1)
	videoWidth := 319
	runtimeTicks := int64(100_000_000)
	p6UpsertVideo(t, items, itemID, "/media/capped-width.mkv", &library.CollectionFolderID, &videoWidth, &runtimeTicks)
	frameRate := float32(23.976)
	if err := items.ReplaceStreams(t.Context(), itemID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, AverageFrameRate: &frameRate}}); err != nil {
		t.Fatalf("persist video stream: %v", err)
	}
	sourceSnapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("snapshot capped-width source revision: %v", err)
	}
	if err := items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{
		ItemID: itemID, SourceRevision: sourceSnapshot.SourceRevision, Width: 318, Height: 180, TileWidth: 10, TileHeight: 8,
		ThumbnailCount: 1, IntervalMS: 10_000, Bandwidth: 1, GenerationToken: strings.Repeat("c", 64),
	}); err != nil {
		t.Fatalf("persist capped effective-width trickplay: %v", err)
	}

	options := tasks.Options{IntervalMilliseconds: 10_000, Widths: []int{320, 319, 320}, TileWidth: 10, TileHeight: 8, Quality: 90}
	source := trickplayTaskSource{items: items, topology: app.topology, options: options}
	snapshot, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot capped-width candidate: %v", err)
	}
	candidates, err := snapshot.Page(t.Context(), 0, 100)
	if err != nil {
		t.Fatalf("read capped-width candidate page: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if !candidate.Generate || !reflect.DeepEqual(candidate.ExistingWidths, []int{318}) {
		t.Fatalf("capped-width candidate = %#v, want Generate=true and deduped ExistingWidths [318]", candidate)
	}

	generator := &p6CappedWidthGenerator{}
	task := tasks.NewTrickplayTask(source, generator, nil, options)
	if err := task.Execute(t.Context()); err != nil {
		t.Fatalf("execute capped-width task: %v", err)
	}
	if len(generator.generations) != 0 {
		t.Fatalf("task regenerated persisted effective width: %#v", generator.generations)
	}
}

type p6CappedWidthGenerator struct{ generations []tasks.Generation }

func (generator *p6CappedWidthGenerator) Generate(_ context.Context, generation tasks.Generation) error {
	generator.generations = append(generator.generations, generation)
	return nil
}
