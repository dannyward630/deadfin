// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
	"golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// testP301NativeMatrix is deliberately called by TestNativeProcessor, which owns
// libvips's single process-lifetime Runtime. Every assertion below crosses the
// public Processor boundary and decodes produced pixels independently through
// Go's image decoders.
func testP301NativeMatrix(t *testing.T, runtimeOwner *images.Runtime) {
	t.Helper()

	t.Run("decodes deterministic native input fixtures", func(t *testing.T) {
		processor := newMatrixProcessor(t, runtimeOwner, t.TempDir(), 1, nil)
		sourceDirectory := t.TempDir()
		cases := []struct {
			name  string
			write func(*testing.T, string) string
		}{
			{name: "jpeg", write: writeMatrixJPEG},
			{name: "png", write: writeMatrixPNG},
			{name: "webp", write: writeMatrixWebP},
			{name: "gif", write: writeMatrixGIF},
			{name: "bmp", write: writeMatrixBMP},
			{name: "svg", write: writeMatrixSVG},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := mustProcess(t, processor, context.Background(), nativeOptions(tc.write(t, sourceDirectory), images.FormatPNG, images.ResizeOptions{}))
				if got.MIMEType != "image/png" {
					t.Errorf("Process(%s).MIMEType = %q, want image/png", tc.name, got.MIMEType)
				}
				if got.Dimensions != (images.Dimensions{Width: 8, Height: 4}) {
					t.Errorf("Process(%s).Dimensions = %#v, want 8x4", tc.name, got.Dimensions)
				}
				assertMatrixSignature(t, got.Path, images.FormatPNG)
				assertImageDimensions(t, got.Path, images.Dimensions{Width: 8, Height: 4})
			})
		}
	})

	t.Run("applies each EXIF orientation to quadrant geometry", func(t *testing.T) {
		processor := newMatrixProcessor(t, runtimeOwner, t.TempDir(), 1, nil)
		cases := []struct {
			orientation uint16
			want        [4]matrixColor
		}{
			{orientation: 1, want: [4]matrixColor{matrixRed, matrixGreen, matrixBlue, matrixYellow}},
			{orientation: 2, want: [4]matrixColor{matrixGreen, matrixRed, matrixYellow, matrixBlue}},
			{orientation: 3, want: [4]matrixColor{matrixYellow, matrixBlue, matrixGreen, matrixRed}},
			{orientation: 4, want: [4]matrixColor{matrixBlue, matrixYellow, matrixRed, matrixGreen}},
			{orientation: 5, want: [4]matrixColor{matrixRed, matrixBlue, matrixGreen, matrixYellow}},
			{orientation: 6, want: [4]matrixColor{matrixBlue, matrixRed, matrixYellow, matrixGreen}},
			{orientation: 7, want: [4]matrixColor{matrixYellow, matrixGreen, matrixBlue, matrixRed}},
			{orientation: 8, want: [4]matrixColor{matrixGreen, matrixYellow, matrixRed, matrixBlue}},
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("orientation-%d", tc.orientation), func(t *testing.T) {
				options := nativeOptions(writeMatrixOrientedJPEG(t, t.TempDir(), tc.orientation), images.FormatPNG, images.ResizeOptions{})
				options.AutoOrient = true
				got := mustProcess(t, processor, context.Background(), options)
				wantDimensions := images.Dimensions{Width: 24, Height: 16}
				if tc.orientation >= 5 {
					wantDimensions = images.Dimensions{Width: 16, Height: 24}
				}
				if got.Dimensions != wantDimensions {
					t.Fatalf("Process(orientation %d).Dimensions = %#v, want %#v", tc.orientation, got.Dimensions, wantDimensions)
				}
				assertMatrixQuadrants(t, mustDecode(t, got.Path), tc.want)
			})
		}
	})

	t.Run("resolves explicit option edges in encoded output", func(t *testing.T) {
		processor := newMatrixProcessor(t, runtimeOwner, t.TempDir(), 1, nil)
		cases := []struct {
			name   string
			source images.Dimensions
			resize images.ResizeOptions
			want   images.Dimensions
		}{
			{
				name:   "explicit zero leaves source geometry unchanged",
				source: images.Dimensions{Width: 8, Height: 4},
				resize: images.ResizeOptions{Width: nativeInt(0)},
				want:   images.Dimensions{Width: 8, Height: 4},
			},
			{
				name:   "aspect midpoint rounds to even",
				source: images.Dimensions{Width: 4, Height: 3},
				resize: images.ResizeOptions{Width: nativeInt(2)},
				want:   images.Dimensions{Width: 2, Height: 2},
			},
			{
				name:   "fixed box is constrained by maximum width",
				source: images.Dimensions{Width: 8, Height: 4},
				resize: images.ResizeOptions{Width: nativeInt(6), Height: nativeInt(6), MaxWidth: nativeInt(4)},
				want:   images.Dimensions{Width: 4, Height: 4},
			},
			{
				name:   "one sided fill uses unit counterpart",
				source: images.Dimensions{Width: 8, Height: 4},
				resize: images.ResizeOptions{FillWidth: nativeInt(2)},
				want:   images.Dimensions{Width: 2, Height: 1},
			},
			{
				name:   "fixed width permits upscale",
				source: images.Dimensions{Width: 8, Height: 4},
				resize: images.ResizeOptions{Width: nativeInt(16)},
				want:   images.Dimensions{Width: 16, Height: 8},
			},
			{
				name:   "nonbinding maximum is an explicit no op",
				source: images.Dimensions{Width: 8, Height: 4},
				resize: images.ResizeOptions{MaxWidth: nativeInt(9)},
				want:   images.Dimensions{Width: 8, Height: 4},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				options := nativeOptions(writeMatrixSizedPNG(t, t.TempDir(), tc.source.Width, tc.source.Height), images.FormatPNG, tc.resize)
				got := mustProcess(t, processor, context.Background(), options)
				if got.Dimensions != tc.want {
					t.Errorf("Process().Dimensions = %#v, want %#v", got.Dimensions, tc.want)
				}
				assertImageDimensions(t, got.Path, tc.want)
			})
		}
	})

	t.Run("encodes format signatures alpha and quality endpoints", func(t *testing.T) {
		processor := newMatrixProcessor(t, runtimeOwner, t.TempDir(), 1, nil)
		alphaSource := writeMatrixAlphaPNG(t, t.TempDir())
		cases := []struct {
			format    images.Format
			quality   int
			mime      string
			wantAlpha bool
		}{
			{format: images.FormatJPEG, quality: 0, mime: "image/jpeg", wantAlpha: false},
			{format: images.FormatJPEG, quality: 100, mime: "image/jpeg", wantAlpha: false},
			{format: images.FormatPNG, quality: 0, mime: "image/png", wantAlpha: true},
			{format: images.FormatWebP, quality: 0, mime: "image/webp", wantAlpha: true},
			{format: images.FormatWebP, quality: 100, mime: "image/webp", wantAlpha: true},
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s-q%d", tc.format, tc.quality), func(t *testing.T) {
				options := nativeOptions(alphaSource, tc.format, images.ResizeOptions{})
				options.Quality = tc.quality
				got := mustProcess(t, processor, context.Background(), options)
				if got.MIMEType != tc.mime {
					t.Errorf("Process(%s).MIMEType = %q, want %q", tc.format, got.MIMEType, tc.mime)
				}
				assertMatrixSignature(t, got.Path, tc.format)
				pixel := color.NRGBAModel.Convert(mustDecode(t, got.Path).At(0, 0)).(color.NRGBA)
				if tc.wantAlpha && pixel.A != 0 {
					t.Errorf("%s alpha at transparent source pixel = %d, want 0", tc.format, pixel.A)
				}
				if !tc.wantAlpha && pixel.A != 0xff {
					t.Errorf("%s alpha at transparent source pixel = %d, want opaque JPEG", tc.format, pixel.A)
				}
			})
		}

		qualitySource, original := writeMatrixGradientPNG(t, t.TempDir(), 64, 64)
		low := nativeOptions(qualitySource, images.FormatJPEG, images.ResizeOptions{})
		low.Quality = 0
		high := low
		high.Quality = 100
		lowImage := mustDecode(t, mustProcess(t, processor, context.Background(), low).Path)
		highImage := mustDecode(t, mustProcess(t, processor, context.Background(), high).Path)
		if lowError, highError := matrixRGBError(original, lowImage), matrixRGBError(original, highImage); highError >= lowError {
			t.Errorf("JPEG q100 pixel error = %d, want less than q0 error %d", highError, lowError)
		}

		// Like WebP, govips filters a zero-valued JPEG Q parameter. Q0 is an
		// explicit caller value, not permission to use libvips's Q75 default.
		jpegQ0 := nativeOptions(qualitySource, images.FormatJPEG, images.ResizeOptions{})
		jpegQ0.Quality = 0
		jpegQ1 := jpegQ0
		jpegQ1.Quality = 1
		jpegQ75 := jpegQ0
		jpegQ75.Quality = 75
		decodedJPEGQ0 := mustDecode(t, mustProcess(t, processor, context.Background(), jpegQ0).Path)
		decodedJPEGQ1 := mustDecode(t, mustProcess(t, processor, context.Background(), jpegQ1).Path)
		decodedJPEGQ75 := mustDecode(t, mustProcess(t, processor, context.Background(), jpegQ75).Path)
		jpegQ0Error := matrixRGBError(original, decodedJPEGQ0)
		jpegQ1Error := matrixRGBError(original, decodedJPEGQ1)
		jpegQ75Error := matrixRGBError(original, decodedJPEGQ75)
		jpegPixels := uint64(original.Bounds().Dx() * original.Bounds().Dy())
		if jpegQ0Error <= jpegQ75Error+jpegPixels {
			t.Errorf("JPEG q0 reconstruction error = %d, want materially more loss than explicit q75 error %d", jpegQ0Error, jpegQ75Error)
		}
		if jpegQ0Error < jpegQ1Error {
			t.Errorf("JPEG q0 reconstruction error = %d, want no less loss than explicit q1 error %d", jpegQ0Error, jpegQ1Error)
		}

		// govips omits a zero-valued Q field, which would silently ask libvips
		// for its default Q75. Decode all outputs through Go's WebP decoder so
		// this tests the consumer-visible reconstruction, not exporter plumbing.
		webP0 := nativeOptions(qualitySource, images.FormatWebP, images.ResizeOptions{})
		webP0.Quality = 0
		webP1 := webP0
		webP1.Quality = 1
		webP75 := webP0
		webP75.Quality = 75
		decodedQ0 := mustDecode(t, mustProcess(t, processor, context.Background(), webP0).Path)
		decodedQ1 := mustDecode(t, mustProcess(t, processor, context.Background(), webP1).Path)
		decodedQ75 := mustDecode(t, mustProcess(t, processor, context.Background(), webP75).Path)
		q0Error := matrixRGBError(original, decodedQ0)
		q1Error := matrixRGBError(original, decodedQ1)
		q75Error := matrixRGBError(original, decodedQ75)
		pixels := uint64(original.Bounds().Dx() * original.Bounds().Dy())
		if q0Error <= q75Error+pixels {
			t.Errorf("WebP q0 reconstruction error = %d, want materially more loss than explicit q75 error %d", q0Error, q75Error)
		}
		if q0Error < q1Error {
			t.Errorf("WebP q0 reconstruction error = %d, want no less loss than explicit q1 error %d", q0Error, q1Error)
		}
	})

	t.Run("passes SVG bytes through and rejects SVG transforms", func(t *testing.T) {
		cacheRoot := t.TempDir()
		processor := newMatrixProcessor(t, runtimeOwner, cacheRoot, 1, nil)
		sourcePath := writeMatrixSVG(t, t.TempDir())
		sourceBytes, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("ReadFile(SVG source): %v", err)
		}
		options := nativeOptions(sourcePath, images.FormatSVG, images.ResizeOptions{})
		got := mustProcess(t, processor, context.Background(), options)
		published, err := os.ReadFile(got.Path)
		if err != nil {
			t.Fatalf("ReadFile(published SVG): %v", err)
		}
		if !bytes.Equal(published, sourceBytes) {
			t.Error("Process(SVG pass-through) changed SVG bytes")
		}
		if got.MIMEType != "image/svg+xml" {
			t.Errorf("Process(SVG).MIMEType = %q, want image/svg+xml", got.MIMEType)
		}

		for _, tc := range []struct {
			name   string
			mutate func(*images.ProcessOptions)
		}{
			{name: "resize", mutate: func(options *images.ProcessOptions) { options.Resize.Width = nativeInt(4) }},
			{name: "orientation", mutate: func(options *images.ProcessOptions) { options.AutoOrient = true }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				transformed := options
				tc.mutate(&transformed)
				isolated := newMatrixProcessor(t, runtimeOwner, t.TempDir(), 1, nil)
				if _, err := isolated.Process(context.Background(), transformed); !errors.Is(err, images.ErrUnsupportedOutputFormat) {
					t.Errorf("Process(SVG %s) error = %v, want ErrUnsupportedOutputFormat", tc.name, err)
				}
			})
		}
	})

	// Pinned oracle: github.com/jellyfin/jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
	// src/Jellyfin.Drawing/ImageProcessor.cs:149-204. It derives autoOrient for
	// encoding but passes neither it nor orientation to GetCacheFilePath.
	t.Run("uses upstream cache identity when auto orienting", func(t *testing.T) {
		cacheRoot := t.TempDir()
		processor := newMatrixProcessor(t, runtimeOwner, cacheRoot, 1, nil)
		options := nativeOptions(writeMatrixOrientedJPEG(t, t.TempDir(), 6), images.FormatPNG, images.ResizeOptions{})
		options.AutoOrient = true
		got := mustProcess(t, processor, context.Background(), options)
		want := cachePathFor(images.ProcessOptions{
			Source:       options.Source,
			OutputFormat: options.OutputFormat,
			Quality:      options.Quality,
			Resize:       options.Resize,
		}, cacheRoot)
		if got.Path != want {
			t.Errorf("Process(AutoOrient).Path = %q, want upstream cache identity %q with no auto-orient suffix", got.Path, want)
		}
	})

	// Pinned ImageProcessor.cs:197-205 checks File.Exists(cacheFilePath) before
	// entering the bounded encoder work. A shared AutoOrient identity must still
	// accept that existing artifact; it must not decode or convert its source.
	t.Run("uses existing shared cache artifact before auto orient work", func(t *testing.T) {
		cacheRoot := t.TempDir()
		conversionCalls := 0
		converter := matrixConverterFunc(func(context.Context, string, string) error {
			conversionCalls++
			return errors.New("cache hit incorrectly attempted source conversion")
		})
		processor := newMatrixProcessor(t, runtimeOwner, cacheRoot, 1, converter)
		options := nativeOptions(filepath.Join(t.TempDir(), "missing.avif"), images.FormatPNG, images.ResizeOptions{})
		options.AutoOrient = true
		cachePath := cachePathFor(options, cacheRoot)
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
			t.Fatalf("MkdirAll(cache path): %v", err)
		}
		if err := writeMatrixPNGFile(cachePath, matrixRaster()); err != nil {
			t.Fatalf("WriteFile(cache artifact): %v", err)
		}
		wantBytes, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatalf("ReadFile(seed cache artifact): %v", err)
		}

		got, err := processor.Process(context.Background(), options)
		if err != nil {
			t.Fatalf("Process(existing auto-orient cache artifact) error: %v", err)
		}
		if got.Path != cachePath {
			t.Errorf("Process(existing auto-orient cache artifact).Path = %q, want %q", got.Path, cachePath)
		}
		if conversionCalls != 0 {
			t.Errorf("existing auto-orient cache artifact triggered %d source conversions, want none", conversionCalls)
		}
		gotBytes, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatalf("ReadFile(cache artifact after Process): %v", err)
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			t.Error("Process(existing auto-orient cache artifact) changed its complete published bytes")
		}
	})

	t.Run("limits live operations while allowing different cache keys in parallel", func(t *testing.T) {
		cacheRoot := t.TempDir()
		gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		started := make(chan int, len(gates))
		var mu sync.Mutex
		active, maximum, calls := 0, 0, 0
		converter := matrixConverterFunc(func(_ context.Context, _, output string) error {
			mu.Lock()
			call := calls
			calls++
			active++
			if active > maximum {
				maximum = active
			}
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			if err := writeMatrixPNGFile(output, matrixRaster()); err != nil {
				return err
			}
			started <- call
			<-gates[call]
			return nil
		})
		processor := newMatrixProcessor(t, runtimeOwner, cacheRoot, 2, converter)
		type outcome struct {
			result images.ProcessedImage
			err    error
		}
		outcomes := make(chan outcome, 3)
		for index := range 3 {
			source := filepath.Join(t.TempDir(), fmt.Sprintf("source-%d.unrecognized", index))
			if err := os.WriteFile(source, []byte("input"), 0o600); err != nil {
				t.Fatalf("WriteFile(concurrency source): %v", err)
			}
			go func(source string) {
				result, err := processor.Process(context.Background(), nativeOptions(source, images.FormatPNG, images.ResizeOptions{}))
				outcomes <- outcome{result: result, err: err}
			}(source)
		}
		first := <-started
		second := <-started
		if first == second {
			t.Fatalf("distinct concurrent starts shared call number %d", first)
		}
		select {
		case third := <-started:
			t.Fatalf("third operation %d started while two slots were held", third)
		default:
		}
		close(gates[first])
		close(gates[second])
		third := <-started
		close(gates[third])

		paths := make(map[string]struct{}, 3)
		for range 3 {
			outcome := <-outcomes
			if outcome.err != nil {
				t.Errorf("concurrent Process() error: %v", outcome.err)
				continue
			}
			paths[outcome.result.Path] = struct{}{}
			assertMatrixSignature(t, outcome.result.Path, images.FormatPNG)
			assertImageDimensions(t, outcome.result.Path, images.Dimensions{Width: 8, Height: 4})
		}
		if len(paths) != 3 {
			t.Errorf("different source keys published %d cache paths, want 3", len(paths))
		}
		mu.Lock()
		gotMaximum := maximum
		mu.Unlock()
		if gotMaximum != 2 {
			t.Errorf("maximum active Process operations = %d, want exactly ProcessorConfig.MaxConcurrent (2)", gotMaximum)
		}
	})
}

type matrixConverterFunc func(context.Context, string, string) error

func (converter matrixConverterFunc) Convert(ctx context.Context, inputPath, outputPath string) error {
	return converter(ctx, inputPath, outputPath)
}

func newMatrixProcessor(t *testing.T, runtimeOwner *images.Runtime, cacheRoot string, maximum int, converter images.SourceConverter) *images.Processor {
	t.Helper()
	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
		CacheRoot:       cacheRoot,
		MaxConcurrent:   maximum,
		SourceConverter: converter,
	})
	if err != nil {
		t.Fatalf("NewProcessor(): %v", err)
	}
	return processor
}

type matrixColor uint8

const (
	matrixRed matrixColor = iota
	matrixGreen
	matrixBlue
	matrixYellow
)

func matrixRaster() *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := range 4 {
		for x := range 8 {
			result.SetNRGBA(x, y, color.NRGBA{R: uint8(20 + x*21), G: uint8(30 + y*47), B: uint8(200 - x*13 - y*9), A: 0xff})
		}
	}
	return result
}

func matrixQuadrantRaster() *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range 16 {
		for x := range 24 {
			colorValue := matrixRed
			if x >= 12 {
				colorValue = matrixGreen
			}
			if y >= 8 {
				colorValue = matrixBlue
				if x >= 12 {
					colorValue = matrixYellow
				}
			}
			result.SetNRGBA(x, y, matrixNRGBA(colorValue))
		}
	}
	return result
}

func matrixNRGBA(value matrixColor) color.NRGBA {
	switch value {
	case matrixRed:
		return color.NRGBA{R: 0xff, A: 0xff}
	case matrixGreen:
		return color.NRGBA{G: 0xff, A: 0xff}
	case matrixBlue:
		return color.NRGBA{B: 0xff, A: 0xff}
	case matrixYellow:
		return color.NRGBA{R: 0xff, G: 0xff, A: 0xff}
	default:
		panic("unknown matrix color")
	}
}

func writeMatrixJPEG(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "matrix.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(matrix JPEG): %v", err)
	}
	if err := jpeg.Encode(file, matrixRaster(), &jpeg.Options{Quality: 100}); err != nil {
		_ = file.Close()
		t.Fatalf("Encode(matrix JPEG): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(matrix JPEG): %v", err)
	}
	return path
}

func writeMatrixPNG(t *testing.T, directory string) string {
	t.Helper()
	return writeMatrixSizedPNG(t, directory, 8, 4)
}

func writeMatrixSizedPNG(t *testing.T, directory string, width, height int) string {
	t.Helper()
	path := filepath.Join(directory, fmt.Sprintf("matrix-%dx%d.png", width, height))
	imageValue := matrixRaster()
	if imageValue.Bounds().Dx() != width || imageValue.Bounds().Dy() != height {
		imageValue = matrixGradient(width, height)
	}
	if err := writeMatrixPNGFile(path, imageValue); err != nil {
		t.Fatalf("Encode(matrix PNG): %v", err)
	}
	return path
}

func writeMatrixWebP(t *testing.T, directory string) string {
	t.Helper()
	pngPath := writeMatrixPNG(t, directory)
	webpPath := filepath.Join(directory, "matrix.webp")
	runVips(t, "copy", pngPath, webpPath)
	return webpPath
}

func writeMatrixGIF(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "matrix.gif")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(matrix GIF): %v", err)
	}
	if err := gif.Encode(file, matrixRaster(), nil); err != nil {
		_ = file.Close()
		t.Fatalf("Encode(matrix GIF): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(matrix GIF): %v", err)
	}
	return path
}

func writeMatrixBMP(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "matrix.bmp")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(matrix BMP): %v", err)
	}
	if err := bmp.Encode(file, matrixRaster()); err != nil {
		_ = file.Close()
		t.Fatalf("Encode(matrix BMP): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(matrix BMP): %v", err)
	}
	return path
}

func writeMatrixSVG(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "matrix.svg")
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="4" viewBox="0 0 8 4"><rect width="4" height="2" fill="#ff0000"/><rect x="4" width="4" height="2" fill="#00ff00"/><rect y="2" width="4" height="2" fill="#0000ff"/><rect x="4" y="2" width="4" height="2" fill="#ffff00"/></svg>`
	if err := os.WriteFile(path, []byte(svg), 0o600); err != nil {
		t.Fatalf("WriteFile(matrix SVG): %v", err)
	}
	return path
}

func writeMatrixOrientedJPEG(t *testing.T, directory string, orientation uint16) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, matrixQuadrantRaster(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("Encode(matrix oriented JPEG): %v", err)
	}
	path := filepath.Join(directory, fmt.Sprintf("orientation-%d.jpg", orientation))
	if err := os.WriteFile(path, jpegWithEXIFOrientation(encoded.Bytes(), orientation), 0o600); err != nil {
		t.Fatalf("WriteFile(matrix oriented JPEG): %v", err)
	}
	return path
}

func writeMatrixAlphaPNG(t *testing.T, directory string) string {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	value.SetNRGBA(0, 0, color.NRGBA{R: 0x22, G: 0x44, B: 0xee, A: 0})
	value.SetNRGBA(1, 0, color.NRGBA{R: 0x11, G: 0xcc, B: 0x33, A: 0xff})
	path := filepath.Join(directory, "alpha.png")
	if err := writeMatrixPNGFile(path, value); err != nil {
		t.Fatalf("Encode(matrix alpha PNG): %v", err)
	}
	return path
}

func writeMatrixGradientPNG(t *testing.T, directory string, width, height int) (string, *image.NRGBA) {
	t.Helper()
	value := matrixGradient(width, height)
	path := filepath.Join(directory, fmt.Sprintf("gradient-%dx%d.png", width, height))
	if err := writeMatrixPNGFile(path, value); err != nil {
		t.Fatalf("Encode(matrix gradient PNG): %v", err)
	}
	return path, value
}

func matrixGradient(width, height int) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			result.SetNRGBA(x, y, color.NRGBA{R: uint8((x*37 + y*11) % 256), G: uint8((x*13 + y*43) % 256), B: uint8((x*29 + y*17) % 256), A: 0xff})
		}
	}
	return result
}

func writeMatrixPNGFile(path string, value image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	encodeErr := png.Encode(file, value)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func assertMatrixQuadrants(t *testing.T, decoded image.Image, want [4]matrixColor) {
	t.Helper()
	bounds := decoded.Bounds()
	left, right := bounds.Min.X+bounds.Dx()/4, bounds.Min.X+bounds.Dx()*3/4
	top, bottom := bounds.Min.Y+bounds.Dy()/4, bounds.Min.Y+bounds.Dy()*3/4
	for _, tc := range []struct {
		name  string
		point image.Point
		want  matrixColor
	}{
		{name: "top-left", point: image.Pt(left, top), want: want[0]},
		{name: "top-right", point: image.Pt(right, top), want: want[1]},
		{name: "bottom-left", point: image.Pt(left, bottom), want: want[2]},
		{name: "bottom-right", point: image.Pt(right, bottom), want: want[3]},
	} {
		got := color.NRGBAModel.Convert(decoded.At(tc.point.X, tc.point.Y)).(color.NRGBA)
		if !matrixMatches(got, tc.want) {
			t.Errorf("%s quadrant pixel = %#v, want %v", tc.name, got, tc.want)
		}
	}
}

func matrixMatches(got color.NRGBA, want matrixColor) bool {
	switch want {
	case matrixRed:
		return got.R > 180 && got.R > got.G*2 && got.R > got.B*2
	case matrixGreen:
		return got.G > 180 && got.G > got.R*2 && got.G > got.B*2
	case matrixBlue:
		return got.B > 180 && got.B > got.R*2 && got.B > got.G*2
	case matrixYellow:
		return got.R > 180 && got.G > 180 && got.B < 100
	default:
		return false
	}
}

func matrixRGBError(want, got image.Image) uint64 {
	bounds := want.Bounds()
	if got.Bounds().Dx() != bounds.Dx() || got.Bounds().Dy() != bounds.Dy() {
		return ^uint64(0)
	}
	var errorSum uint64
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			wantPixel := color.NRGBAModel.Convert(want.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			gotPixel := color.NRGBAModel.Convert(got.At(got.Bounds().Min.X+x, got.Bounds().Min.Y+y)).(color.NRGBA)
			errorSum += uint64(matrixChannelDelta(wantPixel.R, gotPixel.R))
			errorSum += uint64(matrixChannelDelta(wantPixel.G, gotPixel.G))
			errorSum += uint64(matrixChannelDelta(wantPixel.B, gotPixel.B))
		}
	}
	return errorSum
}

func matrixChannelDelta(left, right uint8) uint8 {
	if left >= right {
		return left - right
	}
	return right - left
}

func assertMatrixSignature(t *testing.T, path string, format images.Format) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var matches bool
	switch format {
	case images.FormatJPEG:
		matches = len(contents) >= 3 && bytes.Equal(contents[:3], []byte{0xff, 0xd8, 0xff})
	case images.FormatPNG:
		matches = len(contents) >= 8 && bytes.Equal(contents[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	case images.FormatWebP:
		matches = len(contents) >= 12 && bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("WEBP"))
	default:
		t.Fatalf("no signature assertion for %s", format)
	}
	if !matches {
		t.Errorf("output %s does not have a %s signature", path, format)
	}
}
