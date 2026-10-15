// SPDX-License-Identifier: GPL-2.0-only

package anilist

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

// mediaQuery fetches the series-level fields of one anime. It asks for no
// per-episode data: episodes come from the episode provider.
const mediaQuery = `query ($id: Int) {
  Media(id: $id, type: ANIME) {
    id
    idMal
    format
    status
    episodes
    season
    seasonYear
    startDate { year month day }
    endDate { year month day }
    title { romaji english native }
    synonyms
    description(asHtml: false)
    genres
    averageScore
    studios(isMain: true) { nodes { name } }
    coverImage { extraLarge large }
    bannerImage
    relations { edges { relationType(version: 2) node { id type format title { romaji english } } } }
  }
}`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]int `json:"variables"`
}

type graphQLResponse struct {
	Data struct {
		Media *media `json:"Media"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"errors"`
}

type fuzzyDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

type mediaTitle struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

type media struct {
	ID           int        `json:"id"`
	IDMal        int        `json:"idMal"`
	Format       string     `json:"format"`
	Status       string     `json:"status"`
	Episodes     int        `json:"episodes"`
	Season       string     `json:"season"`
	SeasonYear   int        `json:"seasonYear"`
	StartDate    fuzzyDate  `json:"startDate"`
	EndDate      fuzzyDate  `json:"endDate"`
	Title        mediaTitle `json:"title"`
	Synonyms     []string   `json:"synonyms"`
	Description  string     `json:"description"`
	Genres       []string   `json:"genres"`
	AverageScore int        `json:"averageScore"`
	Studios      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"studios"`
	CoverImage struct {
		ExtraLarge string `json:"extraLarge"`
		Large      string `json:"large"`
	} `json:"coverImage"`
	BannerImage string `json:"bannerImage"`
	Relations   struct {
		Edges []struct {
			RelationType string `json:"relationType"`
			Node         struct {
				ID     int        `json:"id"`
				Type   string     `json:"type"`
				Format string     `json:"format"`
				Title  mediaTitle `json:"title"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"relations"`
}

// apiClient calls the AniList GraphQL API. AniList limits clients per
// minute and reports the budget in X-RateLimit-* headers; on HTTP 429 it
// sends Retry-After. The client paces through the shared transport and also
// pauses all calls when AniList reports the budget spent. Fetched media are
// kept for cacheTTL, so a rescan does not query a series again.
type apiClient struct {
	http     *remote.Client
	clock    clock.Clock
	endpoint string
	logger   *slog.Logger
	cacheTTL time.Duration
	maxCache int

	mu    sync.Mutex
	pause time.Time
	cache map[int]cachedMedia
}

type cachedMedia struct {
	value   *media
	fetched time.Time
}

// maxAttempts bounds retries after HTTP 429.
const maxAttempts = 3

func newAPIClient(transport *remote.Client, clk clock.Clock, endpoint string, cacheTTL time.Duration, logger *slog.Logger) *apiClient {
	return &apiClient{
		http: transport, clock: clk, endpoint: endpoint, logger: logger,
		cacheTTL: cacheTTL, maxCache: 4096, cache: make(map[int]cachedMedia),
	}
}

// media returns one anime by AniList id, or nil when AniList has no such
// anime.
func (c *apiClient) media(ctx context.Context, id int) (*media, error) {
	if cached, ok := c.cached(id); ok {
		return cached, nil
	}
	body, err := json.Marshal(graphQLRequest{Query: mediaQuery, Variables: map[string]int{"id": id}})
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		if err := c.waitForBudget(ctx); err != nil {
			return nil, err
		}
		response, err := c.http.Post(ctx, c.endpoint, body)
		if err != nil {
			return nil, err
		}
		c.observeBudget(response)
		if response.Status == http.StatusTooManyRequests {
			if attempt >= maxAttempts {
				return nil, &remote.StatusError{Status: response.Status}
			}
			continue
		}
		var decoded graphQLResponse
		if err := json.Unmarshal(response.Body, &decoded); err != nil && response.Status < 300 {
			return nil, fmt.Errorf("anilist: decode response: %w", err)
		}
		if response.Status == http.StatusNotFound || notFound(decoded) {
			c.store(id, nil)
			return nil, nil
		}
		if response.Status < 200 || response.Status >= 300 {
			return nil, &remote.StatusError{Status: response.Status}
		}
		if len(decoded.Errors) != 0 {
			return nil, fmt.Errorf("anilist: query error: %s", decoded.Errors[0].Message)
		}
		c.store(id, decoded.Data.Media)
		return decoded.Data.Media, nil
	}
}

func notFound(response graphQLResponse) bool {
	for _, failure := range response.Errors {
		if failure.Status == http.StatusNotFound {
			return true
		}
	}
	return false
}

func (c *apiClient) cached(id int) (*media, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.cache[id]
	if !found || c.clock.Now().Sub(entry.fetched) >= c.cacheTTL {
		return nil, false
	}
	return entry.value, true
}

func (c *apiClient) store(id int, value *media) {
	if c.cacheTTL <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	c.cache[id] = cachedMedia{value: value, fetched: now}
	for len(c.cache) > c.maxCache {
		oldestID, oldest := 0, now
		for key, entry := range c.cache {
			if entry.fetched.Before(oldest) || oldestID == 0 {
				oldestID, oldest = key, entry.fetched
			}
		}
		delete(c.cache, oldestID)
	}
}

// waitForBudget blocks while AniList has asked the client to pause.
func (c *apiClient) waitForBudget(ctx context.Context) error {
	c.mu.Lock()
	delay := c.pause.Sub(c.clock.Now())
	c.mu.Unlock()
	if delay <= 0 {
		return ctx.Err()
	}
	c.logger.LogAttrs(ctx, slog.LevelInfo, "anilist rate limit pause", slog.Duration("delay", delay))
	timer := c.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}

// observeBudget reads AniList's rate-limit headers. A 429 pauses for
// Retry-After (default one minute); a spent budget pauses until
// X-RateLimit-Reset.
func (c *apiClient) observeBudget(response *remote.Response) {
	now := c.clock.Now()
	var until time.Time
	if response.Status == http.StatusTooManyRequests {
		wait := time.Minute
		if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds >= 0 {
			wait = time.Duration(seconds) * time.Second
		}
		until = now.Add(wait)
	} else if remaining, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining"))); err == nil && remaining <= 1 {
		until = now.Add(time.Minute)
		if reset, err := strconv.ParseInt(strings.TrimSpace(response.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if at := time.Unix(reset, 0); at.After(now) && at.Before(now.Add(2*time.Minute)) {
				until = at
			}
		}
	}
	if until.IsZero() {
		return
	}
	c.mu.Lock()
	if until.After(c.pause) {
		c.pause = until
	}
	c.mu.Unlock()
}
