// SPDX-License-Identifier: GPL-2.0-only

package providers

import (
	"image"
	"image/color"
	"testing"
)

// These literals were generated independently with the official BlurHash
// TypeScript encoder at commit 712a47f946b98c30097eb1ada086ea00b18681ec
// (the v2.0.5 source):
// https://github.com/woltapp/blurhash/blob/712a47f946b98c30097eb1ada086ea00b18681ec/TypeScript/src/encode.ts
// The inputs are opaque RGBA pixels in row-major order.
func TestLocalBlurHashMatchesOfficialVectors(t *testing.T) {
	matrix := localBlurHashRaster(4, 3, []color.NRGBA{
		{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
		{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
		{R: 0x00, G: 0x00, B: 0xff, A: 0xff},
		{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		{R: 0x10, G: 0x30, B: 0x50, A: 0xff},
		{R: 0x70, G: 0x90, B: 0xb0, A: 0xff},
		{R: 0xd0, G: 0x20, B: 0x40, A: 0xff},
		{R: 0x60, G: 0x80, B: 0xa0, A: 0xff},
		{R: 0x22, G: 0x88, B: 0xcc, A: 0xff},
		{R: 0xee, G: 0x44, B: 0x11, A: 0xff},
		{R: 0x55, G: 0xaa, B: 0x33, A: 0xff},
		{R: 0x99, G: 0x66, B: 0xdd, A: 0xff},
	})
	tests := []struct {
		name        string
		hash        func(image.Image) string
		image       image.Image
		want        string
		componentsX int
		componentsY int
	}{
		{
			name:        "four by three matrix retains every component row",
			hash:        func(source image.Image) string { return localBlurHashWithComponents(source, 4, 3) },
			image:       matrix,
			want:        "LqIXgq-4wH=k~qnf,*{,.7ne]e|2",
			componentsX: 4,
			componentsY: 3,
		},
		{
			name:        "stock six by four matrix",
			hash:        localBlurHash,
			image:       matrix,
			want:        "W,IXgq$wr;=jHu=j_3ny$b[uy|[u%ynf=B[uqm[u~q$c;L=j9j=j",
			componentsX: 6,
			componentsY: 4,
		},
		{
			name:        "solid color smaller than stock component grid",
			hash:        localBlurHash,
			image:       localBlurHashRaster(3, 1, []color.NRGBA{{R: 0x33, G: 0x66, B: 0xcc, A: 0xff}, {R: 0x33, G: 0x66, B: 0xcc, A: 0xff}, {R: 0x33, G: 0x66, B: 0xcc, A: 0xff}}),
			want:        "W~5?~Ep2fQp2fQp2u6p2fQp2fQp2u6p2fQp2fQp2u6p2fQp2fQp2",
			componentsX: 6,
			componentsY: 4,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.hash(test.image)
			if got != test.want {
				t.Fatalf("BlurHash = %q, want official %dx%d vector %q", got, test.componentsX, test.componentsY, test.want)
			}
			if wantLength := 4 + 2*test.componentsX*test.componentsY; len(got) != wantLength {
				t.Fatalf("BlurHash = %q has length %d, want %d-byte %dx%d hash", got, len(got), wantLength, test.componentsX, test.componentsY)
			}
		})
	}
}

func TestLocalBlurHashReturnsEmptyForEmptyBounds(t *testing.T) {
	if got := localBlurHash(image.NewNRGBA(image.Rect(9, 4, 9, 4))); got != "" {
		t.Fatalf("localBlurHash(empty image) = %q, want empty hash", got)
	}
}

func localBlurHashRaster(width, height int, pixels []color.NRGBA) *image.NRGBA {
	if got, want := len(pixels), width*height; got != want {
		panic("local BlurHash test raster has incorrect pixel count")
	}
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			result.SetNRGBA(x, y, pixels[y*width+x])
		}
	}
	return result
}
