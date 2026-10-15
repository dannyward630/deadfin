// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
)

// Sources: upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:254-352.
// The cache identity is a compatibility format, including its duplicate p= field.
func TestCacheKey_UsesUpstreamFieldOrderAndVersion(t *testing.T) {
	modified := imageTestDateModified()
	got := images.CacheKey(images.CacheOptions{
		SourcePath: "/library/é/rocket🚀.png",
		Quality:    80,
		Modified:   modified,
		Format:     images.FormatWebP,
		Resize: images.ResizeOptions{
			Width:      intPtr(320),
			Height:     intPtr(180),
			MaxWidth:   intPtr(640),
			MaxHeight:  intPtr(360),
			FillWidth:  intPtr(300),
			FillHeight: intPtr(200),
		},
		PercentPlayed: 12.5,
		UnplayedCount: intPtr(7),
		Blur:          intPtr(4),
		Background:    "001122",
		Foreground:    "ffcc00",
	})
	want := "/library/é/rocket🚀.png,quality=80,datemodified=638397614456000000,f=Webp,width=320,height=180,maxwidth=640,maxheight=360,fillwidth=300,fillheight=200,p=12.5,p=7,blur=4,b=001122,fl=ffcc00,v=3"
	if got != want {
		t.Errorf("CacheKey() = %q, want %q", got, want)
	}
}

// Source: upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:283-350.
// Omitting a dimension must omit its field rather than serialize an absent value.
func TestCacheKey_OmitsAbsentOptionalFields(t *testing.T) {
	got := images.CacheKey(images.CacheOptions{
		SourcePath: "/library/a.jpg",
		Quality:    67,
		Modified:   imageTestDateModified(),
		Format:     images.FormatJPEG,
	})
	want := "/library/a.jpg,quality=67,datemodified=638397614456000000,f=Jpg,v=3"
	if got != want {
		t.Errorf("CacheKey() = %q, want %q", got, want)
	}
}

// Source: upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:319-323.
// StringBuilder.Append(double) uses .NET's general format, which keeps this
// small value in uppercase scientific notation rather than Go's fixed decimal.
func TestCacheKey_PercentPlayedUsesDotNetScientificNotation(t *testing.T) {
	got := images.CacheKey(images.CacheOptions{
		SourcePath:    "/library/a.jpg",
		Quality:       67,
		Modified:      imageTestDateModified(),
		Format:        images.FormatJPEG,
		PercentPlayed: 1e-7,
	})
	const want = "/library/a.jpg,quality=67,datemodified=638397614456000000,f=Jpg,p=1E-07,v=3"
	if got != want {
		t.Errorf("CacheKey() = %q, want %q", got, want)
	}

	if got, want := images.CachePath("resized-images", want, images.FormatJPEG), filepath.Join("resized-images", "0", "0139183f695c423bc68046b1ac8ddc47.jpg"); got != want {
		t.Errorf("CachePath() = %q, want %q", got, want)
	}
}

// Sources: upstream/jellyfin/MediaBrowser.Common/Extensions/BaseExtensions.cs:30-35;
// upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:484-521.
func TestMD5GUIDAndCachePath_UseUTF16LEAndDotNetGuidOrdering(t *testing.T) {
	if got, want := images.MD5GUID("Hello 🚀"), "0e7327646feb6592edcb8f7832357e58"; got != want {
		t.Errorf("MD5GUID(Hello rocket) = %q, want %q", got, want)
	}

	key := "/library/é/rocket🚀.png,quality=80,datemodified=638397614456000000,f=Webp,width=320,height=180,maxwidth=640,maxheight=360,fillwidth=300,fillheight=200,p=12.5,p=7,blur=4,b=001122,fl=ffcc00,v=3"
	if got, want := images.CachePath("resized-images", key, images.FormatWebP), filepath.Join("resized-images", "2", "2999fcfe798f68738b3bebed2031ffd1.webp"); got != want {
		t.Errorf("CachePath() = %q, want %q", got, want)
	}
}

// Source: upstream/jellyfin/src/Jellyfin.Drawing/ImageProcessor.cs:408-410.
func TestCacheTag_HashesPathConcatenatedWithDotNetTicks(t *testing.T) {
	if got, want := images.CacheTag("/library/é/rocket🚀.png", imageTestDateModified()), "0b8baee3c36f95642f3d3be1d71ea188"; got != want {
		t.Errorf("CacheTag() = %q, want %q", got, want)
	}
}

func imageTestDateModified() time.Time {
	return time.Date(2024, time.January, 2, 3, 4, 5, 600000000, time.UTC)
}
