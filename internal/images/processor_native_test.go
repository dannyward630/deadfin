// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
)

// TestNativeProcessor exercises the public native boundary rather than the vips
// command-line tool. The CLI is used only to diagnose the local prerequisite,
// generate the WebP input without checked-in binary corpus data, and inspect
// WebP alpha after the Go processor has produced it.
//
// Proposed minimal public seam:
//
//	OpenRuntime(context.Context) (*Runtime, error)
//	(*Runtime).Close() error
//	NewProcessor(*Runtime, ProcessorConfig) (*Processor, error)
//	(*Processor).Process(context.Context, ProcessOptions) (ProcessedImage, error)
//
// ProcessOptions owns Source{Path, Modified}, output/resize/orientation options,
// and the processor derives CacheOptions, CacheKey, and CachePath itself. This
// deliberately rules out callers supplying a cache key that disagrees with their
// source or processing options.
func TestNativeProcessor(t *testing.T) {
	requireNativeVips(t)

	runtimeOwner, err := images.OpenRuntime(context.Background())
	if err != nil {
		t.Fatalf("OpenRuntime() error: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := runtimeOwner.Close(); err != nil {
				t.Errorf("Runtime.Close() during cleanup: %v", err)
			}
		}
	})

	cacheRoot := t.TempDir()
	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
		CacheRoot:     cacheRoot,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatalf("NewProcessor() error: %v", err)
	}
	t.Run("converts unsupported sources through configured converter", func(t *testing.T) {
		testProcessorFallback(t, runtimeOwner)
	})

	t.Run("runs P3-01 native matrix", func(t *testing.T) {
		testP301NativeMatrix(t, runtimeOwner)
	})

	t.Run("runs P3-01 stock output corpus", func(t *testing.T) {
		testP301StockCorpus(t, runtimeOwner)
	})

	t.Run("decodes raster and vector inputs", func(t *testing.T) {
		sourceDirectory := t.TempDir()
		cases := []struct {
			name         string
			write        func(*testing.T, string) string
			outputFormat images.Format
			wantMIME     string
		}{
			{name: "JPEG", write: writeJPEGSource, outputFormat: images.FormatJPEG, wantMIME: "image/jpeg"},
			{name: "PNG", write: writePNGSource, outputFormat: images.FormatPNG, wantMIME: "image/png"},
			{name: "WebP", write: writeWebPSource, outputFormat: images.FormatPNG, wantMIME: "image/png"},
			{name: "SVG", write: writeSVGSource, outputFormat: images.FormatPNG, wantMIME: "image/png"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				sourcePath := tc.write(t, sourceDirectory)
				options := nativeOptions(sourcePath, tc.outputFormat, images.ResizeOptions{Width: nativeInt(6)})
				got := mustProcess(t, processor, context.Background(), options)

				wantPath := cachePathFor(options, cacheRoot)
				if got.Path != wantPath {
					t.Errorf("Process(%s).Path = %q, want cache path %q", tc.name, got.Path, wantPath)
				}
				if got.MIMEType != tc.wantMIME {
					t.Errorf("Process(%s).MIMEType = %q, want %q", tc.name, got.MIMEType, tc.wantMIME)
				}
				assertImageDimensions(t, got.Path, images.Dimensions{Width: 6, Height: 4})
				if got.Dimensions != (images.Dimensions{Width: 6, Height: 4}) {
					t.Errorf("Process(%s).Dimensions = %#v, want %#v", tc.name, got.Dimensions, images.Dimensions{Width: 6, Height: 4})
				}
			})
		}
	})

	t.Run("applies pure resize resolution to encoded pixels", func(t *testing.T) {
		sourcePath := writeSizedPNGSource(t, t.TempDir(), 8, 4)
		cases := []struct {
			name   string
			resize images.ResizeOptions
		}{
			{name: "fixed width", resize: images.ResizeOptions{Width: nativeInt(4)}},
			{name: "maximum height", resize: images.ResizeOptions{MaxHeight: nativeInt(2)}},
			{name: "fill box", resize: images.ResizeOptions{FillWidth: nativeInt(3), FillHeight: nativeInt(5)}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				options := nativeOptions(sourcePath, images.FormatPNG, tc.resize)
				got := mustProcess(t, processor, context.Background(), options)
				want := images.ResolveDimensions(images.Dimensions{Width: 8, Height: 4}, tc.resize)
				if got.Dimensions != want {
					t.Errorf("Process().Dimensions = %#v, want ResolveDimensions result %#v", got.Dimensions, want)
				}
				assertImageDimensions(t, got.Path, want)
			})
		}
	})

	t.Run("keeps existing cache artifacts across later auto-orient requests", func(t *testing.T) {
		cases := []struct {
			name        string
			orientation uint16
		}{
			{name: "right top", orientation: 6},
			{name: "left bottom", orientation: 8},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				sourcePath := writeOrientedJPEGSource(t, t.TempDir(), tc.orientation)
				unorientedOptions := nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{})
				unoriented := mustProcess(t, processor, context.Background(), unorientedOptions)
				assertImageDimensions(t, unoriented.Path, images.Dimensions{Width: 24, Height: 16})
				unorientedHash := normalizedPixelHash(t, unoriented.Path)

				orientedOptions := unorientedOptions
				orientedOptions.AutoOrient = true
				for request := range 2 {
					cached := mustProcess(t, processor, context.Background(), orientedOptions)
					if cached.Path != unoriented.Path {
						t.Errorf("auto-orient cache hit %d path = %q, want existing %q", request+1, cached.Path, unoriented.Path)
					}
					assertImageDimensions(t, cached.Path, images.Dimensions{Width: 24, Height: 16})
					if cachedHash := normalizedPixelHash(t, cached.Path); cachedHash != unorientedHash {
						t.Errorf("auto-orient cache hit %d changed existing artifact pixels", request+1)
					}
				}
			})
		}
	})

	t.Run("preserves alpha when encoding PNG and WebP", func(t *testing.T) {
		sourcePath := writeAlphaPNGSource(t, t.TempDir())
		for _, outputFormat := range []images.Format{images.FormatPNG, images.FormatWebP} {
			t.Run(string(outputFormat), func(t *testing.T) {
				got := mustProcess(t, processor, context.Background(), nativeOptions(sourcePath, outputFormat, images.ResizeOptions{}))
				decodedPath := got.Path
				if outputFormat == images.FormatWebP {
					decodedPath = vipsCopyAsPNG(t, got.Path)
				}
				decoded := mustDecode(t, decodedPath)
				if transparent := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA); transparent.A != 0 {
					t.Errorf("transparent pixel alpha = %d, want 0", transparent.A)
				}
				if opaque := color.NRGBAModel.Convert(decoded.At(1, 0)).(color.NRGBA); opaque.A != 0xff {
					t.Errorf("opaque pixel alpha = %d, want 255", opaque.A)
				}
			})
		}
	})

	t.Run("rejects AVIF output without a cache artifact", func(t *testing.T) {
		isolatedRoot := t.TempDir()
		isolated, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
			CacheRoot:     isolatedRoot,
			MaxConcurrent: 1,
		})
		if err != nil {
			t.Fatalf("NewProcessor(isolated) error: %v", err)
		}
		options := nativeOptions(writePNGSource(t, t.TempDir()), images.Format("Avif"), images.ResizeOptions{})
		_, err = isolated.Process(context.Background(), options)
		if !errors.Is(err, images.ErrUnsupportedOutputFormat) {
			t.Fatalf("Process(AVIF) error = %v, want ErrUnsupportedOutputFormat", err)
		}
		entries, err := os.ReadDir(isolatedRoot)
		if err != nil {
			t.Fatalf("ReadDir(cache root): %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("Process(AVIF) created cache entries %v, want none", entryNames(entries))
		}
	})

	t.Run("publishes one complete cache file for concurrent identical work", func(t *testing.T) {
		sourcePath := writeSizedPNGSource(t, t.TempDir(), 1600, 900)
		options := nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{Width: nativeInt(800)})
		wantPath := cachePathFor(options, cacheRoot)

		const workers = 8
		start := make(chan struct{})
		type outcome struct {
			result images.ProcessedImage
			err    error
		}
		outcomes := make(chan outcome, workers)
		var ready sync.WaitGroup
		ready.Add(workers)
		for range workers {
			go func() {
				ready.Done()
				<-start
				result, err := processor.Process(context.Background(), options)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		ready.Wait()
		close(start)

		remaining := workers
		var observedPartial, statErr error
		for remaining > 0 {
			select {
			case result := <-outcomes:
				remaining--
				if result.err != nil {
					t.Errorf("concurrent Process() error: %v", result.err)
					continue
				}
				if result.result.Path != wantPath {
					t.Errorf("concurrent Process().Path = %q, want %q", result.result.Path, wantPath)
				}
			default:
				if _, err := os.Stat(wantPath); err == nil {
					// A directly-written cache file is observable here while an encoder is
					// still active. Atomic rename means every observed final path decodes.
					if got, err := decodedDimensions(wantPath); err != nil && observedPartial == nil {
						observedPartial = fmt.Errorf("observed incomplete cache file: %w", err)
					} else if err == nil && got != (images.Dimensions{Width: 800, Height: 450}) && observedPartial == nil {
						observedPartial = fmt.Errorf("observed cache dimensions %#v, want 800x450", got)
					}
				} else if !errors.Is(err, os.ErrNotExist) && statErr == nil {
					statErr = err
				}
				runtime.Gosched()
			}
		}
		if statErr != nil {
			t.Errorf("Stat(concurrent cache path): %v", statErr)
		}
		if observedPartial != nil {
			t.Error(observedPartial)
		}
		assertImageDimensions(t, wantPath, images.Dimensions{Width: 800, Height: 450})
	})

	t.Run("returns cancellation before encoding and leaves no cache artifact", func(t *testing.T) {
		isolatedRoot := t.TempDir()
		isolated, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
			CacheRoot:     isolatedRoot,
			MaxConcurrent: 1,
		})
		if err != nil {
			t.Fatalf("NewProcessor(cancellation) error: %v", err)
		}
		options := nativeOptions(writePNGSource(t, t.TempDir()), images.FormatPNG, images.ResizeOptions{Width: nativeInt(6)})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = isolated.Process(ctx, options)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Process(canceled context) error = %v, want context.Canceled", err)
		}
		entries, err := os.ReadDir(isolatedRoot)
		if err != nil {
			t.Fatalf("ReadDir(canceled cache root): %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("Process(canceled context) created cache entries %v, want none", entryNames(entries))
		}
	})

	t.Run("rejects a second live runtime owner", func(t *testing.T) {
		second, err := images.OpenRuntime(context.Background())
		if second != nil {
			if closeErr := second.Close(); closeErr != nil {
				t.Errorf("second Runtime.Close() error: %v", closeErr)
			}
		}
		if !errors.Is(err, images.ErrRuntimeInUse) {
			t.Errorf("second OpenRuntime() error = %v, want ErrRuntimeInUse to prevent one owner shutting down another's active work", err)
		}
	})

	t.Run("runtime close waits for in-flight fallback conversion", func(t *testing.T) {
		testRuntimeCloseWaitsForFallback(t, runtimeOwner)
		closed = true
	})

	t.Run("runtime owner rejects use after its final close", func(t *testing.T) {
		if err := runtimeOwner.Close(); err != nil {
			t.Fatalf("Runtime.Close() error: %v", err)
		}
		closed = true
		_, err := processor.Process(context.Background(), nativeOptions(writePNGSource(t, t.TempDir()), images.FormatPNG, images.ResizeOptions{}))
		if !errors.Is(err, images.ErrRuntimeClosed) {
			t.Errorf("Process() after Runtime.Close() error = %v, want ErrRuntimeClosed", err)
		}
		if err := runtimeOwner.Close(); err != nil {
			t.Errorf("second Runtime.Close() error = %v, want idempotent close", err)
		}
	})
}

func requireNativeVips(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("vips")
	if err != nil {
		t.Skip("native P3-01 prerequisite unavailable: libvips 8.18.4 CLI was not found; install libvips and run `go test ./internal/images -run '^TestNativeProcessor$'`")
	}
	output, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		t.Skipf("native P3-01 prerequisite unavailable: %s --version failed: %v; install libvips 8.18.4 and run `go test ./internal/images -run '^TestNativeProcessor$'`", path, err)
	}
	version := strings.TrimSpace(string(output))
	if !strings.HasPrefix(version, "vips-8.") {
		t.Skipf("native P3-01 prerequisite unavailable: need libvips 8.x (8.18.4 is pinned), found %q; run `go test ./internal/images -run '^TestNativeProcessor$'` after installing it", version)
	}
}

func nativeOptions(sourcePath string, outputFormat images.Format, resize images.ResizeOptions) images.ProcessOptions {
	return images.ProcessOptions{
		Source: images.Source{
			Path:     sourcePath,
			Modified: nativeModified,
		},
		OutputFormat: outputFormat,
		Quality:      83,
		Resize:       resize,
	}
}

var nativeModified = time.Date(2024, time.January, 2, 3, 4, 5, 600000000, time.UTC)

func cachePathFor(options images.ProcessOptions, root string) string {
	return images.CachePath(root, images.CacheKey(images.CacheOptions{
		SourcePath: options.Source.Path,
		Quality:    options.Quality,
		Modified:   options.Source.Modified,
		Format:     options.OutputFormat,
		Resize:     options.Resize,
	}), options.OutputFormat)
}

func mustProcess(t *testing.T, processor *images.Processor, ctx context.Context, options images.ProcessOptions) images.ProcessedImage {
	t.Helper()
	result, err := processor.Process(ctx, options)
	if err != nil {
		t.Fatalf("Process(%q) error: %v", options.Source.Path, err)
	}
	return result
}

func writeJPEGSource(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "source.jpg")
	writeJPEG(t, path, markerImage())
	return path
}

func writePNGSource(t *testing.T, directory string) string {
	t.Helper()
	return writeSizedPNGSource(t, directory, 3, 2)
}

func writeSizedPNGSource(t *testing.T, directory string, width, height int) string {
	t.Helper()
	path := filepath.Join(directory, fmt.Sprintf("source-%dx%d.png", width, height))
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(PNG source): %v", err)
	}
	if err := png.Encode(file, generatedImage(width, height)); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("Close(PNG source) after encode failure: %v", closeErr)
		}
		t.Fatalf("Encode(PNG source): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(PNG source): %v", err)
	}
	return path
}

func writeWebPSource(t *testing.T, directory string) string {
	t.Helper()
	pngPath := writeSizedPNGSource(t, directory, 3, 2)
	webpPath := filepath.Join(directory, "source.webp")
	runVips(t, "copy", pngPath, webpPath)
	return webpPath
}

func writeSVGSource(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "source.svg")
	const source = `<svg xmlns="http://www.w3.org/2000/svg" width="3" height="2" viewBox="0 0 3 2"><rect width="3" height="2" fill="#d22"/></svg>`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile(SVG source): %v", err)
	}
	return path
}

func writeOrientedJPEGSource(t *testing.T, directory string, orientation uint16) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, orientationMarkerImage(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("Encode(oriented JPEG source): %v", err)
	}
	path := filepath.Join(directory, fmt.Sprintf("orientation-%d.jpg", orientation))
	if err := os.WriteFile(path, jpegWithEXIFOrientation(encoded.Bytes(), orientation), 0o600); err != nil {
		t.Fatalf("WriteFile(oriented JPEG source): %v", err)
	}
	return path
}

func writeAlphaPNGSource(t *testing.T, directory string) string {
	t.Helper()
	imageWithAlpha := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	imageWithAlpha.SetNRGBA(0, 0, color.NRGBA{R: 0x22, G: 0x44, B: 0xee, A: 0})
	imageWithAlpha.SetNRGBA(1, 0, color.NRGBA{R: 0x11, G: 0xcc, B: 0x33, A: 0xff})
	path := filepath.Join(directory, "alpha.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(alpha source): %v", err)
	}
	if err := png.Encode(file, imageWithAlpha); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("Close(alpha source) after encode failure: %v", closeErr)
		}
		t.Fatalf("Encode(alpha source): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(alpha source): %v", err)
	}
	return path
}

func writeJPEG(t *testing.T, path string, source image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(JPEG source): %v", err)
	}
	if err := jpeg.Encode(file, source, &jpeg.Options{Quality: 100}); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("Close(JPEG source) after encode failure: %v", closeErr)
		}
		t.Fatalf("Encode(JPEG source): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(JPEG source): %v", err)
	}
}

func markerImage() *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	result.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	result.SetNRGBA(1, 0, color.NRGBA{B: 0xff, A: 0xff})
	result.SetNRGBA(2, 0, color.NRGBA{G: 0xff, A: 0xff})
	result.SetNRGBA(0, 1, color.NRGBA{R: 0xff, G: 0xff, A: 0xff})
	result.SetNRGBA(1, 1, color.NRGBA{G: 0xff, B: 0xff, A: 0xff})
	result.SetNRGBA(2, 1, color.NRGBA{R: 0xff, B: 0xff, A: 0xff})
	return result
}

func orientationMarkerImage() *image.NRGBA {
	// JPEG 4:2:0 shares chroma across 2×2 samples. These 8×8 regions are
	// deliberately aligned blocks, so their post-decode colors still identify
	// geometry instead of testing a lossy codec's interpolation artifacts.
	result := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range 8 {
		for x := range 8 {
			result.SetNRGBA(x, y, color.NRGBA{R: 0xff, A: 0xff})
		}
		for x := range 8 {
			result.SetNRGBA(x+16, y, color.NRGBA{G: 0xff, A: 0xff})
		}
	}
	return result
}

func generatedImage(width, height int) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			result.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*17 + y*3) % 256),
				G: uint8((x*7 + y*29) % 256),
				B: uint8((x*11 + y*13) % 256),
				A: 0xff,
			})
		}
	}
	return result
}

func jpegWithEXIFOrientation(jpegBytes []byte, orientation uint16) []byte {
	// APP1 contains Exif\x00\x00 followed by a little-endian TIFF IFD0 with the
	// required Orientation (0x0112, SHORT, count 1) entry. Keeping it generated
	// here gives the rotation test exact metadata without a fixture corpus.
	tiff := []byte{
		'I', 'I', 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00,
		byte(orientation), byte(orientation >> 8), 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segmentLength := len(payload) + 2
	app1 := append([]byte{0xff, 0xe1, byte(segmentLength >> 8), byte(segmentLength)}, payload...)
	return append(append(append([]byte(nil), jpegBytes[:2]...), app1...), jpegBytes[2:]...)
}

func mustDecode(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	decoded, _, decodeErr := image.Decode(file)
	closeErr := file.Close()
	if decodeErr != nil {
		if closeErr != nil {
			t.Errorf("Close(%s) after decode failure: %v", path, closeErr)
		}
		t.Fatalf("Decode(%s): %v", path, decodeErr)
	}
	if closeErr != nil {
		t.Fatalf("Close(%s): %v", path, closeErr)
	}
	return decoded
}

func normalizedPixelHash(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	decoded := mustDecode(t, path)
	bounds := decoded.Bounds()
	hash := sha256.New()
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			pixel := color.NRGBAModel.Convert(decoded.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			if _, err := hash.Write([]byte{pixel.R, pixel.G, pixel.B, pixel.A}); err != nil {
				t.Fatalf("hash normalized pixels: %v", err)
			}
		}
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func decodedDimensions(path string) (images.Dimensions, error) {
	file, err := os.Open(path)
	if err != nil {
		return images.Dimensions{}, err
	}
	decoded, _, decodeErr := image.Decode(file)
	closeErr := file.Close()
	if decodeErr != nil {
		if closeErr != nil {
			return images.Dimensions{}, fmt.Errorf("decode image: %w (also close: %v)", decodeErr, closeErr)
		}
		return images.Dimensions{}, decodeErr
	}
	if closeErr != nil {
		return images.Dimensions{}, closeErr
	}
	bounds := decoded.Bounds()
	return images.Dimensions{Width: bounds.Dx(), Height: bounds.Dy()}, nil
}

func assertImageDimensions(t *testing.T, path string, want images.Dimensions) {
	t.Helper()
	got := mustDecode(t, path).Bounds()
	if got.Dx() != want.Width || got.Dy() != want.Height {
		t.Errorf("image %s dimensions = %dx%d, want %dx%d", path, got.Dx(), got.Dy(), want.Width, want.Height)
	}
}

func vipsCopyAsPNG(t *testing.T, sourcePath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inspection.png")
	runVips(t, "copy", sourcePath, path)
	return path
}

func runVips(t *testing.T, arguments ...string) {
	t.Helper()
	command := exec.Command("vips", arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("vips %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func nativeInt(value int) *int {
	return &value
}
