// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// MetadataPluginCatalog lists the registered metadata providers per item
// kind. *providers.Registry satisfies it.
type MetadataPluginCatalog interface {
	MetadataPlugins() []providers.MetadataPluginSummary
}

// RegisterLibraryOptionsInfo registers GET /Libraries/AvailableOptions.
// metadataOptions returns the server configuration's MetadataOptions; nil
// acts as an empty list.
func RegisterLibraryOptionsInfo(rt *router.Router, catalog MetadataPluginCatalog, metadataOptions func() []config.MetadataOptions) {
	h := libraryOptionsInfoHandler{catalog: catalog, metadataOptions: metadataOptions}
	rt.Handle(http.MethodGet, "/Libraries/AvailableOptions", router.Route{
		OperationID: "GetLibraryOptionsInfo",
		Policy:      string(auth.PolicyFirstTimeSetupOrDefault),
	}, h.serve)
}

type libraryOptionsInfoHandler struct {
	catalog         MetadataPluginCatalog
	metadataOptions func() []config.MetadataOptions
}

// libraryOptionInfo is LibraryOptionInfoDto.
type libraryOptionInfo struct {
	Name           string `json:"Name"`
	DefaultEnabled bool   `json:"DefaultEnabled"`
}

// libraryTypeOptionsInfo is LibraryTypeOptionsDto.
type libraryTypeOptionsInfo struct {
	Type                 string                `json:"Type"`
	MetadataFetchers     []libraryOptionInfo   `json:"MetadataFetchers"`
	ImageFetchers        []libraryOptionInfo   `json:"ImageFetchers"`
	SimilarItemProviders []libraryOptionInfo   `json:"SimilarItemProviders"`
	SupportedImageTypes  []string              `json:"SupportedImageTypes"`
	DefaultImageOptions  []library.ImageOption `json:"DefaultImageOptions"`
}

// libraryOptionsResult is LibraryOptionsResultDto.
type libraryOptionsResult struct {
	MetadataSavers        []libraryOptionInfo      `json:"MetadataSavers"`
	MetadataReaders       []libraryOptionInfo      `json:"MetadataReaders"`
	SubtitleFetchers      []libraryOptionInfo      `json:"SubtitleFetchers"`
	LyricFetchers         []libraryOptionInfo      `json:"LyricFetchers"`
	MediaSegmentProviders []libraryOptionInfo      `json:"MediaSegmentProviders"`
	TypeOptions           []libraryTypeOptionsInfo `json:"TypeOptions"`
}

type libraryOptionsInfoQuery struct {
	LibraryContentType nullableCollectionType `query:"libraryContentType"`
	IsNewLibrary       bool                   `query:"isNewLibrary"`
}

// nullableCollectionType binds a CollectionType? query value as Jellyfin's
// NullableEnumModelBinder does: EnumConverter parses a name in any casing or
// a number, and a value it cannot parse binds as null without an error.
type nullableCollectionType struct {
	value item.CollectionType
	set   bool
}

func (c *nullableCollectionType) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	if number, err := strconv.ParseInt(value, 10, 64); err == nil {
		if number >= 0 && number <= 255 {
			c.value, c.set = item.CollectionType(number), true
		}
		return nil
	}
	for _, candidate := range collectionTypeCandidates() {
		if strings.EqualFold(candidate.String(), value) {
			c.value, c.set = candidate, true
			return nil
		}
	}
	return nil
}

func collectionTypeCandidates() []item.CollectionType {
	candidates := make([]item.CollectionType, 0, 28)
	for value := item.CollectionTypeUnknown; value <= item.CollectionTypeFolders; value++ {
		candidates = append(candidates, value)
	}
	for value := item.CollectionTypeTVShowSeries; value <= item.CollectionTypeMovieGenre; value++ {
		candidates = append(candidates, value)
	}
	return candidates
}

// representativeItemTypes ports LibraryController.GetRepresentativeItemTypes.
func representativeItemTypes(contentType nullableCollectionType) []item.Kind {
	if !contentType.set {
		return []item.Kind{item.KindSeries, item.KindSeason, item.KindEpisode, item.KindMovie}
	}
	switch contentType.value {
	case item.CollectionTypeBoxSets:
		return []item.Kind{item.KindBoxSet}
	case item.CollectionTypePlaylists:
		return []item.Kind{item.KindPlaylist}
	case item.CollectionTypeMovies:
		return []item.Kind{item.KindMovie}
	case item.CollectionTypeTVShows:
		return []item.Kind{item.KindSeries, item.KindSeason, item.KindEpisode}
	case item.CollectionTypeBooks:
		return []item.Kind{item.KindBook, item.KindAudioBook}
	case item.CollectionTypeMusic:
		return []item.Kind{item.KindMusicArtist, item.KindMusicAlbum, item.KindAudio, item.KindMusicVideo}
	case item.CollectionTypeHomeVideos, item.CollectionTypePhotos:
		return []item.Kind{item.KindVideo, item.KindPhoto}
	case item.CollectionTypeMusicVideos:
		return []item.Kind{item.KindMusicVideo}
	default:
		return []item.Kind{item.KindSeries, item.KindSeason, item.KindEpisode, item.KindMovie}
	}
}

// serve ports LibraryController.GetLibraryOptionsInfo. Deadfin registers no
// metadata savers, subtitle fetchers, lyric fetchers, or media segment
// providers, so those lists are empty, as on a stock server without plugins.
func (h libraryOptionsInfoHandler) serve(w http.ResponseWriter, r *http.Request) {
	var query libraryOptionsInfoQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		writeLibraryRequestError(w, err)
		return
	}
	types := representativeItemTypes(query.LibraryContentType)
	var summaries []providers.MetadataPluginSummary
	if h.catalog != nil {
		for _, summary := range h.catalog.MetadataPlugins() {
			if slices.Contains(types, summary.ItemType) {
				summaries = append(summaries, summary)
			}
		}
	}
	slices.SortStableFunc(summaries, func(a, b providers.MetadataPluginSummary) int {
		return slices.Index(types, a.ItemType) - slices.Index(types, b.ItemType)
	})
	var metadataOptions []config.MetadataOptions
	if h.metadataOptions != nil {
		metadataOptions = h.metadataOptions()
	}

	result := libraryOptionsResult{
		MetadataSavers:        []libraryOptionInfo{},
		MetadataReaders:       []libraryOptionInfo{},
		SubtitleFetchers:      []libraryOptionInfo{},
		LyricFetchers:         []libraryOptionInfo{},
		MediaSegmentProviders: []libraryOptionInfo{},
		TypeOptions:           make([]libraryTypeOptionsInfo, 0, len(types)),
	}
	for _, summary := range summaries {
		for _, plugin := range summary.Plugins {
			if plugin.Type == providers.MetadataPluginLocalMetadataProvider {
				result.MetadataReaders = appendDistinctOption(result.MetadataReaders, libraryOptionInfo{Name: plugin.Name, DefaultEnabled: true})
			}
		}
	}
	for _, kind := range types {
		typeOptions := libraryTypeOptionsInfo{
			Type:                 kind.String(),
			MetadataFetchers:     []libraryOptionInfo{},
			ImageFetchers:        []libraryOptionInfo{},
			SimilarItemProviders: []libraryOptionInfo{},
			SupportedImageTypes:  []string{},
			DefaultImageOptions:  defaultTypeImageOptions(kind),
		}
		options := metadataOptionsForType(metadataOptions, kind.String())
		for _, summary := range summaries {
			if summary.ItemType != kind {
				continue
			}
			for _, plugin := range summary.Plugins {
				switch plugin.Type {
				case providers.MetadataPluginMetadataFetcher:
					typeOptions.MetadataFetchers = appendDistinctOption(typeOptions.MetadataFetchers, libraryOptionInfo{
						Name:           plugin.Name,
						DefaultEnabled: metadataFetcherEnabledByDefault(plugin.Name, kind, query.IsNewLibrary, options),
					})
				case providers.MetadataPluginImageFetcher:
					typeOptions.ImageFetchers = appendDistinctOption(typeOptions.ImageFetchers, libraryOptionInfo{
						Name:           plugin.Name,
						DefaultEnabled: imageFetcherEnabledByDefault(plugin.Name, kind, query.IsNewLibrary, options),
					})
				}
			}
			for _, imageType := range summary.SupportedImageTypes {
				if name := imageType.String(); !slices.Contains(typeOptions.SupportedImageTypes, name) {
					typeOptions.SupportedImageTypes = append(typeOptions.SupportedImageTypes, name)
				}
			}
			// The stock local similar-items provider (LocalSimilarityProvider,
			// always enabled) for each kind that Deadfin's local service serves.
			if localSimilarItemsSupported(kind) {
				typeOptions.SimilarItemProviders = append(typeOptions.SimilarItemProviders, libraryOptionInfo{Name: localSimilarItemsProviderName, DefaultEnabled: true})
			}
		}
		result.TypeOptions = append(result.TypeOptions, typeOptions)
	}
	_ = render.JSON(w, r, http.StatusOK, result)
}

// appendDistinctOption keeps the first option of each name, as DistinctBy
// with StringComparer.OrdinalIgnoreCase does.
func appendDistinctOption(options []libraryOptionInfo, option libraryOptionInfo) []libraryOptionInfo {
	for _, current := range options {
		if strings.EqualFold(current.Name, option.Name) {
			return options
		}
	}
	return append(options, option)
}

// metadataOptionsForType ports GetMetadataOptionsForType: the first server
// MetadataOptions entry whose ItemType matches, or nil.
func metadataOptionsForType(options []config.MetadataOptions, itemType string) *config.MetadataOptions {
	for index := range options {
		if strings.EqualFold(options[index].ItemType, itemType) {
			return &options[index]
		}
	}
	return nil
}

// metadataFetcherEnabledByDefault ports
// LibraryController.IsMetadataFetcherEnabledByDefault.
func metadataFetcherEnabledByDefault(name string, kind item.Kind, isNewLibrary bool, options *config.MetadataOptions) bool {
	if isNewLibrary {
		if strings.EqualFold(name, "TheMovieDb") {
			return kind != item.KindSeason && kind != item.KindEpisode && kind != item.KindMusicVideo
		}
		return strings.EqualFold(name, "TheTVDB") ||
			strings.EqualFold(name, "TheAudioDB") ||
			strings.EqualFold(name, "MusicBrainz")
	}
	return options == nil || !containsFoldName(options.DisabledMetadataFetchers, name)
}

// imageFetcherEnabledByDefault ports
// LibraryController.IsImageFetcherEnabledByDefault.
func imageFetcherEnabledByDefault(name string, kind item.Kind, isNewLibrary bool, options *config.MetadataOptions) bool {
	if isNewLibrary {
		if strings.EqualFold(name, "TheMovieDb") {
			return kind != item.KindSeries && kind != item.KindSeason && kind != item.KindEpisode && kind != item.KindMusicVideo
		}
		return strings.EqualFold(name, "TheTVDB") ||
			strings.EqualFold(name, "Screen Grabber") ||
			strings.EqualFold(name, "TheAudioDB") ||
			strings.EqualFold(name, "Image Extractor")
	}
	return options == nil || !containsFoldName(options.DisabledImageFetchers, name)
}

func containsFoldName(names []string, name string) bool {
	for _, current := range names {
		if strings.EqualFold(current, name) {
			return true
		}
	}
	return false
}

// defaultTypeImageOptions ports TypeOptions.DefaultImageOptions. A kind
// without an entry gets an empty list. ImageOption's constructor sets
// Limit to 1; each entry here states its Limit.
func defaultTypeImageOptions(kind item.Kind) []library.ImageOption {
	option := func(imageType item.ImageType, limit, minWidth int) library.ImageOption {
		return library.ImageOption{Type: imageType.String(), Limit: limit, MinWidth: minWidth}
	}
	switch kind {
	case item.KindMovie, item.KindMusicVideo:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 1, 1280),
			option(item.ImageTypeArt, 0, 0),
			option(item.ImageTypeDisc, 0, 0),
			option(item.ImageTypePrimary, 1, 0),
			option(item.ImageTypeBanner, 0, 0),
			option(item.ImageTypeThumb, 1, 0),
			option(item.ImageTypeLogo, 1, 0),
		}
	case item.KindSeries:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 1, 1280),
			option(item.ImageTypeArt, 0, 0),
			option(item.ImageTypePrimary, 1, 0),
			option(item.ImageTypeBanner, 1, 0),
			option(item.ImageTypeThumb, 1, 0),
			option(item.ImageTypeLogo, 1, 0),
		}
	case item.KindMusicAlbum:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 0, 1280),
			option(item.ImageTypeDisc, 0, 0),
		}
	case item.KindMusicArtist:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 1, 1280),
			option(item.ImageTypeBanner, 0, 0),
			option(item.ImageTypeArt, 0, 0),
			option(item.ImageTypeLogo, 1, 0),
		}
	case item.KindBoxSet:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 1, 1280),
			option(item.ImageTypePrimary, 1, 0),
			option(item.ImageTypeThumb, 1, 0),
			option(item.ImageTypeLogo, 1, 0),
			option(item.ImageTypeArt, 0, 0),
			option(item.ImageTypeDisc, 0, 0),
			option(item.ImageTypeBanner, 0, 0),
		}
	case item.KindSeason:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 0, 1280),
			option(item.ImageTypePrimary, 1, 0),
			option(item.ImageTypeBanner, 0, 0),
			option(item.ImageTypeThumb, 0, 0),
		}
	case item.KindEpisode:
		return []library.ImageOption{
			option(item.ImageTypeBackdrop, 0, 1280),
			option(item.ImageTypePrimary, 1, 0),
		}
	default:
		return []library.ImageOption{}
	}
}
