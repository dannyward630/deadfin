// SPDX-License-Identifier: GPL-2.0-only

package trickplay_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/trickplay"
	"golang.org/x/sys/unix"
)

// TestGenerator runs the orchestration boundary with real filesystem effects and
// injected external adapters. The fakes model only ffmpeg, image composition,
// and durable metadata; Generator remains responsible for plan derivation,
// owned-root publication, and transaction ordering.
func TestGenerator(t *testing.T) {
	itemID := generatorUUID(t, "00112233445566778899aabbccddeeff")

	t.Run("extracts at the exact interval, caps to an even source width, batches tiles, publishes deterministic names, and persists measured bandwidth", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{frameCount: 7, height: 180}
		composer := &generatorComposer{byteSizes: []int64{1_000, 1_001}}
		resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
		store := &generatorStore{beforeReplace: func(metadata trickplay.Metadata) error {
			if got, want := generatorDirectoryNames(t, resolution), []string{".deadfin-trickplay-generation", "0.jpg", "1.jpg"}; !reflect.DeepEqual(got, want) {
				return fmt.Errorf("metadata persistence began before the published tree retained its recovery identity: entries = %#v, want %#v", got, want)
			}
			manifest, err := os.ReadFile(filepath.Join(resolution, ".deadfin-trickplay-generation"))
			if err != nil {
				return fmt.Errorf("read published recovery identity during metadata persistence: %w", err)
			}
			if got, want := strings.TrimSpace(string(manifest)), metadata.GenerationToken; got != want {
				return fmt.Errorf("published recovery identity during metadata persistence = %q, want metadata generation token %q", got, want)
			}
			return nil
		}}
		generator := newGenerator(t, root, extractor, composer, store)

		request := generatorRequest(t, itemID)
		device, inode := request.Source.Identity()
		metadata, err := generator.Generate(t.Context(), request)
		if err != nil {
			t.Fatalf("Generate(): %v", err)
		}

		if got, want := extractor.recordedCalls(), 1; got != want {
			t.Fatalf("frame extraction calls = %d, want %d", got, want)
		}
		extraction := extractor.recordedRequest(t, 0)
		if extraction.Source == nil {
			t.Fatal("frame extraction source = nil, want pinned descriptor")
		}
		if gotDevice, gotInode := generatorFileIdentity(t, extraction.Source); gotDevice != device || gotInode != inode {
			t.Fatalf("frame extraction source identity = %d:%d, want pinned %d:%d", gotDevice, gotInode, device, inode)
		}
		if got, want := extraction.Width, 318; got != want {
			t.Fatalf("frame extraction width = %d, want source-capped even width %d", got, want)
		}
		if got, want := extraction.IntervalMilliseconds, int64(1_500); got != want {
			t.Fatalf("frame extraction interval = %dms, want %dms", got, want)
		}
		physicalRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatalf("EvalSymlinks(owned root %q): %v", root, err)
		}
		if relative, err := filepath.Rel(physicalRoot, extraction.Output.Path()); err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("frame extraction directory = %q, want a private directory below canonical owned root %q", extraction.Output.Path(), physicalRoot)
		}

		requests := composer.recordedRequests()
		if got, want := len(requests), 2; got != want {
			t.Fatalf("tile composition calls = %d, want %d", got, want)
		}
		for index, request := range requests {
			if got, want := request.CellWidth, 318; got != want {
				t.Fatalf("tile %d cell width = %d, want %d", index, got, want)
			}
			if got, want := request.CellHeight, 180; got != want {
				t.Fatalf("tile %d cell height = %d, want %d", index, got, want)
			}
			if got, want := request.Columns, 3; got != want {
				t.Fatalf("tile %d columns = %d, want %d", index, got, want)
			}
			if got, want := request.Rows, 2; got != want {
				t.Fatalf("tile %d rows = %d, want %d", index, got, want)
			}
		}
		if got, want := len(requests[0].SourceNames), 6; got != want {
			t.Fatalf("first tile source count = %d, want full 3x2 capacity %d", got, want)
		}
		if got, want := len(requests[1].SourceNames), 1; got != want {
			t.Fatalf("second tile source count = %d, want partial final tile count %d", got, want)
		}
		if got, want := requests[0].SourceNames[0], "00000001.jpg"; got != want {
			t.Fatalf("first tile first source = %q, want first extracted frame %q", got, want)
		}
		if got, want := requests[1].SourceNames[0], "00000007.jpg"; got != want {
			t.Fatalf("partial tile source = %q, want seventh extracted frame %q", got, want)
		}

		wantMetadata := trickplay.Metadata{
			ItemID: itemID, Width: 318, Height: 180, TileWidth: 3, TileHeight: 2,
			ThumbnailCount: 7, Interval: 1_500, Bandwidth: 890,
			SourceRevision: "test-source-revision",
		}
		if metadata.GenerationToken == "" {
			t.Fatal("Generate() generation token = empty, want durable publication identity")
		}
		wantMetadata.GenerationToken = metadata.GenerationToken
		if metadata != wantMetadata {
			t.Fatalf("Generate() metadata = %#v, want %#v", metadata, wantMetadata)
		}
		if calls := store.recorded(); !reflect.DeepEqual(calls, []trickplay.Metadata{wantMetadata}) {
			t.Fatalf("metadata store calls = %#v, want one completed generation %#v", calls, wantMetadata)
		}

		resolution = generatorResolutionDirectory(root, itemID, 318, 3, 2)
		if got, want := generatorDirectoryNames(t, resolution), []string{"0.jpg", "1.jpg"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("finalized tile names = %#v, want deterministic zero-based names with recovery identity removed %#v", got, want)
		}
		for _, name := range []string{"0.jpg", "1.jpg"} {
			if info, err := os.Stat(filepath.Join(resolution, name)); err != nil || info.Size() == 0 {
				t.Fatalf("published tile %q stat = (%#v, %v), want nonempty file", name, info, err)
			}
		}
		if _, err := os.Stat(extraction.Output.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("extraction scratch directory stat = %v, want cleanup after publication", err)
		}
	})

	t.Run("does not extract or publish when runtime is shorter than one interval", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{frameCount: 1, height: 180}
		composer := &generatorComposer{byteSizes: []int64{100}}
		store := &generatorStore{}
		generator := newGenerator(t, root, extractor, composer, store)
		request := generatorRequest(t, itemID)
		request.RunTimeTicks = generatorInt64(14_999_999)

		if _, err := generator.Generate(t.Context(), request); err == nil {
			t.Fatal("Generate(runtime shorter than one interval) error = nil, want ineligible generation rejection")
		}
		if got := extractor.recordedCalls(); got != 0 {
			t.Fatalf("ineligible generation extraction calls = %d, want none", got)
		}
		if got := composer.recordedRequests(); len(got) != 0 {
			t.Fatalf("ineligible generation composition calls = %#v, want none", got)
		}
		if got := store.recorded(); len(got) != 0 {
			t.Fatalf("ineligible generation metadata store calls = %#v, want none", got)
		}
	})

	t.Run("serializes complete generations through one semaphore", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{
			frameCount: 1, height: 180,
			started: make(chan struct{}, 2),
			release: make(chan struct{}),
		}
		generator := newGenerator(t, root, extractor, &generatorComposer{byteSizes: []int64{100}}, &generatorStore{})
		first := make(chan error, 1)
		second := make(chan error, 1)
		go func() { _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); first <- err }()
		<-extractor.started
		secondInvoking := make(chan struct{})
		go func() {
			close(secondInvoking)
			_, err := generator.Generate(t.Context(), generatorRequest(t, itemID))
			second <- err
		}()
		<-secondInvoking

		for range 1_000 {
			runtime.Gosched()
		}
		select {
		case <-extractor.started:
			t.Fatal("second extraction began while the first generation still owned the semaphore")
		default:
		}
		close(extractor.release)
		if err := <-first; err != nil {
			t.Fatalf("first Generate(): %v", err)
		}
		if err := <-second; err != nil {
			t.Fatalf("second Generate(): %v", err)
		}
		if got, want := extractor.recordedCalls(), 2; got != want {
			t.Fatalf("extraction calls after releases = %d, want %d", got, want)
		}
	})

	t.Run("does not commit metadata or replace prior artifacts when composition fails", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{frameCount: 7, height: 180}
		composer := &generatorComposer{byteSizes: []int64{1_000, 1_001}, failAt: 2}
		store := &generatorStore{}
		generator := newGenerator(t, root, extractor, composer, store)

		if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err == nil {
			t.Fatal("Generate(composition failure) error = nil, want propagated failure")
		}
		if got := store.recorded(); len(got) != 0 {
			t.Fatalf("metadata store calls after composition failure = %#v, want none", got)
		}
		resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
		if _, err := os.Stat(resolution); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed generation published resolution directory: stat error = %v, want absent", err)
		}
		request := extractor.recordedRequest(t, 0)
		if _, err := os.Stat(request.Output.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed generation extraction scratch stat = %v, want cleanup", err)
		}
		if got, want := generatorDirectoryNames(t, root), []string{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("failed generation root entries = %#v, want cleaned scratch artifacts %#v", got, want)
		}
	})

	t.Run("cancellation cleans scratch work and leaves durable state untouched", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{frameCount: 1, height: 180, started: make(chan struct{}, 1), awaitCancellation: true}
		composer := &generatorComposer{byteSizes: []int64{100}}
		store := &generatorStore{}
		generator := newGenerator(t, root, extractor, composer, store)
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { _, err := generator.Generate(ctx, generatorRequest(t, itemID)); result <- err }()
		<-extractor.started
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate(canceled context) error = %v, want context.Canceled", err)
		}
		if got := store.recorded(); len(got) != 0 {
			t.Fatalf("metadata store calls after cancellation = %#v, want none", got)
		}
		request := extractor.recordedRequest(t, 0)
		if _, err := os.Stat(request.Output.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled generation extraction scratch stat = %v, want cleanup", err)
		}
		if _, err := os.Stat(generatorResolutionDirectory(root, itemID, 318, 3, 2)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled generation published a resolution: stat error = %v, want absent", err)
		}
		if got, want := generatorDirectoryNames(t, root), []string{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("canceled generation root entries = %#v, want cleaned scratch artifacts %#v", got, want)
		}
	})

	t.Run("atomically replaces an existing resolution on idempotent regeneration", func(t *testing.T) {
		root := t.TempDir()
		extractor := &generatorExtractor{frameCount: 7, height: 180}
		composer := &generatorComposer{byteSizes: []int64{1_000, 1_001}, marker: "first"}
		store := &generatorStore{}
		generator := newGenerator(t, root, extractor, composer, store)
		if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err != nil {
			t.Fatalf("first Generate(): %v", err)
		}

		extractor.setFrameCount(1)
		composer.reset([]int64{777}, "second")
		if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err != nil {
			t.Fatalf("second Generate(): %v", err)
		}
		resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
		if got, want := generatorDirectoryNames(t, resolution), []string{"0.jpg"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("regenerated resolution entries = %#v, want replacement without stale tile %#v", got, want)
		}
		bytes, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
		if err != nil {
			t.Fatalf("ReadFile(regenerated tile): %v", err)
		}
		if !strings.Contains(string(bytes), "second") {
			t.Fatalf("regenerated tile bytes = %q, want second generation artifact", bytes)
		}
		if got := store.recorded(); len(got) != 2 {
			t.Fatalf("metadata store calls after idempotent regeneration = %d, want two committed generations", len(got))
		}
	})

	t.Run("rejects a symlinked owned-root shard without writing outside the root", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, itemID.Dashed()[:2])); err != nil {
			t.Fatalf("Symlink(owned root shard): %v", err)
		}
		extractor := &generatorExtractor{frameCount: 1, height: 180}
		composer := &generatorComposer{byteSizes: []int64{100}}
		store := &generatorStore{}
		generator := newGenerator(t, root, extractor, composer, store)

		if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err == nil {
			t.Fatal("Generate(symlinked shard) error = nil, want owned-root escape rejection")
		}
		if got, want := generatorDirectoryNames(t, outside), []string{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("outside directory entries = %#v, want no escaped artifacts %#v", got, want)
		}
		if got := extractor.recordedCalls(); got != 0 {
			t.Fatalf("symlinked-root extraction calls = %d, want root validation before external work", got)
		}
		if got := store.recorded(); len(got) != 0 {
			t.Fatalf("metadata store calls after symlink escape = %#v, want none", got)
		}
	})
}

func TestGeneratorPreservesSubsecondEligibilityBeforeClampingGeneratedInterval(t *testing.T) {
	itemID := generatorUUID(t, "112233445566778899aabbccddeeff00")
	for _, test := range []struct {
		name     string
		interval int64
	}{
		{name: "one millisecond runtime remains eligible", interval: 1},
		{name: "999 millisecond runtime remains eligible", interval: 999},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			extractor := &generatorExtractor{frameCount: 1, height: 180}
			generator := newGenerator(t, root, extractor, &generatorComposer{byteSizes: []int64{100}}, &generatorStore{})
			request := generatorRequest(t, itemID)
			request.IntervalMilliseconds = test.interval
			request.RunTimeTicks = generatorInt64(test.interval * 10_000)

			metadata, err := generator.Generate(t.Context(), request)
			if err != nil {
				t.Fatalf("Generate(interval=%dms, runtime=%dms): %v", test.interval, test.interval, err)
			}
			if got, want := extractor.recordedRequest(t, 0).IntervalMilliseconds, int64(1_000); got != want {
				t.Fatalf("extraction interval after eligible %dms request = %dms, want clamped %dms", test.interval, got, want)
			}
			if got, want := metadata.Interval, int64(1_000); got != want {
				t.Fatalf("published metadata interval after eligible %dms request = %dms, want planned %dms", test.interval, got, want)
			}
		})
	}
}

func TestGeneratorRollsBackPublishedArtifactsWhenMetadataCannotCommit(t *testing.T) {
	itemID := generatorUUID(t, "2233445566778899aabbccddeeff0011")
	for _, test := range []struct {
		name          string
		prior         bool
		cancelContext bool
	}{
		{name: "store rejection removes an initially published orphan"},
		{name: "store cancellation removes an initially published orphan", cancelContext: true},
		{name: "store rejection restores the complete prior resolution", prior: true},
		{name: "store cancellation restores the complete prior resolution", prior: true, cancelContext: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			extractor := &generatorExtractor{frameCount: 7, height: 180}
			composer := &generatorComposer{byteSizes: []int64{1_000, 1_001}, marker: "prior"}
			store := &generatorStore{}
			generator := newGenerator(t, root, extractor, composer, store)
			resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
			parent := filepath.Dir(resolution)

			if test.prior {
				if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err != nil {
					t.Fatalf("Generate(prior resolution): %v", err)
				}
				extractor.setFrameCount(1)
				composer.reset([]int64{777}, "replacement")
			} else {
				composer.reset([]int64{777}, "replacement")
			}
			priorTree := generatorArtifactTree(t, resolution)
			generationContext := t.Context()
			cancel := func() {}
			if test.cancelContext {
				generationContext, cancel = context.WithCancel(t.Context())
			}
			t.Cleanup(cancel)
			store.beforeReplace = func(trickplay.Metadata) error {
				if got, want := generatorArtifactTree(t, resolution)["0.jpg"], "tile-00-replacement"; got != want {
					return fmt.Errorf("metadata commit began before replacement publication: 0.jpg = %q, want %q", got, want)
				}
				if test.cancelContext {
					cancel()
					return context.Canceled
				}
				return errors.New("synthetic metadata rejection")
			}

			_, err := generator.Generate(generationContext, generatorRequest(t, itemID))
			if test.cancelContext {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Generate(metadata cancellation) error = %v, want context.Canceled", err)
				}
			} else if err == nil {
				t.Fatal("Generate(metadata rejection) error = nil, want failure")
			}
			if got := generatorArtifactTree(t, resolution); !reflect.DeepEqual(got, priorTree) {
				t.Fatalf("resolution after rejected metadata commit = %#v, want exact prior tree %#v", got, priorTree)
			}
			wantStoreCalls := 0
			if test.prior {
				wantStoreCalls = 1
			}
			if got := len(store.recorded()); got != wantStoreCalls {
				t.Fatalf("metadata store calls after failed commit = %d, want previously durable calls %d", got, wantStoreCalls)
			}
			wantParentEntries := []string{}
			if test.prior {
				wantParentEntries = []string{filepath.Base(resolution)}
			}
			if got := generatorDirectoryNames(t, parent); !reflect.DeepEqual(got, wantParentEntries) {
				t.Fatalf("resolution parent after failed metadata commit = %#v, want no published orphan or recovery debris %#v", got, wantParentEntries)
			}
		})
	}
}

func TestGeneratorRecoversInterruptedReplacementBeforeLaterFailure(t *testing.T) {
	root := t.TempDir()
	itemID := generatorUUID(t, "33445566778899aabbccddeeff001122")
	resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
	interrupted := resolution + ".previous-interrupted"
	if err := os.MkdirAll(interrupted, 0o755); err != nil {
		t.Fatalf("MkdirAll(interrupted prior resolution): %v", err)
	}
	if err := os.WriteFile(filepath.Join(interrupted, "0.jpg"), []byte("tile-00-prior"), 0o644); err != nil {
		t.Fatalf("WriteFile(interrupted prior tile): %v", err)
	}
	priorTree := generatorArtifactTree(t, interrupted)
	extractor := &generatorExtractor{frameCount: 1, height: 180}
	composer := &generatorComposer{byteSizes: []int64{777}, failAt: 1, marker: "replacement"}
	store := &generatorStore{}
	generator := newGenerator(t, root, extractor, composer, store)

	if _, err := generator.Generate(t.Context(), generatorRequest(t, itemID)); err == nil {
		t.Fatal("Generate(after interrupted replacement with composition failure) error = nil, want failure")
	}
	if got := generatorArtifactTree(t, resolution); !reflect.DeepEqual(got, priorTree) {
		t.Fatalf("canonical resolution after interrupted recovery and later failure = %#v, want preserved prior tree %#v", got, priorTree)
	}
	if got, want := generatorDirectoryNames(t, filepath.Dir(resolution)), []string{filepath.Base(resolution)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolution parent after interrupted recovery = %#v, want restored canonical without stale backup %#v", got, want)
	}
	if got := store.recorded(); len(got) != 0 {
		t.Fatalf("metadata store calls after recovery followed by composition failure = %#v, want none", got)
	}
}

func newGenerator(t *testing.T, root string, extractor trickplay.FrameExtractor, composer trickplay.TileComposer, store *generatorStore) *trickplay.Generator {
	t.Helper()
	generator, err := trickplay.NewGenerator(trickplay.GeneratorConfig{
		Root: root, Extractor: extractor, Composer: composer, Store: store, Reader: store, Publication: trickplay.NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	return generator
}

func generatorRequest(t *testing.T, itemID dto.UUID) trickplay.GenerateRequest {
	t.Helper()
	return trickplay.GenerateRequest{
		ItemID: itemID, Source: openPinnedGeneratorSource(t, writePinnedGeneratorSource(t, t.TempDir(), "source.mkv", "pinned test source")), SourceRevision: "test-source-revision", VideoWidth: generatorInt(319),
		RunTimeTicks: generatorInt64(30_000_000), RequestedWidth: 321,
		TileWidth: 3, TileHeight: 2, IntervalMilliseconds: 1_500,
	}
}

func generatorResolutionDirectory(root string, itemID dto.UUID, width, tileWidth, tileHeight int) string {
	dashed := itemID.Dashed()
	return filepath.Join(root, dashed[:2], dashed, fmt.Sprintf("%d - %dx%d", width, tileWidth, tileHeight))
}

func generatorDirectoryNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}
	}
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", directory, err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	sort.Strings(result)
	return result
}

func generatorArtifactTree(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == directory || entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		result[relative] = string(contents)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil {
		t.Fatalf("WalkDir(%q): %v", directory, err)
	}
	return result
}

func generatorUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	value, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", text, err)
	}
	return value
}

func generatorInt(value int) *int { return &value }

func generatorInt64(value int64) *int64 { return &value }
func generatorFileIdentity(t *testing.T, file *os.File) (uint64, uint64) {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatalf("Fstat(frame source): %v", err)
	}
	return uint64(stat.Dev), uint64(stat.Ino)
}

type generatorExtractor struct {
	mu                sync.Mutex
	calls             []trickplay.FrameRequest
	frameCount        int
	height            int
	started           chan struct{}
	release           chan struct{}
	awaitCancellation bool
}

func (extractor *generatorExtractor) Extract(ctx context.Context, request trickplay.FrameRequest) (trickplay.FrameExtraction, error) {
	extractor.mu.Lock()
	extractor.calls = append(extractor.calls, request)
	frameCount, height := extractor.frameCount, extractor.height
	started, release, awaitCancellation := extractor.started, extractor.release, extractor.awaitCancellation
	extractor.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if awaitCancellation {
		<-ctx.Done()
		return trickplay.FrameExtraction{}, ctx.Err()
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return trickplay.FrameExtraction{}, ctx.Err()
		}
	}
	names := make([]string, 0, frameCount)
	for index := range frameCount {
		name := fmt.Sprintf("%08d.jpg", index+1)
		file, err := request.Output.CreateExclusive(name, 0o644)
		if err != nil {
			return trickplay.FrameExtraction{}, err
		}
		if _, err := file.Write([]byte("frame")); err != nil {
			_ = file.Close()
			return trickplay.FrameExtraction{}, err
		}
		if err := file.Close(); err != nil {
			return trickplay.FrameExtraction{}, err
		}
		names = append(names, name)
	}
	return trickplay.FrameExtraction{Names: names, Height: height}, nil
}

func (extractor *generatorExtractor) recordedCalls() int {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	return len(extractor.calls)
}

func (extractor *generatorExtractor) recordedRequest(t *testing.T, index int) trickplay.FrameRequest {
	t.Helper()
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if index >= len(extractor.calls) {
		t.Fatalf("frame extraction request %d missing; calls = %d", index, len(extractor.calls))
	}
	return extractor.calls[index]
}

func (extractor *generatorExtractor) setFrameCount(value int) {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	extractor.frameCount = value
}

type generatorComposer struct {
	mu        sync.Mutex
	requests  []images.TileRequest
	byteSizes []int64
	failAt    int
	marker    string
}

func (composer *generatorComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	composer.mu.Lock()
	composer.requests = append(composer.requests, request)
	index := len(composer.requests) - 1
	failAt, marker := composer.failAt, composer.marker
	if len(composer.byteSizes) == 0 {
		composer.mu.Unlock()
		return images.TileResult{}, errors.New("synthetic composer has no byte sizes")
	}
	byteSize := composer.byteSizes[min(index, len(composer.byteSizes)-1)]
	composer.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	if failAt != 0 && index+1 == failAt {
		return images.TileResult{}, errors.New("synthetic composition failure")
	}
	contents := []byte("tile-" + generatorTwoDigit(index) + "-" + marker)
	file, err := request.OutputDir.CreateExclusive(request.OutputName, 0o644)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return images.TileResult{}, err
	}
	if err := file.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: filepath.Join(request.OutputDir.Path(), request.OutputName), ByteSize: byteSize}, nil
}

func (composer *generatorComposer) recordedRequests() []images.TileRequest {
	composer.mu.Lock()
	defer composer.mu.Unlock()
	return append([]images.TileRequest(nil), composer.requests...)
}

func (composer *generatorComposer) reset(byteSizes []int64, marker string) {
	composer.mu.Lock()
	defer composer.mu.Unlock()
	composer.requests = nil
	composer.byteSizes = byteSizes
	composer.marker = marker
}

type generatorStore struct {
	mu            sync.Mutex
	calls         []trickplay.Metadata
	beforeReplace func(trickplay.Metadata) error
}

func (store *generatorStore) ReplaceTrickplay(_ context.Context, metadata trickplay.Metadata) error {
	if store.beforeReplace != nil {
		if err := store.beforeReplace(metadata); err != nil {
			return err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls = append(store.calls, metadata)
	return nil
}

func (store *generatorStore) recorded() []trickplay.Metadata {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]trickplay.Metadata(nil), store.calls...)
}

func (store *generatorStore) ReadTrickplay(_ context.Context, itemID dto.UUID, width int) (*trickplay.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := len(store.calls) - 1; index >= 0; index-- {
		metadata := store.calls[index]
		if metadata.ItemID == itemID && metadata.Width == width {
			return &metadata, nil
		}
	}
	return nil, nil
}

func generatorTwoDigit(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
