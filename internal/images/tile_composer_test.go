// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/rootfs"
)

const tileComposerSubprocessMode = "DEADFIN_TILE_COMPOSER_SUBPROCESS"

// TestTileComposer exercises the public collage boundary over the process-owned
// libvips Runtime. A caller receives one complete JPEG at a caller-selected
// location beneath the composer's owned root; it never needs access to native
// image handles or a package-global vips lifecycle.
func TestTileComposer(t *testing.T) {
	// libvips shutdown is terminal for a process. Run the entire tile boundary
	// in a fresh test binary so package-level lifecycle tests cannot make this
	// public composer contract order-dependent.
	if os.Getenv(tileComposerSubprocessMode) == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestTileComposer$")
		command.Env = append(os.Environ(), tileComposerSubprocessMode+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("tile composer subprocess failed: %v\n%s", err, output)
		}
		return
	}
	requireNativeVips(t)

	runtimeOwner, err := images.OpenRuntime(t.Context())
	if err != nil {
		t.Fatalf("OpenRuntime(): %v", err)
	}
	t.Cleanup(func() {
		if err := runtimeOwner.Close(); err != nil {
			t.Errorf("Runtime.Close(): %v", err)
		}
	})

	newComposer := func(t *testing.T) (*images.TileComposer, string) {
		t.Helper()
		root := t.TempDir()
		composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{
			Root:          root,
			MaxConcurrent: 1,
		})
		if err != nil {
			t.Fatalf("NewTileComposer(): %v", err)
		}
		t.Cleanup(func() {
			if err := composer.Close(); err != nil {
				t.Errorf("TileComposer.Close(): %v", err)
			}
		})
		return composer, root
	}

	t.Run("syncs a newly created root and its parent before constructing a composer", func(t *testing.T) {
		parent := t.TempDir()
		canonicalParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			t.Fatalf("EvalSymlinks(tile root parent): %v", err)
		}
		root := filepath.Join(canonicalParent, "new-owned-root")
		syncFailure := errors.New("injected root-directory sync failure")
		expectedSyncs := map[string]int{canonicalParent: 1, root: 1}
		synced := make(map[string]int, len(expectedSyncs))
		composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{
			Root: root, MaxConcurrent: 1,
			SyncDirectory: func(directory *os.File) error {
				synced[directory.Name()]++
				for path, want := range expectedSyncs {
					if synced[path] < want {
						return nil
					}
				}
				return syncFailure
			},
		})
		if !errors.Is(err, syncFailure) {
			t.Fatalf("NewTileComposer() root sync error = %v, want injected sync failure", err)
		}
		if composer != nil {
			t.Fatalf("NewTileComposer() composer after root sync failure = %v, want nil", composer)
		}
		for path, want := range expectedSyncs {
			if got := synced[path]; got < want {
				t.Errorf("SyncDirectory calls for %q = %d, want at least %d", path, got, want)
			}
		}
	})

	t.Run("retains the securely walked root inode across a pathname replacement", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "owned")
		originalSourcesPath := filepath.Join(root, "sources")
		originalTilesPath := filepath.Join(root, "tiles")
		if err := os.MkdirAll(originalSourcesPath, 0o755); err != nil {
			t.Fatalf("MkdirAll(original sources): %v", err)
		}
		if err := os.MkdirAll(originalTilesPath, 0o755); err != nil {
			t.Fatalf("MkdirAll(original tiles): %v", err)
		}
		name := filepath.Base(writeTileComposerPNG(t, originalSourcesPath, "source.png", color.NRGBA{R: 0xff, A: 0xff}))
		originalSources, err := rootfs.Pin(originalSourcesPath)
		if err != nil {
			t.Fatalf("Pin(original sources): %v", err)
		}
		t.Cleanup(func() { _ = originalSources.Close() })
		originalTiles, err := rootfs.Pin(originalTilesPath)
		if err != nil {
			t.Fatalf("Pin(original tiles): %v", err)
		}
		t.Cleanup(func() { _ = originalTiles.Close() })

		retiredRoot := filepath.Join(parent, "retired")
		composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{
			Root: root, MaxConcurrent: 1,
			AfterRootSecureWalk: func() error {
				if err := os.Rename(root, retiredRoot); err != nil {
					return fmt.Errorf("rename securely walked root: %w", err)
				}
				if err := os.MkdirAll(filepath.Join(root, "sources"), 0o755); err != nil {
					return fmt.Errorf("create replacement sources: %w", err)
				}
				if err := os.MkdirAll(filepath.Join(root, "tiles"), 0o755); err != nil {
					return fmt.Errorf("create replacement tiles: %w", err)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("NewTileComposer() after root replacement: %v", err)
		}
		t.Cleanup(func() {
			if err := composer.Close(); err != nil {
				t.Errorf("TileComposer.Close(): %v", err)
			}
		})

		if _, err := composer.Compose(t.Context(), tileComposerRequest(originalSources, originalTiles, "original.jpg", 24, 24, 1, 1, 100, name)); err != nil {
			t.Fatalf("Compose() with original root descendants: %v", err)
		}
		if _, err := os.Stat(filepath.Join(retiredRoot, "tiles", "original.jpg")); err != nil {
			t.Fatalf("Stat(tile published through original root descriptor): %v", err)
		}

		replacementSources, err := rootfs.Pin(filepath.Join(root, "sources"))
		if err != nil {
			t.Fatalf("Pin(replacement sources): %v", err)
		}
		t.Cleanup(func() { _ = replacementSources.Close() })
		replacementTiles, err := rootfs.Pin(filepath.Join(root, "tiles"))
		if err != nil {
			t.Fatalf("Pin(replacement tiles): %v", err)
		}
		t.Cleanup(func() { _ = replacementTiles.Close() })
		if _, err := composer.Compose(t.Context(), tileComposerRequest(replacementSources, replacementTiles, "replacement.jpg", 24, 24, 1, 1, 100, name)); err == nil {
			t.Fatal("Compose() with replacement root descendants error = nil, want rejection")
		}
		if _, err := os.Stat(filepath.Join(root, "tiles", "replacement.jpg")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(tile under replacement root) = %v, want no publication", err)
		}
	})

	t.Run("places source cells in row-major order and leaves unused final cells black", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		request := tileComposerRequest(sources, outputDir, "0.jpg", 24, 24, 2, 2, 100,
			filepath.Base(writeTileComposerPNG(t, sourcePath, "red.png", color.NRGBA{R: 0xff, A: 0xff})),
			filepath.Base(writeTileComposerPNG(t, sourcePath, "green.png", color.NRGBA{G: 0xff, A: 0xff})),
			filepath.Base(writeTileComposerPNG(t, sourcePath, "blue.png", color.NRGBA{B: 0xff, A: 0xff})))
		result, err := composer.Compose(t.Context(), request)
		if err != nil {
			t.Fatalf("Compose(): %v", err)
		}
		output := filepath.Join(outputPath, "0.jpg")
		if result.Path != output {
			t.Fatalf("Compose() diagnostic path = %q, want %q", result.Path, output)
		}
		tileBytes, err := os.ReadFile(output)
		if err != nil {
			t.Fatalf("ReadFile(composed tile): %v", err)
		}
		if len(tileBytes) < 2 || tileBytes[0] != 0xff || tileBytes[1] != 0xd8 {
			t.Fatalf("composed tile prefix = %x, want JPEG SOI", tileBytes[:min(len(tileBytes), 8)])
		}
		decoded, format, err := image.DecodeConfig(bytes.NewReader(tileBytes))
		if err != nil {
			t.Fatalf("DecodeConfig(composed tile): %v", err)
		}
		if format != "jpeg" || decoded.Width != 48 || decoded.Height != 48 {
			t.Fatalf("composed tile config = %s %dx%d, want jpeg 48x48", format, decoded.Width, decoded.Height)
		}
		imageValue := decodeTileComposerJPEG(t, output)
		assertTileComposerColor(t, imageValue, 12, 12, color.NRGBA{R: 0xff, A: 0xff})
		assertTileComposerColor(t, imageValue, 36, 12, color.NRGBA{G: 0xff, A: 0xff})
		assertTileComposerColor(t, imageValue, 12, 36, color.NRGBA{B: 0xff, A: 0xff})
		assertTileComposerColor(t, imageValue, 36, 36, color.NRGBA{A: 0xff})
	})

	t.Run("rejects canceled work before native composition and publishes no tile", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{R: 0xff, A: 0xff}))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := composer.Compose(ctx, tileComposerRequest(sources, outputDir, "canceled.jpg", 8, 8, 1, 1, 90, name))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Compose(canceled context) error = %v, want context.Canceled", err)
		}
		if _, statErr := os.Stat(filepath.Join(outputPath, "canceled.jpg")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("Compose(canceled context) output stat error = %v, want no publication", statErr)
		}
	})

	t.Run("rejects invalid geometry components and foreign directory handles without publication", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{R: 0xff, A: 0xff}))
		foreignPath := t.TempDir()
		foreign, err := rootfs.Pin(foreignPath)
		if err != nil {
			t.Fatalf("Pin(foreign directory): %v", err)
		}
		t.Cleanup(func() { _ = foreign.Close() })
		for _, tc := range []struct {
			name    string
			request images.TileRequest
			output  string
		}{
			{name: "zero cell width", request: tileComposerRequest(sources, outputDir, "zero-width.jpg", 0, 8, 1, 1, 90, name), output: filepath.Join(outputPath, "zero-width.jpg")},
			{name: "zero columns", request: tileComposerRequest(sources, outputDir, "zero-columns.jpg", 8, 8, 0, 1, 90, name), output: filepath.Join(outputPath, "zero-columns.jpg")},
			{name: "cell grid capacity overflow", request: tileComposerRequest(sources, outputDir, "overflow.jpg", 8, 8, int(^uint(0)>>1), 2, 90, name), output: filepath.Join(outputPath, "overflow.jpg")},
			{name: "path separator output component", request: tileComposerRequest(sources, outputDir, "nested/escaped.jpg", 8, 8, 1, 1, 90, name)},
			{name: "path separator source component", request: tileComposerRequest(sources, outputDir, "bad-source.jpg", 8, 8, 1, 1, 90, "../source.png"), output: filepath.Join(outputPath, "bad-source.jpg")},
			{name: "foreign source handle", request: tileComposerRequest(foreign, outputDir, "foreign-source.jpg", 8, 8, 1, 1, 90, name), output: filepath.Join(outputPath, "foreign-source.jpg")},
			{name: "foreign output handle", request: tileComposerRequest(sources, foreign, "foreign-output.jpg", 8, 8, 1, 1, 90, name), output: filepath.Join(foreignPath, "foreign-output.jpg")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := composer.Compose(t.Context(), tc.request); err == nil {
					t.Fatal("Compose() error = nil, want rejected request")
				}
				if tc.output != "" {
					if _, err := os.Stat(tc.output); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("Compose() invalid request published %q: stat error = %v, want no file", tc.output, err)
					}
				}
			})
		}
	})

	t.Run("rejects a non-overflowing output above the pixel ceiling before native composition", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{R: 0xff, A: 0xff}))
		result, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "pixel-limit.jpg", 8193, 8192, 1, 1, 90, name))
		if err == nil || result != (images.TileResult{}) {
			t.Fatalf("Compose() pixel ceiling result = %#v, %v; want rejection without result", result, err)
		}
		if _, statErr := os.Stat(filepath.Join(outputPath, "pixel-limit.jpg")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("Compose() above pixel ceiling published output: %v", statErr)
		}
	})

	t.Run("reports native source errors without publication", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		if err := os.WriteFile(filepath.Join(sourcePath, "not-an-image.png"), []byte("not an image"), 0o600); err != nil {
			t.Fatalf("WriteFile(corrupt source): %v", err)
		}
		result, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "native-error.jpg", 24, 24, 1, 1, 90, "not-an-image.png"))
		if err == nil || result != (images.TileResult{}) {
			t.Fatalf("Compose(corrupt source) = %#v, %v; want native decode error without result", result, err)
		}
		if _, statErr := os.Stat(filepath.Join(outputPath, "native-error.jpg")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("Compose(corrupt source) published output: %v", statErr)
		}
	})

	t.Run("never exposes an incomplete JPEG at its final component", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerSizedPNG(t, sourcePath, "large.png", 1024, 1024, color.NRGBA{R: 0xc0, G: 0x20, B: 0x10, A: 0xff}))
		output := filepath.Join(outputPath, "atomic.jpg")
		finished := make(chan error, 1)
		go func() {
			_, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "atomic.jpg", 1024, 1024, 4, 4, 90, name, name, name, name, name, name, name, name, name, name, name, name, name, name, name, name))
			finished <- err
		}()
		for {
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("Compose(atomic tile): %v", err)
				}
				decoded := decodeTileComposerJPEG(t, output)
				if got, want := decoded.Bounds(), image.Rect(0, 0, 4096, 4096); got != want {
					t.Fatalf("published tile bounds = %v, want %v", got, want)
				}
				return
			default:
				if _, err := os.Stat(output); err == nil {
					decoded := decodeTileComposerJPEG(t, output)
					if got, want := decoded.Bounds(), image.Rect(0, 0, 4096, 4096); got != want {
						t.Fatalf("observed tile bounds = %v, want %v", got, want)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Stat(tile while composing): %v", err)
				}
				runtime.Gosched()
			}
		}
	})

	t.Run("reports post-rename directory sync failure without exposing a corrupt tile", func(t *testing.T) {
		root := t.TempDir()
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		output := filepath.Join(outputPath, "durability.jpg")
		syncFailure := errors.New("injected containing-directory sync failure")
		composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{
			Root: root, MaxConcurrent: 1,
			SyncDirectory: func(directory *os.File) error {
				if directory.Name() != outputPath {
					return nil
				}
				decoded := decodeTileComposerJPEG(t, output)
				if got, want := decoded.Bounds(), image.Rect(0, 0, 24, 24); got != want {
					t.Fatalf("tile visible during containing-directory sync bounds = %v, want %v", got, want)
				}
				return syncFailure
			},
		})
		if err != nil {
			t.Fatalf("NewTileComposer(): %v", err)
		}
		t.Cleanup(func() { _ = composer.Close() })
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{G: 0xff, A: 0xff}))
		result, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "durability.jpg", 24, 24, 1, 1, 90, name))
		if !errors.Is(err, syncFailure) || result != (images.TileResult{}) {
			t.Fatalf("Compose() sync failure = %#v, %v; want injected error and no result", result, err)
		}
		decoded := decodeTileComposerJPEG(t, output)
		assertTileComposerColor(t, decoded, 12, 12, color.NRGBA{G: 0xff, A: 0xff})
		entries, err := os.ReadDir(outputPath)
		if err != nil || len(entries) != 1 || entries[0].Name() != "durability.jpg" {
			t.Fatalf("output directory after sync failure = %v, %v; want only durability.jpg", entries, err)
		}
	})

	t.Run("publishes through pinned descriptors after its root pathname is replaced", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "root")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatalf("Mkdir(root): %v", err)
		}
		composer, err := images.NewTileComposer(runtimeOwner, images.TileComposerConfig{Root: root, MaxConcurrent: 1})
		if err != nil {
			t.Fatalf("NewTileComposer(): %v", err)
		}
		t.Cleanup(func() { _ = composer.Close() })
		sources, outputDir, sourcePath, _ := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{B: 0xff, A: 0xff}))
		movedRoot := filepath.Join(parent, "pinned-root")
		if err := os.Rename(root, movedRoot); err != nil {
			t.Fatalf("Rename(root): %v", err)
		}
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatalf("Mkdir(replacement root): %v", err)
		}
		if _, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "swapped.jpg", 24, 24, 1, 1, 90, name)); err != nil {
			t.Fatalf("Compose() after root pathname swap: %v", err)
		}
		if _, err := os.Stat(filepath.Join(movedRoot, "tiles", "swapped.jpg")); err != nil {
			t.Fatalf("Stat(tile in pinned renamed tree): %v", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("replacement root entries = %v, %v; want empty replacement", entries, err)
		}
	})

	t.Run("Close is idempotent and rejects subsequent composition", func(t *testing.T) {
		composer, root := newComposer(t)
		sources, outputDir, sourcePath, outputPath := tileComposerDirs(t, root)
		name := filepath.Base(writeTileComposerPNG(t, sourcePath, "source.png", color.NRGBA{R: 0xff, A: 0xff}))
		if err := composer.Close(); err != nil {
			t.Fatalf("TileComposer.Close() first call: %v", err)
		}
		if err := composer.Close(); err != nil {
			t.Fatalf("TileComposer.Close() second call: %v", err)
		}
		if _, err := composer.Compose(t.Context(), tileComposerRequest(sources, outputDir, "closed.jpg", 24, 24, 1, 1, 90, name)); err == nil {
			t.Fatal("Compose() after Close() error = nil, want rejection")
		}
		if _, err := os.Stat(filepath.Join(outputPath, "closed.jpg")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Compose() after Close() published output: %v", err)
		}
	})
}

func tileComposerDirs(t *testing.T, root string) (sources, output *rootfs.Dir, sourcePath, outputPath string) {
	t.Helper()
	sourcePath = filepath.Join(root, "sources")
	outputPath = filepath.Join(root, "tiles")
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("MkdirAll(tile sources): %v", err)
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(tile output): %v", err)
	}
	var err error
	sources, err = rootfs.Pin(sourcePath)
	if err != nil {
		t.Fatalf("Pin(tile source directory): %v", err)
	}
	t.Cleanup(func() { _ = sources.Close() })
	output, err = rootfs.Pin(outputPath)
	if err != nil {
		t.Fatalf("Pin(tile output directory): %v", err)
	}
	t.Cleanup(func() { _ = output.Close() })
	return sources, output, sourcePath, outputPath
}

func tileComposerRequest(sourceDir, outputDir *rootfs.Dir, outputName string, cellWidth, cellHeight, columns, rows, quality int, sourceNames ...string) images.TileRequest {
	return images.TileRequest{SourceDir: sourceDir, SourceNames: sourceNames, OutputDir: outputDir, OutputName: outputName, CellWidth: cellWidth, CellHeight: cellHeight, Columns: columns, Rows: rows, JPEGQuality: quality}
}

func writeTileComposerPNG(t *testing.T, directory, name string, value color.NRGBA) string {
	t.Helper()
	return writeTileComposerSizedPNG(t, directory, name, 24, 24, value)
}

func writeTileComposerSizedPNG(t *testing.T, directory, name string, width, height int, value color.NRGBA) string {
	t.Helper()
	path := filepath.Join(directory, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(tile source): %v", err)
	}
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			imageValue.SetNRGBA(x, y, value)
		}
	}
	if err := png.Encode(file, imageValue); err != nil {
		_ = file.Close()
		t.Fatalf("Encode(tile source): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(tile source): %v", err)
	}
	return path
}

func decodeTileComposerJPEG(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(composed tile): %v", err)
	}
	decoded, err := jpeg.Decode(file)
	closeErr := file.Close()
	if err != nil {
		if closeErr != nil {
			t.Errorf("Close(composed tile) after decode failure: %v", closeErr)
		}
		t.Fatalf("Decode(composed tile): %v", err)
	}
	if closeErr != nil {
		t.Fatalf("Close(composed tile): %v", closeErr)
	}
	return decoded
}

func assertTileComposerColor(t *testing.T, imageValue image.Image, x, y int, want color.NRGBA) {
	t.Helper()
	got := color.NRGBAModel.Convert(imageValue.At(x, y)).(color.NRGBA)
	const tolerance = 12
	if absTileComposerDifference(got.R, want.R) > tolerance || absTileComposerDifference(got.G, want.G) > tolerance || absTileComposerDifference(got.B, want.B) > tolerance || got.A != want.A {
		t.Fatalf("pixel (%d,%d) = %#v, want near %#v", x, y, got, want)
	}
}

func absTileComposerDifference(left, right uint8) uint8 {
	if left >= right {
		return left - right
	}
	return right - left
}
