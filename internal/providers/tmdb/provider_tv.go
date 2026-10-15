// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// parentSeries returns the TMDb id and display order of the nearest Series
// ancestor. A missing or invalid id returns 0.
func parentSeries(refresh library.RefreshContext) (int, string) {
	series, ok := refresh.NearestAncestor(item.KindSeries)
	if !ok {
		return 0, ""
	}
	value, ok := series.ProviderIDs.Get("Tmdb")
	if !ok {
		return 0, series.DisplayOrder
	}
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || id <= 0 {
		return 0, series.DisplayOrder
	}
	return id, series.DisplayOrder
}

// refreshSeason ports TmdbSeasonProvider.GetMetadata and the season poster of
// TmdbSeasonImageProvider. It needs the parent series TMDb id and the season
// number; without them it makes no request.
func (provider *Provider) refreshSeason(ctx context.Context, refresh library.RefreshContext, target *item.Item, language, country string, replaceImages bool) (bool, error) {
	seriesID, _ := parentSeries(refresh)
	if seriesID == 0 || target.IndexNumber == nil {
		return false, nil
	}
	result, err := provider.api.getSeason(ctx, seriesID, *target.IndexNumber, language, imageLanguagesParam(language, country))
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	provider.applySeason(ctx, target, result, replaceImages)
	return true, nil
}

// applySeason maps a TMDb season onto target.
func (provider *Provider) applySeason(ctx context.Context, target *item.Item, result *tvSeason, replaceImages bool) {
	setString(&target.Overview, result.Overview)
	if premiere, ok := parseTMDbDate(result.AirDate); ok {
		target.PremiereDate = &premiere
		year := premiere.Year()
		target.ProductionYear = &year
	}
	if provider.config.ImportSeasonName {
		setString(&target.Name, result.Name)
	}
	if result.ExternalIDs != nil && result.ExternalIDs.TvdbID > 0 {
		target.ProviderIDs.Set("Tvdb", strconv.Itoa(result.ExternalIDs.TvdbID))
	}
	if people := mapCastCrew(provider.config, provider.profileURL(ctx), seriesCast(result.Credits), seriesCrew(result.Credits)); len(people) > 0 {
		target.People = people
	}

	primary := imageData{FilePath: result.PosterPath}
	if result.Images != nil && len(result.Images.Posters) > 0 {
		primary = result.Images.Posters[0]
	}
	provider.putPrimary(ctx, target, primary, replaceImages)
}

// refreshEpisode ports TmdbEpisodeProvider.GetMetadata and the episode still of
// TmdbEpisodeImageProvider. A missing episode, parent series id, or episode
// number makes no request. A ranged episode (IndexNumberEnd) requests every
// number in the range and joins the names and overviews.
func (provider *Provider) refreshEpisode(ctx context.Context, refresh library.RefreshContext, target *item.Item, language, country string, replaceImages bool) (bool, error) {
	// Upstream skips missing episodes: "Allowing this will dramatically increase scan times".
	if target.IsVirtualItem {
		return false, nil
	}
	seriesID, displayOrder := parentSeries(refresh)
	if seriesID == 0 || target.IndexNumber == nil {
		return false, nil
	}
	seasonNumber := 1
	if target.ParentIndexNumber != nil {
		seasonNumber = *target.ParentIndexNumber
	}
	first := *target.IndexNumber
	last := first
	var indexNumberEnd *int
	if target.Extra.Episode != nil && target.Extra.Episode.IndexNumberEnd != nil {
		end := *target.Extra.Episode.IndexNumberEnd
		indexNumberEnd = &end
		last = end
	}

	imageLanguages := imageLanguagesParam(language, country)
	var results []*tvEpisode
	for number := first; number <= last; number++ {
		result, err := provider.getOrderedEpisode(ctx, seriesID, seasonNumber, number, displayOrder, language, imageLanguages)
		if err != nil {
			return false, err
		}
		if result != nil {
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return false, nil
	}

	// Copy the first result so a cached response is never changed.
	merged := *results[0]
	if len(results) > 1 {
		name := []string{merged.Name}
		overview := []string{merged.Overview}
		for _, result := range results[1:] {
			name = append(name, result.Name)
			overview = append(overview, result.Overview)
		}
		merged.Name = strings.Join(name, " / ")
		merged.Overview = strings.Join(overview, " / ")
	}
	provider.applyEpisode(ctx, target, &merged, seasonNumber, indexNumberEnd, replaceImages)
	return true, nil
}

// getOrderedEpisode maps a display-order position onto TMDb's aired numbers
// through the series episode group, then fetches the episode (GetEpisodeAsync).
func (provider *Provider) getOrderedEpisode(ctx context.Context, seriesID, seasonNumber, episodeNumber int, displayOrder, language, imageLanguages string) (*tvEpisode, error) {
	if kind, ok := displayOrderGroupType(displayOrder); ok {
		group, err := provider.api.getEpisodeGroup(ctx, seriesID, kind, language)
		if err != nil {
			return nil, err
		}
		if season, episode, ok := group.find(seasonNumber, episodeNumber); ok {
			seasonNumber, episodeNumber = season, episode
		}
	}
	return provider.api.getEpisode(ctx, seriesID, seasonNumber, episodeNumber, language, imageLanguages)
}

// find returns the aired numbers of the group episode at a display position.
// Group episode order starts at 0.
func (group *episodeGroupCollection) find(seasonNumber, episodeNumber int) (int, int, bool) {
	if group == nil {
		return 0, 0, false
	}
	for _, season := range group.Groups {
		if season.Order != seasonNumber {
			continue
		}
		for _, episode := range season.Episodes {
			if episode.Order == episodeNumber-1 {
				return episode.SeasonNumber, episode.EpisodeNumber, true
			}
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// applyEpisode maps a TMDb episode onto target.
func (provider *Provider) applyEpisode(ctx context.Context, target *item.Item, result *tvEpisode, seasonNumber int, indexNumberEnd *int, replaceImages bool) {
	if target.ParentIndexNumber == nil {
		season := seasonNumber
		target.ParentIndexNumber = &season
	}
	if indexNumberEnd != nil {
		if target.Extra.Episode == nil {
			target.Extra.Episode = &item.EpisodeExtra{}
		}
		end := *indexNumberEnd
		target.Extra.Episode.IndexNumberEnd = &end
	}
	setString(&target.Name, result.Name)
	setString(&target.Overview, result.Overview)
	if premiere, ok := parseTMDbDate(result.AirDate); ok {
		target.PremiereDate = &premiere
		year := premiere.Year()
		target.ProductionYear = &year
	}
	rating := float32(result.VoteAverage)
	target.CommunityRating = &rating

	if ids := result.ExternalIDs; ids != nil {
		if ids.TvdbID > 0 {
			target.ProviderIDs.Set("Tvdb", strconv.Itoa(ids.TvdbID))
		}
		if imdb := strings.TrimSpace(ids.ImdbID); imdb != "" {
			target.ProviderIDs.Set("Imdb", imdb)
		}
		if ids.TvrageID > 0 {
			target.ProviderIDs.Set("TvRage", strconv.Itoa(ids.TvrageID))
		}
	}
	if trailers := trailers(result.Videos); len(trailers) > 0 {
		target.Extra.RemoteTrailers = trailers
	}

	if result.Credits != nil {
		profile := provider.profileURL(ctx)
		people := mapCast(provider.config, profile, result.Credits.Cast, item.PersonKindActor)
		people = append(people, mapCast(provider.config, profile, result.Credits.GuestStars, item.PersonKindGuestStar)...)
		people = append(people, mapCrew(provider.config, profile, result.Credits.Crew)...)
		if len(people) > 0 {
			target.People = people
		}
	}

	primary := imageData{FilePath: result.StillPath}
	if result.Images != nil && len(result.Images.Stills) > 0 {
		primary = result.Images.Stills[0]
	}
	provider.putPrimary(ctx, target, primary, replaceImages)
}

// putPrimary records data as the Primary image with its dimensions.
func (provider *Provider) putPrimary(ctx context.Context, target *item.Item, data imageData, replace bool) {
	if data.FilePath == "" {
		return
	}
	base := provider.api.imageBaseURL(ctx)
	putRemoteImage(target, item.ImageTypePrimary, imageURL(base, provider.config.imageSizeOrOriginal(), data.FilePath), data.Width, data.Height, replace)
}
