// SPDX-License-Identifier: GPL-2.0-only

package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"runtime"

	_ "golang.org/x/image/webp"

	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
)

// enrichLocalImageMetadata adds durable facts to already-discovered local
// artwork. Invalid or unsupported artwork remains a valid discovered image;
// it cannot contribute decoded metadata.
func enrichLocalImageMetadata(ctx context.Context, target *item.Item) error {
	if target == nil {
		return nil
	}
	for index := range target.ImageInfos {
		if err := ctx.Err(); err != nil {
			return err
		}
		imageInfo := &target.ImageInfos[index]
		contents, err := os.ReadFile(imageInfo.Path)
		if err != nil {
			continue
		}
		decoded, _, err := image.Decode(bytes.NewReader(contents))
		if err != nil {
			continue
		}
		bounds := decoded.Bounds()
		imageInfo.Width, imageInfo.Height = bounds.Dx(), bounds.Dy()
		blurSource := decoded
		stockPixels, stockErr := images.DecodeSkiaBlurHashPixels(imageInfo.Path)
		switch {
		case stockErr == nil:
			blurSource = stockPixels
		case errors.Is(stockErr, images.ErrSkiaBlurHashUnavailable), errors.Is(stockErr, images.ErrSkiaBlurHashUnsupported):
			// Linux hashes must come from the packaged SkiaSharp decoder. Keeping
			// image metadata without a hash is preferable to persisting a different
			// pure-Go reconstruction when that runtime is absent.
			if runtime.GOOS == "linux" {
				continue
			}
			// The platform-native bridge is optional on other platforms.
		default:
			return fmt.Errorf("decode stock-compatible BlurHash pixels for %q: %w", imageInfo.Path, stockErr)
		}
		if hash := localBlurHash(blurSource); hash != "" {
			imageInfo.BlurHash = &hash
		}
		if target.Kind == item.KindPhoto && imageInfo.Type == item.ImageTypePrimary && imageInfo.Index == 0 {
			orientation := jpegEXIFOrientation(contents)
			if orientation == 0 {
				orientation = int(item.ImageOrientationTopLeft)
			}
			if orientation >= int(item.ImageOrientationTopLeft) && orientation <= int(item.ImageOrientationLeftBottom) {
				value := item.ImageOrientation(orientation)
				if target.Extra.Photo == nil {
					target.Extra.Photo = &item.PhotoExtra{}
				}
				target.Extra.Photo.Orientation = &value
			}
		}
	}
	return nil
}

// jpegEXIFOrientation reads IFD0's EXIF Orientation (0x0112). It returns zero
// when no well-formed JPEG EXIF orientation is present.
func jpegEXIFOrientation(contents []byte) int {
	if len(contents) < 4 || contents[0] != 0xff || contents[1] != 0xd8 {
		return 0
	}
	for offset := 2; offset+4 <= len(contents); {
		if contents[offset] != 0xff {
			return 0
		}
		for offset < len(contents) && contents[offset] == 0xff {
			offset++
		}
		if offset >= len(contents) || contents[offset] == 0xd9 || contents[offset] == 0xda {
			return 0
		}
		marker := contents[offset]
		offset++
		if offset+2 > len(contents) {
			return 0
		}
		length := int(contents[offset])<<8 | int(contents[offset+1])
		if length < 2 || offset+length > len(contents) {
			return 0
		}
		segment := contents[offset+2 : offset+length]
		if marker == 0xe1 && len(segment) >= 14 && bytes.Equal(segment[:6], []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		offset += length
	}
	return 0
}

func tiffOrientation(contents []byte) int {
	if len(contents) < 8 {
		return 0
	}
	little := bytes.Equal(contents[:2], []byte("II"))
	if !little && !bytes.Equal(contents[:2], []byte("MM")) {
		return 0
	}
	read16 := func(offset int) uint16 {
		if little {
			return uint16(contents[offset]) | uint16(contents[offset+1])<<8
		}
		return uint16(contents[offset])<<8 | uint16(contents[offset+1])
	}
	read32 := func(offset int) uint32 {
		if little {
			return uint32(contents[offset]) | uint32(contents[offset+1])<<8 | uint32(contents[offset+2])<<16 | uint32(contents[offset+3])<<24
		}
		return uint32(contents[offset])<<24 | uint32(contents[offset+1])<<16 | uint32(contents[offset+2])<<8 | uint32(contents[offset+3])
	}
	if read16(2) != 42 {
		return 0
	}
	first := int(read32(4))
	if first < 0 || first+2 > len(contents) {
		return 0
	}
	count := int(read16(first))
	for index := 0; index < count; index++ {
		offset := first + 2 + index*12
		if offset+12 > len(contents) {
			return 0
		}
		if read16(offset) == 0x0112 && read16(offset+2) == 3 && read32(offset+4) == 1 {
			return int(read16(offset + 8))
		}
	}
	return 0
}

type blurFactor [3]float32

var blurSRGBToLinearLookup = func() [256]float32 {
	var lookup [256]float32
	for value := range lookup {
		if value < 11 {
			lookup[value] = float32(float64(value) / 3294.6)
			continue
		}
		lookup[value] = float32(math.Pow((float64(value)+14.025)/269.025, 2.4))
	}
	return lookup
}()

func localBlurHash(source image.Image) string {
	return localBlurHashWithComponents(source, 6, 4)
}

// localBlurHashWithComponents mirrors BlurHashSharp.CoreBlurHashEncoder's
// float32 lookup, cosine, scan, and quantization operations.
func localBlurHashWithComponents(source image.Image, componentsX, componentsY int) string {
	if componentsX < 1 || componentsX > 9 || componentsY < 1 || componentsY > 9 {
		return ""
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return ""
	}
	// BlurHashSharp receives the unpremultiplied RGBA8888 byte span directly.
	// image.Color's RGBA method premultiplies color.NRGBA by alpha, which would
	// change every translucent WebP source byte before the encoder sees it.
	// Keep the Skia bridge raster on its raw RGB path; generic image.Image inputs
	// retain their normal color-model behavior.
	var nativeRGBA []byte
	if pixels, ok := source.(images.BlurHashPixels); ok && len(pixels.RGBA) == width*height*4 {
		nativeRGBA = pixels.RGBA
	}
	factors := make([]blurFactor, componentsX*componentsY)
	cosines := make([]float32, width+height)
	cosY, cosX := cosines[:height], cosines[height:]
	dcScale := float32(1) / float32(width*height)
	acScale := float32(2) / float32(width*height)
	for yComponent := 0; yComponent < componentsY; yComponent++ {
		precomputeBlurCosines(cosY, float32(math.Pi)*float32(yComponent)/float32(height))
		for xComponent := 0; xComponent < componentsX; xComponent++ {
			precomputeBlurCosines(cosX, float32(math.Pi)*float32(xComponent)/float32(width))
			var factor blurFactor
			for y := 0; y < height; y++ {
				yBasis := cosY[y]
				for x := 0; x < width; x++ {
					basis := cosX[x] * yBasis
					if nativeRGBA != nil {
						offset := (y*width + x) * 4
						factor[0] += basis * blurSRGBToLinearLookup[nativeRGBA[offset]]
						factor[1] += basis * blurSRGBToLinearLookup[nativeRGBA[offset+1]]
						factor[2] += basis * blurSRGBToLinearLookup[nativeRGBA[offset+2]]
						continue
					}
					r, g, b, _ := source.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
					factor[0] += basis * blurSRGBToLinearLookup[uint8(r>>8)]
					factor[1] += basis * blurSRGBToLinearLookup[uint8(g>>8)]
					factor[2] += basis * blurSRGBToLinearLookup[uint8(b>>8)]
				}
			}
			scale := acScale
			if xComponent == 0 && yComponent == 0 {
				scale = dcScale
			}
			factor[0] *= scale
			factor[1] *= scale
			factor[2] *= scale
			factors[yComponent*componentsX+xComponent] = factor
		}
	}

	maximum := float32(1)
	quantizedMaximum := 0
	if len(factors) > 1 {
		maximum = 0
		for _, factor := range factors[1:] {
			for _, value := range factor {
				absolute := float32(math.Abs(float64(value)))
				if absolute > maximum {
					maximum = absolute
				}
			}
		}
		quantizedMaximum = clampBlurInt(int(maximum*166-0.5), 0, 82)
		maximum = float32(quantizedMaximum+1) / 166
	}

	result := blur83(componentsX-1+(componentsY-1)*9, 1) + blur83(quantizedMaximum, 1)
	dc := factors[0]
	result += blur83(encodeBlurDC(dc[0], dc[1], dc[2]), 4)
	for _, factor := range factors[1:] {
		result += blur83(encodeBlurAC(factor[0], factor[1], factor[2], maximum), 2)
	}
	return result
}

func precomputeBlurCosines(table []float32, offset float32) {
	if offset == 0 {
		for index := range table {
			table[index] = 1
		}
		return
	}
	coefficient := float32(0)
	for index := range table {
		table[index] = float32(math.Cos(float64(coefficient)))
		coefficient += offset
	}
}

func clampBlurInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func linearToBlurSRGB(value float32) int {
	if value < 0 {
		value = 0
	} else if value > 1 {
		value = 1
	}
	if value <= 0.0031308 {
		return int(3294.6*value + 0.5)
	}
	return int(269.025*float32(math.Pow(float64(value), float64(float32(1)/2.4))) - 13.525)
}

func encodeBlurDC(red, green, blue float32) int {
	return linearToBlurSRGB(red)<<16 + linearToBlurSRGB(green)<<8 + linearToBlurSRGB(blue)
}

func encodeBlurAC(red, green, blue, maximum float32) int {
	return 19*19*quantizeBlurAC(red, maximum) + 19*quantizeBlurAC(green, maximum) + quantizeBlurAC(blue, maximum)
}

func quantizeBlurAC(value, maximum float32) int {
	normalized := float32(math.Copysign(math.Sqrt(math.Abs(float64(value/maximum))), float64(value)))
	return clampBlurInt(int(9*normalized+9.5), 0, 18)
}

func blur83(value, length int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"
	encoded := make([]byte, length)
	encoded[length-1] = alphabet[value%len(alphabet)]
	for index := length - 2; index >= 0; index-- {
		value /= len(alphabet)
		encoded[index] = alphabet[value%len(alphabet)]
	}
	return string(encoded)
}
