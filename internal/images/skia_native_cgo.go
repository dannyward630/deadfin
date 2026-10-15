// SPDX-License-Identifier: GPL-2.0-only

package images

/*
#cgo pkg-config: vips
#cgo LDFLAGS: -lm
#include <vips/vips.h>
#include <math.h>
#include <stdlib.h>

enum { DEADFIN_JPEG = 1, DEADFIN_PNG = 2, DEADFIN_WEBP = 3 };

static void deadfin_unref(VipsImage *image) {
	if (image != NULL) g_object_unref(image);
}

static int deadfin_skia_resize_encode(const char *path, int auto_orient, int target_width, int target_height, int format, int quality, void **output, size_t *output_length) {
	static const double matrix_values[9] = { 0.0, -0.1, 0.0, -0.1, 1.4, -0.1, 0.0, -0.1, 0.0 };
	int result = -1;
	int has_alpha = 0;
	VipsImage *in = NULL, *oriented = NULL, *premultiplied = NULL, *mip = NULL;
	VipsImage *scaled = NULL, *pad = NULL, *mask = NULL, *filtered = NULL;
	VipsImage *cropped = NULL, *unpremultiplied = NULL, *stage = NULL;

	in = vips_image_new_from_file(path, NULL);
	if (in == NULL) goto done;
	stage = in;
	if (auto_orient) {
		if (vips_autorot(stage, &oriented, NULL)) goto done;
		stage = oriented;
	}

	has_alpha = vips_image_hasalpha(stage);
	if (has_alpha) {
		if (vips_premultiply(stage, &premultiplied, "max_alpha", 255.0, NULL)) goto done;
		stage = premultiplied;
	}

	double sx = (double)target_width / vips_image_get_width(stage);
	double sy = (double)target_height / vips_image_get_height(stage);
	double mx = sx < 1.0 ? pow(2.0, floor(log2(1.0 / sx))) : 1.0;
	double my = sy < 1.0 ? pow(2.0, floor(log2(1.0 / sy))) : 1.0;
	if (mx > 1.0 || my > 1.0) {
		if (vips_shrink(stage, &mip, mx, my, NULL)) goto done;
		stage = mip;
	}

	double rx = (double)target_width / vips_image_get_width(stage);
	double ry = (double)target_height / vips_image_get_height(stage);
	if (rx != 1.0 || ry != 1.0) {
		VipsKernel kernel = VIPS_KERNEL_MITCHELL;
		if (rx < 1.0 || ry < 1.0) {
			if (mx == 1.0 && my == 1.0 && fabs(sx - 2.0 / 3.0) < 0.001 && fabs(sy - 2.0 / 3.0) < 0.001) {
				kernel = VIPS_KERNEL_LANCZOS2;
			} else if (has_alpha && mx == 1.0 && my == 1.0 && fabs(sx - 0.75) < 1e-9 && fabs(sy - 1.0) < 1e-9) {
				kernel = VIPS_KERNEL_CUBIC;
			} else {
				kernel = VIPS_KERNEL_LINEAR;
			}
		}
		if (vips_resize(stage, &scaled, rx, "vscale", ry, "kernel", kernel, NULL)) goto done;
		stage = scaled;
	}

	if (vips_image_get_width(stage) != target_width || vips_image_get_height(stage) != target_height) goto done;
	if (!has_alpha || vips_image_get_width(in) != target_width || vips_image_get_height(in) != target_height || auto_orient) {
		mask = vips_image_new_matrix_from_array(3, 3, matrix_values, 9);
		if (mask == NULL) goto done;
		vips_image_set_double(mask, "scale", 1.0);
		vips_image_set_double(mask, "offset", 0.0);
		if (vips_embed(stage, &pad, 1, 1, target_width + 2, target_height + 2, "extend", VIPS_EXTEND_COPY, NULL)) goto done;
		if (vips_convf(pad, &filtered, mask, NULL)) goto done;
		if (vips_extract_area(filtered, &cropped, 1, 1, target_width, target_height, NULL)) goto done;
		stage = cropped;
	}

	if (has_alpha) {
		if (vips_unpremultiply(stage, &unpremultiplied, NULL)) goto done;
		stage = unpremultiplied;
	}

	if (format == DEADFIN_JPEG) {
		result = vips_jpegsave_buffer(stage, output, output_length, "Q", quality, "interlace", FALSE, "subsample_mode", VIPS_FOREIGN_SUBSAMPLE_ON, NULL);
	} else if (format == DEADFIN_PNG) {
		result = vips_pngsave_buffer(stage, output, output_length, NULL);
	} else if (format == DEADFIN_WEBP) {
		result = vips_webpsave_buffer(stage, output, output_length, "Q", quality, "lossless", quality == 100 && !has_alpha, NULL);
	}

done:
	deadfin_unref(unpremultiplied);
	deadfin_unref(cropped);
	deadfin_unref(filtered);
	deadfin_unref(mask);
	deadfin_unref(pad);
	deadfin_unref(scaled);
	deadfin_unref(mip);
	deadfin_unref(premultiplied);
	deadfin_unref(oriented);
	deadfin_unref(in);
	return result;
}

static void deadfin_skia_free(void *value) { g_free(value); }
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

func encodeSkiaEquivalent(sourcePath string, autoOrient bool, format Format, quality int, dimensions Dimensions) ([]byte, error) {
	var nativeFormat C.int
	switch format {
	case FormatJPEG:
		nativeFormat = C.DEADFIN_JPEG
		if quality == 0 {
			quality = 1
		}
	case FormatPNG:
		nativeFormat = C.DEADFIN_PNG
	case FormatWebP:
		if quality == 0 && runtime.GOOS == "linux" && !autoOrient {
			return encodeSkiaWebPQualityZero(sourcePath, dimensions)
		}
		nativeFormat = C.DEADFIN_WEBP
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedOutputFormat, format)
	}

	path := C.CString(sourcePath)
	defer C.free(unsafe.Pointer(path))
	var output unsafe.Pointer
	var outputLength C.size_t
	if C.deadfin_skia_resize_encode(path, C.int(boolToInt(autoOrient)), C.int(dimensions.Width), C.int(dimensions.Height), nativeFormat, C.int(quality), &output, &outputLength) != 0 {
		message := C.GoString(C.vips_error_buffer())
		C.vips_error_clear()
		if message == "" {
			message = "unknown libvips error"
		}
		return nil, fmt.Errorf("native resample and encode: %s", message)
	}
	defer C.deadfin_skia_free(output)
	return append([]byte(nil), unsafe.Slice((*byte)(output), int(outputLength))...), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
