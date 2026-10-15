// SPDX-License-Identifier: GPL-2.0-only

// Package anilist identifies anime series through Deadfin's offline anime
// crosswalk and fetches series-level metadata from AniList.
//
// Identification needs no API request: the crosswalk maps the series' TVDB
// id (from Sonarr, an NFO, or another provider) through Anime-Lists to
// AniDB, and manami maps AniDB to AniList and MyAnimeList. A title match
// against the offline title index is the fallback. The provider then asks
// AniList's GraphQL API for titles, synopsis, format, season, genres,
// studios, artwork, and the relation graph. It never fetches episodes.
//
// The provider acts only in a library that lists "AniList" among the series
// metadata fetchers, so a library that does not hold anime is never changed.
package anilist

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/anime"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/store"
)

const (
	// ProviderName is the refresh-pipeline identity a library lists in its
	// series MetadataFetchers to enable the anime stack.
	ProviderName = "AniList"

	// Provider id keys written to items.
	KeyAniList = "AniList"
	KeyAniDB   = "AniDB"
	KeyMAL     = "MyAnimeList"

	// Endpoint is the AniList GraphQL API.
	Endpoint = "https://graphql.anilist.co"
)

// Crosswalks supplies the current offline crosswalk; nil means no seed data
// is available yet.
type Crosswalks interface {
	Crosswalk(context.Context) (*anime.Crosswalk, error)
}

// MetadataStore persists the anime facts that live outside the item:
// titles for search, the relation graph, and the TVDB season map.
type MetadataStore interface {
	SaveMedia(context.Context, store.AnimeMedia) error
	ReplaceSeasonMap(context.Context, string, int, map[int][]store.AnimeSegment) error
}

// Options constructs a Provider.
type Options struct {
	// HTTP is the paced transport for AniList; nil disables API calls, so
	// the provider only writes crosswalk ids.
	HTTP       *remote.Client
	Crosswalks Crosswalks
	Store      MetadataStore
	Clock      clock.Clock
	// Endpoint overrides the GraphQL endpoint; empty selects Endpoint.
	Endpoint string
	// CacheTTL is how long a fetched series is reused; zero selects 24 hours.
	CacheTTL time.Duration
	Logger   *slog.Logger
}

// Provider is the AniList remote-metadata provider.
type Provider struct {
	api        *apiClient
	crosswalks Crosswalks
	store      MetadataStore
	clock      clock.Clock
	logger     *slog.Logger
}

// NewProvider constructs the provider.
func NewProvider(options Options) *Provider {
	clk := options.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	ttl := options.CacheTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var api *apiClient
	if options.HTTP != nil {
		api = newAPIClient(options.HTTP, clk, endpoint, ttl, logger)
	}
	return &Provider{api: api, crosswalks: options.Crosswalks, store: options.Store, clock: clk, logger: logger}
}

// Name is the pipeline identity matched against MetadataFetchers.
func (*Provider) Name() string { return ProviderName }

// Stage places the provider in the remote-metadata phase.
func (*Provider) Stage() providers.ProviderStage { return providers.ProviderStageRemoteMetadata }

// SupportsKind reports the kinds that Refresh enriches.
func (*Provider) SupportsKind(kind item.Kind) bool {
	return kind == item.KindSeries || kind == item.KindSeason
}

// SupportedImages reports the series cover and banner that Refresh applies.
func (*Provider) SupportedImages(kind item.Kind) []item.ImageType {
	if kind == item.KindSeries {
		return []item.ImageType{item.ImageTypePrimary, item.ImageTypeBanner}
	}
	return nil
}

// Enabled reports whether a library lists the provider explicitly among the
// metadata fetchers of kind. The registry runs every provider for a kind
// without type options; the anime stack requires the explicit opt-in.
func Enabled(options library.LibraryOptions, kind item.Kind) bool {
	for _, typeOptions := range options.TypeOptions {
		if !strings.EqualFold(typeOptions.Type, kind.String()) {
			continue
		}
		for _, name := range append(slices.Clone(typeOptions.MetadataFetchers), typeOptions.MetadataFetcherOrder...) {
			if strings.EqualFold(name, ProviderName) {
				return true
			}
		}
	}
	return false
}

// Refresh identifies and enriches a series or season. It returns
// handled=false without an error when the library does not enable the
// provider, the item is not a series or season, no seed data exists, or the
// item is not identified as anime.
func (p *Provider) Refresh(ctx context.Context, refresh library.RefreshContext, target *item.Item) (bool, error) {
	if target == nil {
		return false, errors.New("anilist: item is required")
	}
	if target.Kind != item.KindSeries && target.Kind != item.KindSeason {
		return false, nil
	}
	if !Enabled(refresh.Options, item.KindSeries) {
		p.skipped(ctx, target, "library does not list AniList in its Series metadata fetchers")
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var crosswalk *anime.Crosswalk
	if p.crosswalks != nil {
		var err error
		if crosswalk, err = p.crosswalks.Crosswalk(ctx); err != nil {
			p.skipped(ctx, target, "anime seed data failed to load: "+err.Error())
			return false, err
		}
	}
	if crosswalk == nil && target.Kind == item.KindSeason {
		p.skipped(ctx, target, "no anime seed data")
		return false, nil
	}
	if target.Kind == item.KindSeason {
		return p.refreshSeason(ctx, refresh, target, crosswalk)
	}
	return p.refreshSeries(ctx, refresh, target, crosswalk)
}

func (p *Provider) refreshSeries(ctx context.Context, refresh library.RefreshContext, target *item.Item, crosswalk *anime.Crosswalk) (bool, error) {
	query := queryFor(target.ProviderIDs)
	query.AllowTitle = true
	// The folder name comes first: in an anime library it is the operator's
	// title, while Name may come from a wrong match of another provider.
	if folder := folderName(target, refresh.LibraryRoot); folder != "" {
		query.Names = append(query.Names, folder)
	}
	if target.Name != nil {
		query.Names = append(query.Names, *target.Name)
	}
	if target.OriginalTitle != nil {
		query.Names = append(query.Names, *target.OriginalTitle)
	}
	if target.ProductionYear != nil {
		query.Year = *target.ProductionYear
	}
	identity, identified := crosswalk.Identify(query)
	if !identified {
		if query.AniList == 0 {
			reason := "no id or title matches the anime crosswalk"
			if crosswalk == nil {
				reason = "no anime seed data"
			}
			p.skipped(ctx, target, reason)
			return false, nil
		}
		// An AniList id without seed data is still usable.
		identity = anime.Identity{Anime: anime.Anime{AniList: query.AniList}, Method: anime.MethodAniList, TVDB: query.TVDB}
	}
	setID(target, KeyAniList, identity.Anime.AniList)
	setID(target, KeyAniDB, identity.Anime.AniDB)
	setID(target, KeyMAL, identity.Anime.MAL)
	if _, has := target.ProviderIDs.Get("Tvdb"); (!has || identity.WrongTVDB > 0) && identity.TVDB > 0 {
		target.ProviderIDs.Set("Tvdb", strconv.Itoa(identity.TVDB))
	}
	// A TMDb id that maps to no anime is replaced by the Anime-Lists id, so
	// the TMDb provider fetches the right series.
	if identity.WrongTMDb > 0 && identity.TMDb > 0 {
		target.ProviderIDs.Set("Tmdb", strconv.Itoa(identity.TMDb))
	}
	attrs := []slog.Attr{
		slog.String("method", string(identity.Method)),
		slog.Int("anilist", identity.Anime.AniList), slog.Int("anidb", identity.Anime.AniDB),
	}
	if identity.WrongTVDB > 0 {
		attrs = append(attrs, slog.Int("replacedTvdb", identity.WrongTVDB), slog.Int("tvdb", identity.TVDB))
	}
	if identity.WrongTMDb > 0 {
		attrs = append(attrs, slog.Int("replacedTmdb", identity.WrongTMDb), slog.Int("tmdb", identity.TMDb))
	}

	if err := p.saveSeasonMaps(ctx, crosswalk, identity); err != nil {
		return true, err
	}
	if identity.Anime.AniList == 0 || p.api == nil {
		p.identified(ctx, target, append(attrs, slog.String("metadata", "none: no AniList id or API client"))...)
		return true, nil
	}
	result, err := p.api.media(ctx, identity.Anime.AniList)
	if err != nil {
		p.identified(ctx, target, append(attrs, slog.String("metadata", "AniList request failed: "+err.Error()))...)
		return true, err
	}
	if result == nil {
		p.identified(ctx, target, append(attrs, slog.String("metadata", "none: AniList has no media for the id"))...)
		return true, nil
	}
	p.identified(ctx, target, append(attrs, slog.String("metadata", "fetched"))...)
	replaceImages := refresh.ReplaceAllImages && refresh.Image == library.RefreshModeFullRefresh
	applySeries(target, result, replaceImages)
	if p.store != nil {
		if err := p.store.SaveMedia(ctx, p.mediaRecord(result, identity, crosswalk)); err != nil {
			return true, err
		}
	}
	return true, nil
}

// refreshSeason writes the AniDB, AniList, and MyAnimeList ids of the
// season's first segment. The full split-cour list is in the season map the
// series refresh stores.
func (p *Provider) refreshSeason(ctx context.Context, refresh library.RefreshContext, target *item.Item, crosswalk *anime.Crosswalk) (bool, error) {
	if target.IndexNumber == nil {
		p.skipped(ctx, target, "season has no number")
		return false, nil
	}
	var series *library.RefreshAncestor
	for index := range refresh.Ancestors {
		if refresh.Ancestors[index].Kind == item.KindSeries {
			series = &refresh.Ancestors[index]
			break
		}
	}
	if series == nil {
		p.skipped(ctx, target, "season has no series ancestor")
		return false, nil
	}
	identity, identified := crosswalk.Identify(queryFor(series.ProviderIDs))
	if !identified || identity.TVDB == 0 {
		p.skipped(ctx, target, "series is not identified with a TVDB id")
		return false, nil
	}
	segments := crosswalk.SeasonSegments(identity.TVDB, *target.IndexNumber)
	if len(segments) == 0 {
		p.skipped(ctx, target, "Anime-Lists does not map the TVDB season")
		return false, nil
	}
	setID(target, KeyAniDB, segments[0].AniDB)
	setID(target, KeyAniList, segments[0].AniList)
	setID(target, KeyMAL, segments[0].MAL)
	p.identified(ctx, target, slog.String("method", string(identity.Method)), slog.Int("tvdb", identity.TVDB),
		slog.Int("anilist", segments[0].AniList), slog.Int("anidb", segments[0].AniDB), slog.Int("segments", len(segments)))
	return true, nil
}

// skipped records one provider run that changed nothing.
func (p *Provider) skipped(ctx context.Context, target *item.Item, reason string) {
	p.logger.LogAttrs(ctx, slog.LevelInfo, "anilist refresh", append(itemAttrs(target),
		slog.String("outcome", "skipped"), slog.String("reason", reason))...)
}

// identified records one provider run that identified the item.
func (p *Provider) identified(ctx context.Context, target *item.Item, attrs ...slog.Attr) {
	p.logger.LogAttrs(ctx, slog.LevelInfo, "anilist refresh", append(append(itemAttrs(target),
		slog.String("outcome", "identified")), attrs...)...)
}

func itemAttrs(target *item.Item) []slog.Attr {
	name := ""
	if target.Name != nil {
		name = *target.Name
	}
	return []slog.Attr{slog.String("item", target.ID.String()), slog.String("kind", target.Kind.String()), slog.String("name", name)}
}

// saveSeasonMaps stores the season segments of the series in TVDB and in
// TMDb numbering, so the episode provider and franchise collections can
// read the split-cour mapping in either numbering without the crosswalk.
func (p *Provider) saveSeasonMaps(ctx context.Context, crosswalk *anime.Crosswalk, identity anime.Identity) error {
	if p.store == nil || crosswalk == nil {
		return nil
	}
	numberings := []struct {
		name     string
		id       int
		entries  func(int) []anime.ListEntry
		season   func(anime.ListEntry) (int, bool)
		segments func(int, int) []anime.Segment
	}{
		{store.AnimeNumberingTVDB, identity.TVDB, crosswalk.TVDBEntries,
			func(entry anime.ListEntry) (int, bool) { return entry.TVDBSeason, entry.HasTVDBSeason }, crosswalk.SeasonSegments},
		{store.AnimeNumberingTMDb, identity.TMDb, crosswalk.TMDbEntries,
			func(entry anime.ListEntry) (int, bool) { return entry.TMDbSeason, entry.HasTMDbSeason }, crosswalk.TMDbSeasonSegments},
	}
	for _, numbering := range numberings {
		if numbering.id <= 0 {
			continue
		}
		seasons := make(map[int][]store.AnimeSegment)
		for _, entry := range numbering.entries(numbering.id) {
			season, ok := numbering.season(entry)
			if !ok || season < 0 {
				continue
			}
			if _, done := seasons[season]; done {
				continue
			}
			for _, segment := range numbering.segments(numbering.id, season) {
				seasons[season] = append(seasons[season], store.AnimeSegment{
					AniDBID: segment.AniDB, AniListID: segment.AniList, MALID: segment.MAL,
					EpisodeOffset: segment.EpisodeOffset, Name: segment.Name,
				})
			}
		}
		if len(seasons) == 0 {
			continue
		}
		if err := p.store.ReplaceSeasonMap(ctx, numbering.name, numbering.id, seasons); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) mediaRecord(result *media, identity anime.Identity, crosswalk *anime.Crosswalk) store.AnimeMedia {
	record := store.AnimeMedia{
		AniListID: result.ID, AniDBID: identity.Anime.AniDB, MALID: result.IDMal,
		Format: result.Format, Status: result.Status, Season: result.Season, SeasonYear: result.SeasonYear,
		Episodes: result.Episodes, Title: seriesName(result.Title), DateFetched: p.clock.Now(),
	}
	for _, edge := range result.Relations.Edges {
		record.Relations = append(record.Relations, store.AnimeRelation{
			RelationType: edge.RelationType, TargetAniListID: edge.Node.ID, TargetType: edge.Node.Type,
			TargetFormat: edge.Node.Format, TargetTitle: seriesName(edge.Node.Title),
		})
	}
	titles := []store.AnimeTitle{
		{Title: strings.TrimSpace(result.Title.English), Language: "en", Kind: "english"},
		{Title: strings.TrimSpace(result.Title.Romaji), Language: "x-jat", Kind: "romaji"},
		{Title: strings.TrimSpace(result.Title.Native), Language: "ja", Kind: "native"},
	}
	for _, synonym := range result.Synonyms {
		titles = append(titles, store.AnimeTitle{Title: strings.TrimSpace(synonym), Kind: "synonym"})
	}
	for _, title := range crosswalk.AniDBTitles(identity.Anime.AniDB) {
		titles = append(titles, store.AnimeTitle{Title: title.Value, Language: title.Language, Kind: title.Type})
	}
	record.TitleSets = []store.AnimeTitleSet{{Provider: KeyAniList, ExternalID: strconv.Itoa(result.ID), Titles: titles}}
	return record
}

// applySeries maps AniList media onto a series. Name is the English title,
// else romaji; OriginalTitle is the native title; SortName follows Name.
func applySeries(target *item.Item, result *media, replaceImages bool) {
	if name := seriesName(result.Title); name != "" && (target.Name == nil || *target.Name != name) {
		target.Name = &name
		target.SortName = nil
		if sortName, ok := item.ComputeSortName(*target, item.DefaultSortOptions()); ok {
			target.SortName = &sortName
		}
	}
	setString(&target.OriginalTitle, result.Title.Native)
	setString(&target.Overview, cleanDescription(result.Description))
	if result.IDMal > 0 {
		target.ProviderIDs.Set(KeyMAL, strconv.Itoa(result.IDMal))
	}
	target.ProviderIDs.Set(KeyAniList, strconv.Itoa(result.ID))
	if genres := trimmed(result.Genres); len(genres) != 0 {
		target.Values.Genres = genres
	}
	studios := make([]string, 0, len(result.Studios.Nodes))
	for _, studio := range result.Studios.Nodes {
		studios = append(studios, studio.Name)
	}
	if studios = trimmed(studios); len(studios) != 0 {
		target.Values.Studios = studios
	}
	if result.AverageScore > 0 {
		rating := float32(result.AverageScore) / 10
		target.CommunityRating = &rating
	}
	if start, ok := date(result.StartDate); ok {
		target.PremiereDate = &start
		year := start.Year()
		target.ProductionYear = &year
	} else if result.SeasonYear > 0 {
		year := result.SeasonYear
		target.ProductionYear = &year
	}
	if end, ok := date(result.EndDate); ok {
		target.EndDate = &end
	}
	if status, ok := seriesStatus(result.Status); ok {
		target.SeriesStatus = &status
	}
	cover := result.CoverImage.ExtraLarge
	if cover == "" {
		cover = result.CoverImage.Large
	}
	putRemoteImage(target, item.ImageTypePrimary, cover, replaceImages)
	putRemoteImage(target, item.ImageTypeBanner, result.BannerImage, replaceImages)
}

func seriesName(title mediaTitle) string {
	if english := strings.TrimSpace(title.English); english != "" {
		return english
	}
	return strings.TrimSpace(title.Romaji)
}

func seriesStatus(status string) (item.SeriesStatus, bool) {
	switch status {
	case "FINISHED", "CANCELLED":
		return item.SeriesStatusEnded, true
	case "RELEASING", "HIATUS":
		return item.SeriesStatusContinuing, true
	case "NOT_YET_RELEASED":
		return item.SeriesStatusUnreleased, true
	default:
		return 0, false
	}
}

func date(value fuzzyDate) (time.Time, bool) {
	if value.Year <= 0 {
		return time.Time{}, false
	}
	month, day := max(value.Month, 1), max(value.Day, 1)
	return time.Date(value.Year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

var (
	lineBreakTag = regexp.MustCompile(`(?i)<br\s*/?>`)
	markupTag    = regexp.MustCompile(`<[^>]*>`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
)

// cleanDescription turns AniList's description into plain text: line-break
// tags become newlines, other tags are removed, and entities are decoded.
func cleanDescription(value string) string {
	value = lineBreakTag.ReplaceAllString(value, "\n")
	value = markupTag.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = blankLines.ReplaceAllString(value, "\n\n")
	return strings.TrimSpace(value)
}

func queryFor(ids item.ProviderIDs) anime.Query {
	return anime.Query{
		AniList: providerInt(ids, KeyAniList),
		AniDB:   providerInt(ids, KeyAniDB),
		MAL:     providerInt(ids, KeyMAL),
		TVDB:    providerInt(ids, "Tvdb"),
		TMDb:    providerInt(ids, "Tmdb"),
	}
}

func providerInt(ids item.ProviderIDs, key string) int {
	value, ok := ids.Get(key)
	if !ok {
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func setID(target *item.Item, key string, value int) {
	if value > 0 {
		target.ProviderIDs.Set(key, strconv.Itoa(value))
	}
}

func setString(field **string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*field = &value
}

func trimmed(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// folderName returns the series folder name below the library root.
func folderName(target *item.Item, libraryRoot string) string {
	if target.Path == nil {
		return ""
	}
	path := strings.TrimRight(*target.Path, "/")
	if libraryRoot != "" && strings.TrimRight(libraryRoot, "/") == path {
		return ""
	}
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

// putRemoteImage adds or, when replace is set, replaces the image of a type.
// It mirrors the TMDb provider: existing artwork is kept unless the refresh
// replaces all images.
func putRemoteImage(target *item.Item, kind item.ImageType, url string, replace bool) {
	if url == "" {
		return
	}
	for index := range target.ImageInfos {
		if target.ImageInfos[index].Type != kind {
			continue
		}
		if replace {
			target.ImageInfos[index].Path = url
			target.ImageInfos[index].Width = 0
			target.ImageInfos[index].Height = 0
		}
		return
	}
	target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: kind, Path: url})
}
