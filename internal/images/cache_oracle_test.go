// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"math"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
)

// These are the two exceptional literals that complete the five pinned C# cache
// oracles (the ordinary, absent-option, and scientific-notation literals remain
// in cache_test.go). Provenance: github.com/jellyfin/jellyfin at
// b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// src/Jellyfin.Drawing/ImageProcessor.cs:271-352; ImageFormat.cs:6-35.
func TestCacheKey_MatchesPinnedCSharpExceptionalOptionLiterals(t *testing.T) {
	cases := []struct {
		name string
		opts images.CacheOptions
		want string
	}{
		{
			name: "positive infinity uses invariant C# spelling",
			opts: images.CacheOptions{
				SourcePath:    "/library/a.jpg",
				Quality:       67,
				Modified:      imageTestDateModified(),
				Format:        images.FormatJPEG,
				PercentPlayed: math.Inf(1),
			},
			want: "/library/a.jpg,quality=67,datemodified=638397614456000000,f=Jpg,p=Infinity,v=3",
		},
		{
			name: "present zero option fields remain serialized",
			opts: images.CacheOptions{
				SourcePath: "/library/a.jpg",
				Quality:    0,
				Modified:   imageTestDateModified(),
				Format:     images.FormatPNG,
				Resize: images.ResizeOptions{
					Width:      intPtr(0),
					Height:     intPtr(0),
					MaxWidth:   intPtr(0),
					MaxHeight:  intPtr(0),
					FillWidth:  intPtr(0),
					FillHeight: intPtr(0),
				},
				UnplayedCount: intPtr(0),
				Blur:          intPtr(0),
			},
			want: "/library/a.jpg,quality=0,datemodified=638397614456000000,f=Png,width=0,height=0,maxwidth=0,maxheight=0,fillwidth=0,fillheight=0,p=0,blur=0,v=3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := images.CacheKey(tc.opts); got != tc.want {
				t.Errorf("CacheKey() = %q, want pinned C# literal %q", got, tc.want)
			}
		})
	}
}
