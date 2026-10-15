// SPDX-License-Identifier: GPL-2.0-only

package local_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/metadata/local"
)

func TestDiscoverImagesPrefersExtensionAndReturnsStableNamedImageOrder(t *testing.T) {
	fixture := imageFixture{
		modified: time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC),
		entries: map[string][]local.ImageEntry{
			"/library/Movie": {
				{Name: "poster.jpg"},
				{Name: "poster.png"},
				{Name: "art.webp"},
				{Name: "fanart.jpg"},
				{Name: "banner.gif"},
				{Name: "logo.svg"},
				{Name: "thumb.tbn"},
				{Name: "disc.jpeg"},
				{Name: "clearlogo.png"},
				{Name: "zero-byte.png"},
			},
		},
		stats: map[string]local.ImageStat{},
	}
	for _, name := range fixture.entries["/library/Movie"] {
		fixture.stats[filepath.Join("/library/Movie", name.Name)] = local.ImageStat{Size: 128, Modified: fixture.modified}
	}
	fixture.stats["/library/Movie/zero-byte.png"] = local.ImageStat{Size: 0, Modified: fixture.modified}

	got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{
		Directory: "/library/Movie",
		ItemPath:  "/library/Movie/Movie.mkv",
		Name:      "Movie",
		Kind:      item.KindMovie,
	}))
	if err != nil {
		t.Fatalf("discover images: %v", err)
	}
	want := []item.ImageInfo{
		fixture.image(item.ImageTypePrimary, 0, "/library/Movie/poster.png"),
		fixture.image(item.ImageTypeArt, 0, "/library/Movie/art.webp"),
		fixture.image(item.ImageTypeBackdrop, 0, "/library/Movie/fanart.jpg"),
		fixture.image(item.ImageTypeBanner, 0, "/library/Movie/banner.gif"),
		fixture.image(item.ImageTypeLogo, 0, "/library/Movie/logo.svg"),
		fixture.image(item.ImageTypeThumb, 0, "/library/Movie/thumb.tbn"),
		fixture.image(item.ImageTypeDisc, 0, "/library/Movie/disc.jpeg"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered images = %#v, want %#v", got, want)
	}
	wantReadPaths := []string{
		"/library/Movie",
		"/library/Movie/extrafanart",
	}
	if !reflect.DeepEqual(fixture.readPaths, wantReadPaths) {
		t.Fatalf("directory read paths = %#v, want %#v", fixture.readPaths, wantReadPaths)
	}
}

type imageFixture struct {
	entries   map[string][]local.ImageEntry
	stats     map[string]local.ImageStat
	statErrs  map[string]error
	reads     int
	readPaths []string
	modified  time.Time
}

func (f *imageFixture) context(imageContext local.ImageContext) local.ImageContext {
	imageContext.ReadDir = func(ctx context.Context, directory string) ([]local.ImageEntry, error) {
		f.reads++
		f.readPaths = append(f.readPaths, directory)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return append([]local.ImageEntry(nil), f.entries[directory]...), nil
	}
	imageContext.Stat = func(ctx context.Context, path string) (local.ImageStat, error) {
		if err := ctx.Err(); err != nil {
			return local.ImageStat{}, err
		}
		if err := f.statErrs[path]; err != nil {
			return local.ImageStat{}, err
		}
		stat, ok := f.stats[path]
		if !ok {
			return local.ImageStat{}, fs.ErrNotExist
		}
		return stat, nil
	}
	return imageContext
}

func (f *imageFixture) image(kind item.ImageType, index int, path string) item.ImageInfo {
	modified := f.modified
	return item.ImageInfo{Type: kind, Index: index, Path: path, DateModified: &modified}
}

func (f *imageFixture) addFiles(directory string, names ...string) {
	if f.entries == nil {
		f.entries = make(map[string][]local.ImageEntry)
	}
	if f.stats == nil {
		f.stats = make(map[string]local.ImageStat)
	}
	if f.modified.IsZero() {
		f.modified = time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	}
	for _, name := range names {
		f.entries[directory] = append(f.entries[directory], local.ImageEntry{Name: name})
		f.stats[filepath.Join(directory, name)] = local.ImageStat{Size: 128, Modified: f.modified}
	}
}
