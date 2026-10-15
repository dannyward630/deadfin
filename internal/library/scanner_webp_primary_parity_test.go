// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerPersistsBasenameWebPPrimaryForMovieAndFolder drives a genuine
// WebP through local discovery, native metadata extraction, SQLite, and the
// public DTO builder. Stock presents the same artwork on both the movie and
// its ordinary containing Folder, so both shapes must retain the exact local
// primary tag and one persisted hash.
func TestScannerPersistsBasenameWebPPrimaryForMovieAndFolder(t *testing.T) {
	fixture := scanWebPPrimaryFixture(t)
	for _, value := range []struct {
		shape string
		item  item.Item
		dto   dto.BaseItemDto
	}{
		{shape: "movie", item: fixture.movie, dto: fixture.movieDTO},
		{shape: "folder", item: fixture.folder, dto: fixture.folderDTO},
	} {
		t.Run(value.shape, func(t *testing.T) {
			image := scannerWebPPrimaryImage(t, value.item, fixture.artwork, fixture.modified)
			if image.BlurHash == nil || *image.BlurHash == "" {
				t.Fatalf("persisted %s WebP primary = %#v, want a durable BlurHash", value.shape, image)
			}
			if got, want := value.dto.ImageTags["Primary"], filepath.Base(fixture.artwork); got != want {
				t.Fatalf("%s DTO Primary ImageTag = %q, want exact basename tag %q", value.shape, got, want)
			}
			if got, want := value.dto.ImageBlurHashes["Primary"][filepath.Base(fixture.artwork)], *image.BlurHash; got != want {
				t.Fatalf("%s DTO Primary ImageBlurHash = %q, want persisted hash %q", value.shape, got, want)
			}
		})
	}
}

// TestScannerLocalImagePrimaryRetainsPNGsAndJPEGs keeps the established image
// formats on the same scan/store/DTO route while WebP decoding is registered.
func TestScannerLocalImagePrimaryRetainsPNGsAndJPEGs(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		ext    string
		source string
	}{
		{name: "PNG", ext: ".png", source: filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "alpha.png")},
		{name: "JPEG", ext: ".jpg", source: filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "landscape.jpg")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newScannerHarness(t, &scannerFixtureResolver{})
			directory := scannerPath(t, harness.source, "Still "+testCase.name)
			media := scannerPath(t, directory, "Still "+testCase.name+".mkv")
			artwork := scannerPath(t, directory, "Still "+testCase.name+testCase.ext)
			writeScannerFile(t, media, "movie")
			copyScannerReplayImage(t, testCase.source, artwork)
			scannerWebPUseLocalImageRegistry(t, harness)

			result, err := harness.scanner.Scan(t.Context())
			if err != nil || len(result.Errors) != 0 {
				t.Fatalf("scan %s primary artwork = %#v, %v", testCase.name, result, err)
			}
			movie := scannerItemAtPath(t, harness.items, media)
			image := scannerWebPPrimaryImage(t, movie, artwork, time.Time{})
			if image.BlurHash == nil || *image.BlurHash == "" {
				t.Fatalf("persisted %s primary = %#v, want a durable BlurHash", testCase.name, image)
			}
			dtoValue := scannerWebPDTO(t, harness.items, movie)
			if got, want := dtoValue.ImageTags["Primary"], filepath.Base(artwork); got != want {
				t.Fatalf("%s DTO Primary ImageTag = %q, want %q", testCase.name, got, want)
			}
		})
	}
}

type scannerWebPFixture struct {
	artwork   string
	modified  time.Time
	movie     item.Item
	folder    item.Item
	movieDTO  dto.BaseItemDto
	folderDTO dto.BaseItemDto
}

func scanWebPPrimaryFixture(t *testing.T) scannerWebPFixture {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	directory := scannerPath(t, harness.source, "Subtitle Sample (2024)")
	media := scannerPath(t, directory, "Subtitle Sample (2024).mkv")
	artwork := scannerPath(t, directory, "Subtitle Sample (2024).webp")
	modified := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	writeScannerFile(t, media, "movie")
	copyScannerReplayImage(t, filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "texture.webp"), artwork)
	if err := os.Chtimes(artwork, modified, modified); err != nil {
		t.Fatalf("set deterministic WebP timestamp: %v", err)
	}
	scannerWebPUseLocalImageRegistry(t, harness)

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan basename WebP primary: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan basename WebP primary errors = %v, want none", result.Errors)
	}
	folder := scannerItemAtPath(t, harness.items, directory)
	movie := scannerItemAtPath(t, harness.items, media)
	if folder.Kind != item.KindFolder || !folder.IsFolder || movie.Kind != item.KindMovie || movie.IsFolder || movie.ParentID == nil || *movie.ParentID != folder.ID {
		t.Fatalf("WebP directory topology: folder=%#v movie=%#v, want visible Folder owning Movie", folder, movie)
	}
	return scannerWebPFixture{
		artwork: artwork, modified: modified, movie: movie, folder: folder,
		movieDTO: scannerWebPDTO(t, harness.items, movie), folderDTO: scannerWebPDTO(t, harness.items, folder),
	}
}

func scannerWebPUseLocalImageRegistry(t *testing.T, harness *scannerHarness) {
	t.Helper()
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close scanner before configuring local image provider: %v", err)
	}
	registry := providers.NewRegistry(
		providers.NewLocalImageProvider(),
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }}),
	)
	harness.chain = library.NewChain(scannerProviderContextResolver{base: &scannerFixtureResolver{}})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})
}

func scannerWebPPrimaryImage(t *testing.T, value item.Item, path string, modified time.Time) item.ImageInfo {
	t.Helper()
	for _, image := range value.ImageInfos {
		if image.Type != item.ImageTypePrimary || image.Index != 0 || image.Path != path {
			continue
		}
		if image.Width != 640 || image.Height != 360 {
			t.Fatalf("persisted primary dimensions = %dx%d, want 640x360", image.Width, image.Height)
		}
		if !modified.IsZero() && (image.DateModified == nil || !image.DateModified.Equal(modified)) {
			t.Fatalf("persisted primary DateModified = %v, want %s", image.DateModified, modified.Format(time.RFC3339))
		}
		return image
	}
	t.Fatalf("persisted images = %#v, want Primary %q", value.ImageInfos, path)
	return item.ImageInfo{}
}

func scannerWebPDTO(t *testing.T, items *store.ItemStore, value item.Item) dto.BaseItemDto {
	t.Helper()
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{
		EnableImages:   true,
		ImageTypes:     []item.ImageType{item.ImageTypePrimary},
		ImageTypeLimit: 1,
	}, nil)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("build image DTO = %#v, %v", result, err)
	}
	return result.Items[0]
}
