// SPDX-License-Identifier: GPL-2.0-only

package identify

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// LookupAdapter exposes the identify service through the handler-facing
// ItemLookupSearcher interface. It adapts the service's context-first Search
// method to the handler's flat-parameter signature, so handlers stay free of
// the identify package's internal types. The request context is carried through
// so disconnects and deadlines cancel provider calls, downloads, refreshes,
// and persistence.
type LookupAdapter struct{ service *Service }

// NewLookupAdapter wraps a service for handler use.
func NewLookupAdapter(service *Service) LookupAdapter { return LookupAdapter{service: service} }

// Search runs a remote search across enabled providers. The handler passes the
// extracted search fields; the adapter assembles the RemoteSearchQuery.
func (a LookupAdapter) Search(ctx context.Context, kind item.Kind, name string, year *int, providerIDs map[string]string, language, country string, albumArtists []string, songInfos []providers.SongInfo, seriesName string, artistProviderIDs map[string]string, itemID dto.UUID, searchProviderName string, includeDisabled bool) ([]providers.RemoteSearchResult, error) {
	return a.service.Search(ctx, RemoteSearchQuery{
		Kind:                     kind,
		Name:                     name,
		Year:                     year,
		ProviderIDs:              providerIDs,
		Language:                 language,
		Country:                  country,
		AlbumArtists:             albumArtists,
		SongInfos:                songInfos,
		SeriesName:               seriesName,
		ArtistIDs:                artistProviderIDs,
		ItemID:                   itemID,
		SearchProviderName:       searchProviderName,
		IncludeDisabledProviders: includeDisabled,
	})
}

// Apply applies a chosen remote-search result to an item.
func (a LookupAdapter) Apply(ctx context.Context, itemID dto.UUID, result providers.RemoteSearchResult, replaceAllImages bool) error {
	return a.service.Apply(ctx, itemID, result, replaceAllImages)
}

// ExternalIDs returns the external-id descriptors for an item's kind.
func (a LookupAdapter) ExternalIDs(ctx context.Context, itemID dto.UUID) ([]providers.ExternalIDDescriptor, error) {
	return a.service.ExternalIDs(ctx, itemID)
}

// RemoteImagesAdapter exposes the identify service through the handler-facing
// RemoteImagesService interface.
type RemoteImagesAdapter struct{ service *Service }

// NewRemoteImagesAdapter wraps a service for handler use.
func NewRemoteImagesAdapter(service *Service) RemoteImagesAdapter {
	return RemoteImagesAdapter{service: service}
}

// RemoteImages lists available remote images for an item.
func (a RemoteImagesAdapter) RemoteImages(ctx context.Context, itemID dto.UUID, imageType *item.ImageType, providerName string, includeAllLanguages bool, startIndex int, limit *int, language string) (
	RemoteImagesResult, error,
) {
	// This is the identify package's own RemoteImagesResult type, not the
	// handler's. The handler's adapter converts in the composition root.
	return a.service.RemoteImages(ctx, RemoteImagesQuery{
		ItemID:              itemID,
		Type:                imageType,
		ProviderName:        providerName,
		IncludeAllLanguages: includeAllLanguages,
		StartIndex:          startIndex,
		Limit:               limit,
		Language:            language,
	})
}

// RemoteImageProviders lists image providers supporting the item.
func (a RemoteImagesAdapter) RemoteImageProviders(ctx context.Context, itemID dto.UUID) ([]ImageProviderInfo, error) {
	return a.service.RemoteImageProviders(ctx, itemID)
}

// DownloadImage downloads a remote image and stores it on the item.
func (a RemoteImagesAdapter) DownloadImage(ctx context.Context, itemID dto.UUID, imageType item.ImageType, imageURL string) error {
	return a.service.DownloadImage(ctx, itemID, imageType, imageURL)
}
