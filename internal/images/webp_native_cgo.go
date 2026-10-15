// SPDX-License-Identifier: GPL-2.0-only

package images

/*
#cgo pkg-config: vips
#include <vips/vips.h>

static int deadfin_webpsave_q0(void *input, size_t input_length, void **output, size_t *output_length) {
	VipsImage *image = vips_image_new_from_buffer(input, input_length, "", NULL);
	if (image == NULL) {
		return -1;
	}

	int result = vips_webpsave_buffer(image, output, output_length, "Q", 0, NULL);
	g_object_unref(image);
	return result;
}


static void deadfin_vips_free(void *value) {
	g_free(value);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/davidbyttow/govips/v2/vips"
)

// exportWebPQualityZero bypasses govips's zero-value filter. govips v2.16.0
// only sets webpsave's Q option when Quality is nonzero, while Q=0 is a
// meaningful libvips request rather than its default quality.
func exportWebPQualityZero(imageRef *vips.ImageRef) ([]byte, error) {
	png, _, err := imageRef.ExportPng(nil)
	if err != nil {
		return nil, fmt.Errorf("encode intermediate PNG for WebP: %w", err)
	}
	if len(png) == 0 {
		return nil, errors.New("encode intermediate PNG for WebP: empty output")
	}

	var output unsafe.Pointer
	var outputLength C.size_t
	if C.deadfin_webpsave_q0(unsafe.Pointer(&png[0]), C.size_t(len(png)), &output, &outputLength) != 0 {
		message := C.GoString(C.vips_error_buffer())
		C.vips_error_clear()
		if message == "" {
			message = "unknown libvips error"
		}
		return nil, fmt.Errorf("encode WebP: %s", message)
	}
	defer C.deadfin_vips_free(output)

	return append([]byte(nil), unsafe.Slice((*byte)(output), int(outputLength))...), nil
}
