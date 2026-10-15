// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestFreshLibraryScanExtractsDynamicImages covers dynamic image extraction
// on a fresh library. A fresh install scans generated videos that have no
// local artwork. The Screen Grabber gives a plain video its primary image,
// and the Embedded Image Extractor takes the primary image from an MKV cover
// attachment and an MP4 cover stream. A rescan of the unchanged files extracts nothing.
func TestFreshLibraryScanExtractsDynamicImages(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("fresh-library dynamic images require ffmpeg: %v", err)
	}
	programData := filepath.Join(t.TempDir(), "program-data")
	paths, err := config.NewPaths(programData)
	if err != nil {
		t.Fatalf("resolve program-data paths: %v", err)
	}
	fixtures := t.TempDir()
	cover := filepath.Join(fixtures, "cover.jpg")
	runFixtureFFmpeg(t, ffmpegPath, "-f", "lavfi", "-i", "color=c=red:size=200x300", "-frames:v", "1", cover)
	plainVideo := filepath.Join(fixtures, "plain.mp4")
	runFixtureFFmpeg(t, ffmpegPath, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=10", "-t", "20",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", plainVideo)

	grabRoot, embeddedRoot := t.TempDir(), t.TempDir()
	grabbed := filepath.Join(grabRoot, "Grabbed Clip (2024)", "Grabbed Clip (2024).mkv")
	runFixtureFFmpeg(t, ffmpegPath, "-i", plainVideo, "-c", "copy", mkdirFor(t, grabbed))
	// The Screen Grabber library also holds a video with a cover; only the
	// Screen Grabber is enabled there, so the cover is not used.
	grabbedWithCover := filepath.Join(grabRoot, "Grabbed Cover (2024)", "Grabbed Cover (2024).mkv")
	runFixtureFFmpeg(t, ffmpegPath, "-i", plainVideo, "-c", "copy", "-attach", cover, "-metadata:s:t", "mimetype=image/jpeg", mkdirFor(t, grabbedWithCover))
	attached := filepath.Join(embeddedRoot, "Attached Cover (2024)", "Attached Cover (2024).mkv")
	runFixtureFFmpeg(t, ffmpegPath, "-i", plainVideo, "-c", "copy", "-attach", cover, "-metadata:s:t", "mimetype=image/jpeg", mkdirFor(t, attached))
	covr := filepath.Join(embeddedRoot, "Covr Cover (2024)", "Covr Cover (2024).mp4")
	runFixtureFFmpeg(t, ffmpegPath, "-i", plainVideo, "-i", cover, "-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic", mkdirFor(t, covr))

	app := newCompositionApplication(t, programData)
	// The Screen Grabber library carries the new-library TypeOptions
	// defaults; the embedded library has no TypeOptions, so the server
	// MetadataOptions enable both providers in default order.
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Grabbed", "movies", library.LibraryOptions{
		PathInfos:   []library.MediaPathInfo{{Path: grabRoot}},
		TypeOptions: []library.TypeOptions{{Type: "Movie", ImageFetchers: []string{providers.ScreenGrabberName}}},
	}); err != nil {
		t.Fatalf("add Screen Grabber library: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Embedded", "movies", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: embeddedRoot}},
	}); err != nil {
		t.Fatalf("add embedded image library: %v", err)
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan fresh libraries: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan errors = %v", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	want := []struct {
		path          string
		width, height int
	}{
		{grabbed, 640, 360},
		{grabbedWithCover, 640, 360},
		{attached, 200, 300},
		{covr, 200, 300},
	}
	before := make(map[string][]item.ImageInfo, len(want))
	for _, test := range want {
		scanned := applicationScannedItem(t, items, test.path, item.KindMovie)
		assertFreshPrimaryImage(t, paths.Metadata, scanned, test.width, test.height)
		before[test.path] = scanned.ImageInfos
	}
	files := metadataImageFiles(t, paths.Metadata)
	if len(files) != len(want) {
		t.Fatalf("metadata image files = %q, want one per video", files)
	}

	// A second scan of unchanged files extracts nothing.
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("rescan: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("rescan errors = %v", result.Errors)
	}
	for _, test := range want {
		scanned := applicationScannedItem(t, items, test.path, item.KindMovie)
		if !reflect.DeepEqual(scanned.ImageInfos, before[test.path]) {
			t.Fatalf("rescan images for %s = %+v, want %+v", test.path, scanned.ImageInfos, before[test.path])
		}
	}
	if after := metadataImageFiles(t, paths.Metadata); !reflect.DeepEqual(after, files) {
		t.Fatalf("rescan metadata image files = %q, want %q", after, files)
	}
}

func runFixtureFFmpeg(t *testing.T, ffmpegPath string, args ...string) {
	t.Helper()
	command := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)
	if output, err := exec.CommandContext(t.Context(), ffmpegPath, command...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot synthesize the fixture: %v: %s", err, output)
	}
}

func mkdirFor(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	return path
}

func assertFreshPrimaryImage(t *testing.T, metadataRoot string, value item.Item, width, height int) {
	t.Helper()
	var primary []item.ImageInfo
	for _, info := range value.ImageInfos {
		if info.Type == item.ImageTypePrimary {
			primary = append(primary, info)
		}
	}
	if len(primary) != 1 || primary[0].DateModified == nil {
		t.Fatalf("primary images for %s = %+v, want one", *value.Path, value.ImageInfos)
	}
	id := value.ID.String()
	prefix := "%MetadataPath%/library/" + id[:2] + "/" + id + "/poster-"
	if !strings.HasPrefix(primary[0].Path, prefix) || !strings.HasSuffix(primary[0].Path, ".jpg") {
		t.Fatalf("primary image path for %s = %q, want %s<tag>.jpg", *value.Path, primary[0].Path, prefix)
	}
	path := filepath.Join(metadataRoot, filepath.FromSlash(strings.TrimPrefix(primary[0].Path, "%MetadataPath%/")))
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open primary image for %s: %v", *value.Path, err)
	}
	defer func() { _ = file.Close() }()
	decoded, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode primary image for %s: %v", *value.Path, err)
	}
	if decoded.Width != width || decoded.Height != height {
		t.Fatalf("primary image for %s is %dx%d, want %dx%d", *value.Path, decoded.Width, decoded.Height, width, height)
	}
}

func metadataImageFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "library"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "poster-") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk metadata images: %v", err)
	}
	return files
}
