// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
)

// Sources: upstream/jellyfin/MediaBrowser.Controller/Drawing/ImageHelper.cs:9-15;
// upstream/jellyfin/MediaBrowser.Model/Drawing/DrawingUtils.cs:19-104.
// The optional dimensions are present independently of their numeric value: presence
// controls cache identity even when the resize math treats zero as unrequested.
func TestResizeOptionsHasDimensions_TracksExplicitZeroValues(t *testing.T) {
	cases := []struct {
		name string
		opts images.ResizeOptions
		want bool
	}{
		{name: "absent", want: false},
		{name: "width zero", opts: images.ResizeOptions{Width: intPtr(0)}, want: true},
		{name: "max height zero", opts: images.ResizeOptions{MaxHeight: intPtr(0)}, want: true},
		{name: "fill width zero", opts: images.ResizeOptions{FillWidth: intPtr(0)}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.HasDimensions(); got != tc.want {
				t.Errorf("HasDimensions() = %t, want %t", got, tc.want)
			}
		})
	}
}

// Sources: upstream/jellyfin/MediaBrowser.Controller/Drawing/ImageHelper.cs:9-15;
// upstream/jellyfin/MediaBrowser.Model/Drawing/DrawingUtils.cs:19-104.
func TestResolveDimensions_RespectsResizeMaxAndFillOrder(t *testing.T) {
	source := images.Dimensions{Width: 400, Height: 200}
	cases := []struct {
		name string
		opts images.ResizeOptions
		want images.Dimensions
	}{
		{
			name: "width preserves aspect ratio",
			opts: images.ResizeOptions{Width: intPtr(120)},
			want: images.Dimensions{Width: 120, Height: 60},
		},
		{
			name: "height preserves aspect ratio",
			opts: images.ResizeOptions{Height: intPtr(80)},
			want: images.Dimensions{Width: 160, Height: 80},
		},
		{
			name: "width and height are fixed before bounds",
			opts: images.ResizeOptions{
				Width:    intPtr(300),
				Height:   intPtr(200),
				MaxWidth: intPtr(150),
			},
			want: images.Dimensions{Width: 150, Height: 100},
		},
		{
			name: "max height then max width constrain sequentially",
			opts: images.ResizeOptions{
				MaxHeight: intPtr(100),
				MaxWidth:  intPtr(150),
			},
			want: images.Dimensions{Width: 150, Height: 75},
		},
		{
			name: "fill uses the smaller scale after resize",
			opts: images.ResizeOptions{
				Width:      intPtr(200),
				FillWidth:  intPtr(100),
				FillHeight: intPtr(100),
			},
			want: images.Dimensions{Width: 200, Height: 100},
		},
		{
			name: "one fill dimension supplies the upstream unit counterpart",
			opts: images.ResizeOptions{FillWidth: intPtr(100)},
			want: images.Dimensions{Width: 100, Height: 50},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := images.ResolveDimensions(source, tc.opts); got != tc.want {
				t.Errorf("ResolveDimensions(%+v, %+v) = %+v, want %+v", source, tc.opts, got, tc.want)
			}
		})
	}
}

// Source: upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:223-249;
// SkiaEncoder.cs:97-99,581-584. AVIF is deliberately not an output format.
func TestSelectFormat_PreservesCurrentServerPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		source      images.Format
		clients     []images.Format
		transparent bool
		want        images.Format
	}{
		{
			name:    "webp wins even when client lists jpeg first",
			source:  images.FormatJPEG,
			clients: []images.Format{images.FormatJPEG, images.FormatWebP},
			want:    images.FormatWebP,
		},
		{
			name:        "alpha uses png when webp is unavailable",
			source:      images.FormatPNG,
			clients:     []images.Format{images.FormatJPEG, images.FormatPNG},
			transparent: true,
			want:        images.FormatPNG,
		},
		{
			name:    "otherwise client ordering chooses the first supported format",
			source:  images.FormatJPEG,
			clients: []images.Format{images.FormatPNG, images.FormatJPEG},
			want:    images.FormatPNG,
		},
		{
			name:    "svg output is allowed only for svg input",
			source:  images.FormatSVG,
			clients: []images.Format{images.FormatSVG, images.FormatJPEG},
			want:    images.FormatSVG,
		},
		{
			name:    "raster source skips svg and selects the next client format",
			source:  images.FormatPNG,
			clients: []images.Format{images.FormatSVG, images.FormatJPEG},
			want:    images.FormatJPEG,
		},
		{
			name:    "avif is ignored because the encoder does not offer it",
			source:  images.FormatJPEG,
			clients: []images.Format{images.Format("avif"), images.FormatJPEG},
			want:    images.FormatJPEG,
		},
		{
			name:    "no supported client format falls back to jpeg",
			source:  images.FormatPNG,
			clients: []images.Format{images.Format("avif")},
			want:    images.FormatJPEG,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := images.SelectFormat(tc.source, tc.clients, tc.transparent); got != tc.want {
				t.Errorf("SelectFormat(%q, %v, %t) = %q, want %q", tc.source, tc.clients, tc.transparent, got, tc.want)
			}
		})
	}
}

func intPtr(value int) *int {
	return &value
}
