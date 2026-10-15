// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz

import (
	"context"
	"net/url"
	"strings"

	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

type apiClient struct {
	http   *remote.Client
	server string
}

func newAPIClient(http *remote.Client, server string) *apiClient {
	parsed, _ := url.Parse(strings.TrimRight(server, "/"))
	base := parsed.Scheme + "://" + parsed.Host
	return &apiClient{http: http, server: strings.TrimRight(base, "/")}
}

type artistSearchResponse struct {
	Artists []artistResult `json:"artists"`
}

type artistResult struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	LifeSpan lifeSpan `json:"life-span"`
}

type lifeSpan struct {
	Begin string `json:"begin"`
}

type releaseSearchResponse struct {
	Releases []releaseResult `json:"releases"`
}

type releaseResult struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Date         string         `json:"date"`
	Annotation   string         `json:"annotation"`
	ArtistCredit []artistCredit `json:"artist-credit"`
	ReleaseGroup *releaseGroup  `json:"release-group"`
}

type artistCredit struct {
	Name   string        `json:"name"`
	Artist *artistResult `json:"artist"`
}

type releaseGroup struct {
	ID       string          `json:"id"`
	Releases []releaseResult `json:"releases"`
}

func (client *apiClient) searchArtists(ctx context.Context, query string) ([]artistResult, error) {
	var response artistSearchResponse
	err := client.getJSON(ctx, "artist/", url.Values{"fmt": {"json"}, "query": {query}}, &response)
	return response.Artists, err
}

func (client *apiClient) lookupRelease(ctx context.Context, id string) (*releaseResult, error) {
	var response releaseResult
	err := client.getJSON(ctx, "release/"+url.PathEscape(id), url.Values{
		"fmt": {"json"},
		"inc": {"release-groups"},
	}, &response)
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func (client *apiClient) lookupReleaseGroup(ctx context.Context, id string) (*releaseGroup, error) {
	var response releaseGroup
	err := client.getJSON(ctx, "release-group/"+url.PathEscape(id), url.Values{
		"fmt": {"json"},
		"inc": {"releases"},
	}, &response)
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func (client *apiClient) searchReleases(ctx context.Context, query string) ([]releaseResult, error) {
	var response releaseSearchResponse
	err := client.getJSON(ctx, "release/", url.Values{"fmt": {"json"}, "query": {query}}, &response)
	return response.Releases, err
}

func (client *apiClient) getJSON(ctx context.Context, resource string, query url.Values, destination any) error {
	endpoint := client.server + "/ws/2/" + resource
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return client.http.GetJSON(ctx, endpoint, destination)
}
