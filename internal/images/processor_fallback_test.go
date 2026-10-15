// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bufio"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
	"golang.org/x/image/bmp"
)

// testProcessorFallback is called from TestNativeProcessor so every image test
// shares its one process-lifetime libvips owner.
func testProcessorFallback(t *testing.T, runtimeOwner *images.Runtime) {
	t.Helper()

	t.Run("bypasses conversion for natively supported sources", func(t *testing.T) {
		sourceDirectory := t.TempDir()
		cases := []struct {
			name  string
			write func(*testing.T, string) string
		}{
			{name: "JPEG .jpg", write: writeJPEGSource},
			{name: "JPEG .jpeg", write: writeJPEGFallbackSource},
			{name: "PNG", write: writePNGSource},
			{name: "WebP", write: writeWebPSource},
			{name: "GIF", write: writeGIFFallbackSource},
			{name: "BMP", write: writeBMPFallbackSource},
			{name: "SVG", write: writeSVGSource},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				converter := fallbackConverterFunc(func(context.Context, string, string) error {
					return errors.New("native source was incorrectly sent to converter")
				})
				processor := newFallbackProcessor(t, runtimeOwner, t.TempDir(), 1, converter)
				got := mustProcess(t, processor, context.Background(), nativeOptions(tc.write(t, sourceDirectory), images.FormatPNG, images.ResizeOptions{}))
				if got.MIMEType != "image/png" {
					t.Errorf("Process().MIMEType = %q, want image/png", got.MIMEType)
				}
			})
		}
	})

	t.Run("converts unsupported source to alpha-safe PNG before normal processing", func(t *testing.T) {
		cacheRoot := t.TempDir()
		sourcePath := writeUnsupportedFallbackSource(t, t.TempDir(), "cover.avif")
		options := nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{Width: nativeInt(4)})
		options.Source.Modified = time.Date(2025, time.February, 3, 4, 5, 6, 700000000, time.UTC)

		var inputPath, outputPath string
		converterCalls := 0
		converter := fallbackConverterFunc(func(_ context.Context, input, output string) error {
			converterCalls++
			inputPath, outputPath = input, output
			return writeFallbackPNG(output, 8, 4, true)
		})
		processor := newFallbackProcessor(t, runtimeOwner, cacheRoot, 1, converter)
		got := mustProcess(t, processor, context.Background(), options)

		if inputPath != sourcePath {
			t.Errorf("Converter input = %q, want original source %q", inputPath, sourcePath)
		}
		if converterCalls != 1 {
			t.Errorf("Converter calls = %d, want exactly one", converterCalls)
		}
		if filepath.Ext(outputPath) != ".png" {
			t.Errorf("Converter output = %q, want temporary PNG path", outputPath)
		}
		if got.Path != cachePathFor(options, cacheRoot) {
			t.Errorf("Process().Path = %q, want cache identity from original path and modified time %q", got.Path, cachePathFor(options, cacheRoot))
		}
		if got.MIMEType != "image/png" {
			t.Errorf("Process().MIMEType = %q, want image/png", got.MIMEType)
		}
		if got.Dimensions != (images.Dimensions{Width: 4, Height: 2}) {
			t.Errorf("Process().Dimensions = %#v, want 4x2 after normal resize", got.Dimensions)
		}
		assertImageDimensions(t, got.Path, images.Dimensions{Width: 4, Height: 2})
		assertEncodedFormat(t, got.Path, "png")
		decoded := mustDecode(t, got.Path)
		hasTransparency := false
		for y := range decoded.Bounds().Dy() {
			for x := range decoded.Bounds().Dx() {
				if pixel := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA); pixel.A < 0xff {
					hasTransparency = true
				}
			}
		}
		if !hasTransparency {
			t.Error("converted PNG lost all alpha while resizing")
		}
		opaque := color.NRGBAModel.Convert(decoded.At(3, 1)).(color.NRGBA)
		if opaque.A != 0xff || opaque.R*3 >= opaque.B {
			t.Errorf("opaque converted pixel = %#v, want unpolluted opaque blue", opaque)
		}
		assertRemoved(t, outputPath)
	})

	t.Run("removes conversion output and publishes no cache artifact on failures", func(t *testing.T) {
		conversionFailure := errors.New("converter failed")
		cases := []struct {
			name    string
			convert func(string) error
			want    error
		}{
			{
				name: "converter error",
				convert: func(output string) error {
					if err := os.WriteFile(output, []byte("partial conversion"), 0o600); err != nil {
						return err
					}
					return conversionFailure
				},
				want: conversionFailure,
			},
			{
				name:    "missing converter output",
				convert: func(string) error { return nil },
			},
			{
				name: "malformed converter output",
				convert: func(output string) error {
					return os.WriteFile(output, []byte("not a PNG"), 0o600)
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cacheRoot := t.TempDir()
				sourcePath := writeUnsupportedFallbackSource(t, t.TempDir(), "broken.avif")
				options := nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{})
				var outputPath string
				converterCalls := 0
				converter := fallbackConverterFunc(func(_ context.Context, _, output string) error {
					converterCalls++
					outputPath = output
					return tc.convert(output)
				})
				processor := newFallbackProcessor(t, runtimeOwner, cacheRoot, 1, converter)
				_, err := processor.Process(context.Background(), options)
				if err == nil {
					t.Fatal("Process() error = nil, want conversion/decode failure")
				}
				if tc.want != nil && !errors.Is(err, tc.want) {
					t.Errorf("Process() error = %v, want wrapped %v", err, tc.want)
				}
				if converterCalls != 1 {
					t.Errorf("Converter calls = %d, want exactly one", converterCalls)
				}
				assertRemoved(t, outputPath)
				assertNoCacheArtifact(t, cacheRoot, options)
			})
		}
	})

	t.Run("cancellation interrupts conversion and removes its temporary output", func(t *testing.T) {
		cacheRoot := t.TempDir()
		sourcePath := writeUnsupportedFallbackSource(t, t.TempDir(), "cancel.avif")
		started := make(chan struct{})
		var outputPath string
		converter := fallbackConverterFunc(func(ctx context.Context, _, output string) error {
			outputPath = output
			if err := writeFallbackPNG(output, 8, 4, true); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		processor := newFallbackProcessor(t, runtimeOwner, cacheRoot, 1, converter)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := processor.Process(ctx, nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{}))
			result <- err
		}()
		<-started
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("Process(canceled conversion) error = %v, want context.Canceled", err)
		}
		assertRemoved(t, outputPath)
		assertNoCacheArtifact(t, cacheRoot, nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{}))
	})

	t.Run("counts conversion work against maximum concurrency", func(t *testing.T) {
		cacheRoot := t.TempDir()
		releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
		started := make(chan int, 2)
		var mu sync.Mutex
		active, maximum := 0, 0
		calls := 0
		converter := fallbackConverterFunc(func(_ context.Context, _, output string) error {
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
			if err := writeFallbackPNG(output, 8, 4, false); err != nil {
				return err
			}
			started <- call
			<-releases[call]
			return nil
		})
		processor := newFallbackProcessor(t, runtimeOwner, cacheRoot, 1, converter)
		sources := []string{
			writeUnsupportedFallbackSource(t, t.TempDir(), "first.avif"),
			writeUnsupportedFallbackSource(t, t.TempDir(), "second.avif"),
		}
		errs := make(chan error, len(sources))
		for _, source := range sources {
			go func(source string) {
				_, err := processor.Process(context.Background(), nativeOptions(source, images.FormatPNG, images.ResizeOptions{}))
				errs <- err
			}(source)
		}
		if call := <-started; call != 0 {
			t.Fatalf("first converter call = %d, want 0", call)
		}
		close(releases[0])
		if call := <-started; call != 1 {
			t.Fatalf("second converter call = %d, want 1", call)
		}
		close(releases[1])
		for range sources {
			if err := <-errs; err != nil {
				t.Errorf("concurrent Process() error: %v", err)
			}
		}
		mu.Lock()
		gotMaximum := maximum
		mu.Unlock()
		if gotMaximum != 1 {
			t.Errorf("maximum simultaneous conversions = %d, want 1 from ProcessorConfig.MaxConcurrent", gotMaximum)
		}
	})

	t.Run("command converter has deterministic arguments and rejects bad process output", func(t *testing.T) {
		argumentFile := filepath.Join(t.TempDir(), "argv")
		outputPath := filepath.Join(t.TempDir(), "converted.png")
		inputPath := filepath.Join(t.TempDir(), "source.avif")
		if err := os.WriteFile(inputPath, []byte("unsupported"), 0o600); err != nil {
			t.Fatal(err)
		}
		pngFixture := filepath.Join(t.TempDir(), "adapter-output.png")
		if err := writeFallbackPNG(pngFixture, 1, 1, false); err != nil {
			t.Fatal(err)
		}
		jpegFixture := filepath.Join(t.TempDir(), "adapter-output.jpg")
		writeJPEG(t, jpegFixture, markerImage())
		fake := writeFallbackCommand(t, `
:	"${ARGV_FILE:?}"
:	"${PNG_FIXTURE:?}"
printf '%s\n' "$@" > "$ARGV_FILE"
case "${MODE:-success}" in
success) cp "$PNG_FIXTURE" "${11}" ;;
jpeg) cp "$JPEG_FIXTURE" "${11}" ;;
symlink) ln -s "$PNG_FIXTURE" "${11}" ;;
missing) : ;;
failure) exit 17 ;;
wait) : "${READY_FIFO:?}"; printf 'ready\n' > "$READY_FIFO"; while :; do :; done ;;
*) exit 19 ;;
esac`)
		t.Setenv("ARGV_FILE", argumentFile)
		t.Setenv("PNG_FIXTURE", pngFixture)
		t.Setenv("JPEG_FIXTURE", jpegFixture)
		t.Setenv("MODE", "success")
		converter := images.FFmpegConverter{Path: fake}
		if err := converter.Convert(context.Background(), inputPath, outputPath); err != nil {
			t.Fatalf("CommandConverter.Convert() error: %v", err)
		}
		arguments, err := os.ReadFile(argumentFile)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := splitArguments(string(arguments)), []string{"-nostdin", "-y", "-i", inputPath, "-map", "0:v:0", "-frames:v", "1", "-c:v", "png", outputPath}; !reflect.DeepEqual(got, want) {
			t.Errorf("CommandConverter argv = %#v, want %#v", got, want)
		}

		t.Run("cancellation terminates a running command", func(t *testing.T) {
			readyFIFO := filepath.Join(t.TempDir(), "ready")
			if err := syscall.Mkfifo(readyFIFO, 0o600); err != nil {
				t.Fatalf("Mkfifo(%q): %v", readyFIFO, err)
			}
			t.Setenv("MODE", "wait")
			t.Setenv("READY_FIFO", readyFIFO)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- converter.Convert(ctx, inputPath, filepath.Join(t.TempDir(), "canceled.png"))
			}()
			reader, err := os.Open(readyFIFO)
			if err != nil {
				t.Fatalf("Open(%q): %v", readyFIFO, err)
			}
			t.Cleanup(func() {
				if err := reader.Close(); err != nil {
					t.Errorf("close command ready pipe: %v", err)
				}
			})
			ready, err := bufio.NewReader(reader).ReadString('\n')
			if err != nil {
				t.Fatalf("read command ready signal: %v", err)
			}
			if ready != "ready\n" {
				t.Fatalf("command ready signal = %q, want ready", ready)
			}
			cancel()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("CommandConverter.Convert(canceled context) error = %v, want context.Canceled", err)
			}
		})

		t.Run("rejects symlink and non-PNG command outputs without caching", func(t *testing.T) {
			for _, tc := range []struct {
				name string
				mode string
			}{
				{name: "symlink", mode: "symlink"},
				{name: "JPEG bytes named PNG", mode: "jpeg"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cacheRoot := t.TempDir()
					sourcePath := writeUnsupportedFallbackSource(t, t.TempDir(), "invalid.avif")
					options := nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{})
					t.Setenv("MODE", tc.mode)
					processor := newFallbackProcessor(t, runtimeOwner, cacheRoot, 1, converter)
					if _, err := processor.Process(context.Background(), options); err == nil {
						t.Fatal("Process() error = nil, want command output validation failure")
					}
					arguments, err := os.ReadFile(argumentFile)
					if err != nil {
						t.Fatalf("ReadFile(command argv): %v", err)
					}
					argv := splitArguments(string(arguments))
					if len(argv) == 0 {
						t.Fatal("command wrote no argv")
					}
					assertRemoved(t, argv[len(argv)-1])
					assertNoCacheArtifact(t, cacheRoot, options)
				})
			}
		})

		for _, tc := range []struct {
			name string
			mode string
		}{
			{name: "missing output", mode: "missing"},
			{name: "nonzero exit", mode: "failure"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("MODE", tc.mode)
				if err := converter.Convert(context.Background(), inputPath, filepath.Join(t.TempDir(), "out.png")); err == nil {
					t.Fatal("CommandConverter.Convert() error = nil, want rejected command result")
				}
			})
		}
	})
}

type fallbackConverterFunc func(context.Context, string, string) error

func (convert fallbackConverterFunc) Convert(ctx context.Context, inputPath, outputPath string) error {
	return convert(ctx, inputPath, outputPath)
}

func newFallbackProcessor(t *testing.T, runtimeOwner *images.Runtime, cacheRoot string, maximum int, converter images.SourceConverter) *images.Processor {
	t.Helper()
	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
		CacheRoot:       cacheRoot,
		MaxConcurrent:   maximum,
		SourceConverter: converter,
	})
	if err != nil {
		t.Fatalf("NewProcessor() error: %v", err)
	}
	return processor
}

func writeJPEGFallbackSource(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "source.jpeg")
	writeJPEG(t, path, markerImage())
	return path
}

func writeGIFFallbackSource(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "source.gif")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(file, markerImage(), nil); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeBMPFallbackSource(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "source.bmp")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bmp.Encode(file, markerImage()); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeUnsupportedFallbackSource(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("unsupported image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFallbackPNG(path string, width, height int, transparent bool) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	decoded := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			decoded.SetNRGBA(x, y, color.NRGBA{R: 0x33, G: 0x66, B: 0xcc, A: 0xff})
		}
	}
	if transparent {
		// A distinct RGB value at zero alpha catches incorrect non-premultiplied
		// resizing that bleeds invisible red into visible pixels.
		decoded.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0})
	}
	return png.Encode(file, decoded)
}

func assertEncodedFormat(t *testing.T, path, want string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	_, got, decodeErr := image.Decode(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatalf("Decode(%q): %v", path, decodeErr)
	}
	if closeErr != nil {
		t.Fatalf("Close(%q): %v", path, closeErr)
	}
	if got != want {
		t.Errorf("encoded image format = %q, want %q", got, want)
	}
}

func assertRemoved(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("converter never received a temporary output path")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary converted output %q still exists or could not be statted: %v", path, err)
	}
}

func assertNoCacheArtifact(t *testing.T, cacheRoot string, options images.ProcessOptions) {
	t.Helper()
	cachePath := cachePathFor(options, cacheRoot)
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed Process() cache artifact %q exists or could not be statted: %v", cachePath, err)
	}
}

func writeFallbackCommand(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "converter")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func splitArguments(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func testRuntimeCloseWaitsForFallback(t *testing.T, runtimeOwner *images.Runtime) {
	t.Helper()
	sourcePath := writeUnsupportedFallbackSource(t, t.TempDir(), "close.avif")
	conversionStarted := make(chan struct{})
	releaseConversion := make(chan struct{})
	var convertedPath string
	converter := fallbackConverterFunc(func(_ context.Context, _, output string) error {
		convertedPath = output
		close(conversionStarted)
		<-releaseConversion
		return writeFallbackPNG(output, 8, 4, false)
	})
	processor := newFallbackProcessor(t, runtimeOwner, t.TempDir(), 1, converter)
	processResult := make(chan error, 1)
	go func() {
		_, err := processor.Process(context.Background(), nativeOptions(sourcePath, images.FormatPNG, images.ResizeOptions{}))
		processResult <- err
	}()
	<-conversionStarted

	closeAttempted := make(chan struct{})
	closeResult := make(chan error, 1)
	go func() {
		close(closeAttempted)
		closeResult <- runtimeOwner.Close()
	}()
	<-closeAttempted
	close(releaseConversion)
	if err := <-processResult; err != nil {
		t.Fatalf("Process() during Runtime.Close() error = %v, want fallback conversion and processing to complete", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
	assertRemoved(t, convertedPath)
}
