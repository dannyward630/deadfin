// SPDX-License-Identifier: GPL-2.0-only

package audiodb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

type apiClient struct {
	http   *remote.Client
	base   string
	apiKey string
}

func newAPIClient(http *remote.Client, config Config) *apiClient {
	return &apiClient{
		http:   http.WithLogURLRedactor(func(rawURL string) string { return redactLogURL(rawURL, config.APIKey) }),
		base:   strings.TrimRight(config.Server, "/"),
		apiKey: config.APIKey,
	}
}

type artistResponse struct {
	Artists []artist `json:"artists"`
}

type artist struct {
	IDArtist          string `json:"idArtist"`
	Artist            string `json:"strArtist"`
	Genre             string `json:"strGenre"`
	Biography         string `json:"strBiography"`
	BiographyEN       string `json:"strBiographyEN"`
	BiographyDE       string `json:"strBiographyDE"`
	BiographyFR       string `json:"strBiographyFR"`
	BiographyIT       string `json:"strBiographyIT"`
	BiographyRU       string `json:"strBiographyRU"`
	BiographyPT       string `json:"strBiographyPT"`
	BiographyNL       string `json:"strBiographyNL"`
	ArtistThumb       string `json:"strArtistThumb"`
	ArtistLogo        string `json:"strArtistLogo"`
	ArtistBanner      string `json:"strArtistBanner"`
	ArtistFanart      string `json:"strArtistFanart"`
	ArtistFanart2     string `json:"strArtistFanart2"`
	ArtistFanart3     string `json:"strArtistFanart3"`
	MusicBrainzArtist string `json:"strMusicBrainzID"`
}

type albumResponse struct {
	Albums []album `json:"album"`
}

type album struct {
	IDAlbum                string `json:"idAlbum"`
	IDArtist               string `json:"idArtist"`
	Album                  string `json:"strAlbum"`
	Artist                 string `json:"strArtist"`
	YearReleased           string `json:"intYearReleased"`
	Genre                  string `json:"strGenre"`
	AlbumThumb             string `json:"strAlbumThumb"`
	AlbumCDArt             string `json:"strAlbumCDart"`
	Description            string `json:"strDescription"`
	DescriptionEN          string `json:"strDescriptionEN"`
	DescriptionDE          string `json:"strDescriptionDE"`
	DescriptionFR          string `json:"strDescriptionFR"`
	DescriptionIT          string `json:"strDescriptionIT"`
	DescriptionRU          string `json:"strDescriptionRU"`
	DescriptionPT          string `json:"strDescriptionPT"`
	DescriptionNL          string `json:"strDescriptionNL"`
	MusicBrainzGroup       string `json:"strMusicBrainzID"`
	MusicBrainzAlbumArtist string `json:"strMusicBrainzArtistID"`
}

func (client *apiClient) getArtist(ctx context.Context, musicBrainzID string) (*artist, error) {
	var response artistResponse
	if err := client.getJSON(ctx, "artist-mb.php", musicBrainzID, &response); err != nil {
		return nil, err
	}
	if len(response.Artists) == 0 {
		return nil, nil
	}
	return &response.Artists[0], nil
}

func (client *apiClient) getAlbum(ctx context.Context, releaseGroupID string) (*album, error) {
	var response albumResponse
	endpoint := client.endpoint("album-mb.php", releaseGroupID)
	upstream, err := client.http.Get(ctx, endpoint)
	if err != nil {
		return nil, client.redact(err)
	}
	// Pinned AudioDbAlbumProvider intentionally copies and deserializes the
	// response without EnsureSuccessStatusCode; preserve that observable
	// difference from its artist counterpart.
	if err := json.Unmarshal(upstream.Body, &response); err != nil {
		return nil, fmt.Errorf("audiodb: decode album response: %w", err)
	}
	if len(response.Albums) == 0 {
		return nil, nil
	}
	return &response.Albums[0], nil
}

func (client *apiClient) getJSON(ctx context.Context, resource, id string, destination any) error {
	if err := client.http.GetJSON(ctx, client.endpoint(resource, id), destination); err != nil {
		return client.redact(err)
	}
	return nil
}

func (client *apiClient) endpoint(resource, id string) string {
	query := url.Values{"i": {id}}
	return client.base + "/" + url.PathEscape(client.apiKey) + "/" + resource + "?" + query.Encode()
}

// redactLogURL preserves the AudioDB resource path while removing its
// credential-bearing path component and all query data from observations.
func redactLogURL(rawURL, apiKey string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<unparseable url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if apiKey != "" {
		parsed.Path = strings.ReplaceAll(parsed.Path, apiKey, "[REDACTED]")
		parsed.RawPath = ""
	}
	return parsed.String()
}

func (client *apiClient) redact(err error) error {
	message := err.Error()
	for _, secret := range []string{url.PathEscape(client.apiKey), client.apiKey} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return redactedError{message: message, cause: err}
}

type redactedError struct {
	message string
	cause   error
}

func (err redactedError) Error() string { return err.message }

func (err redactedError) Unwrap() error { return err.cause }
