// SPDX-License-Identifier: GPL-2.0-only

package images

import (
	"errors"
	"image"
	"image/color"
)

// ErrSkiaBlurHashUnavailable reports that the optional packaged Linux Skia
// runtime cannot be used. Callers may use the pure-Go decoder in this case.
var ErrSkiaBlurHashUnavailable = errors.New("SkiaSharp BlurHash pixels unavailable")

// ErrSkiaBlurHashUnsupported reports a source codec Skia does not support.
// Callers may use the pure-Go decoder in this case.
var ErrSkiaBlurHashUnsupported = errors.New("SkiaSharp BlurHash source unsupported")

// BlurHashPixels is an owned, Skia-decoded RGBA raster used for stock-compatible
// BlurHash calculation. It implements image.Image without a second pixel copy.
type BlurHashPixels struct {
	Width  int
	Height int
	RGBA   []byte
}

func (pixels BlurHashPixels) ColorModel() color.Model { return color.NRGBAModel }

func (pixels BlurHashPixels) Bounds() image.Rectangle {
	return image.Rect(0, 0, pixels.Width, pixels.Height)
}

func (pixels BlurHashPixels) At(x, y int) color.Color {
	if x < 0 || x >= pixels.Width || y < 0 || y >= pixels.Height {
		return color.NRGBA{}
	}
	offset := (y*pixels.Width + x) * 4
	return color.NRGBA{R: pixels.RGBA[offset], G: pixels.RGBA[offset+1], B: pixels.RGBA[offset+2], A: pixels.RGBA[offset+3]}
}

// DecodeSkiaBlurHashPixels mirrors BlurHashSharp.SkiaSharp's two-stage 128x128
// maximum policy: codec scaling, then SKBitmap.Resize by the same factor.
func DecodeSkiaBlurHashPixels(path string) (BlurHashPixels, error) {
	return decodeSkiaBlurHashPixels(path, 128)
}
