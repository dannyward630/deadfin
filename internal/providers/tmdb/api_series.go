// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
)

// --- Series wire models (fields the ported series mapping consumes) ---

type externalIDs struct {
	ImdbID   string `json:"imdb_id"`
	TvdbID   int    `json:"tvdb_id"`
	TvrageID int    `json:"tvrage_id"`
}

type contentRating struct {
	Iso31661 string `json:"iso_3166_1"`
	Rating   string `json:"rating"`
}

type contentRatings struct {
	Results []contentRating `json:"results"`
}

type keywordResults struct {
	Results []namedValue `json:"results"`
}

type createdBy struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ProfilePath string `json:"profile_path"`
}

// tvShow is the /tv/{id} response with the appended sub-resources the ported
// provider reads (credits, external_ids, content_ratings, keywords, videos,
// images).
type tvShow struct {
	ID                  int             `json:"id"`
	Name                string          `json:"name"`
	OriginalName        string          `json:"original_name"`
	OriginalLanguage    string          `json:"original_language"`
	Overview            string          `json:"overview"`
	PosterPath          string          `json:"poster_path"`
	Homepage            string          `json:"homepage"`
	Status              string          `json:"status"`
	FirstAirDate        string          `json:"first_air_date"`
	LastAirDate         string          `json:"last_air_date"`
	VoteAverage         float64         `json:"vote_average"`
	EpisodeRunTime      []int           `json:"episode_run_time"`
	Genres              []namedValue    `json:"genres"`
	Networks            []namedValue    `json:"networks"`
	ProductionCompanies []namedValue    `json:"production_companies"`
	CreatedBy           []createdBy     `json:"created_by"`
	Credits             *seriesCredits  `json:"credits"`
	ExternalIDs         *externalIDs    `json:"external_ids"`
	ContentRatings      *contentRatings `json:"content_ratings"`
	Keywords            *keywordResults `json:"keywords"`
	Videos              *videos         `json:"videos"`
	Images              *images         `json:"images"`
}

// seriesCredits adds guest stars to the shared credits shape (episodes use it).
type seriesCredits struct {
	Cast []castMember `json:"cast"`
	Crew []crewMember `json:"crew"`
}

type searchTv struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	FirstAirDate string `json:"first_air_date"`
}

type searchTvResponse struct {
	Results []searchTv `json:"results"`
}

// getSeries fetches a series with the sub-resources the provider maps.
func (client *apiClient) getSeries(ctx context.Context, tmdbID int, language, imageLanguages string) (*tvShow, error) {
	query := url.Values{}
	if language != "" {
		query.Set("language", language)
	}
	if imageLanguages != "" {
		query.Set("include_image_language", imageLanguages)
	}
	query.Set("append_to_response", "credits,external_ids,content_ratings,keywords,videos,images")
	var result tvShow
	found, err := client.getJSON(ctx, "/tv/"+strconv.Itoa(tmdbID), query, &result)
	if err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb get failed", slog.String("op", "series"), slog.Int("tmdbId", tmdbID), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb get", slog.String("op", "series"), slog.Int("tmdbId", tmdbID), slog.Bool("found", found))
	if !found {
		return nil, nil
	}
	return &result, nil
}

// searchSeries searches for a series by name, optionally constrained by year.
func (client *apiClient) searchSeries(ctx context.Context, name string, year int, language string, includeAdult bool) ([]searchTv, error) {
	query := url.Values{}
	query.Set("query", name)
	if language != "" {
		query.Set("language", language)
	}
	if year > 0 {
		query.Set("first_air_date_year", strconv.Itoa(year))
	}
	query.Set("include_adult", strconv.FormatBool(includeAdult))
	var response searchTvResponse
	if _, err := client.getJSON(ctx, "/search/tv", query, &response); err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb search failed", slog.String("op", "search/tv"), slog.Int("year", year), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb search", slog.String("op", "search/tv"), slog.Int("year", year), slog.Int("results", len(response.Results)))
	return response.Results, nil
}

// findSeriesByExternalID resolves a series from an IMDb or TVDB id.
func (client *apiClient) findSeriesByExternalID(ctx context.Context, externalID, source, language string) ([]searchTv, error) {
	query := url.Values{}
	query.Set("external_source", source)
	if language != "" {
		query.Set("language", language)
	}
	var response struct {
		TvResults []searchTv `json:"tv_results"`
	}
	found, err := client.getJSON(ctx, "/find/"+externalID, query, &response)
	if err != nil || !found {
		return nil, err
	}
	return response.TvResults, nil
}

// --- Season and episode wire models (TmdbSeasonProvider, TmdbEpisodeProvider) ---

// episodeCredits adds guest stars to the shared credits shape.
type episodeCredits struct {
	Cast       []castMember `json:"cast"`
	Crew       []crewMember `json:"crew"`
	GuestStars []castMember `json:"guest_stars"`
}

type seasonImages struct {
	Posters []imageData `json:"posters"`
}

type episodeImages struct {
	Stills []imageData `json:"stills"`
}

// tvSeason is the /tv/{id}/season/{n} response with credits, images,
// external_ids, and videos appended (TvSeasonMethods in GetSeasonAsync).
type tvSeason struct {
	ID           int            `json:"id"`
	Name         string         `json:"name"`
	Overview     string         `json:"overview"`
	AirDate      string         `json:"air_date"`
	SeasonNumber int            `json:"season_number"`
	PosterPath   string         `json:"poster_path"`
	Credits      *seriesCredits `json:"credits"`
	ExternalIDs  *externalIDs   `json:"external_ids"`
	Images       *seasonImages  `json:"images"`
	Videos       *videos        `json:"videos"`
}

// tvEpisode is the /tv/{id}/season/{n}/episode/{m} response with credits,
// images, external_ids, and videos appended (TvEpisodeMethods in GetEpisodeAsync).
type tvEpisode struct {
	ID            int             `json:"id"`
	Name          string          `json:"name"`
	Overview      string          `json:"overview"`
	AirDate       string          `json:"air_date"`
	SeasonNumber  int             `json:"season_number"`
	EpisodeNumber int             `json:"episode_number"`
	VoteAverage   float64         `json:"vote_average"`
	StillPath     string          `json:"still_path"`
	Credits       *episodeCredits `json:"credits"`
	ExternalIDs   *externalIDs    `json:"external_ids"`
	Images        *episodeImages  `json:"images"`
	Videos        *videos         `json:"videos"`
}

// episodeGroupType is TMDb's episode group type (TvGroupType).
type episodeGroupType int

const (
	episodeGroupOriginalAirDate episodeGroupType = 1
	episodeGroupAbsolute        episodeGroupType = 2
	episodeGroupDVD             episodeGroupType = 3
	episodeGroupDigital         episodeGroupType = 4
	episodeGroupStoryArc        episodeGroupType = 5
	episodeGroupProduction      episodeGroupType = 6
	episodeGroupTV              episodeGroupType = 7
)

// displayOrderGroupType maps a series DisplayOrder onto an episode group type,
// as GetSeriesGroupAsync does. The default aired order has no group.
func displayOrderGroupType(displayOrder string) (episodeGroupType, bool) {
	switch displayOrder {
	case "originalAirDate":
		return episodeGroupOriginalAirDate, true
	case "absolute":
		return episodeGroupAbsolute, true
	case "dvd":
		return episodeGroupDVD, true
	case "digital":
		return episodeGroupDigital, true
	case "storyArc":
		return episodeGroupStoryArc, true
	case "production":
		return episodeGroupProduction, true
	case "tv":
		return episodeGroupTV, true
	default:
		return 0, false
	}
}

type episodeGroupRef struct {
	ID   string           `json:"id"`
	Type episodeGroupType `json:"type"`
}

type episodeGroupEpisode struct {
	Order         int `json:"order"`
	SeasonNumber  int `json:"season_number"`
	EpisodeNumber int `json:"episode_number"`
}

type episodeGroup struct {
	Order    int                   `json:"order"`
	Episodes []episodeGroupEpisode `json:"episodes"`
}

type episodeGroupCollection struct {
	Groups []episodeGroup `json:"groups"`
}

// seasonEpisodeQuery builds the shared season and episode query.
func seasonEpisodeQuery(language, imageLanguages string) url.Values {
	query := url.Values{}
	if language != "" {
		query.Set("language", language)
	}
	if imageLanguages != "" {
		query.Set("include_image_language", imageLanguages)
	}
	query.Set("append_to_response", "credits,images,external_ids,videos")
	return query
}

// getSeason fetches one season of a series. Not found returns (nil, nil).
func (client *apiClient) getSeason(ctx context.Context, seriesID, seasonNumber int, language, imageLanguages string) (*tvSeason, error) {
	var result tvSeason
	path := "/tv/" + strconv.Itoa(seriesID) + "/season/" + strconv.Itoa(seasonNumber)
	found, err := client.getJSON(ctx, path, seasonEpisodeQuery(language, imageLanguages), &result)
	if err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb get failed", slog.String("op", "season"), slog.Int("tmdbId", seriesID), slog.Int("season", seasonNumber), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb get", slog.String("op", "season"), slog.Int("tmdbId", seriesID), slog.Int("season", seasonNumber), slog.Bool("found", found))
	if !found {
		return nil, nil
	}
	return &result, nil
}

// getEpisode fetches one episode. Not found returns (nil, nil).
func (client *apiClient) getEpisode(ctx context.Context, seriesID, seasonNumber, episodeNumber int, language, imageLanguages string) (*tvEpisode, error) {
	var result tvEpisode
	path := "/tv/" + strconv.Itoa(seriesID) + "/season/" + strconv.Itoa(seasonNumber) + "/episode/" + strconv.Itoa(episodeNumber)
	found, err := client.getJSON(ctx, path, seasonEpisodeQuery(language, imageLanguages), &result)
	if err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb get failed", slog.String("op", "episode"), slog.Int("tmdbId", seriesID), slog.Int("season", seasonNumber), slog.Int("episode", episodeNumber), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb get", slog.String("op", "episode"), slog.Int("tmdbId", seriesID), slog.Int("season", seasonNumber), slog.Int("episode", episodeNumber), slog.Bool("found", found))
	if !found {
		return nil, nil
	}
	return &result, nil
}

// getEpisodeGroup resolves the series episode group of kind, as
// GetSeriesGroupAsync does. No group of that kind returns (nil, nil).
func (client *apiClient) getEpisodeGroup(ctx context.Context, seriesID int, kind episodeGroupType, language string) (*episodeGroupCollection, error) {
	query := url.Values{}
	if language != "" {
		query.Set("language", language)
	}
	var refs struct {
		Results []episodeGroupRef `json:"results"`
	}
	found, err := client.getJSON(ctx, "/tv/"+strconv.Itoa(seriesID)+"/episode_groups", query, &refs)
	if err != nil || !found {
		return nil, err
	}
	groupID := ""
	for _, ref := range refs.Results {
		if ref.Type == kind {
			groupID = ref.ID
			break
		}
	}
	if groupID == "" {
		return nil, nil
	}
	var group episodeGroupCollection
	found, err = client.getJSON(ctx, "/tv/episode_group/"+url.PathEscape(groupID), query, &group)
	if err != nil || !found {
		return nil, err
	}
	return &group, nil
}
