// SPDX-License-Identifier: GPL-2.0-only

//go:build !linux || !cgo

package images

import "fmt"

func encodeSkiaWebPQualityZero(_ string, _ Dimensions) ([]byte, error) {
	return nil, fmt.Errorf("SkiaSharp WebP Q0 requires the packaged Linux runtime")
}

func decodeSkiaBlurHashPixels(_ string, _ int) (BlurHashPixels, error) {
	return BlurHashPixels{}, ErrSkiaBlurHashUnavailable
}
