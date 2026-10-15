// SPDX-License-Identifier: GPL-2.0-only

package audiodb

import (
	"context"
	"errors"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

const (
	ProviderName = "TheAudioDB"
	PluginName   = "AudioDB"
	PluginID     = "a629c0da-fac5-4c7e-931a-7174223f14c8"

	// DefaultRequestInterval observes TheAudioDB's free 30-request/minute limit.
	DefaultRequestInterval = 2 * time.Second
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

type Options struct {
	HTTP   *remote.Client
	Config Config
}

type Provider struct {
	api    *apiClient
	config Config
}

func NewProvider(options Options) *Provider {
	config := normalizeConfig(options.Config)
	var api *apiClient
	if options.HTTP != nil && config.APIKey != "" {
		api = newAPIClient(options.HTTP, config)
	}
	return &Provider{api: api, config: config}
}

func (*Provider) Name() string { return ProviderName }

func (*Provider) Stage() providers.ProviderStage { return providers.ProviderStageRemoteMetadata }

// SupportsKind reports the kinds of AudioDbArtistProvider and
// AudioDbAlbumProvider.
func (*Provider) SupportsKind(kind item.Kind) bool {
	return kind == item.KindMusicArtist || kind == item.KindMusicAlbum
}

// SupportedImages ports GetSupportedImages of AudioDbArtistImageProvider and
// AudioDbAlbumImageProvider.
func (*Provider) SupportedImages(kind item.Kind) []item.ImageType {
	switch kind {
	case item.KindMusicArtist:
		return []item.ImageType{item.ImageTypePrimary, item.ImageTypeLogo, item.ImageTypeBanner, item.ImageTypeBackdrop}
	case item.KindMusicAlbum:
		return []item.ImageType{item.ImageTypePrimary, item.ImageTypeDisc}
	default:
		return nil
	}
}

// Refresh runs after MusicBrainz and requires its stable artist or release
// group identity, exactly like the pinned AudioDb providers.
func (provider *Provider) Refresh(ctx context.Context, refresh library.RefreshContext, target *item.Item) (bool, error) {
	if target == nil {
		return false, errors.New("audiodb: item is required")
	}
	if provider.api == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	replaceImages := refresh.ReplaceAllImages && refresh.Image == library.RefreshModeFullRefresh
	switch target.Kind {
	case item.KindMusicArtist:
		return provider.refreshArtist(ctx, target, metadataLanguage(refresh), replaceImages)
	case item.KindMusicAlbum:
		return provider.refreshAlbum(ctx, target, metadataLanguage(refresh), replaceImages)
	default:
		return false, nil
	}
}

func (provider *Provider) refreshArtist(ctx context.Context, target *item.Item, language string, replaceImages bool) (bool, error) {
	id, ok := target.ProviderIDs.Get("MusicBrainzArtist")
	id = strings.TrimSpace(id)
	if !ok || id == "" {
		return false, nil
	}
	result, err := provider.api.getArtist(ctx, id)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	if genre := strings.TrimSpace(result.Genre); genre != "" {
		target.Values.Genres = []string{genre}
	}
	setString(&target.Overview, stripHTML(localizedArtistBiography(*result, language)))
	setProviderID(&target.ProviderIDs, "AudioDbArtist", result.IDArtist)
	setProviderID(&target.ProviderIDs, "MusicBrainzArtist", result.MusicBrainzArtist)
	mergeImages(target, []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: strings.TrimSpace(result.ArtistThumb)},
		{Type: item.ImageTypeLogo, Path: strings.TrimSpace(result.ArtistLogo)},
		{Type: item.ImageTypeBanner, Path: strings.TrimSpace(result.ArtistBanner)},
		{Type: item.ImageTypeBackdrop, Path: strings.TrimSpace(result.ArtistFanart), Index: 0},
		{Type: item.ImageTypeBackdrop, Path: strings.TrimSpace(result.ArtistFanart2), Index: 1},
		{Type: item.ImageTypeBackdrop, Path: strings.TrimSpace(result.ArtistFanart3), Index: 2},
	}, replaceImages)
	return true, nil
}

func (provider *Provider) refreshAlbum(ctx context.Context, target *item.Item, language string, replaceImages bool) (bool, error) {
	id, ok := target.ProviderIDs.Get("MusicBrainzReleaseGroup")
	id = strings.TrimSpace(id)
	if !ok || id == "" {
		return false, nil
	}
	result, err := provider.api.getAlbum(ctx, id)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	if provider.config.ReplaceAlbumName {
		setString(&target.Album, result.Album)
	}
	if artistName := strings.TrimSpace(result.Artist); artistName != "" {
		target.Values.AlbumArtists = []string{artistName}
	}
	if yearText := strings.TrimSpace(result.YearReleased); yearText != "" {
		if year, err := strconv.Atoi(yearText); err == nil {
			target.ProductionYear = &year
		}
	}
	if genre := strings.TrimSpace(result.Genre); genre != "" {
		target.Values.Genres = []string{genre}
	}
	setString(&target.Overview, stripHTML(localizedAlbumDescription(*result, language)))
	setProviderID(&target.ProviderIDs, "AudioDbArtist", result.IDArtist)
	setProviderID(&target.ProviderIDs, "AudioDbAlbum", result.IDAlbum)
	setProviderID(&target.ProviderIDs, "MusicBrainzAlbumArtist", result.MusicBrainzAlbumArtist)
	setProviderID(&target.ProviderIDs, "MusicBrainzReleaseGroup", result.MusicBrainzGroup)
	mergeImages(target, []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: strings.TrimSpace(result.AlbumThumb)},
		{Type: item.ImageTypeDisc, Path: strings.TrimSpace(result.AlbumCDArt)},
	}, replaceImages)
	return true, nil
}

func metadataLanguage(refresh library.RefreshContext) string {
	return strings.TrimSpace(refresh.Options.PreferredMetadataLanguage)
}

func localizedArtistBiography(value artist, language string) string {
	switch {
	case strings.EqualFold(language, "de"):
		return firstNonBlank(value.BiographyDE, value.BiographyEN, value.Biography)
	case strings.EqualFold(language, "fr"):
		return firstNonBlank(value.BiographyFR, value.BiographyEN, value.Biography)
	case strings.EqualFold(language, "nl"):
		return firstNonBlank(value.BiographyNL, value.BiographyEN, value.Biography)
	case strings.EqualFold(language, "ru"):
		return firstNonBlank(value.BiographyRU, value.BiographyEN, value.Biography)
	case strings.EqualFold(language, "it"):
		return firstNonBlank(value.BiographyIT, value.BiographyEN, value.Biography)
	case strings.HasPrefix(strings.ToLower(language), "pt"):
		return firstNonBlank(value.BiographyPT, value.BiographyEN, value.Biography)
	default:
		return firstNonBlank(value.BiographyEN, value.Biography)
	}
}

func localizedAlbumDescription(value album, language string) string {
	switch {
	case strings.EqualFold(language, "de"):
		return firstNonBlank(value.DescriptionDE, value.DescriptionEN, value.Description)
	case strings.EqualFold(language, "fr"):
		return firstNonBlank(value.DescriptionFR, value.DescriptionEN, value.Description)
	case strings.EqualFold(language, "nl"):
		return firstNonBlank(value.DescriptionNL, value.DescriptionEN, value.Description)
	case strings.EqualFold(language, "ru"):
		return firstNonBlank(value.DescriptionRU, value.DescriptionEN, value.Description)
	case strings.EqualFold(language, "it"):
		return firstNonBlank(value.DescriptionIT, value.DescriptionEN, value.Description)
	case strings.HasPrefix(strings.ToLower(language), "pt"):
		return firstNonBlank(value.DescriptionPT, value.DescriptionEN, value.Description)
	default:
		return firstNonBlank(value.DescriptionEN, value.Description)
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stripHTML(value string) string {
	value = htmlTag.ReplaceAllString(value, "")
	return strings.TrimSpace(html.UnescapeString(value))
}

func setString(destination **string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*destination = &value
	}
}

func setProviderID(destination *item.ProviderIDs, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		destination.Set(name, value)
	}
}

func mergeImages(target *item.Item, candidates []item.ImageInfo, replace bool) {
	grouped := make(map[item.ImageType][]item.ImageInfo)
	order := make([]item.ImageType, 0)
	for _, candidate := range candidates {
		if candidate.Path == "" {
			continue
		}
		if _, exists := grouped[candidate.Type]; !exists {
			order = append(order, candidate.Type)
		}
		grouped[candidate.Type] = append(grouped[candidate.Type], candidate)
	}
	for _, imageType := range order {
		images := grouped[imageType]
		existing := target.ImageInfos[:0]
		found := false
		for _, current := range target.ImageInfos {
			if current.Type == imageType {
				found = true
				if replace {
					continue
				}
			}
			existing = append(existing, current)
		}
		if found && !replace {
			continue
		}
		target.ImageInfos = existing
		target.ImageInfos = append(target.ImageInfos, images...)
	}
}
