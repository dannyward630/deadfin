// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

const (
	// ProviderName matches the value stored in library options.xml.
	ProviderName = "MusicBrainz"
	// PluginName and PluginID retain Jellyfin's stable dashboard identity.
	PluginName = "MusicBrainz"
	PluginID   = "8c95c4d2-e50c-4fb0-a4f3-6c06ff0f9a1a"
)

// Options constructs a Provider.
type Options struct {
	HTTP   *remote.Client
	Config Config
}

// Provider resolves the MusicBrainz identities used by downstream music
// metadata and image providers.
type Provider struct {
	api    *apiClient
	config Config
}

// NewProvider builds the MusicBrainz refresh provider. A nil transport leaves
// it inert, which keeps tests and installations without network composition
// deterministic.
func NewProvider(options Options) *Provider {
	config := normalizeConfig(options.Config)
	if strings.TrimSpace(options.Config.Server) == "" {
		config = DefaultConfig()
		config.ReplaceArtistName = options.Config.ReplaceArtistName
	}
	var api *apiClient
	if options.HTTP != nil {
		api = newAPIClient(options.HTTP, config.Server)
	}
	return &Provider{api: api, config: config}
}

func (*Provider) Name() string { return ProviderName }

func (*Provider) Stage() providers.ProviderStage { return providers.ProviderStageRemoteMetadata }

// SupportsKind reports the kinds of MusicBrainzArtistProvider and
// MusicBrainzAlbumProvider.
func (*Provider) SupportsKind(kind item.Kind) bool {
	return kind == item.KindMusicArtist || kind == item.KindMusicAlbum
}

// Refresh ports MusicBrainzArtistProvider.GetMetadata and
// MusicBrainzAlbumProvider.GetMetadata. No match is a normal unhandled result;
// request, decoding, and cancellation failures abort the refresh.
func (provider *Provider) Refresh(ctx context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
	if target == nil {
		return false, errors.New("musicbrainz: item is required")
	}
	if provider.api == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	switch target.Kind {
	case item.KindMusicArtist:
		return provider.refreshArtist(ctx, target)
	case item.KindMusicAlbum:
		return provider.refreshAlbum(ctx, target)
	default:
		return false, nil
	}
}

func (provider *Provider) refreshArtist(ctx context.Context, target *item.Item) (bool, error) {
	if _, ok := providerID(target, "MusicBrainzArtist"); ok {
		return true, nil
	}
	name := itemName(target)
	if name == "" {
		return false, nil
	}
	results, err := provider.api.searchArtists(ctx, lucenePhrase(name))
	if err != nil {
		return false, err
	}
	if len(results) == 0 && hasDiacritics(name) {
		results, err = provider.api.searchArtists(ctx, `artistaccent:`+lucenePhrase(name))
		if err != nil {
			return false, err
		}
	}
	if len(results) == 0 || strings.TrimSpace(results[0].ID) == "" {
		return false, nil
	}
	if err := validMBID(results[0].ID); err != nil {
		return false, fmt.Errorf("musicbrainz: response artist id: %w", err)
	}
	target.ProviderIDs.Set("MusicBrainzArtist", results[0].ID)
	if provider.config.ReplaceArtistName {
		setString(&target.Name, results[0].Name)
	}
	return true, nil
}

func (provider *Provider) refreshAlbum(ctx context.Context, target *item.Item) (bool, error) {
	releaseID, _ := providerID(target, "MusicBrainzAlbum")
	groupID, _ := providerID(target, "MusicBrainzReleaseGroup")
	if validMBID(releaseID) != nil {
		releaseID = ""
	}
	if validMBID(groupID) != nil {
		groupID = ""
	}

	var matched *releaseResult
	switch {
	case releaseID == "" && groupID != "":
		group, err := provider.api.lookupReleaseGroup(ctx, groupID)
		if err != nil {
			return false, err
		}
		if len(group.Releases) > 0 {
			releaseID = strings.TrimSpace(group.Releases[0].ID)
		}
	case releaseID == "":
		query := albumQuery(target)
		if query == "" {
			return false, nil
		}
		results, err := provider.api.searchReleases(ctx, query)
		if err != nil {
			return false, err
		}
		if len(results) > 0 {
			matched = &results[0]
			releaseID = strings.TrimSpace(matched.ID)
			if matched.ReleaseGroup != nil {
				groupID = strings.TrimSpace(matched.ReleaseGroup.ID)
			}
		}
	case groupID == "":
		release, err := provider.api.lookupRelease(ctx, releaseID)
		if err != nil {
			return false, err
		}
		if release.ReleaseGroup != nil {
			groupID = strings.TrimSpace(release.ReleaseGroup.ID)
		}
	}

	if releaseID == "" && groupID == "" {
		return false, nil
	}
	if releaseID != "" {
		if err := validMBID(releaseID); err != nil {
			return false, fmt.Errorf("musicbrainz: response release id: %w", err)
		}
		target.ProviderIDs.Set("MusicBrainzAlbum", releaseID)
	}
	if groupID != "" {
		if err := validMBID(groupID); err != nil {
			return false, fmt.Errorf("musicbrainz: response release group id: %w", err)
		}
		target.ProviderIDs.Set("MusicBrainzReleaseGroup", groupID)
	}
	if matched != nil {
		if date, ok := parsePartialDate(matched.Date); ok {
			year := date.Year()
			target.ProductionYear = &year
		}
		setString(&target.Overview, matched.Annotation)
	}
	return true, nil
}

func albumQuery(target *item.Item) string {
	name := itemName(target)
	if name == "" {
		return ""
	}
	if artistID, ok := validProviderID(target, "MusicBrainzAlbumArtist"); ok {
		return lucenePhrase(name) + ` AND arid:` + artistID
	}
	if artistID, ok := validProviderID(target, "MusicBrainzArtist"); ok {
		return lucenePhrase(name) + ` AND arid:` + artistID
	}
	artist := ""
	if len(target.Values.AlbumArtists) > 0 {
		artist = strings.TrimSpace(target.Values.AlbumArtists[0])
	}
	// AlbumInfo.GetAlbumArtist falls back through SongInfos in stock. The
	// scanner seam deliberately does not expose child items before refreshing
	// their parent, so the conventional artist/album directory hierarchy is
	// the equivalent already-discovered source fact here.
	if artist == "" && target.Path != nil {
		artist = filepath.Base(filepath.Dir(*target.Path))
	}
	if artist == "" {
		return ""
	}
	return lucenePhrase(name) + ` AND artist:` + lucenePhrase(artist)
}

func validProviderID(target *item.Item, name string) (string, bool) {
	value, ok := providerID(target, name)
	return value, ok && validMBID(value) == nil
}

func lucenePhrase(value string) string {
	runes := []rune(value)
	var escaped strings.Builder
	escaped.Grow(len(value) + 2)
	escaped.WriteByte('"')
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		if (char == '&' || char == '|') && index+1 < len(runes) && runes[index+1] == char {
			escaped.WriteByte('\\')
			escaped.WriteRune(char)
			escaped.WriteRune(char)
			index++
			continue
		}
		if strings.ContainsRune(`+-!(){}[]^"~*?:\/`, char) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(char)
	}
	escaped.WriteByte('"')
	return escaped.String()
}

func providerID(target *item.Item, name string) (string, bool) {
	if target == nil {
		return "", false
	}
	value, ok := target.ProviderIDs.Get(name)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func itemName(target *item.Item) string {
	if target == nil || target.Name == nil {
		return ""
	}
	return strings.TrimSpace(*target.Name)
}

func setString(destination **string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*destination = &value
	}
}

func validMBID(value string) error {
	_, err := dto.ParseUUID(strings.TrimSpace(value))
	return err
}

func hasDiacritics(value string) bool {
	for _, char := range value {
		if char > unicode.MaxASCII {
			return true
		}
	}
	return false
}

func parsePartialDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
