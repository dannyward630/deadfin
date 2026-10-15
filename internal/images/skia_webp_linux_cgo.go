// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package images

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>

// These declarations are the SkiaSharp 3.119.4 generated C ABI, verified
// against binding/SkiaSharp/SkiaApi.generated.cs. The library is intentionally
// opened at runtime: the Go process must remain buildable on development hosts
typedef struct { int32_t width, height; } sk_isize_t;
// that do not carry the Linux production asset.
typedef struct {
	void *colorspace;
	int32_t width;
	int32_t height;
	int32_t color_type;
	int32_t alpha_type;
} sk_imageinfo_t;
typedef struct { float left, top, right, bottom; } sk_rect_t;
typedef struct { int32_t max_aniso; uint8_t use_cubic; uint8_t pad[3]; float cubic_b, cubic_c; int32_t filter, mipmap; } sk_sampling_options_t;
typedef struct { int32_t compression; float quality; void *icc_profile; const char *icc_profile_description; } sk_webpencoder_options_t;
typedef struct { uint8_t zero_initialized; uint8_t pad[7]; void *subset; int32_t frame_index, prior_frame; } sk_codec_options_t;

typedef void* (*sk_data_new_from_file_fn)(const char *);
typedef void (*sk_data_unref_fn)(void *);
typedef const void* (*sk_data_get_data_fn)(void *);
typedef size_t (*sk_data_get_size_fn)(void *);
typedef void* (*sk_codec_new_from_data_fn)(void *);
typedef void (*sk_codec_destroy_fn)(void *);
typedef void (*sk_codec_get_info_fn)(void *, sk_imageinfo_t *);
typedef void (*sk_codec_get_scaled_dimensions_fn)(void *, float, sk_isize_t *);
typedef int32_t (*sk_codec_get_pixels_fn)(void *, const sk_imageinfo_t *, void *, size_t, const sk_codec_options_t *);
typedef void* (*sk_bitmap_new_fn)(void);
typedef uint8_t (*sk_bitmap_try_alloc_pixels_fn)(void *, const sk_imageinfo_t *, size_t);
typedef void* (*sk_bitmap_get_pixels_fn)(void *, size_t *);
typedef uint8_t (*sk_bitmap_peek_pixels_fn)(void *, void *);
typedef void (*sk_bitmap_destructor_fn)(void *);
typedef void* (*sk_image_new_from_bitmap_fn)(void *);
typedef void (*sk_image_unref_fn)(void *);
typedef void* (*sk_surface_new_raster_fn)(const sk_imageinfo_t *, size_t, const void *);
typedef void* (*sk_surface_new_raster_direct_fn)(const sk_imageinfo_t *, void *, size_t, void (*)(void *, void *), void *, const void *);
typedef void (*sk_surface_unref_fn)(void *);
typedef void* (*sk_surface_get_canvas_fn)(void *);
typedef void* (*sk_surface_new_image_snapshot_fn)(void *);
typedef void* (*sk_imagefilter_new_matrix_convolution_fn)(const void *, const float *, float, float, const void *, int32_t, uint8_t, const void *, const void *);
typedef void (*sk_imagefilter_unref_fn)(void *);
typedef void* (*sk_paint_new_fn)(void);
typedef void (*sk_paint_delete_fn)(void *);
typedef void (*sk_paint_set_imagefilter_fn)(void *, void *);
typedef void (*sk_canvas_draw_image_rect_fn)(void *, void *, const sk_rect_t *, const sk_rect_t *, const sk_sampling_options_t *, void *);
typedef void* (*sk_pixmap_new_fn)(void);
typedef void (*sk_pixmap_destructor_fn)(void *);
typedef uint8_t (*sk_image_peek_pixels_fn)(void *, void *);
typedef uint8_t (*sk_pixmap_scale_pixels_fn)(const void *, const void *, const sk_sampling_options_t *);
typedef void* (*sk_colorspace_new_srgb_fn)(void);
typedef void (*sk_colorspace_unref_fn)(void *);
typedef void* (*sk_dynamicmemorywstream_new_fn)(void);
typedef void (*sk_dynamicmemorywstream_destroy_fn)(void *);
typedef void* (*sk_dynamicmemorywstream_detach_as_data_fn)(void *);
typedef uint8_t (*sk_webpencoder_encode_fn)(void *, void *, const sk_webpencoder_options_t *);

enum {
	DEADFIN_SKIA_OK = 0,
	DEADFIN_SKIA_ERROR = -1,
	DEADFIN_SKIA_UNAVAILABLE = 1,
	DEADFIN_SKIA_UNSUPPORTED = 2,
};


static void deadfin_skia_error(char *error, size_t error_length, const char *message) {
	if (error_length == 0) return;
	snprintf(error, error_length, "%s", message);
}

static void *deadfin_skia_open(char *error, size_t error_length) {
	void *library = dlopen("/usr/local/lib/deadfin/libSkiaSharp.so", RTLD_NOW | RTLD_LOCAL);
	if (library == NULL) {
		deadfin_skia_error(error, error_length, dlerror());
	}
	return library;
}

#define DEADFIN_SKIA_LOAD(name) \
	name##_fn name = (name##_fn)dlsym(library, #name); \
	if (name == NULL) { deadfin_skia_error(error, error_length, #name " missing from libSkiaSharp.so"); result = DEADFIN_SKIA_UNAVAILABLE; goto done; }

static int deadfin_skia_webp_q0(const char *path, int target_width, int target_height, void **output, size_t *output_length, void **output_library, char *error, size_t error_length) {
	void *library = deadfin_skia_open(error, error_length);
	void *input = NULL, *codec = NULL, *bitmap = NULL, *source_image = NULL;
	void *surface = NULL, *filter = NULL, *paint = NULL, *snapshot = NULL, *pixmap = NULL;
	void *stream = NULL, *encoded = NULL;
	int result = -1;
	*output = NULL;
	*output_length = 0;
	*output_library = NULL;
	if (library == NULL) return result;

	DEADFIN_SKIA_LOAD(sk_data_new_from_file);
	DEADFIN_SKIA_LOAD(sk_data_unref);
	DEADFIN_SKIA_LOAD(sk_data_get_data);
	DEADFIN_SKIA_LOAD(sk_data_get_size);
	DEADFIN_SKIA_LOAD(sk_codec_new_from_data);
	DEADFIN_SKIA_LOAD(sk_codec_destroy);
	DEADFIN_SKIA_LOAD(sk_codec_get_info);
	DEADFIN_SKIA_LOAD(sk_codec_get_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_new);
	DEADFIN_SKIA_LOAD(sk_bitmap_try_alloc_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_get_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_destructor);
	DEADFIN_SKIA_LOAD(sk_image_new_from_bitmap);
	DEADFIN_SKIA_LOAD(sk_image_unref);
	DEADFIN_SKIA_LOAD(sk_surface_new_raster);
	DEADFIN_SKIA_LOAD(sk_surface_unref);
	DEADFIN_SKIA_LOAD(sk_surface_get_canvas);
	DEADFIN_SKIA_LOAD(sk_surface_new_image_snapshot);
	DEADFIN_SKIA_LOAD(sk_imagefilter_new_matrix_convolution);
	DEADFIN_SKIA_LOAD(sk_imagefilter_unref);
	DEADFIN_SKIA_LOAD(sk_paint_new);
	DEADFIN_SKIA_LOAD(sk_paint_delete);
	DEADFIN_SKIA_LOAD(sk_paint_set_imagefilter);
	DEADFIN_SKIA_LOAD(sk_canvas_draw_image_rect);
	DEADFIN_SKIA_LOAD(sk_pixmap_new);
	DEADFIN_SKIA_LOAD(sk_pixmap_destructor);
	DEADFIN_SKIA_LOAD(sk_image_peek_pixels);
	DEADFIN_SKIA_LOAD(sk_dynamicmemorywstream_new);
	DEADFIN_SKIA_LOAD(sk_dynamicmemorywstream_destroy);
	DEADFIN_SKIA_LOAD(sk_dynamicmemorywstream_detach_as_data);
	DEADFIN_SKIA_LOAD(sk_webpencoder_encode);

	input = sk_data_new_from_file(path);
	if (input == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not read source image"); goto done; }
	codec = sk_codec_new_from_data(input);
	if (codec == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not decode source image"); goto done; }

	sk_imageinfo_t source_info;
	sk_codec_get_info(codec, &source_info);
	if (source_info.width <= 0 || source_info.height <= 0 || target_width <= 0 || target_height <= 0) { deadfin_skia_error(error, error_length, "invalid Skia image dimensions"); goto done; }
	source_info.color_type = 6; // kBGRA_8888_SkColorType
	source_info.alpha_type = 2; // kPremul_SkAlphaType
	const size_t source_row_bytes = (size_t)source_info.width * 4;
	if ((size_t)source_info.height > SIZE_MAX / source_row_bytes) { deadfin_skia_error(error, error_length, "invalid Skia source row bytes"); goto done; }
	bitmap = sk_bitmap_new();
	if (bitmap == NULL || !sk_bitmap_try_alloc_pixels(bitmap, &source_info, source_row_bytes)) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate source pixels"); goto done; }
	const sk_codec_options_t codec_options = { 0, { 0 }, NULL, 0, -1 };
	size_t pixel_length = 0;
	void *pixels = sk_bitmap_get_pixels(bitmap, &pixel_length);
	if (pixels == NULL || pixel_length < source_row_bytes * (size_t)source_info.height) { deadfin_skia_error(error, error_length, "SkiaSharp source pixels unavailable"); goto done; }
	if (sk_codec_get_pixels(codec, &source_info, pixels, source_row_bytes, &codec_options) != 0) { deadfin_skia_error(error, error_length, "SkiaSharp source decode failed"); goto done; }
	source_image = sk_image_new_from_bitmap(bitmap);
	if (source_image == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not create source image"); goto done; }

	sk_imageinfo_t target_info = source_info;
	target_info.width = target_width;
	target_info.height = target_height;
	const size_t target_row_bytes = (size_t)target_info.width * 4;
	surface = sk_surface_new_raster(&target_info, target_row_bytes, NULL);
	if (surface == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate output surface"); goto done; }
	const int32_t kernel_size[2] = { 3, 3 };
	const float kernel[9] = { 0.0f, -0.1f, 0.0f, -0.1f, 1.4f, -0.1f, 0.0f, -0.1f, 0.0f };
	const int32_t kernel_offset[2] = { 1, 1 };
	filter = sk_imagefilter_new_matrix_convolution(kernel_size, kernel, 1.0f, 0.0f, kernel_offset, 0, 1, NULL, NULL);
	paint = sk_paint_new();
	if (filter == NULL || paint == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not create sharpen filter"); goto done; }
	sk_paint_set_imagefilter(paint, filter);
	sk_rect_t source_rect = { 0, 0, (float)source_info.width, (float)source_info.height };
	sk_rect_t target_rect = { 0, 0, (float)target_width, (float)target_height };
	const sk_sampling_options_t sampling = { 0, 1, { 0, 0, 0 }, 1.0f / 3.0f, 1.0f / 3.0f, 0, 0 };
	sk_canvas_draw_image_rect(sk_surface_get_canvas(surface), source_image, &source_rect, &target_rect, &sampling, paint);
	snapshot = sk_surface_new_image_snapshot(surface);
	pixmap = sk_pixmap_new();
	if (snapshot == NULL || pixmap == NULL || !sk_image_peek_pixels(snapshot, pixmap)) { deadfin_skia_error(error, error_length, "SkiaSharp could not read output pixels"); goto done; }
	stream = sk_dynamicmemorywstream_new();
	const sk_webpencoder_options_t webp_options = { 0, 0.0f, NULL, NULL }; // lossy Q0
	if (stream == NULL || !sk_webpencoder_encode(stream, pixmap, &webp_options)) { deadfin_skia_error(error, error_length, "SkiaSharp WebP Q0 encode failed"); goto done; }
	encoded = sk_dynamicmemorywstream_detach_as_data(stream);
	if (encoded == NULL || sk_data_get_size(encoded) == 0) { deadfin_skia_error(error, error_length, "SkiaSharp WebP Q0 returned no data"); goto done; }
	*output = encoded;
	*output_length = sk_data_get_size(encoded);
	*output_library = library;
	encoded = NULL; // Go copies the buffer, then releases its SkData and dlopen owner.
	library = NULL;
	result = 0;

done:
	if (encoded != NULL) sk_data_unref(encoded);
	if (stream != NULL) sk_dynamicmemorywstream_destroy(stream);
	if (pixmap != NULL) sk_pixmap_destructor(pixmap);
	if (snapshot != NULL) sk_image_unref(snapshot);
	if (paint != NULL) sk_paint_delete(paint);
	if (filter != NULL) sk_imagefilter_unref(filter);
	if (surface != NULL) sk_surface_unref(surface);
	if (source_image != NULL) sk_image_unref(source_image);
	if (bitmap != NULL) sk_bitmap_destructor(bitmap);
	if (codec != NULL) sk_codec_destroy(codec);
	if (input != NULL) sk_data_unref(input);
	if (library != NULL) dlclose(library);
	return result;
}

static void deadfin_skia_data_unref(void *library, void *data) {
	if (library == NULL) return;
	if (data != NULL) {
		sk_data_unref_fn unref = (sk_data_unref_fn)dlsym(library, "sk_data_unref");
		if (unref != NULL) unref(data);
	}
	dlclose(library);
}

static const void *deadfin_skia_data_bytes(void *library, void *data) {
	if (data == NULL || library == NULL) return NULL;
	sk_data_get_data_fn bytes = (sk_data_get_data_fn)dlsym(library, "sk_data_get_data");
	return bytes == NULL ? NULL : bytes(data);
}
static int32_t deadfin_skia_dotnet_round_to_int(float value) {
	if (value <= 0 || value > INT32_MAX) return 0;
	int32_t integral = (int32_t)value;
	float fraction = value - integral;
	if (fraction > 0.5f || (fraction == 0.5f && (integral & 1))) integral++;
	return integral;
}

static int deadfin_skia_blurhash_pixels(const char *path, int max_dimension, void **output, size_t *output_length, int *output_width, int *output_height, char *error, size_t error_length) {
	void *library = deadfin_skia_open(error, error_length);
	void *input = NULL, *codec = NULL, *bitmap = NULL, *target_bitmap = NULL, *color_space = NULL;
	void *source_pixmap = NULL, *target_pixmap = NULL, *pixels = NULL;
	int result = DEADFIN_SKIA_ERROR;
	float scale_factor = 0.0f;
	*output = NULL; *output_length = 0; *output_width = 0; *output_height = 0;
	if (max_dimension <= 0) { deadfin_skia_error(error, error_length, "invalid Skia maximum dimension"); return result; }
	if (library == NULL) return DEADFIN_SKIA_UNAVAILABLE;

	DEADFIN_SKIA_LOAD(sk_data_new_from_file);
	DEADFIN_SKIA_LOAD(sk_data_unref);
	DEADFIN_SKIA_LOAD(sk_codec_new_from_data);
	DEADFIN_SKIA_LOAD(sk_codec_destroy);
	DEADFIN_SKIA_LOAD(sk_codec_get_info);
	DEADFIN_SKIA_LOAD(sk_codec_get_scaled_dimensions);
	DEADFIN_SKIA_LOAD(sk_codec_get_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_new);
	DEADFIN_SKIA_LOAD(sk_bitmap_try_alloc_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_get_pixels);
	DEADFIN_SKIA_LOAD(sk_bitmap_destructor);
	DEADFIN_SKIA_LOAD(sk_pixmap_new);
	DEADFIN_SKIA_LOAD(sk_pixmap_destructor);
	DEADFIN_SKIA_LOAD(sk_bitmap_peek_pixels);
	DEADFIN_SKIA_LOAD(sk_pixmap_scale_pixels);
	DEADFIN_SKIA_LOAD(sk_colorspace_new_srgb);
	DEADFIN_SKIA_LOAD(sk_colorspace_unref);

	input = sk_data_new_from_file(path);
	if (input == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not read source image"); goto done; }
	codec = sk_codec_new_from_data(input);
	if (codec == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp does not support source image"); result = DEADFIN_SKIA_UNSUPPORTED; goto done; }

	// BlurHashSharp.SkiaSharp first asks the codec for its supported scale, then
	// applies the same factor a second time through SKBitmap.Resize.
	sk_imageinfo_t source_info;
	sk_codec_get_info(codec, &source_info);
	if (source_info.width <= 0 || source_info.height <= 0) { deadfin_skia_error(error, error_length, "invalid Skia image dimensions"); goto done; }
	if (source_info.width > max_dimension || source_info.height > max_dimension) {
		float scale_x = (float)max_dimension / source_info.width;
		float scale_y = (float)max_dimension / source_info.height;
		scale_factor = scale_x < scale_y ? scale_x : scale_y;
		sk_isize_t scaled;
		sk_codec_get_scaled_dimensions(codec, scale_factor, &scaled);
		if (scaled.width <= 0 || scaled.height <= 0) { deadfin_skia_error(error, error_length, "SkiaSharp scaled dimensions unavailable"); goto done; }
		source_info.width = scaled.width;
		source_info.height = scaled.height;
	}
	color_space = sk_colorspace_new_srgb();
	if (color_space == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp sRGB color space unavailable"); goto done; }
	// Match BlurHashSharp's SKImageInfo exactly: RGBA8888, unpremultiplied, sRGB.
	source_info.colorspace = color_space;
	source_info.color_type = 4;
	source_info.alpha_type = 3;
	const size_t source_row_bytes = (size_t)source_info.width * 4;
	if (source_row_bytes == 0 || (size_t)source_info.height > SIZE_MAX / source_row_bytes) { deadfin_skia_error(error, error_length, "invalid Skia source bytes"); goto done; }
	bitmap = sk_bitmap_new();
	if (bitmap == NULL || !sk_bitmap_try_alloc_pixels(bitmap, &source_info, source_row_bytes)) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate source pixels"); goto done; }
	size_t source_length = 0;
	void *source_pixels = sk_bitmap_get_pixels(bitmap, &source_length);
	const sk_codec_options_t codec_options = { 0, { 0 }, NULL, 0, -1 };
	if (source_pixels == NULL || source_length < source_row_bytes * (size_t)source_info.height || sk_codec_get_pixels(codec, &source_info, source_pixels, source_row_bytes, &codec_options) != 0) { deadfin_skia_error(error, error_length, "SkiaSharp source decode failed"); goto done; }

	if (scale_factor == 0.0f) {
		size_t source_bytes = source_row_bytes * (size_t)source_info.height;
		pixels = malloc(source_bytes);
		if (pixels == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate output pixels"); goto done; }
		memcpy(pixels, source_pixels, source_bytes);
		*output = pixels; *output_length = source_bytes; *output_width = source_info.width; *output_height = source_info.height;
		pixels = NULL;
		result = DEADFIN_SKIA_OK;
		goto done;
	}

	// SKBitmap.Resize constructs a destination bitmap and delegates to
	// SKPixmap.ScalePixels. Do the same rather than drawing SrcOver into an
	// uninitialized raster surface: alpha-bearing sources then have no implicit
	// destination pixels to blend with.
	sk_imageinfo_t target_info = source_info;
	target_info.width = deadfin_skia_dotnet_round_to_int(source_info.width * scale_factor);
	target_info.height = deadfin_skia_dotnet_round_to_int(source_info.height * scale_factor);
	if (target_info.width <= 0 || target_info.height <= 0) { deadfin_skia_error(error, error_length, "invalid Skia target dimensions"); goto done; }
	const size_t target_row_bytes = (size_t)target_info.width * 4;
	if (target_row_bytes == 0 || (size_t)target_info.height > SIZE_MAX / target_row_bytes) { deadfin_skia_error(error, error_length, "invalid Skia target bytes"); goto done; }
	const size_t target_bytes = target_row_bytes * (size_t)target_info.height;
	target_bitmap = sk_bitmap_new();
	if (target_bitmap == NULL || !sk_bitmap_try_alloc_pixels(target_bitmap, &target_info, target_row_bytes)) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate target pixels"); goto done; }
	size_t target_length = 0;
	void *target_pixels = sk_bitmap_get_pixels(target_bitmap, &target_length);
	if (target_pixels == NULL || target_length < target_bytes) { deadfin_skia_error(error, error_length, "SkiaSharp target pixels unavailable"); goto done; }
	source_pixmap = sk_pixmap_new();
	target_pixmap = sk_pixmap_new();
	if (source_pixmap == NULL || target_pixmap == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not create scale pixmaps"); goto done; }
	if (!sk_bitmap_peek_pixels(bitmap, source_pixmap) || !sk_bitmap_peek_pixels(target_bitmap, target_pixmap)) { deadfin_skia_error(error, error_length, "SkiaSharp could not inspect scale pixmaps"); goto done; }
	const sk_sampling_options_t sampling = { 0 }; // SKSamplingOptions.Default
	if (!sk_pixmap_scale_pixels(source_pixmap, target_pixmap, &sampling)) { deadfin_skia_error(error, error_length, "SkiaSharp could not scale BlurHash pixels"); goto done; }
	pixels = malloc(target_bytes);
	if (pixels == NULL) { deadfin_skia_error(error, error_length, "SkiaSharp could not allocate output pixels"); goto done; }
	memcpy(pixels, target_pixels, target_bytes);
	*output = pixels; *output_length = target_bytes; *output_width = target_info.width; *output_height = target_info.height;
	pixels = NULL;
	result = DEADFIN_SKIA_OK;

done:
	if (target_pixmap != NULL) sk_pixmap_destructor(target_pixmap);
	if (source_pixmap != NULL) sk_pixmap_destructor(source_pixmap);
	if (target_bitmap != NULL) sk_bitmap_destructor(target_bitmap);
	if (bitmap != NULL) sk_bitmap_destructor(bitmap);
	if (codec != NULL) sk_codec_destroy(codec);
	if (input != NULL) sk_data_unref(input);
	if (color_space != NULL) sk_colorspace_unref(color_space);
	if (pixels != NULL) free(pixels);
	if (library != NULL) dlclose(library);
	return result;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func encodeSkiaWebPQualityZero(sourcePath string, dimensions Dimensions) ([]byte, error) {
	path := C.CString(sourcePath)
	defer C.free(unsafe.Pointer(path))
	var output unsafe.Pointer
	var outputLength C.size_t
	var outputLibrary unsafe.Pointer
	errorBuffer := make([]byte, 512)
	if C.deadfin_skia_webp_q0(path, C.int(dimensions.Width), C.int(dimensions.Height), &output, &outputLength, &outputLibrary, (*C.char)(unsafe.Pointer(&errorBuffer[0])), C.size_t(len(errorBuffer))) != 0 {
		return nil, fmt.Errorf("SkiaSharp WebP Q0: %s", C.GoString((*C.char)(unsafe.Pointer(&errorBuffer[0]))))
	}
	defer C.deadfin_skia_data_unref(outputLibrary, output)
	if outputLength == 0 || output == nil {
		return nil, fmt.Errorf("SkiaSharp WebP Q0: empty output")
	}
	bytes := C.deadfin_skia_data_bytes(outputLibrary, output)
	if bytes == nil {
		return nil, fmt.Errorf("SkiaSharp WebP Q0: output bytes unavailable")
	}
	return append([]byte(nil), unsafe.Slice((*byte)(bytes), int(outputLength))...), nil
}

func decodeSkiaBlurHashPixels(sourcePath string, maxDimension int) (BlurHashPixels, error) {
	path := C.CString(sourcePath)
	defer C.free(unsafe.Pointer(path))
	var output unsafe.Pointer
	var outputLength C.size_t
	var width, height C.int
	errorBuffer := make([]byte, 512)
	status := C.deadfin_skia_blurhash_pixels(path, C.int(maxDimension), &output, &outputLength, &width, &height, (*C.char)(unsafe.Pointer(&errorBuffer[0])), C.size_t(len(errorBuffer)))
	if status != C.DEADFIN_SKIA_OK {
		message := C.GoString((*C.char)(unsafe.Pointer(&errorBuffer[0])))
		switch status {
		case C.DEADFIN_SKIA_UNAVAILABLE:
			return BlurHashPixels{}, fmt.Errorf("%w: %s", ErrSkiaBlurHashUnavailable, message)
		case C.DEADFIN_SKIA_UNSUPPORTED:
			return BlurHashPixels{}, fmt.Errorf("%w: %s", ErrSkiaBlurHashUnsupported, message)
		default:
			return BlurHashPixels{}, fmt.Errorf("SkiaSharp BlurHash pixels: %s", message)
		}
	}
	defer C.free(output)
	if output == nil || outputLength == 0 || width <= 0 || height <= 0 {
		return BlurHashPixels{}, fmt.Errorf("SkiaSharp BlurHash pixels: empty output")
	}
	return BlurHashPixels{Width: int(width), Height: int(height), RGBA: append([]byte(nil), unsafe.Slice((*byte)(output), int(outputLength))...)}, nil
}
