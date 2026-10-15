// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestFreshLibraryScanGeneratesTrickplay covers trickplay generation on a
// fresh library. A fresh install with no imported data scans generated
// videos. The scan must generate trickplay for a library with
// ExtractTrickplayImagesDuringLibraryScan (TrickplayProvider), and only the scheduled "Generate Trickplay Images" task
// covers a library that only enables extraction (TrickplayImagesTask).
//
// libvips starts once per process and cannot restart after shutdown, and
// other tests in this package own it. The test therefore runs its body in a
// child process of the test binary.
func TestFreshLibraryScanGeneratesTrickplay(t *testing.T) {
	const childEnvironment = "DEADFIN_TEST_FRESH_TRICKPLAY_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFreshLibraryScanGeneratesTrickplay$", "-test.v", "-test.count=1")
		command.Env = append(os.Environ(), childEnvironment+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh-library trickplay child: %v\n%s", err, output)
		}
		if strings.Contains(string(output), "--- SKIP: TestFreshLibraryScanGeneratesTrickplay") {
			t.Skipf("fresh-library trickplay child skipped:\n%s", output)
		}
		return
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("fresh-library trickplay requires ffmpeg: %v", err)
	}
	programData := filepath.Join(t.TempDir(), "program-data")
	paths, err := config.NewPaths(programData)
	if err != nil {
		t.Fatalf("resolve program-data paths: %v", err)
	}
	runtimeOwner, err := images.OpenRuntime(t.Context())
	if err != nil {
		t.Fatalf("open image runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := runtimeOwner.Close(); err != nil {
			t.Errorf("close image runtime: %v", err)
		}
	})
	composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{Root: paths.Trickplay, MaxConcurrent: 1})
	if err != nil {
		t.Fatalf("open tile composer: %v", err)
	}
	t.Cleanup(func() {
		if err := composer.Close(); err != nil {
			t.Errorf("close tile composer: %v", err)
		}
	})

	scanRoot, scheduledRoot := t.TempDir(), t.TempDir()
	scanVideo := writeTrickplayFixtureVideo(t, ffmpegPath, scanRoot, "Scan Clip (2024)")
	scheduledVideo := writeTrickplayFixtureVideo(t, ffmpegPath, scheduledRoot, "Scheduled Clip (2024)")

	app := newCompositionApplication(t, programData, withTileComposer(composer))
	if info, ok := app.scheduler.Get(tasks.RefreshTrickplayImagesID); !ok || info.Key != "RefreshTrickplayImages" {
		t.Fatalf("scheduled task %s = %+v, %v; want the registered Generate Trickplay Images task", tasks.RefreshTrickplayImagesID, info, ok)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Scan Movies", "movies", library.LibraryOptions{
		EnableTrickplayImageExtraction: true, ExtractTrickplayImagesDuringLibraryScan: true,
		PathInfos: []library.MediaPathInfo{{Path: scanRoot}},
	}); err != nil {
		t.Fatalf("add scan-time trickplay library: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Scheduled Movies", "movies", library.LibraryOptions{
		EnableTrickplayImageExtraction: true,
		PathInfos:                      []library.MediaPathInfo{{Path: scheduledRoot}},
	}); err != nil {
		t.Fatalf("add scheduled-only trickplay library: %v", err)
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan fresh libraries: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan errors = %v", result.Errors)
	}
	if err := app.trickplayRunner.Wait(t.Context()); err != nil {
		t.Fatalf("wait for scan trickplay: %v", err)
	}

	items := store.NewItemStore(app.stores)
	scanItem := applicationScannedItem(t, items, scanVideo, item.KindMovie)
	scheduledItem := applicationScannedItem(t, items, scheduledVideo, item.KindMovie)
	assertFreshTrickplay(t, items, paths.Trickplay, scanItem, true)
	assertFreshTrickplay(t, items, paths.Trickplay, scheduledItem, false)

	if err := app.trickplayTask.Run(t.Context(), func(float64) {}); err != nil {
		t.Fatalf("run Generate Trickplay Images: %v", err)
	}
	assertFreshTrickplay(t, items, paths.Trickplay, scheduledItem, true)

	// A second scan of unchanged files does no trickplay work.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("rescan: %v", err)
	} else if result.Inserted+result.Updated != 0 {
		t.Fatalf("rescan of unchanged files inserted %d and updated %d items, want none", result.Inserted, result.Updated)
	}
}

func writeTrickplayFixtureVideo(t *testing.T, ffmpegPath, root, name string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	path := filepath.Join(directory, name+".mkv")
	output, err := exec.CommandContext(t.Context(), ffmpegPath, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=10", "-t", "25",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot synthesize an H.264 fixture: %v: %s", err, output)
	}
	return path
}

func assertFreshTrickplay(t *testing.T, items *store.ItemStore, root string, value item.Item, want bool) {
	t.Helper()
	infos, err := items.Trickplays(t.Context(), value.ID)
	if err != nil {
		t.Fatalf("read trickplay rows for %s: %v", *value.Path, err)
	}
	if !want {
		if len(infos) != 0 {
			t.Fatalf("trickplay rows for %s = %+v, want none", *value.Path, infos)
		}
		return
	}
	if len(infos) != 1 {
		t.Fatalf("trickplay rows for %s = %+v, want one 320-wide rendition", *value.Path, infos)
	}
	info := infos[0]
	if info.Width != 320 || info.ThumbnailCount != 3 || info.IntervalMS != 10_000 {
		t.Fatalf("trickplay row for %s = %+v, want 320 wide with three thumbnails at 10000 ms", *value.Path, info)
	}
	dashed := value.ID.Dashed()
	directory := filepath.Join(root, dashed[:2], dashed, fmt.Sprintf("%d - %dx%d", info.Width, info.TileWidth, info.TileHeight))
	if _, err := os.Stat(filepath.Join(directory, "0.jpg")); err != nil {
		t.Fatalf("trickplay tile for %s: %v", *value.Path, err)
	}
}
