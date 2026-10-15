// SPDX-License-Identifier: GPL-2.0-only

package trickplay_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/trickplay"
	"golang.org/x/sys/unix"
)

func TestGeneratorPassesSamePinnedInodeToExtractor(t *testing.T) {
	root := t.TempDir()
	path := writePinnedGeneratorSource(t, root, "source.mkv", "original media")
	source := openPinnedGeneratorSource(t, path)
	device, inode := source.Identity()
	extractor := &sourceRevisionExtractor{
		delegate: &generatorExtractor{frameCount: 1, height: 180},
		assertSource: func(file *os.File) error {
			var stat unix.Stat_t
			if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
				return fmt.Errorf("fstat extractor source: %w", err)
			}
			if gotDevice, gotInode := uint64(stat.Dev), uint64(stat.Ino); gotDevice != device || gotInode != inode {
				return fmt.Errorf("extractor source identity = %d:%d, want pinned %d:%d", gotDevice, gotInode, device, inode)
			}
			return nil
		},
	}
	store := &generatorStore{}
	generator := newGenerator(t, root, extractor, &generatorComposer{byteSizes: []int64{100}}, store)

	if _, err := generator.Generate(t.Context(), pinnedGeneratorRequest(t, generatorUUID(t, "8c7a0b6e5d4c3b2a1908070605040302"), source)); err != nil {
		t.Fatalf("Generate(): %v", err)
	}
	if got, want := extractor.delegate.recordedCalls(), 1; got != want {
		t.Fatalf("extractor calls = %d, want %d", got, want)
	}
	if got, want := len(store.recorded()), 1; got != want {
		t.Fatalf("store writes = %d, want %d", got, want)
	}
}

func TestGeneratorRejectsInPlaceSourceMutationWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	path := writePinnedGeneratorSource(t, root, "source.mkv", "original media")
	source := openPinnedGeneratorSource(t, path)
	itemID := generatorUUID(t, "3a7b0b6e5d4c3b2a1908070605040302")
	extractor := &sourceRevisionExtractor{
		delegate: &generatorExtractor{frameCount: 1, height: 180},
		mutate: func() error {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			_, writeErr := file.WriteString(" mutation")
			return errors.Join(writeErr, file.Close())
		},
	}
	store := &generatorStore{}
	generator := newGenerator(t, root, extractor, &generatorComposer{byteSizes: []int64{100}}, store)

	_, err := generator.Generate(t.Context(), pinnedGeneratorRequest(t, itemID, source))
	assertStaleSourceRollback(t, err, root, itemID, store)
}

func TestGeneratorRejectsSourcePathRenameWithoutPersistingOrLeavingJournal(t *testing.T) {
	root := t.TempDir()
	path := writePinnedGeneratorSource(t, root, "source.mkv", "original media")
	source := openPinnedGeneratorSource(t, path)
	itemID := generatorUUID(t, "4a7b0b6e5d4c3b2a1908070605040302")
	extractor := &sourceRevisionExtractor{
		delegate: &generatorExtractor{frameCount: 1, height: 180},
		mutate: func() error {
			replacement := filepath.Join(root, "replacement.mkv")
			if err := os.WriteFile(replacement, []byte("replacement media"), 0o600); err != nil {
				return err
			}
			return os.Rename(replacement, path)
		},
	}
	store := &generatorStore{}
	generator := newGenerator(t, root, extractor, &generatorComposer{byteSizes: []int64{100}}, store)

	_, err := generator.Generate(t.Context(), pinnedGeneratorRequest(t, itemID, source))
	assertStaleSourceRollback(t, err, root, itemID, store)
}

func assertStaleSourceRollback(t *testing.T, err error, root string, itemID dto.UUID, store *generatorStore) {
	t.Helper()
	if !errors.Is(err, trickplay.ErrStaleSourceRevision) {
		t.Fatalf("Generate() error = %v, want ErrStaleSourceRevision", err)
	}
	if got := store.recorded(); len(got) != 0 {
		t.Fatalf("store writes = %#v, want none after stale source", got)
	}
	resolution := generatorResolutionDirectory(root, itemID, 318, 3, 2)
	if _, statErr := os.Stat(resolution); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("published resolution stat = %v, want no published result at %q", statErr, resolution)
	}
	matches, globErr := filepath.Glob(filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), "*.journal"))
	if globErr != nil {
		t.Fatalf("Glob(publication journals): %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("publication journals = %q, want none after rollback", matches)
	}
}

type sourceRevisionExtractor struct {
	delegate     *generatorExtractor
	assertSource func(*os.File) error
	mutate       func() error
}

func (extractor *sourceRevisionExtractor) Extract(ctx context.Context, request trickplay.FrameRequest) (trickplay.FrameExtraction, error) {
	if request.Source == nil {
		return trickplay.FrameExtraction{}, errors.New("generator did not pass a pinned source descriptor")
	}
	if extractor.assertSource != nil {
		if err := extractor.assertSource(request.Source); err != nil {
			return trickplay.FrameExtraction{}, err
		}
	}
	if extractor.mutate != nil {
		if err := extractor.mutate(); err != nil {
			return trickplay.FrameExtraction{}, fmt.Errorf("mutate source fixture: %w", err)
		}
	}
	return extractor.delegate.Extract(ctx, request)
}

func pinnedGeneratorRequest(t *testing.T, itemID dto.UUID, source *sourcefile.Source) trickplay.GenerateRequest {
	t.Helper()
	if source == nil {
		t.Fatal("pinned generator source is nil")
	}
	return trickplay.GenerateRequest{
		ItemID: itemID, Source: source, SourceRevision: "test-source-revision", VideoWidth: generatorInt(319),
		RunTimeTicks: generatorInt64(30_000_000), RequestedWidth: 321,
		TileWidth: 3, TileHeight: 2, IntervalMilliseconds: 1_500,
	}
}

func writePinnedGeneratorSource(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(source fixture): %v", err)
	}
	return path
}

func openPinnedGeneratorSource(t *testing.T, path string) *sourcefile.Source {
	t.Helper()
	source, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("Open(source fixture): %v", err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("Close(source fixture): %v", err)
		}
	})
	return source
}
