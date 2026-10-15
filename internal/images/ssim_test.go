// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
)

// SSIM is the perceptual acceptance seam documented in docs/divergences.md D-05.
// The image is generated in-process so it does not couple this pure comparator to
// an encoder, a native library, or fabricated stock output.
func TestSSIM_IdentityIsExactlyOne(t *testing.T) {
	original := image.NewGray(image.Rect(0, 0, 3, 2))
	for y := range original.Bounds().Dy() {
		for x := range original.Bounds().Dx() {
			original.SetGray(x, y, color.Gray{Y: uint8(17 + x*41 + y*29)})
		}
	}

	got, err := images.SSIM(original, original)
	if err != nil {
		t.Fatalf("SSIM(identity) returned error: %v", err)
	}
	if got != 1 {
		t.Errorf("SSIM(identity) = %.17g, want 1", got)
	}
}

// A uniform 100-to-50 grayscale change isolates SSIM's luminance term:
// (2*100*50 + 6.5025) / (100² + 50² + 6.5025).
func TestSSIM_UniformKnownDegradationMatchesReferenceValue(t *testing.T) {
	original := solidGray(4, 3, 100)
	degraded := solidGray(4, 3, 50)

	got, err := images.SSIM(original, degraded)
	if err != nil {
		t.Fatalf("SSIM(degraded) returned error: %v", err)
	}
	const want = 0.800103985906531
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("SSIM(uniform 100, uniform 50) = %.15f, want %.15f", got, want)
	}
}

// A whole-image comparator hides this local defect behind the bright half's
// mean. Local-window MSSIM must instead expose the dark-region lift.
func TestSSIM_LocalizedToneShiftFallsBelowLocalQualityThreshold(t *testing.T) {
	original := splitGray(100, 100, 0, 255)
	comparison := splitGray(100, 100, 20, 235)

	got, err := images.SSIM(original, comparison)
	if err != nil {
		t.Fatalf("SSIM(localized tone shift) returned error: %v", err)
	}
	if got >= 0.95 {
		t.Errorf("SSIM(localized tone shift) = %.15f, want < 0.95 to expose the altered dark half", got)
	}
}

// Equal luma does not make red and gray visually identical. Per-channel MSSIM
// must retain chroma information rather than collapse colors to grayscale.
func TestSSIM_EqualLumaDifferentChromaIsNotIdentity(t *testing.T) {
	red := solidNRGBA(8, 8, color.NRGBA{R: 255, A: 255})
	gray := solidNRGBA(8, 8, color.NRGBA{R: 76, G: 76, B: 76, A: 255})

	got, err := images.SSIM(red, gray)
	if err != nil {
		t.Fatalf("SSIM(equal luma colors) returned error: %v", err)
	}
	if got >= 1 {
		t.Errorf("SSIM(red, equal-luma gray) = %.17g, want < 1", got)
	}
}

// Alpha is image content at this seam: transparent black and opaque black must
// not compare as identical even though their premultiplied RGB components match.
func TestSSIM_AlphaDifferenceIsNotIdentity(t *testing.T) {
	transparent := solidNRGBA(8, 8, color.NRGBA{})
	opaque := solidNRGBA(8, 8, color.NRGBA{A: 255})

	got, err := images.SSIM(transparent, opaque)
	if err != nil {
		t.Fatalf("SSIM(alpha difference) returned error: %v", err)
	}
	if got >= 1 {
		t.Errorf("SSIM(transparent black, opaque black) = %.17g, want < 1", got)
	}
}

func TestSSIM_DimensionMismatchReturnsPublicError(t *testing.T) {
	_, err := images.SSIM(solidGray(2, 2, 100), solidGray(3, 2, 100))
	if !errors.Is(err, images.ErrDimensionsMismatch) {
		t.Errorf("SSIM(different dimensions) error = %v, want ErrDimensionsMismatch", err)
	}
}

func solidGray(width, height int, value uint8) *image.Gray {
	result := image.NewGray(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			result.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return result
}

func splitGray(width, height int, left, right uint8) *image.Gray {
	result := image.NewGray(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value := right
			if x < width/2 {
				value = left
			}
			result.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return result
}

func solidNRGBA(width, height int, value color.NRGBA) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			result.SetNRGBA(x, y, value)
		}
	}
	return result
}
