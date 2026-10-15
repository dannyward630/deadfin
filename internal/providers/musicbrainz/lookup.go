// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz

import (
	"context"
	"net/url"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// LookupProvider implements the remote-search capability for MusicBrainz
// MusicArtist and MusicAlbum. It wraps the same apiClient the refresh Provider
// uses, so it shares the cassette-backed transport and is inert without a
// transport.
type LookupProvider struct {
	api    *apiClient
	config Config
}

// NewLookupProvider builds the MusicBrainz lookup provider from the same
// options as the refresh provider. A nil transport yields an inert provider.
func NewLookupProvider(options Options) *LookupProvider {
	config := normalizeConfig(options.Config)
	if strings.TrimSpace(options.Config.Server) == "" {
		config = DefaultConfig()
		config.ReplaceArtistName = options.Config.ReplaceArtistName
	}
	var api *apiClient
	if options.HTTP != nil {
		api = newAPIClient(options.HTTP, config.Server)
	}
	return &LookupProvider{api: api, config: config}
}

func (*LookupProvider) Name() string { return ProviderName }

// SearchSupports reports the kinds MusicBrainz can search (MusicArtist, MusicAlbum).
func (provider *LookupProvider) SearchSupports(kind item.Kind) bool {
	if provider.api == nil {
		return false
	}
	return kind == item.KindMusicArtist || kind == item.KindMusicAlbum
}

// Search runs a MusicBrainz remote search for the given lookup. Artist search
// reuses searchArtists with Lucene escaping (D-18). Album search reuses
// searchReleases, building the query the same way albumQuery does.
func (provider *LookupProvider) Search(ctx context.Context, lookup providers.Lookup) ([]providers.RemoteSearchResult, error) {
	if provider.api == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch lookup.Kind {
	case item.KindMusicArtist:
		return provider.searchArtist(ctx, lookup)
	case item.KindMusicAlbum:
		return provider.searchAlbum(ctx, lookup)
	default:
		return nil, nil
	}
}

func (provider *LookupProvider) searchArtist(ctx context.Context, lookup providers.Lookup) ([]providers.RemoteSearchResult, error) {
	// ID-first: when a MusicBrainzArtist id is set in the provider IDs,
	// the artist provider IDs (ArtistProviderIds from the wire), or the
	// SongInfos' MusicBrainzAlbumArtist (ArtistInfoExtensions.
	// GetMusicBrainzArtistId), look the artist up directly by ID — no name
	// is needed (MusicBrainzArtistProvider.cs:39-44). This is the path the
	// identify flow takes when the user picked a specific remote result.
	if id := providerIDString(lookup.ProviderIDs, "MusicBrainzArtist"); id != "" {
		return provider.lookupArtistByID(ctx, id)
	}
	if id := providerIDString(lookup.ArtistIDs, "MusicBrainzArtist"); id != "" {
		return provider.lookupArtistByID(ctx, id)
	}
	if id := songInfoProviderID(lookup.SongInfos, "MusicBrainzAlbumArtist"); id != "" {
		return provider.lookupArtistByID(ctx, id)
	}
	name := strings.TrimSpace(lookup.Name)
	if name == "" {
		return nil, nil
	}
	results, err := provider.api.searchArtists(ctx, lucenePhrase(name))
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && hasDiacritics(name) {
		results, err = provider.api.searchArtists(ctx, "artistaccent:"+lucenePhrase(name))
		if err != nil {
			return nil, err
		}
	}
	var out []providers.RemoteSearchResult
	for _, artist := range results {
		if strings.TrimSpace(artist.ID) == "" {
			continue
		}
		out = append(out, artistToResult(&artist))
	}
	return out, nil
}

// lookupArtistByID fetches a single artist by MusicBrainz ID and returns it as
// a RemoteSearchResult, mirroring upstream's GetResultFromResponse
// (MusicBrainzArtistProvider.cs:65-76): Name, ProductionYear/PremiereDate from
// LifeSpan.Begin, and the MusicBrainzArtist provider ID.
func (provider *LookupProvider) lookupArtistByID(ctx context.Context, id string) ([]providers.RemoteSearchResult, error) {
	artist, err := provider.api.lookupArtist(ctx, id)
	if err != nil {
		return nil, err
	}
	if artist == nil || strings.TrimSpace(artist.ID) == "" {
		return nil, nil
	}
	return []providers.RemoteSearchResult{artistToResult(artist)}, nil
}

// artistToResult maps a MusicBrainz artist to a RemoteSearchResult, mirroring
// upstream's GetResultFromResponse (MusicBrainzArtistProvider.cs:65-76).
func artistToResult(artist *artistResult) providers.RemoteSearchResult {
	r := providers.RemoteSearchResult{
		ProviderIDs: map[string]string{"MusicBrainzArtist": artist.ID},
	}
	if name := strings.TrimSpace(artist.Name); name != "" {
		n := name
		r.Name = &n
	}
	if date, ok := parsePartialDate(artist.LifeSpan.Begin); ok {
		year := date.Year()
		r.ProductionYear = &year
		formatted := date.Format("2006-01-02T15:04:05.0000000Z")
		r.PremiereDate = &formatted
	}
	return r
}

func (provider *LookupProvider) searchAlbum(ctx context.Context, lookup providers.Lookup) ([]providers.RemoteSearchResult, error) {
	// ID-first: when a MusicBrainzAlbum id is already set, look the release
	// up directly — no name is needed (MusicBrainzAlbumProvider.cs:39-43).
	// Falls back to SongInfos' MusicBrainzAlbum (AlbumInfoExtensions.
	// GetReleaseId).
	if releaseID := providerIDString(lookup.ProviderIDs, "MusicBrainzAlbum"); releaseID != "" {
		release, err := provider.api.lookupRelease(ctx, releaseID)
		if err != nil {
			return nil, err
		}
		if release != nil {
			return []providers.RemoteSearchResult{releaseToResult(release)}, nil
		}
	}
	if releaseID := songInfoProviderID(lookup.SongInfos, "MusicBrainzAlbum"); releaseID != "" {
		release, err := provider.api.lookupRelease(ctx, releaseID)
		if err != nil {
			return nil, err
		}
		if release != nil {
			return []providers.RemoteSearchResult{releaseToResult(release)}, nil
		}
	}
	// Release-group ID: look up and return each release with full artist
	// credit. Falls back to SongInfos' MusicBrainzReleaseGroup
	// (AlbumInfoExtensions.GetReleaseGroupId), before generic name search
	// (MusicBrainzAlbumProvider.cs:44-50).
	if releaseGroupID := providerIDString(lookup.ProviderIDs, "MusicBrainzReleaseGroup"); releaseGroupID != "" {
		return provider.searchByReleaseGroup(ctx, releaseGroupID)
	}
	if releaseGroupID := songInfoProviderID(lookup.SongInfos, "MusicBrainzReleaseGroup"); releaseGroupID != "" {
		return provider.searchByReleaseGroup(ctx, releaseGroupID)
	}
	name := strings.TrimSpace(lookup.Name)
	if name == "" {
		return nil, nil
	}
	query := albumLookupQuery(name, lookup)
	if query == "" {
		return nil, nil
	}
	results, err := provider.api.searchReleases(ctx, query)
	if err != nil {
		return nil, err
	}
	var out []providers.RemoteSearchResult
	for _, release := range results {
		if strings.TrimSpace(release.ID) == "" {
			continue
		}
		out = append(out, releaseToResult(&release))
	}
	return out, nil
}

// searchByReleaseGroup looks up a release group by ID and returns each
// release with full artist credit. Upstream's GetReleaseGroupResultAsync
// fetches each release individually so the ArtistCredit is populated
// (MusicBrainzAlbumProvider.cs:94-108).
func (provider *LookupProvider) searchByReleaseGroup(ctx context.Context, releaseGroupID string) ([]providers.RemoteSearchResult, error) {
	group, err := provider.api.lookupReleaseGroup(ctx, releaseGroupID)
	if err != nil {
		return nil, err
	}
	if group == nil || len(group.Releases) == 0 {
		return nil, nil
	}
	out := make([]providers.RemoteSearchResult, 0, len(group.Releases))
	for _, release := range group.Releases {
		if strings.TrimSpace(release.ID) == "" {
			continue
		}
		full, err := provider.api.lookupRelease(ctx, release.ID)
		if err != nil {
			return nil, err
		}
		if full != nil {
			out = append(out, releaseToResult(full))
		}
	}
	return out, nil
}

func releaseToResult(release *releaseResult) providers.RemoteSearchResult {
	r := providers.RemoteSearchResult{
		ProviderIDs: map[string]string{"MusicBrainzAlbum": release.ID},
	}
	if title := strings.TrimSpace(release.Title); title != "" {
		r.Name = &title
	}
	if release.ReleaseGroup != nil {
		if gid := strings.TrimSpace(release.ReleaseGroup.ID); gid != "" {
			r.ProviderIDs["MusicBrainzReleaseGroup"] = gid
		}
	}
	if date, ok := parsePartialDate(release.Date); ok {
		year := date.Year()
		r.ProductionYear = &year
		formatted := date.Format("2006-01-02T15:04:05.0000000Z")
		r.PremiereDate = &formatted
	}
	overview := strings.TrimSpace(release.Annotation)
	if overview != "" {
		r.Overview = &overview
	}
	// Project the full artist credit into the Artists array, each carrying
	// the pinned MusicBrainzArtist provider ID. The first artist becomes
	// AlbumArtist. This matches upstream's GetReleaseResult
	// (MusicBrainzAlbumProvider.cs:120-143).
	if len(release.ArtistCredit) > 0 {
		artists := make([]providers.RemoteSearchResult, len(release.ArtistCredit))
		for i, credit := range release.ArtistCredit {
			artistName := strings.TrimSpace(credit.Name)
			ar := providers.RemoteSearchResult{Name: &artistName}
			if credit.Artist != nil && strings.TrimSpace(credit.Artist.ID) != "" {
				ar.ProviderIDs = map[string]string{"MusicBrainzArtist": credit.Artist.ID}
			}
			artists[i] = ar
		}
		r.Artists = artists
		albumArtist := artists[0]
		r.AlbumArtist = &albumArtist
	}
	return r
}

// albumLookupQuery builds the Lucene album search query, respecting D-18
// escaping. It mirrors albumQuery but works from the Lookup struct. Provider
// IDs on the album and artist provider IDs are checked first, then SongInfos
// fall back for MusicBrainzAlbumArtist and MusicBrainzArtist
// (AlbumInfoExtensions.GetMusicBrainzArtistId), then SongInfos' AlbumArtists
// (AlbumInfoExtensions.GetAlbumArtist), then AlbumInfo.AlbumArtists[0].
func albumLookupQuery(name string, lookup providers.Lookup) string {
	if artistID := providerIDString(lookup.ProviderIDs, "MusicBrainzAlbumArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	if artistID := providerIDString(lookup.ArtistIDs, "MusicBrainzAlbumArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	if artistID := providerIDString(lookup.ProviderIDs, "MusicBrainzArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	if artistID := providerIDString(lookup.ArtistIDs, "MusicBrainzArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	if artistID := songInfoProviderID(lookup.SongInfos, "MusicBrainzAlbumArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	if artistID := songInfoProviderID(lookup.SongInfos, "MusicBrainzArtist"); artistID != "" {
		return lucenePhrase(name) + " AND arid:" + artistID
	}
	// SongInfos' AlbumArtists take precedence over AlbumInfo.AlbumArtists
	// (AlbumInfoExtensions.GetAlbumArtist).
	if artist := songInfoAlbumArtist(lookup.SongInfos); artist != "" {
		return lucenePhrase(name) + " AND artist:" + lucenePhrase(artist)
	}
	if len(lookup.AlbumArtists) > 0 {
		artist := strings.TrimSpace(lookup.AlbumArtists[0])
		if artist != "" {
			return lucenePhrase(name) + " AND artist:" + lucenePhrase(artist)
		}
	}
	return lucenePhrase(name)
}

// songInfoProviderID returns the first non-empty provider ID with the given
// name from any SongInfo in the slice (AlbumInfoExtensions.GetReleaseId etc.).
func songInfoProviderID(songs []providers.SongInfo, name string) string {
	for _, song := range songs {
		if id := providerIDString(song.ProviderIDs, name); id != "" {
			return id
		}
	}
	return ""
}

// songInfoAlbumArtist returns the first non-empty album artist from any
// SongInfo in the slice (AlbumInfoExtensions.GetAlbumArtist).
func songInfoAlbumArtist(songs []providers.SongInfo) string {
	for _, song := range songs {
		for _, artist := range song.AlbumArtists {
			artist = strings.TrimSpace(artist)
			if artist != "" {
				return artist
			}
		}
	}
	return ""
}

func providerIDString(ids map[string]string, name string) string {
	if ids == nil {
		return ""
	}
	for key, value := range ids {
		if strings.EqualFold(key, name) {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
			break
		}
	}
	return ""
}

// Compile-time interface check.
var _ providers.Searcher = (*LookupProvider)(nil)

func (client *apiClient) lookupArtist(ctx context.Context, id string) (*artistResult, error) {
	var response artistResult
	err := client.getJSON(ctx, "artist/"+url.PathEscape(id), url.Values{
		"fmt": {"json"},
		"inc": {"aliases"},
	}, &response)
	if err != nil {
		return nil, err
	}
	return &response, nil
}
