// SPDX-License-Identifier: GPL-2.0-only

package images

import "math"

// Format is an image output format named as Jellyfin's ImageFormat enum renders it.
type Format string

const (
	FormatBMP  Format = "Bmp"
	FormatGIF  Format = "Gif"
	FormatJPEG Format = "Jpg"
	FormatPNG  Format = "Png"
	FormatWebP Format = "Webp"
	FormatSVG  Format = "Svg"
)

// Dimensions is the width and height of an image in pixels.
type Dimensions struct {
	Width  int
	Height int
}

// ResizeOptions matches Jellyfin's independent optional image resize fields. A
// non-nil field participates in cache identity, even when its value is zero.
type ResizeOptions struct {
	Width      *int
	Height     *int
	MaxWidth   *int
	MaxHeight  *int
	FillWidth  *int
	FillHeight *int
}

// HasDimensions reports whether any image-dimension option was explicitly set.
func (options ResizeOptions) HasDimensions() bool {
	return options.Width != nil ||
		options.Height != nil ||
		options.MaxWidth != nil ||
		options.MaxHeight != nil ||
		options.FillWidth != nil ||
		options.FillHeight != nil
}

// ResolveDimensions applies Jellyfin's resize, bounds, then fill-box steps to
// source. Fixed dimensions and bounds treat zero values as unspecified; fill
// dimensions retain their distinct one-pixel counterpart behavior.
func ResolveDimensions(source Dimensions, options ResizeOptions) Dimensions {
	result := resize(source, optionValue(options.Width), optionValue(options.Height), optionValue(options.MaxWidth), optionValue(options.MaxHeight))
	return resizeFill(result, options.FillWidth, options.FillHeight)
}

func optionValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func resize(size Dimensions, width, height, maxWidth, maxHeight int) Dimensions {
	newWidth, newHeight := size.Width, size.Height

	if width > 0 && height > 0 {
		newWidth, newHeight = width, height
	} else if height > 0 {
		newWidth = scaledDimension(newHeight, newWidth, height)
		newHeight = height
	} else if width > 0 {
		newHeight = scaledDimension(newWidth, newHeight, width)
		newWidth = width
	}

	if maxHeight > 0 && maxHeight < newHeight {
		newWidth = scaledDimension(newHeight, newWidth, maxHeight)
		newHeight = maxHeight
	}
	if maxWidth > 0 && maxWidth < newWidth {
		newHeight = scaledDimension(newWidth, newHeight, maxWidth)
		newWidth = maxWidth
	}

	return Dimensions{Width: newWidth, Height: newHeight}
}

// scaledDimension is DrawingUtils' Convert.ToInt32(new/current*other), which
// rounds midpoints to even rather than Go's half-away-from-zero convention.
func scaledDimension(current, other, target int) int {
	return int(math.RoundToEven(float64(target) / float64(current) * float64(other)))
}

func resizeFill(size Dimensions, fillWidth, fillHeight *int) Dimensions {
	if (fillWidth == nil || *fillWidth == 0) && (fillHeight == nil || *fillHeight == 0) {
		return size
	}

	width, height := 1, 1
	if fillWidth != nil && *fillWidth != 0 {
		width = *fillWidth
	}
	if fillHeight != nil && *fillHeight != 0 {
		height = *fillHeight
	}

	scale := math.Min(float64(size.Width)/float64(width), float64(size.Height)/float64(height))
	if scale < 1 {
		return size
	}

	return Dimensions{
		Width:  int(math.Ceil(float64(size.Width) / scale)),
		Height: int(math.Ceil(float64(size.Height) / scale)),
	}
}

// SelectFormat chooses an encoded image format using the server's current
// precedence: WebP, transparent PNG, then client ordering, with JPEG fallback.
// SVG is emitted only when the source is itself SVG.
func SelectFormat(source Format, clientFormats []Format, transparent bool) Format {
	if containsFormat(clientFormats, FormatWebP) {
		return FormatWebP
	}
	if transparent && containsFormat(clientFormats, FormatPNG) {
		return FormatPNG
	}
	for _, format := range clientFormats {
		if isSupportedOutputFormat(source, format) {
			return format
		}
	}
	return FormatJPEG
}

func containsFormat(formats []Format, wanted Format) bool {
	for _, format := range formats {
		if format == wanted {
			return true
		}
	}
	return false
}

func isSupportedOutputFormat(source, format Format) bool {
	switch format {
	case FormatWebP, FormatJPEG, FormatPNG:
		return true
	case FormatSVG:
		return source == FormatSVG
	default:
		return false
	}
}

func (format Format) extension() string {
	switch format {
	case FormatBMP:
		return ".bmp"
	case FormatGIF:
		return ".gif"
	case FormatJPEG:
		return ".jpg"
	case FormatPNG:
		return ".png"
	case FormatWebP:
		return ".webp"
	case FormatSVG:
		return ".svg"
	default:
		return ""
	}
}
