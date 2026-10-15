// SPDX-License-Identifier: GPL-2.0-only

package images

import (
	"errors"
	"image"
	"image/color"
)

// ErrDimensionsMismatch is returned when SSIM receives differently sized images.
var ErrDimensionsMismatch = errors.New("image dimensions mismatch")

const (
	ssimL          = 255.0
	ssimK1         = 0.01
	ssimK2         = 0.03
	ssimC1         = ssimK1 * ssimK1 * ssimL * ssimL
	ssimC2         = ssimK2 * ssimK2 * ssimL * ssimL
	ssimWindowSize = 8
)

// SSIM calculates mean structural similarity from independent 8×8 windows.
// It compares premultiplied red, green, and blue channels, so RGB hidden by
// alpha does not influence the score. If either image has non-opaque pixels,
// alpha is included as a fourth channel.
func SSIM(original, comparison image.Image) (float64, error) {
	originalBounds := original.Bounds()
	comparisonBounds := comparison.Bounds()
	width, height := originalBounds.Dx(), originalBounds.Dy()
	if width != comparisonBounds.Dx() || height != comparisonBounds.Dy() {
		return 0, ErrDimensionsMismatch
	}
	if width == 0 || height == 0 {
		return 1, nil
	}
	originalSampler := newPixelSampler(original)
	comparisonSampler := newPixelSampler(comparison)

	var rgbTotal, alphaTotal, pixelCount float64
	hasTransparency := false
	for y := 0; y < height; y += ssimWindowSize {
		windowHeight := min(ssimWindowSize, height-y)
		for x := 0; x < width; x += ssimWindowSize {
			windowWidth := min(ssimWindowSize, width-x)
			score, transparent := ssimWindow(originalSampler, comparisonSampler, originalBounds.Min.X+x, originalBounds.Min.Y+y, comparisonBounds.Min.X+x, comparisonBounds.Min.Y+y, windowWidth, windowHeight)
			weight := float64(windowWidth * windowHeight)
			rgbTotal += (score[0] + score[1] + score[2]) * weight
			alphaTotal += score[3] * weight
			pixelCount += weight
			hasTransparency = hasTransparency || transparent
		}
	}

	if hasTransparency {
		return (rgbTotal + alphaTotal) / (pixelCount * 4), nil
	}
	return rgbTotal / (pixelCount * 3), nil
}

func ssimWindow(original, comparison pixelSampler, originalX, originalY, comparisonX, comparisonY, width, height int) ([4]float64, bool) {
	var originalMean, comparisonMean [4]float64
	var originalVariance, comparisonVariance, covariance [4]float64
	count := 0.0
	transparent := false
	for y := range height {
		for x := range width {
			originalValue := original(originalX+x, originalY+y)
			comparisonValue := comparison(comparisonX+x, comparisonY+y)
			transparent = transparent || originalValue[3] != 255 || comparisonValue[3] != 255

			count++
			for channel := range 4 {
				originalDelta := originalValue[channel] - originalMean[channel]
				originalMean[channel] += originalDelta / count
				comparisonDelta := comparisonValue[channel] - comparisonMean[channel]
				comparisonMean[channel] += comparisonDelta / count
				originalVariance[channel] += originalDelta * (originalValue[channel] - originalMean[channel])
				comparisonVariance[channel] += comparisonDelta * (comparisonValue[channel] - comparisonMean[channel])
				covariance[channel] += originalDelta * (comparisonValue[channel] - comparisonMean[channel])
			}
		}
	}

	var score [4]float64
	for channel := range 4 {
		originalVariance[channel] /= count
		comparisonVariance[channel] /= count
		covariance[channel] /= count
		score[channel] = ((2*originalMean[channel]*comparisonMean[channel] + ssimC1) * (2*covariance[channel] + ssimC2)) /
			((originalMean[channel]*originalMean[channel] + comparisonMean[channel]*comparisonMean[channel] + ssimC1) * (originalVariance[channel] + comparisonVariance[channel] + ssimC2))
	}
	return score, transparent
}

func rgbaValue(value color.Color) [4]float64 {
	red, green, blue, alpha := value.RGBA()
	return [4]float64{
		float64(red) / 257,
		float64(green) / 257,
		float64(blue) / 257,
		float64(alpha) / 257,
	}
}

type pixelSampler func(x, y int) [4]float64

func newPixelSampler(source image.Image) pixelSampler {
	switch source := source.(type) {
	case *image.NRGBA:
		return func(x, y int) [4]float64 {
			offset := source.PixOffset(x, y)
			return rgba8Unpremultiplied(source.Pix[offset], source.Pix[offset+1], source.Pix[offset+2], source.Pix[offset+3])
		}
	case *image.RGBA:
		return func(x, y int) [4]float64 {
			offset := source.PixOffset(x, y)
			return rgba8Premultiplied(source.Pix[offset], source.Pix[offset+1], source.Pix[offset+2], source.Pix[offset+3])
		}
	case *image.NRGBA64:
		return func(x, y int) [4]float64 {
			offset := source.PixOffset(x, y)
			return rgba16Unpremultiplied(readUint16(source.Pix[offset:]), readUint16(source.Pix[offset+2:]), readUint16(source.Pix[offset+4:]), readUint16(source.Pix[offset+6:]))
		}
	case *image.RGBA64:
		return func(x, y int) [4]float64 {
			offset := source.PixOffset(x, y)
			return rgba16Premultiplied(readUint16(source.Pix[offset:]), readUint16(source.Pix[offset+2:]), readUint16(source.Pix[offset+4:]), readUint16(source.Pix[offset+6:]))
		}
	case *image.Gray:
		return func(x, y int) [4]float64 {
			gray := float64(source.Pix[source.PixOffset(x, y)])
			return [4]float64{gray, gray, gray, 255}
		}
	case *image.Gray16:
		return func(x, y int) [4]float64 {
			gray := float64(readUint16(source.Pix[source.PixOffset(x, y):])) / 257
			return [4]float64{gray, gray, gray, 255}
		}
	case *image.Alpha:
		return func(x, y int) [4]float64 {
			alpha := float64(source.Pix[source.PixOffset(x, y)])
			return [4]float64{alpha, alpha, alpha, alpha}
		}
	case *image.Alpha16:
		return func(x, y int) [4]float64 {
			alpha := float64(readUint16(source.Pix[source.PixOffset(x, y):])) / 257
			return [4]float64{alpha, alpha, alpha, alpha}
		}
	case *image.YCbCr:
		return func(x, y int) [4]float64 {
			yOffset := source.YOffset(x, y)
			cOffset := source.COffset(x, y)
			red, green, blue := color.YCbCrToRGB(source.Y[yOffset], source.Cb[cOffset], source.Cr[cOffset])
			return [4]float64{float64(red), float64(green), float64(blue), 255}
		}
	default:
		return func(x, y int) [4]float64 { return rgbaValue(source.At(x, y)) }
	}
}

func rgba8Premultiplied(red, green, blue, alpha uint8) [4]float64 {
	return [4]float64{float64(red), float64(green), float64(blue), float64(alpha)}
}

func rgba8Unpremultiplied(red, green, blue, alpha uint8) [4]float64 {
	return [4]float64{
		premultiply8(red, alpha),
		premultiply8(green, alpha),
		premultiply8(blue, alpha),
		float64(alpha),
	}
}

func premultiply8(value, alpha uint8) float64 {
	return float64(uint32(value)*0x101*uint32(alpha)*0x101/0xffff) / 257
}

func rgba16Premultiplied(red, green, blue, alpha uint16) [4]float64 {
	return [4]float64{float64(red) / 257, float64(green) / 257, float64(blue) / 257, float64(alpha) / 257}
}

func rgba16Unpremultiplied(red, green, blue, alpha uint16) [4]float64 {
	return [4]float64{
		float64(uint32(red)*uint32(alpha)/0xffff) / 257,
		float64(uint32(green)*uint32(alpha)/0xffff) / 257,
		float64(uint32(blue)*uint32(alpha)/0xffff) / 257,
		float64(alpha) / 257,
	}
}

func readUint16(value []uint8) uint16 {
	return uint16(value[0])<<8 | uint16(value[1])
}
