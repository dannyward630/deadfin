// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
	"github.com/deadfin-org/deadfin/internal/trickplay"
)

// TestP6ScheduledTrickplayDiscardsBlockedGenerationAfterSourceRevisionChange
// protects the cross-boundary publication invariant: a task snapshot may
// describe source A, but it must never make source A durable after a scanner
// has atomically replaced the same item ID with source B.
func TestP6ScheduledTrickplayDiscardsBlockedGenerationAfterSourceRevisionChange(t *testing.T) {
	harness := newP6RevisionGenerationHarness(t)
	execution := harness.execute(t)

	// Extraction has crossed the scheduled snapshot and task-generator adapter,
	// but cannot continue until the scanner's source-B transaction commits.
	harness.awaitExtraction(t, execution)
	harness.reconcile(t, harness.sourceB)
	harness.extractor.releaseExtraction()
	if err := <-execution; err != nil {
		t.Fatalf("scheduled trickplay execution after stale-source discard: %v", err)
	}

	metadata, err := harness.metadata.ReadTrickplay(t.Context(), harness.itemID, 320)
	if err != nil {
		t.Errorf("read trickplay metadata after source-B reconcile: %v", err)
	} else if metadata != nil {
		t.Errorf("stale source-A generation repopulated metadata/token %#v after source-B reconcile", metadata)
	}

	resolution := harness.resolutionDirectory()
	if _, err := os.Stat(filepath.Join(resolution, "0.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale source-A generation published canonical tile after source-B reconcile: stat error = %v, want absent", err)
	}

	candidate := harness.nextCandidate(t)
	if !candidate.Generate || len(candidate.ExistingWidths) != 0 {
		t.Errorf("next scheduled candidate after source-B reconcile = %#v, want eligible without persisted resolutions", candidate)
	}
}

// TestP6ScheduledTrickplayCommitsBlockedGenerationWhenSourceRevisionIsUnchanged
// is the positive control for the stale-publication regression: merely
// blocking extraction must not discard a generation whose source revision is
// still current at commit time.
func TestP6ScheduledTrickplayCommitsBlockedGenerationWhenSourceRevisionIsUnchanged(t *testing.T) {
	harness := newP6RevisionGenerationHarness(t)
	execution := harness.execute(t)
	harness.awaitExtraction(t, execution)
	harness.extractor.releaseExtraction()
	if err := <-execution; err != nil {
		t.Fatalf("scheduled trickplay execution with unchanged source revision: %v", err)
	}

	metadata, err := harness.metadata.ReadTrickplay(t.Context(), harness.itemID, 320)
	if err != nil {
		t.Fatalf("read trickplay metadata after unchanged source generation: %v", err)
	}
	if metadata == nil || metadata.GenerationToken == "" {
		t.Fatalf("unchanged source generation metadata = %#v, want a committed token-bearing rendition", metadata)
	}

	contents, err := os.ReadFile(filepath.Join(harness.resolutionDirectory(), "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after unchanged source generation: %v", err)
	}
	if got, want := string(contents), "tile:frame:"+harness.sourceA.path; got != want {
		t.Fatalf("canonical tile after unchanged source generation = %q, want source-A artifact %q", got, want)
	}

	candidate := harness.nextCandidate(t)
	if !candidate.Generate || len(candidate.ExistingWidths) != 1 || candidate.ExistingWidths[0] != 320 {
		t.Fatalf("next scheduled candidate after unchanged source generation = %#v, want current persisted width 320", candidate)
	}
}

type p6RevisionGenerationHarness struct {
	items     *store.ItemStore
	source    trickplayTaskSource
	task      *tasks.Task
	metadata  trickplayStoreAdapter
	extractor *p6RevisionBlockingExtractor
	root      string
	libraryID dto.UUID
	itemID    dto.UUID
	sourceA   p6RevisionSource
	sourceB   p6RevisionSource
}

type p6RevisionSource struct {
	path      string
	size      int64
	modified  time.Time
	inode     int64
	changedAt time.Time
	runtime   int64
	width     int
	codec     string
	rate      float32
}

func newP6RevisionGenerationHarness(t *testing.T) *p6RevisionGenerationHarness {
	t.Helper()
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	library := p6AddTrickplayLibrary(t, app, "revision-publication", true)
	itemID := p6AdapterUUID(t, 0x66a)
	sourceAFile := p6WriteSourceFixture(t, "revision-source-A.mkv", "revision source A")
	sourceA := p6RevisionSource{
		path: sourceAFile.path, size: sourceAFile.size, modified: sourceAFile.modified, inode: sourceAFile.inode, changedAt: sourceAFile.changedAt,
		runtime: 100_000_000, width: 320, codec: "h264", rate: 24,
	}
	sourceBFile := p6WriteSourceFixture(t, "revision-source-B.mkv", "revision source B")
	sourceB := p6RevisionSource{
		path: sourceBFile.path, size: sourceBFile.size, modified: sourceBFile.modified, inode: sourceBFile.inode, changedAt: sourceBFile.changedAt,
		runtime: 120_000_000, width: 640, codec: "hevc", rate: 60,
	}
	p6ReconcileRevisionSource(t, items, library.CollectionFolderID, itemID, sourceA)

	options := tasks.Options{Widths: []int{320}, TileWidth: 1, TileHeight: 1, IntervalMilliseconds: 10_000, Quality: 73}
	extractor := &p6RevisionBlockingExtractor{sourcePath: sourceA.path, started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(extractor.releaseExtraction)
	root := filepath.Join(t.TempDir(), "trickplay")
	metadata := trickplayStoreAdapter{items: items}
	generator, err := trickplay.NewGenerator(trickplay.GeneratorConfig{
		Root: root, Extractor: extractor, Composer: p6RevisionTileComposer{}, Store: metadata, Reader: metadata, Publication: trickplay.NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("create revision-aware scheduled generator harness: %v", err)
	}
	source := trickplayTaskSource{items: items, topology: app.topology, options: options}
	return &p6RevisionGenerationHarness{
		items: items, source: source,
		task:     tasks.NewTrickplayTask(source, trickplayTaskGenerator{generator: generator}, trickplayTaskReporter{}, options),
		metadata: metadata, extractor: extractor, root: root, libraryID: library.CollectionFolderID, itemID: itemID, sourceA: sourceA, sourceB: sourceB,
	}
}

func (harness *p6RevisionGenerationHarness) execute(t *testing.T) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- harness.task.Execute(t.Context()) }()
	return result
}

func (harness *p6RevisionGenerationHarness) awaitExtraction(t *testing.T, execution <-chan error) {
	t.Helper()
	select {
	case <-harness.extractor.started:
	case err := <-execution:
		t.Fatalf("scheduled generation finished before entering blocked source-A extraction: %v", err)
	}
}

func (harness *p6RevisionGenerationHarness) reconcile(t *testing.T, source p6RevisionSource) {
	t.Helper()
	p6ReconcileRevisionSource(t, harness.items, harness.libraryID, harness.itemID, source)
}

func (harness *p6RevisionGenerationHarness) nextCandidate(t *testing.T) tasks.Candidate {
	t.Helper()
	snapshot, err := harness.source.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot next scheduled trickplay candidate: %v", err)
	}
	if got, want := snapshot.Count(), 1; got != want {
		t.Fatalf("next scheduled candidate count = %d, want %d", got, want)
	}
	page, err := snapshot.Page(t.Context(), 0, 1)
	if err != nil {
		t.Fatalf("read next scheduled candidate: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("next scheduled candidate page = %#v, want one candidate", page)
	}
	return page[0]
}

func (harness *p6RevisionGenerationHarness) resolutionDirectory() string {
	return filepath.Join(harness.root, harness.itemID.Dashed()[:2], harness.itemID.Dashed(), "320 - 1x1")
}

func p6ReconcileRevisionSource(t *testing.T, items *store.ItemStore, libraryID, itemID dto.UUID, source p6RevisionSource) {
	t.Helper()
	name, container, mediaType := "revision publication movie", "mkv", item.MediaTypeVideo
	candidate := item.Item{
		ID: itemID, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &source.path, Container: &container,
		ParentID: &libraryID, TopParentID: &libraryID, Size: &source.size, DateModified: &source.modified, FileInode: &source.inode, FileChangedAt: &source.changedAt,
		RunTimeTicks: &source.runtime, Width: &source.width, MediaType: &mediaType, SourceType: item.SourceTypeLibrary,
	}
	media := []store.ScannedChildMedia{{
		ItemID:  itemID,
		Streams: []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: source.codec, AverageFrameRate: &source.rate}},
	}}
	if _, err := items.ReconcileScannedPlan(t.Context(), libraryID, []item.Item{candidate}, media, nil); err != nil {
		t.Fatalf("reconcile source %q: %v", source.path, err)
	}
}

type p6RevisionBlockingExtractor struct {
	sourcePath  string
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (extractor *p6RevisionBlockingExtractor) Extract(ctx context.Context, request trickplay.FrameRequest) (trickplay.FrameExtraction, error) {
	if err := ctx.Err(); err != nil {
		return trickplay.FrameExtraction{}, err
	}
	if request.Source == nil || request.Source.Name() != extractor.sourcePath {
		return trickplay.FrameExtraction{}, errors.New("scheduled snapshot did not retain source-A extraction descriptor")
	}
	close(extractor.started)
	select {
	case <-extractor.release:
	case <-ctx.Done():
		return trickplay.FrameExtraction{}, ctx.Err()
	}
	frame, err := request.Output.CreateExclusive("00000001.jpg", 0o600)
	if err != nil {
		return trickplay.FrameExtraction{}, err
	}
	if _, err := frame.Write([]byte("frame:" + extractor.sourcePath)); err != nil {
		_ = frame.Close()
		return trickplay.FrameExtraction{}, err
	}
	if err := frame.Close(); err != nil {
		return trickplay.FrameExtraction{}, err
	}
	return trickplay.FrameExtraction{Names: []string{"00000001.jpg"}, Height: 180}, nil
}

type p6RevisionTileComposer struct{}

func (extractor *p6RevisionBlockingExtractor) releaseExtraction() {
	extractor.releaseOnce.Do(func() { close(extractor.release) })
}

func (p6RevisionTileComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	frame, err := request.SourceDir.ReadFile(request.SourceNames[0], 1<<20)
	if err != nil {
		return images.TileResult{}, err
	}
	contents := append([]byte("tile:"), frame...)
	output, err := request.OutputDir.CreateExclusive(request.OutputName, 0o600)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := output.Write(contents); err != nil {
		_ = output.Close()
		return images.TileResult{}, err
	}
	if err := output.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: request.OutputName, ByteSize: int64(len(contents))}, nil
}
