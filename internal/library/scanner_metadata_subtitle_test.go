// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestScannerKeepsInternalMetadataSubtitles pins MediaInfoResolver's second
// external-file directory: a subtitle in the video's internal metadata
// directory (where an upload goes when the media folder is read-only) is an
// external stream after the media-folder sidecars, and a rescan keeps it.
func TestScannerKeepsInternalMetadataSubtitles(t *testing.T) {
	fixture := newScannerSidecarFixture(t)
	metadataRoot := filepath.Join(t.TempDir(), "metadata")
	modified := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := fixture.harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	fixture.harness.scanner = newScanner(t, fixture.harness.topology, fixture.harness.items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{
		Concurrency:  1,
		ReadDir:      fixture.filesystem.ReadDir,
		MediaProbe:   fixture.probe,
		Now:          func() time.Time { return modified },
		MetadataPath: metadataRoot,
	})

	scannerScan(t, fixture.harness.scanner, "seed movie")
	movie := scannerItemAtPath(t, fixture.harness.items, fixture.media)
	assertScannerSidecarStreams(t, fixture.harness.items, movie, fixture.sidecar, true)

	metadataDir := library.InternalMetadataDir(metadataRoot, movie.ID)
	uploaded := filepath.Join(metadataDir, "Standalone Movie (2023).fre.srt")
	fixture.filesystem.mu.Lock()
	fixture.filesystem.directories[metadataDir] = []library.Entry{
		scannerSidecarEntry(uploaded, false, modified),
		scannerSidecarEntry(filepath.Join(metadataDir, "poster-1a2b3c4d.jpg"), false, modified),
	}
	fixture.filesystem.mu.Unlock()

	for cycle := 1; cycle <= 2; cycle++ {
		scannerScan(t, fixture.harness.scanner, "rescan with internal metadata subtitle")
		movie = scannerItemAtPath(t, fixture.harness.items, fixture.media)
		streams, err := fixture.harness.items.Streams(t.Context(), movie.ID)
		if err != nil {
			t.Fatalf("load streams: %v", err)
		}
		if len(streams) != 3 {
			t.Fatalf("cycle %d streams = %#v, want sidecar, metadata subtitle, video", cycle, streams)
		}
		assertScannerExternalSubtitle(t, streams[0], fixture.sidecar, 0)
		if got := streams[1]; got.Type != playback.StreamTypeSubtitle || !got.IsExternal || got.Path != uploaded || got.Index != 1 || got.Language == nil || *got.Language != "fre" {
			t.Fatalf("cycle %d metadata subtitle = %#v", cycle, got)
		}
		assertScannerVideoStream(t, streams[2], 2)
	}

	fixture.filesystem.Remove(uploaded)
	scannerScan(t, fixture.harness.scanner, "rescan after metadata subtitle delete")
	movie = scannerItemAtPath(t, fixture.harness.items, fixture.media)
	assertScannerSidecarStreams(t, fixture.harness.items, movie, fixture.sidecar, true)
}
