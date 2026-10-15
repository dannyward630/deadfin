// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package images

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

// This is the Standalone Movie primary poster source. The fixture's pinned stock
// /Items response records the same 6x4 hash below. It is calculated here with
// the official Wolt encoder algorithm, independently from providers, so this
// test isolates Skia's two-stage decode-and-scale path for the 128px BlurHash box.
func TestDecodeSkiaBlurHashPixelsMatchesStockPoster(t *testing.T) {
	pixels, err := DecodeSkiaBlurHashPixels(filepath.Join("testdata", "stock-corpus", "inputs", "landscape.jpg"))
	if err != nil {
		t.Fatalf("decode canonical poster through bundled libSkiaSharp: %v", err)
	}
	if got, want := pixels.Bounds().Dx(), 32; got != want {
		t.Fatalf("Skia poster raster width = %d, want %d", got, want)
	}
	if got, want := pixels.Bounds().Dy(), 18; got != want {
		t.Fatalf("Skia poster raster height = %d, want %d", got, want)
	}
	if got, want := len(pixels.RGBA), 32*18*4; got != want {
		t.Fatalf("Skia poster RGBA length = %d, want %d", got, want)
	}
	if got, want := hex.EncodeToString(pixels.RGBA[:16]), "816744ff1e6b49ff97cf4aff253451ff"; got != want {
		t.Fatalf("Skia poster RGBA first 16 bytes = %q, want %q", got, want)
	}
	if sum := sha256.Sum256(pixels.RGBA); hex.EncodeToString(sum[:]) != "9ff7c789eab852d17c9711cbade9f43cb6d2dd48dc3de5f61ffe652c1cac5b0f" {
		t.Fatalf("Skia poster RGBA SHA-256 = %x, want %s", sum, "9ff7c789eab852d17c9711cbade9f43cb6d2dd48dc3de5f61ffe652c1cac5b0f")
	}
	if got, want := stockBlurHashReference(pixels, 6, 4), "W8G+RE%NbF%MbZx]?coXn#k9oIoebMSQbFS2bZOA?dbTnzbXn~k9"; got != want {
		t.Fatalf("Skia poster BlurHash = %q, want pinned stock hash %q", got, want)
	}
}

// TestDecodeSkiaBlurHashPixelsWebPMatchesStockUnderRepeatedConcurrentCalls
// protects the exact SkiaSharp WebP pixel contract, not merely successful
// decoding. The scanner asks this bridge concurrently for the Folder and Movie
// that share Subtitle Sample's basename artwork.
func TestDecodeSkiaBlurHashPixelsWebPMatchesStockUnderRepeatedConcurrentCalls(t *testing.T) {
	source := filepath.Join("testdata", "stock-corpus", "inputs", "texture.webp")
	for iteration := 0; iteration != 3; iteration++ {
		pixels, err := DecodeSkiaBlurHashPixels(source)
		if err != nil {
			t.Fatalf("decode WebP iteration %d: %v", iteration, err)
		}
		assertStockWebPBlurHashPixels(t, pixels)
	}

	const workers = 32
	type result struct {
		pixels BlurHashPixels
		err    error
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			pixels, err := DecodeSkiaBlurHashPixels(source)
			results <- result{pixels: pixels, err: err}
		}()
	}
	group.Wait()
	close(results)
	for value := range results {
		if value.err != nil {
			t.Fatalf("concurrent WebP decode: %v", value.err)
		}
		assertStockWebPBlurHashPixels(t, value.pixels)
	}
}

func assertStockWebPBlurHashPixels(t *testing.T, pixels BlurHashPixels) {
	t.Helper()
	if got, want := pixels.Bounds().Dx(), 26; got != want {
		t.Fatalf("Skia WebP raster width = %d, want %d", got, want)
	}
	if got, want := pixels.Bounds().Dy(), 14; got != want {
		t.Fatalf("Skia WebP raster height = %d, want %d", got, want)
	}
	if got, want := len(pixels.RGBA), 26*14*4; got != want {
		t.Fatalf("Skia WebP RGBA length = %d, want %d", got, want)
	}
	if got, want := hex.EncodeToString(pixels.RGBA[:16]), "9c2e6eff49adafffa92d99f0a8a838fa"; got != want {
		t.Fatalf("Skia WebP RGBA first 16 bytes = %q, want %q", got, want)
	}
	if sum := sha256.Sum256(pixels.RGBA); hex.EncodeToString(sum[:]) != "70828a04fef438671647fa9bda81599d55454963a70d9940751c4f3c1fe86c5f" {
		t.Fatalf("Skia WebP RGBA SHA-256 = %x, want %s", sum, "70828a04fef438671647fa9bda81599d55454963a70d9940751c4f3c1fe86c5f")
	}
}

type stockBlurFactor [3]float64

func stockBlurHashReference(source image.Image, componentsX, componentsY int) string {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	factors := make([]stockBlurFactor, 0, componentsX*componentsY)
	for yComponent := range componentsY {
		for xComponent := range componentsX {
			normalization := 2.0
			if xComponent == 0 && yComponent == 0 {
				normalization = 1
			}
			var factor stockBlurFactor
			for x := range width {
				for y := range height {
					r, g, b, _ := source.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
					basis := normalization * math.Cos(math.Pi*float64(xComponent*x)/float64(width)) * math.Cos(math.Pi*float64(yComponent*y)/float64(height))
					factor[0] += basis * stockSRGBToLinear(float64(uint8(r>>8)))
					factor[1] += basis * stockSRGBToLinear(float64(uint8(g>>8)))
					factor[2] += basis * stockSRGBToLinear(float64(uint8(b>>8)))
				}
			}
			factor[0] /= float64(width * height)
			factor[1] /= float64(width * height)
			factor[2] /= float64(width * height)
			factors = append(factors, factor)
		}
	}

	maximum := 0.0
	for _, factor := range factors[1:] {
		for _, value := range factor {
			maximum = math.Max(maximum, math.Abs(value))
		}
	}
	quantizedMaximum := int(math.Max(0, math.Min(82, math.Floor(maximum*166-0.5))))
	actualMaximum := float64(quantizedMaximum+1) / 166
	result := stockBlur83(componentsX-1+(componentsY-1)*9, 1) + stockBlur83(quantizedMaximum, 1)
	dc := factors[0]
	result += stockBlur83(stockLinearToSRGB(dc[0])*65536+stockLinearToSRGB(dc[1])*256+stockLinearToSRGB(dc[2]), 4)
	for _, factor := range factors[1:] {
		result += stockBlur83(stockQuantizeAC(factor[0], actualMaximum)*19*19+stockQuantizeAC(factor[1], actualMaximum)*19+stockQuantizeAC(factor[2], actualMaximum), 2)
	}
	return result
}

func stockSRGBToLinear(value float64) float64 {
	value /= 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func stockLinearToSRGB(value float64) int {
	if value <= 0.0031308 {
		return int(math.Max(0, math.Min(255, math.Floor(value*12.92*255+0.5))))
	}
	return int(math.Max(0, math.Min(255, math.Floor((1.055*math.Pow(value, 1/2.4)-0.055)*255+0.5))))
}

func stockQuantizeAC(value, maximum float64) int {
	normalized := math.Copysign(math.Sqrt(math.Abs(value)/maximum), value)
	return int(math.Max(0, math.Min(18, math.Floor(normalized*9+9.5))))
}

func stockBlur83(value, length int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"
	encoded := make([]byte, length)
	for index := range encoded {
		encoded[index] = alphabet[(value/int(math.Pow(83, float64(length-index-1))))%83]
	}
	return string(encoded)
}
