// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

func TestLocalImageProviderUsesFrozenDirectoryAndChildFacts(t *testing.T) {
	provider := providers.NewLocalImageProvider()
	modified := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	refresh := library.RefreshContext{
		Directory: library.Entry{Path: "/media", IsDir: true},
		Entries: []library.Entry{
			{Path: "/media/movie.mkv", Size: 1},
			{Path: "/media/poster.jpg", Size: 2, Modified: modified},
			{Path: "/media/fanart.jpg", Size: 2, Modified: modified},
			{Path: "/media/fanart-2.jpg", Size: 2, Modified: modified},
			{Path: "/media/extrafanart", IsDir: true},
		},
		ChildEntries: map[string][]library.Entry{
			"/media/extrafanart": {{Path: "/media/extrafanart/extra.jpg", Size: 2, Modified: modified}},
		},
	}
	target := item.Item{Kind: item.KindMovie, Path: adapterString("/media/movie.mkv"), Name: adapterString("movie")}
	handled, err := provider.Refresh(t.Context(), refresh, &target)
	if err != nil {
		t.Fatalf("refresh local images: %v", err)
	}
	if !handled {
		t.Fatal("local image provider did not claim a candidate with frozen artwork facts")
	}
	if !adapterHasImage(target.ImageInfos, item.ImageTypePrimary, "/media/poster.jpg") || !adapterHasImage(target.ImageInfos, item.ImageTypeBackdrop, "/media/fanart.jpg") || !adapterHasImage(target.ImageInfos, item.ImageTypeBackdrop, "/media/extrafanart/extra.jpg") {
		t.Fatalf("local image provider images = %#v, want poster and frozen extra-fanart discoveries", target.ImageInfos)
	}
}

func TestNFOProviderMergesParsedMetadataAndToleratesMalformedTail(t *testing.T) {
	opened := make([]string, 0, 1)
	provider := providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			opened = append(opened, path)
			return io.NopCloser(strings.NewReader(`<movie><title>NFO Movie</title><year>2024</year><genre>Drama</genre><actor><name>Actor One</name><role>Lead</role></actor><uniqueid type="tmdb">123</uniqueid><unknown>ignored</unknown><broken>`)), nil
		},
	})
	refresh := library.RefreshContext{Directory: library.Entry{Path: "/media", IsDir: true}, Entries: []library.Entry{{Path: "/media/movie.mkv"}, {Path: "/media/movie.nfo"}}}
	target := item.Item{Kind: item.KindMovie, Path: adapterString("/media/movie.mkv"), Name: adapterString("resolver title")}
	handled, err := provider.Refresh(t.Context(), refresh, &target)
	if err != nil {
		t.Fatalf("refresh NFO metadata: %v", err)
	}
	if !handled || !reflect.DeepEqual(opened, []string{"/media/movie.nfo"}) {
		t.Fatalf("NFO provider handled=%t opened=%v, want true and the known sibling NFO", handled, opened)
	}
	if target.Name == nil || *target.Name != "NFO Movie" || target.ProductionYear == nil || *target.ProductionYear != 2024 || !reflect.DeepEqual(target.Values.Genres, []string{"Drama"}) || len(target.People) != 1 || target.People[0].Name != "Actor One" {
		t.Fatalf("NFO merged item = %#v, want title/year/genre/person parsed before malformed trailing XML", target)
	}
	if value, ok := target.ProviderIDs.Get("tmdb"); !ok || value != "123" {
		t.Fatalf("NFO provider IDs = %#v, want tmdb 123", target.ProviderIDs.Snapshot())
	}
}

func TestRegistryAppliesRealAdapterStagesAndDisabledNFO(t *testing.T) {
	nfo := providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`<movie><title>NFO Title</title><thumb>https://metadata.example/nfo-poster.jpg</thumb></movie>`)), nil
		},
	})
	registry := providers.NewRegistry(providers.NewLocalImageProvider(), nfo)
	refresh := library.RefreshContext{
		Directory: library.Entry{Path: "/media", IsDir: true},
		Entries:   []library.Entry{{Path: "/media/movie.mkv"}, {Path: "/media/poster.jpg", Size: 1}, {Path: "/media/movie.nfo"}},
		Options: library.LibraryOptions{
			DisabledLocalMetadataReaders: []string{nfo.Name()},
			LocalMetadataReaderOrder:     []string{nfo.Name()},
		},
	}
	target := item.Item{Kind: item.KindMovie, Path: adapterString("/media/movie.mkv"), Name: adapterString("resolver title")}
	if err := registry.Refresh(t.Context(), refresh, &target); err != nil {
		t.Fatalf("refresh disabled NFO registry: %v", err)
	}
	if target.Name == nil || *target.Name != "resolver title" || !adapterHasImage(target.ImageInfos, item.ImageTypePrimary, "/media/poster.jpg") {
		t.Fatalf("disabled NFO registry result = %#v, want images but resolver title retained", target)
	}

	enabled := refresh
	enabled.Options.DisabledLocalMetadataReaders = nil
	target = item.Item{Kind: item.KindMovie, Path: adapterString("/media/movie.mkv"), Name: adapterString("resolver title")}
	if err := registry.Refresh(t.Context(), enabled, &target); err != nil {
		t.Fatalf("refresh enabled NFO registry: %v", err)
	}
	if target.Name == nil || *target.Name != "NFO Title" || !adapterHasImage(target.ImageInfos, item.ImageTypePrimary, "https://metadata.example/nfo-poster.jpg") {
		t.Fatalf("enabled registry result = %#v, want NFO metadata after local-image stage replaced the primary artwork", target)
	}
}

func adapterHasImage(images []item.ImageInfo, kind item.ImageType, path string) bool {
	for _, image := range images {
		if image.Type == kind && image.Path == path {
			return true
		}
	}
	return false
}

func adapterString(value string) *string { return &value }
