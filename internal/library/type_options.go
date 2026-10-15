// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
)

// defaultImageLimit ports TypeOptions.DefaultImageOptions
// (MediaBrowser.Model/Configuration/TypeOptions.cs). The table lists limit 0
// for the image types below and limit 1 for every other listed type; a type
// or image type that the table does not list takes the ImageOption default
// of 1.
func defaultImageLimit(kind item.Kind, imageType item.ImageType) int {
	var disabled []item.ImageType
	switch kind {
	case item.KindMovie, item.KindMusicVideo, item.KindBoxSet:
		disabled = []item.ImageType{item.ImageTypeArt, item.ImageTypeDisc, item.ImageTypeBanner}
	case item.KindSeries:
		disabled = []item.ImageType{item.ImageTypeArt}
	case item.KindMusicAlbum:
		disabled = []item.ImageType{item.ImageTypeBackdrop, item.ImageTypeDisc}
	case item.KindMusicArtist:
		disabled = []item.ImageType{item.ImageTypeBanner, item.ImageTypeArt}
	case item.KindSeason:
		disabled = []item.ImageType{item.ImageTypeBackdrop, item.ImageTypeBanner, item.ImageTypeThumb}
	case item.KindEpisode:
		disabled = []item.ImageType{item.ImageTypeBackdrop}
	}
	for _, value := range disabled {
		if value == imageType {
			return 0
		}
	}
	return 1
}

// TypeOptionsFor ports LibraryOptions.GetTypeOptions: the entry whose Type
// matches kind, case-insensitively, or nil.
func (options LibraryOptions) TypeOptionsFor(kind item.Kind) *TypeOptions {
	for index := range options.TypeOptions {
		if strings.EqualFold(options.TypeOptions[index].Type, kind.String()) {
			return &options.TypeOptions[index]
		}
	}
	return nil
}

// ImageLimit ports TypeOptions.GetLimit for kind: a configured ImageOptions
// entry wins, then DefaultImageOptions, then the ImageOption default of 1.
// ItemImageProvider.RefreshImages refreshes an image type only when its limit
// is above zero (TypeOptions.IsEnabled).
func (options LibraryOptions) ImageLimit(kind item.Kind, imageType item.ImageType) int {
	if typeOptions := options.TypeOptionsFor(kind); typeOptions != nil {
		for _, option := range typeOptions.ImageOptions {
			if strings.EqualFold(option.Type, imageType.String()) {
				return option.Limit
			}
		}
	}
	return defaultImageLimit(kind, imageType)
}
