// SPDX-License-Identifier: GPL-2.0-only

package providers

import (
	"slices"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// KindSupporter is implemented by a provider that enriches only some item
// kinds. The catalog lists a provider for a kind only when it supports the
// kind, as each upstream provider's Supports or generic type does. A local
// image provider without it is never listed; it runs for every kind.
type KindSupporter interface {
	SupportsKind(item.Kind) bool
}

// ImageFetcher is implemented by a provider that supplies images and that a
// library can turn on or off through TypeOptions.ImageFetchers. It mirrors
// IRemoteImageProvider and IDynamicImageProvider GetSupportedImages: a
// provider that returns image types for a kind is listed as an image fetcher
// for that kind in GET /Libraries/AvailableOptions.
//
// The registry honors the library's choice. A local-images provider that
// implements ImageFetcher runs only when the kind's TypeOptions entry lists
// it. A remote metadata provider that implements it keeps its metadata but
// drops the images it added when the entry does not list it. Without a
// TypeOptions entry for the kind, the server MetadataOptions
// DisabledImageFetchers decide (Registry.WithDisabledImageFetchers). Image fetchers
// that run outside the registry, such as DynamicImages, are listed through
// an ImageFetcherSource in Catalog and gate themselves.
type ImageFetcher interface {
	SupportedImages(item.Kind) []item.ImageType
}

// MetadataPluginType matches MediaBrowser.Model.Configuration.MetadataPluginType
// for the plugin kinds Deadfin registers.
type MetadataPluginType uint8

const (
	MetadataPluginImageFetcher MetadataPluginType = iota
	MetadataPluginLocalMetadataProvider
	MetadataPluginMetadataFetcher
)

// MetadataPlugin is one provider entry of a plugin summary.
type MetadataPlugin struct {
	Name string
	Type MetadataPluginType
}

// MetadataPluginSummary lists the providers of one item kind.
type MetadataPluginSummary struct {
	ItemType            item.Kind
	Plugins             []MetadataPlugin
	SupportedImageTypes []item.ImageType
}

// catalogKinds returns the item type order of
// ProviderManager.GetAllMetadataPlugins.
func catalogKinds() []item.Kind {
	return []item.Kind{
		item.KindMovie,
		item.KindBoxSet,
		item.KindBook,
		item.KindSeries,
		item.KindSeason,
		item.KindEpisode,
		item.KindMusicAlbum,
		item.KindMusicArtist,
		item.KindAudio,
		item.KindAudioBook,
		item.KindStudio,
		item.KindMusicVideo,
		item.KindVideo,
	}
}

// MetadataPlugins ports ProviderManager.GetAllMetadataPlugins over the
// registered providers. Each summary lists the local metadata readers, then
// the metadata fetchers, then the image fetchers, in registration order, as
// GetPluginSummary does. SupportedImageTypes is the distinct union of the
// image fetchers' types.
func (r *Registry) MetadataPlugins() []MetadataPluginSummary {
	kinds := catalogKinds()
	summaries := make([]MetadataPluginSummary, 0, len(kinds))
	for _, kind := range kinds {
		summary := MetadataPluginSummary{ItemType: kind, Plugins: []MetadataPlugin{}, SupportedImageTypes: []item.ImageType{}}
		if r != nil {
			summary.Plugins = append(summary.Plugins, r.stagePlugins(kind, ProviderStageLocalMetadata, MetadataPluginLocalMetadataProvider)...)
			summary.Plugins = append(summary.Plugins, r.stagePlugins(kind, ProviderStageRemoteMetadata, MetadataPluginMetadataFetcher)...)
			for _, provider := range r.providers {
				images := supportedImages(provider, kind)
				if len(images) == 0 {
					continue
				}
				summary.Plugins = append(summary.Plugins, MetadataPlugin{Name: provider.Name(), Type: MetadataPluginImageFetcher})
				for _, imageType := range images {
					if !slices.Contains(summary.SupportedImageTypes, imageType) {
						summary.SupportedImageTypes = append(summary.SupportedImageTypes, imageType)
					}
				}
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (r *Registry) stagePlugins(kind item.Kind, stage ProviderStage, pluginType MetadataPluginType) []MetadataPlugin {
	var plugins []MetadataPlugin
	for _, provider := range r.providers {
		if provider == nil || provider.Stage() != stage {
			continue
		}
		if supporter, ok := provider.(KindSupporter); ok && supporter.SupportsKind(kind) {
			plugins = append(plugins, MetadataPlugin{Name: provider.Name(), Type: pluginType})
		}
	}
	return plugins
}

// supportedImages returns the image types provider fetches for kind, or nil
// when it is not an image fetcher for kind.
func supportedImages(provider Provider, kind item.Kind) []item.ImageType {
	if provider == nil {
		return nil
	}
	fetcher, ok := provider.(ImageFetcher)
	if !ok {
		return nil
	}
	if supporter, ok := provider.(KindSupporter); ok && !supporter.SupportsKind(kind) {
		return nil
	}
	return fetcher.SupportedImages(kind)
}

// imageFetcherEnabled ports BaseItemManager.IsImageFetcherEnabled: a library
// TypeOptions entry for the kind enables exactly the fetchers it names, so an
// empty ImageFetchers list runs none; without an entry, a fetcher runs unless
// the server MetadataOptions for the type disables it.
func imageFetcherEnabled(typeOptions *library.TypeOptions, serverDisabled []string, name string) bool {
	if typeOptions != nil {
		return containsFold(typeOptions.ImageFetchers, name)
	}
	return !containsFold(serverDisabled, name)
}

// ImageFetcherInfo is one image fetcher that runs outside the registry.
type ImageFetcherInfo struct {
	Name            string
	SupportedImages []item.ImageType
}

// ImageFetcherSource lists the image fetchers for a kind that run outside
// the registry, such as DynamicImages, in their default order.
type ImageFetcherSource interface {
	ImageFetchers(item.Kind) []ImageFetcherInfo
}

// Catalog lists the registry providers and then the image fetchers of each
// source. Upstream GetImageProvidersInternal orders image providers by
// IHasOrder; the remote image providers have order 0 and the dynamic ones
// 99 and 100, so they follow the registry's image fetchers.
type Catalog struct {
	Registry      *Registry
	ImageFetchers []ImageFetcherSource
}

// MetadataPlugins returns Registry.MetadataPlugins with each source's image
// fetchers appended to the summary of its kind.
func (c Catalog) MetadataPlugins() []MetadataPluginSummary {
	summaries := c.Registry.MetadataPlugins()
	for index := range summaries {
		summary := &summaries[index]
		for _, source := range c.ImageFetchers {
			if source == nil {
				continue
			}
			for _, fetcher := range source.ImageFetchers(summary.ItemType) {
				if len(fetcher.SupportedImages) == 0 {
					continue
				}
				summary.Plugins = append(summary.Plugins, MetadataPlugin{Name: fetcher.Name, Type: MetadataPluginImageFetcher})
				for _, imageType := range fetcher.SupportedImages {
					if !slices.Contains(summary.SupportedImageTypes, imageType) {
						summary.SupportedImageTypes = append(summary.SupportedImageTypes, imageType)
					}
				}
			}
		}
	}
	return summaries
}
