// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerDiscoversSidecarForSelfContainedMovieDirectory pins the resolver
// boundary where a movie directory resolves to its direct video child. The
// scanner must use that directory's frozen listing for the video sidecars,
// rather than the parent source listing.
func TestScannerDiscoversSidecarForSelfContainedMovieDirectory(t *testing.T) {
	fixture := newScannerSidecarFixture(t)

	scannerScan(t, fixture.harness.scanner, "scan self-contained movie with sidecar")
	movie := scannerItemAtPath(t, fixture.harness.items, fixture.media)
	assertScannerNoItemAtPath(t, fixture.harness.items, fixture.directory)
	assertScannerSidecarStreams(t, fixture.harness.items, movie, fixture.sidecar, true)
	if calls := fixture.probe.Calls(); len(calls) != 1 || calls[0] != fixture.media {
		t.Fatalf("probe calls = %q, want one call for %q", calls, fixture.media)
	}
}

// TestScannerRemovesDeletedSidecarForUnchangedSelfContainedMovie verifies that
// sidecar removal reprojects the persisted stream satellites even when the
// video entry's size and modification time are unchanged. The fake directory
// listing changes atomically between scans, without a timing dependency.
func TestScannerRemovesDeletedSidecarForUnchangedSelfContainedMovie(t *testing.T) {
	fixture := newScannerSidecarFixture(t)

	scannerScan(t, fixture.harness.scanner, "seed self-contained movie sidecar")
	movie := scannerItemAtPath(t, fixture.harness.items, fixture.media)
	assertScannerSidecarStreams(t, fixture.harness.items, movie, fixture.sidecar, true)

	fixture.filesystem.Remove(fixture.sidecar)
	for cycle := 1; cycle <= 3; cycle++ {
		scannerScan(t, fixture.harness.scanner, "rescan unchanged video after sidecar deletion")
		movie = scannerItemAtPath(t, fixture.harness.items, fixture.media)
		assertScannerSidecarStreams(t, fixture.harness.items, movie, fixture.sidecar, false)
	}
}

type scannerSidecarFixture struct {
	harness    *scannerHarness
	filesystem *scannerSidecarFilesystem
	probe      *scannerSidecarProbe
	directory  string
	media      string
	sidecar    string
}

func newScannerSidecarFixture(t *testing.T) scannerSidecarFixture {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}

	directory := scannerPath(t, harness.source, "Standalone Movie (2023)")
	media := scannerPath(t, directory, "Standalone Movie (2023).mkv")
	sidecar := scannerPath(t, directory, "Standalone Movie (2023).en.srt")
	modified := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	filesystem := newScannerSidecarFilesystem(map[string][]library.Entry{
		harness.source: {
			scannerSidecarEntry(directory, true, modified),
		},
		directory: {
			scannerSidecarEntry(media, false, modified),
			scannerSidecarEntry(scannerPath(t, directory, "movie.nfo"), false, modified),
			scannerSidecarEntry(sidecar, false, modified),
		},
	})
	probe := &scannerSidecarProbe{}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     filesystem.ReadDir,
		MediaProbe:  probe,
		Now:         func() time.Time { return modified },
	})
	return scannerSidecarFixture{harness: harness, filesystem: filesystem, probe: probe, directory: directory, media: media, sidecar: sidecar}
}

func assertScannerSidecarStreams(t *testing.T, items *store.ItemStore, movie item.Item, sidecar string, wantSidecar bool) {
	t.Helper()
	persisted, err := items.Streams(t.Context(), movie.ID)
	if err != nil {
		t.Fatalf("load persisted streams: %v", err)
	}
	projected := scannerSidecarDTO(t, items, movie)
	if wantSidecar {
		if len(persisted) != 2 {
			t.Fatalf("persisted streams = %#v, want external subtitle and video", persisted)
		}
		assertScannerExternalSubtitle(t, persisted[0], sidecar, 0)
		assertScannerVideoStream(t, persisted[1], 1)
		if len(projected.MediaStreams) != 2 {
			t.Fatalf("projected streams = %#v, want external subtitle and video", projected.MediaStreams)
		}
		assertScannerProjectedExternalSubtitle(t, projected.MediaStreams[0], sidecar, 0)
		assertScannerProjectedVideoStream(t, projected.MediaStreams[1], 1)
		return
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted streams after sidecar deletion = %#v, want only video", persisted)
	}
	assertScannerVideoStream(t, persisted[0], 0)
	if len(projected.MediaStreams) != 1 {
		t.Fatalf("projected streams after sidecar deletion = %#v, want only video", projected.MediaStreams)
	}
	assertScannerProjectedVideoStream(t, projected.MediaStreams[0], 0)
}

func scannerSidecarDTO(t *testing.T, items *store.ItemStore, movie item.Item) dto.BaseItemDto {
	t.Helper()
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{movie}, store.ItemQueryDTOOptions{Fields: []string{"MediaStreams"}}, nil)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("build movie DTO = %#v, %v", result, err)
	}
	return result.Items[0]
}

func assertScannerExternalSubtitle(t *testing.T, stream playback.MediaStream, path string, index int) {
	t.Helper()
	if stream.Type != playback.StreamTypeSubtitle || stream.Codec != "subrip" || stream.Language == nil || *stream.Language != "eng" || !stream.IsExternal || !stream.SupportsExternalStream || stream.Path != path || stream.Index != index {
		t.Fatalf("persisted external subtitle = %#v, want English sidecar %q at index %d", stream, path, index)
	}
}

func assertScannerVideoStream(t *testing.T, stream playback.MediaStream, index int) {
	t.Helper()
	if stream.Type != playback.StreamTypeVideo || stream.Codec != "h264" || stream.IsExternal || stream.Index != index {
		t.Fatalf("persisted video stream = %#v, want internal h264 at index %d", stream, index)
	}
}

func assertScannerProjectedExternalSubtitle(t *testing.T, stream dto.MediaStreamInfo, path string, index int) {
	t.Helper()
	if string(stream.Type) != "Subtitle" || stream.Codec == nil || *stream.Codec != "subrip" || stream.Language == nil || *stream.Language != "eng" || stream.IsExternal == nil || !*stream.IsExternal || stream.SupportsExternalStream == nil || !*stream.SupportsExternalStream || stream.Path == nil || *stream.Path != path || stream.Index != index {
		t.Fatalf("projected external subtitle = %#v, want English sidecar %q at index %d", stream, path, index)
	}
}

func assertScannerProjectedVideoStream(t *testing.T, stream dto.MediaStreamInfo, index int) {
	t.Helper()
	if string(stream.Type) != "Video" || stream.Codec == nil || *stream.Codec != "h264" || stream.IsExternal == nil || *stream.IsExternal || stream.Index != index {
		t.Fatalf("projected video stream = %#v, want internal h264 at index %d", stream, index)
	}
}

type scannerSidecarFilesystem struct {
	mu          sync.Mutex
	directories map[string][]library.Entry
}

func newScannerSidecarFilesystem(directories map[string][]library.Entry) *scannerSidecarFilesystem {
	copied := make(map[string][]library.Entry, len(directories))
	for path, entries := range directories {
		copied[filepath.Clean(path)] = append([]library.Entry(nil), entries...)
	}
	return &scannerSidecarFilesystem{directories: copied}
}

func (filesystem *scannerSidecarFilesystem) ReadDir(path string) ([]library.Entry, error) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	entries, found := filesystem.directories[filepath.Clean(path)]
	if !found {
		return nil, fs.ErrNotExist
	}
	return append([]library.Entry(nil), entries...), nil
}

func (filesystem *scannerSidecarFilesystem) Remove(path string) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	parent := filepath.Clean(filepath.Dir(path))
	entries := filesystem.directories[parent]
	kept := entries[:0]
	for _, entry := range entries {
		if filepath.Clean(entry.Path) != filepath.Clean(path) {
			kept = append(kept, entry)
		}
	}
	filesystem.directories[parent] = kept
}

func scannerSidecarEntry(path string, isDir bool, modified time.Time) library.Entry {
	return library.Entry{Path: path, IsDir: isDir, Size: 1024, Modified: modified}
}

type scannerSidecarProbe struct {
	mu    sync.Mutex
	calls []string
}

func (probe *scannerSidecarProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	probe.mu.Lock()
	probe.calls = append(probe.calls, input.Entry.Path)
	probe.mu.Unlock()
	size := input.Entry.Size
	return library.MediaProbeResult{
		Container: "mkv",
		Size:      &size,
		Streams: []playback.MediaStream{{
			Codec: "h264",
			Index: 0,
			Type:  playback.StreamTypeVideo,
		}},
	}, nil
}

func (probe *scannerSidecarProbe) Calls() []string {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]string(nil), probe.calls...)
}
