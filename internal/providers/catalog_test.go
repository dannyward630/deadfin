// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// imageFetcherFunc is a providerFunc that supports kinds and lists images.
type imageFetcherFunc struct {
	providerFunc
	kinds  []item.Kind
	images []item.ImageType
}

func (provider imageFetcherFunc) SupportsKind(kind item.Kind) bool {
	for _, current := range provider.kinds {
		if current == kind {
			return true
		}
	}
	return false
}

func (provider imageFetcherFunc) SupportedImages(item.Kind) []item.ImageType {
	return provider.images
}

func noRefresh(context.Context, library.RefreshContext, *item.Item) (bool, error) { return false, nil }

// TestRegistryMetadataPluginsListRegisteredProviders checks the summary
// order and that a local-images ImageFetcher, such as a future Screen
// Grabber, is listed with no catalog change.
func TestRegistryMetadataPluginsListRegisteredProviders(t *testing.T) {
	registry := providers.NewRegistry(
		providerFunc{name: "LocalImageProvider", stage: providers.ProviderStageLocalImages, refresh: noRefresh},
		imageFetcherFunc{providerFunc: providerFunc{name: "Screen Grabber", stage: providers.ProviderStageLocalImages, refresh: noRefresh}, kinds: []item.Kind{item.KindMovie, item.KindEpisode}, images: []item.ImageType{item.ImageTypePrimary}},
		imageFetcherFunc{providerFunc: providerFunc{name: "Remote", stage: providers.ProviderStageRemoteMetadata, refresh: noRefresh}, kinds: []item.Kind{item.KindMovie}, images: []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop}},
		providerFunc{name: "Unscoped", stage: providers.ProviderStageRemoteMetadata, refresh: noRefresh},
	)
	summaries := registry.MetadataPlugins()
	if len(summaries) != 13 || summaries[0].ItemType != item.KindMovie || summaries[12].ItemType != item.KindVideo {
		t.Fatalf("summaries = %#v, want the 13 GetAllMetadataPlugins types", summaries)
	}
	want := []providers.MetadataPlugin{
		{Name: "Remote", Type: providers.MetadataPluginMetadataFetcher},
		{Name: "Screen Grabber", Type: providers.MetadataPluginImageFetcher},
		{Name: "Remote", Type: providers.MetadataPluginImageFetcher},
	}
	if !reflect.DeepEqual(summaries[0].Plugins, want) {
		t.Fatalf("movie plugins = %#v, want %#v", summaries[0].Plugins, want)
	}
	if got := summaries[0].SupportedImageTypes; !reflect.DeepEqual(got, []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop}) {
		t.Fatalf("movie supported images = %v", got)
	}
	for _, summary := range summaries {
		if summary.ItemType != item.KindEpisode {
			continue
		}
		if want := []providers.MetadataPlugin{{Name: "Screen Grabber", Type: providers.MetadataPluginImageFetcher}}; !reflect.DeepEqual(summary.Plugins, want) {
			t.Fatalf("episode plugins = %#v, want %#v", summary.Plugins, want)
		}
	}
}

// TestRegistryImageFetchersGateImageProviders follows
// BaseItemManager.IsImageFetcherEnabled for providers that implement
// ImageFetcher: the library TypeOptions entry decides, and without one the
// server MetadataOptions DisabledImageFetchers do.
func TestRegistryImageFetchersGateImageProviders(t *testing.T) {
	newRegistry := func(ranLocal *bool, serverDisabled []string) *providers.Registry {
		registry := providers.NewRegistry(
			imageFetcherFunc{providerFunc: providerFunc{name: "Screen Grabber", stage: providers.ProviderStageLocalImages, refresh: func(_ context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
				*ranLocal = true
				target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: item.ImageTypePrimary, Path: "/grab.jpg"})
				return true, nil
			}}, kinds: []item.Kind{item.KindMovie}, images: []item.ImageType{item.ImageTypePrimary}},
			imageFetcherFunc{providerFunc: providerFunc{name: "Remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(_ context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
				target.Overview = providerString("remote overview")
				target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: item.ImageTypeBackdrop, Path: "https://remote/backdrop.jpg"})
				return true, nil
			}}, kinds: []item.Kind{item.KindMovie}, images: []item.ImageType{item.ImageTypeBackdrop}},
		)
		return registry.WithDisabledImageFetchers(func(itemType string) []string {
			if itemType == "Movie" {
				return serverDisabled
			}
			return nil
		})
	}
	for _, testCase := range []struct {
		name           string
		typeOptions    []library.TypeOptions
		serverDisabled []string
		wantLocal      bool
		wantImages     []string
	}{
		{name: "no type options runs every image fetcher", wantLocal: true, wantImages: []string{"/grab.jpg", "https://remote/backdrop.jpg"}},
		{name: "empty image fetchers runs none", typeOptions: []library.TypeOptions{{Type: "Movie", MetadataFetchers: []string{"Remote"}, ImageFetchers: []string{}}}},
		{name: "listed fetchers run", typeOptions: []library.TypeOptions{{Type: "movie", MetadataFetchers: []string{"Remote"}, ImageFetchers: []string{"screen grabber", "Remote"}}}, wantLocal: true, wantImages: []string{"/grab.jpg", "https://remote/backdrop.jpg"}},
		{name: "server options disable a fetcher without an entry", serverDisabled: []string{"remote"}, wantLocal: true, wantImages: []string{"/grab.jpg"}},
		{name: "server options disable a local fetcher without an entry", serverDisabled: []string{"Screen Grabber"}, wantImages: []string{"https://remote/backdrop.jpg"}},
		{name: "an entry overrides the server options", typeOptions: []library.TypeOptions{{Type: "Movie", MetadataFetchers: []string{"Remote"}, ImageFetchers: []string{"Screen Grabber", "Remote"}}}, serverDisabled: []string{"Remote", "Screen Grabber"}, wantLocal: true, wantImages: []string{"/grab.jpg", "https://remote/backdrop.jpg"}},
		{name: "another kind's entry does not apply", typeOptions: []library.TypeOptions{{Type: "Episode", ImageFetchers: []string{}}}, wantLocal: true, wantImages: []string{"/grab.jpg", "https://remote/backdrop.jpg"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ranLocal := false
			refresh := remoteRefreshContext()
			refresh.Options.TypeOptions = testCase.typeOptions
			target := item.Item{Kind: item.KindMovie}
			if err := newRegistry(&ranLocal, testCase.serverDisabled).Refresh(t.Context(), refresh, &target); err != nil {
				t.Fatalf("registry refresh: %v", err)
			}
			if ranLocal != testCase.wantLocal {
				t.Fatalf("local image fetcher ran = %t, want %t", ranLocal, testCase.wantLocal)
			}
			if target.Overview == nil || *target.Overview != "remote overview" {
				t.Fatalf("remote metadata = %v, want the overview kept", target.Overview)
			}
			var images []string
			for _, image := range target.ImageInfos {
				images = append(images, image.Path)
			}
			if !reflect.DeepEqual(images, testCase.wantImages) {
				t.Fatalf("images = %v, want %v", images, testCase.wantImages)
			}
		})
	}
}

// TestCatalogListsDynamicImageFetchers checks that the Embedded Image
// Extractor and the Screen Grabber follow the registry's image fetchers for
// movies and episodes, with EmbeddedImageProvider.GetSupportedImages.
func TestCatalogListsDynamicImageFetchers(t *testing.T) {
	catalog := providers.Catalog{
		Registry: providers.NewRegistry(
			imageFetcherFunc{providerFunc: providerFunc{name: "Remote", stage: providers.ProviderStageRemoteMetadata, refresh: noRefresh}, kinds: []item.Kind{item.KindMovie}, images: []item.ImageType{item.ImageTypeThumb}},
		),
		ImageFetchers: []providers.ImageFetcherSource{providers.NewDynamicImages(providers.DynamicImagesOptions{})},
	}
	byKind := map[item.Kind]providers.MetadataPluginSummary{}
	for _, summary := range catalog.MetadataPlugins() {
		byKind[summary.ItemType] = summary
	}
	for _, testCase := range []struct {
		kind    item.Kind
		plugins []providers.MetadataPlugin
		images  []item.ImageType
	}{
		{
			kind: item.KindMovie,
			plugins: []providers.MetadataPlugin{
				{Name: "Remote", Type: providers.MetadataPluginMetadataFetcher},
				{Name: "Remote", Type: providers.MetadataPluginImageFetcher},
				{Name: providers.EmbeddedImageExtractorName, Type: providers.MetadataPluginImageFetcher},
				{Name: providers.ScreenGrabberName, Type: providers.MetadataPluginImageFetcher},
			},
			images: []item.ImageType{item.ImageTypeThumb, item.ImageTypePrimary, item.ImageTypeBackdrop, item.ImageTypeLogo},
		},
		{
			kind: item.KindEpisode,
			plugins: []providers.MetadataPlugin{
				{Name: providers.EmbeddedImageExtractorName, Type: providers.MetadataPluginImageFetcher},
				{Name: providers.ScreenGrabberName, Type: providers.MetadataPluginImageFetcher},
			},
			images: []item.ImageType{item.ImageTypePrimary},
		},
		{kind: item.KindSeries, plugins: []providers.MetadataPlugin{}, images: []item.ImageType{}},
	} {
		summary := byKind[testCase.kind]
		if !reflect.DeepEqual(summary.Plugins, testCase.plugins) || !reflect.DeepEqual(summary.SupportedImageTypes, testCase.images) {
			t.Errorf("%s plugins = %#v images %v, want %#v images %v", testCase.kind, summary.Plugins, summary.SupportedImageTypes, testCase.plugins, testCase.images)
		}
	}
}
