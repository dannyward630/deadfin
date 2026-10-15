// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

// apiClient issues TMDb REST v3 requests over the shared caching transport. It
// holds no mutable state beyond the memoized image base URL, which it fetches
// once from /configuration.
type apiClient struct {
	http   *remote.Client
	apiKey string
	logger *slog.Logger
}

func newAPIClient(http *remote.Client, apiKey string, logger *slog.Logger) *apiClient {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &apiClient{http: http, apiKey: apiKey, logger: logger}
}

// request builds a TMDb URL for path with the api key and caller query merged.
func (client *apiClient) url(path string, query url.Values) string {
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_key", client.apiKey)
	return apiBaseURL + path + "?" + query.Encode()
}

// getJSON fetches a TMDb endpoint into destination, mapping a 404 to (found=false).
func (client *apiClient) getJSON(ctx context.Context, path string, query url.Values, destination any) (found bool, err error) {
	if err := client.http.GetJSON(ctx, client.url(path, query), destination); err != nil {
		if remote.NotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// --- Wire models (only the fields the ported providers consume) ---

type configuration struct {
	Images struct {
		SecureBaseURL string `json:"secure_base_url"`
	} `json:"images"`
}

type namedValue struct {
	Name string `json:"name"`
}

type collectionRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type productionCountry struct {
	Iso31661 string `json:"iso_3166_1"`
	Name     string `json:"name"`
}

type castMember struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
	ProfilePath string `json:"profile_path"`
}

type crewMember struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Job         string `json:"job"`
	Department  string `json:"department"`
	ProfilePath string `json:"profile_path"`
}

type credits struct {
	Cast []castMember `json:"cast"`
	Crew []crewMember `json:"crew"`
}

type video struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Site string `json:"site"`
	Type string `json:"type"`
}

type videos struct {
	Results []video `json:"results"`
}

type keywords struct {
	Keywords []namedValue `json:"keywords"`
}

type releaseDate struct {
	Certification string `json:"certification"`
}

type releaseDatesEntry struct {
	Iso31661     string        `json:"iso_3166_1"`
	ReleaseDates []releaseDate `json:"release_dates"`
}

type releaseDates struct {
	Results []releaseDatesEntry `json:"results"`
}

type imageData struct {
	FilePath    string  `json:"file_path"`
	Iso6391     string  `json:"iso_639_1"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
}

type images struct {
	Posters   []imageData `json:"posters"`
	Backdrops []imageData `json:"backdrops"`
	Logos     []imageData `json:"logos"`
}

// movie is the /movie/{id} response with the appended sub-resources the ported
// provider reads (credits, release_dates, images, videos, keywords).
type movie struct {
	ID                  int                 `json:"id"`
	Title               string              `json:"title"`
	OriginalTitle       string              `json:"original_title"`
	OriginalLanguage    string              `json:"original_language"`
	Overview            string              `json:"overview"`
	Tagline             string              `json:"tagline"`
	PosterPath          string              `json:"poster_path"`
	ReleaseDate         string              `json:"release_date"`
	ImdbID              string              `json:"imdb_id"`
	VoteAverage         float64             `json:"vote_average"`
	BelongsToCollection *collectionRef      `json:"belongs_to_collection"`
	Genres              []namedValue        `json:"genres"`
	ProductionCompanies []namedValue        `json:"production_companies"`
	ProductionCountries []productionCountry `json:"production_countries"`
	Credits             *credits            `json:"credits"`
	Keywords            *keywords           `json:"keywords"`
	ReleaseDates        *releaseDates       `json:"release_dates"`
	Videos              *videos             `json:"videos"`
	Images              *images             `json:"images"`
}

type searchMovie struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Overview      string `json:"overview"`
	PosterPath    string `json:"poster_path"`
	ReleaseDate   string `json:"release_date"`
}

type searchMovieResponse struct {
	Results []searchMovie `json:"results"`
}

type findResponse struct {
	MovieResults []searchMovie `json:"movie_results"`
}

// --- Endpoint methods ---

// imageBaseURL returns TMDb's secure image CDN root, fetched once and cached in
// the transport's response cache; it falls back to the documented default.
func (client *apiClient) imageBaseURL(ctx context.Context) string {
	var config configuration
	if found, err := client.getJSON(ctx, "/configuration", nil, &config); err == nil && found {
		if base := strings.TrimSpace(config.Images.SecureBaseURL); base != "" {
			return base
		}
	}
	return defaultImageBaseURL
}

// getMovie fetches a movie with the sub-resources the provider maps.
func (client *apiClient) getMovie(ctx context.Context, tmdbID int, language, imageLanguages string) (*movie, error) {
	query := url.Values{}
	if language != "" {
		query.Set("language", language)
	}
	if imageLanguages != "" {
		query.Set("include_image_language", imageLanguages)
	}
	query.Set("append_to_response", "credits,release_dates,images,videos,keywords")
	var result movie
	found, err := client.getJSON(ctx, "/movie/"+strconv.Itoa(tmdbID), query, &result)
	if err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb get failed", slog.String("op", "movie"), slog.Int("tmdbId", tmdbID), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb get", slog.String("op", "movie"), slog.Int("tmdbId", tmdbID), slog.Bool("found", found))
	if !found {
		return nil, nil
	}
	return &result, nil
}

// searchMovie searches by name, optionally constrained by release year.
func (client *apiClient) searchMovie(ctx context.Context, name string, year int, language string, includeAdult bool) ([]searchMovie, error) {
	query := url.Values{}
	query.Set("query", name)
	if language != "" {
		query.Set("language", language)
	}
	if year > 0 {
		query.Set("year", strconv.Itoa(year))
	}
	query.Set("include_adult", strconv.FormatBool(includeAdult))
	var response searchMovieResponse
	if _, err := client.getJSON(ctx, "/search/movie", query, &response); err != nil {
		client.logger.LogAttrs(ctx, slog.LevelWarn, "tmdb search failed", slog.String("op", "search/movie"), slog.Int("year", year), slog.String("error", "request failed"))
		return nil, err
	}
	client.logger.LogAttrs(ctx, slog.LevelDebug, "tmdb search", slog.String("op", "search/movie"), slog.Int("year", year), slog.Int("results", len(response.Results)))
	return response.Results, nil
}

// findByExternalID resolves a movie from an IMDb or TVDB id.
func (client *apiClient) findByExternalID(ctx context.Context, externalID, source, language string) (*findResponse, error) {
	query := url.Values{}
	query.Set("external_source", source)
	if language != "" {
		query.Set("language", language)
	}
	var response findResponse
	found, err := client.getJSON(ctx, "/find/"+externalID, query, &response)
	if err != nil || !found {
		return nil, err
	}
	return &response, nil
}

// imageURL joins the CDN base, a size segment, and a TMDb relative path.
func imageURL(baseURL, size, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if size == "" {
		size = "original"
	}
	return fmt.Sprintf("%s%s%s", strings.TrimRight(baseURL, "/")+"/", size, path)
}
