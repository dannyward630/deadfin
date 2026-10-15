// SPDX-License-Identifier: GPL-2.0-only

//go:build linux && amd64

package images_test

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
)

const nativeSkiaWebPQualityZeroLifecycleMode = "DEADFIN_NATIVE_SKIA_WEBP_Q0_LIFECYCLE_MODE"

// TestNativeSkiaWebPQualityZeroLifecycle proves the Linux SkiaSharp Q0 path can
// serve independent work concurrently, release loader-owned native state after
// a codec failure, and serve fresh work afterward. It runs the destructive
// process-lifetime runtime case in a child test process so it cannot depend on
// another native test's execution order.
func TestNativeSkiaWebPQualityZeroLifecycle(t *testing.T) {
	if mode := os.Getenv(nativeSkiaWebPQualityZeroLifecycleMode); mode != "" {
		if mode != "child" {
			t.Fatalf("unknown Skia WebP Q0 lifecycle mode %q", mode)
		}
		testNativeSkiaWebPQualityZeroLifecycle(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestNativeSkiaWebPQualityZeroLifecycle$")
	command.Env = append(os.Environ(), nativeSkiaWebPQualityZeroLifecycleMode+"=child")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Skia WebP Q0 lifecycle child failed: %v\n%s", err, output)
	}
}

func testNativeSkiaWebPQualityZeroLifecycle(t *testing.T) {
	t.Helper()

	runtimeOwner, err := images.OpenRuntime(context.Background())
	if err != nil {
		t.Fatalf("OpenRuntime(): %v", err)
	}
	t.Cleanup(func() {
		if err := runtimeOwner.Close(); err != nil {
			t.Errorf("Runtime.Close(): %v", err)
		}
	})

	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
		CacheRoot:     t.TempDir(),
		MaxConcurrent: 8,
	})
	if err != nil {
		t.Fatalf("NewProcessor(): %v", err)
	}

	stock := mustDecode(t, filepath.Join(stockCorpusRoot, "stock", "webp-landscape-quality-0.webp"))
	wantDimensions := images.Dimensions{Width: 640, Height: 360}
	if got := (images.Dimensions{Width: stock.Bounds().Dx(), Height: stock.Bounds().Dy()}); got != wantDimensions {
		t.Fatalf("quality-zero stock dimensions = %#v, want %#v", got, wantDimensions)
	}

	sourceBytes, err := os.ReadFile(filepath.Join(stockCorpusRoot, "inputs", "landscape.jpg"))
	if err != nil {
		t.Fatalf("ReadFile(quality-zero source): %v", err)
	}

	const workers = 8
	start := make(chan struct{})
	type outcome struct {
		input  string
		result images.ProcessedImage
		err    error
	}
	outcomes := make(chan outcome, workers)
	var ready sync.WaitGroup
	ready.Add(workers)

	for worker := range workers {
		inputPath := filepath.Join(t.TempDir(), fmt.Sprintf("quality-zero-%d.jpg", worker))
		if err := os.WriteFile(inputPath, sourceBytes, 0o600); err != nil {
			t.Fatalf("WriteFile(concurrent quality-zero source %d): %v", worker, err)
		}
		options := images.ProcessOptions{
			Source: images.Source{
				Path:     inputPath,
				Modified: nativeModified.Add(time.Duration(worker+1) * time.Nanosecond),
			},
			OutputFormat: images.FormatWebP,
			Quality:      0,
			AutoOrient:   false,
		}
		go func(input string, options images.ProcessOptions) {
			ready.Done()
			<-start
			result, err := processor.Process(context.Background(), options)
			outcomes <- outcome{input: input, result: result, err: err}
		}(inputPath, options)
	}
	ready.Wait()
	close(start)

	for range workers {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Errorf("Process(concurrent quality-zero %q): %v", outcome.input, outcome.err)
			continue
		}
		assertNativeSkiaWebPQualityZeroOutput(t, outcome.result, stock, wantDimensions)
	}

	failed := images.ProcessOptions{
		Source: images.Source{
			Path:     writeMatrixSVG(t, t.TempDir()),
			Modified: nativeModified.Add(100 * time.Nanosecond),
		},
		OutputFormat: images.FormatWebP,
		Quality:      0,
		AutoOrient:   false,
	}
	if _, err := processor.Process(context.Background(), failed); err == nil {
		t.Fatal("Process(SVG quality-zero) error = nil, want the Skia codec failure after its loader opened")
	} else if !strings.Contains(err.Error(), "SkiaSharp WebP Q0: SkiaSharp could not decode source image") {
		t.Errorf("Process(SVG quality-zero) error = %v, want Skia codec failure", err)
	}

	retryPath := filepath.Join(t.TempDir(), "quality-zero-after-failure.jpg")
	if err := os.WriteFile(retryPath, sourceBytes, 0o600); err != nil {
		t.Fatalf("WriteFile(quality-zero retry source): %v", err)
	}
	retry, err := processor.Process(context.Background(), images.ProcessOptions{
		Source:       images.Source{Path: retryPath, Modified: nativeModified.Add(101 * time.Nanosecond)},
		OutputFormat: images.FormatWebP,
		Quality:      0,
		AutoOrient:   false,
	})
	if err != nil {
		t.Fatalf("Process(quality-zero after Skia codec failure): %v", err)
	}
	assertNativeSkiaWebPQualityZeroOutput(t, retry, stock, wantDimensions)
}

func assertNativeSkiaWebPQualityZeroOutput(t *testing.T, result images.ProcessedImage, stock image.Image, wantDimensions images.Dimensions) {
	t.Helper()
	if result.MIMEType != "image/webp" {
		t.Errorf("Process(quality-zero).MIMEType = %q, want image/webp", result.MIMEType)
	}
	if result.Dimensions != wantDimensions {
		t.Errorf("Process(quality-zero).Dimensions = %#v, want %#v", result.Dimensions, wantDimensions)
	}
	produced := mustDecode(t, result.Path)
	if got := (images.Dimensions{Width: produced.Bounds().Dx(), Height: produced.Bounds().Dy()}); got != wantDimensions {
		t.Errorf("decoded Process(quality-zero) dimensions = %#v, want %#v", got, wantDimensions)
	}
	score, err := images.SSIM(stock, produced)
	if err != nil {
		t.Errorf("SSIM(stock, Process(quality-zero)): %v", err)
		return
	}
	if score < minimumStockCorpusSSIM {
		t.Errorf("SSIM(stock, Process(quality-zero)) = %.6f, want >= %.2f", score, minimumStockCorpusSSIM)
	}
}
