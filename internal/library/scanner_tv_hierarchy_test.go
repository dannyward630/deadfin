// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

type tvHierarchyTree struct {
	show, season, episode string
}

// writeTVHierarchy builds Show/Season 01/Show S01E01.mkv in a tvshows library.
func writeTVHierarchy(t *testing.T, harness *scannerHarness, nfo string) tvHierarchyTree {
	t.Helper()
	rewriteScannerCollection(t, harness.topology, "movies", "tvshows")
	tree := tvHierarchyTree{show: scannerPath(t, harness.source, "Breaking Bad")}
	tree.season = scannerPath(t, tree.show, "Season 01")
	tree.episode = scannerPath(t, tree.season, "Breaking Bad S01E01.mkv")
	writeScannerFile(t, tree.episode, "episode")
	if nfo != "" {
		writeScannerFile(t, scannerPath(t, tree.show, "tvshow.nfo"), nfo)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	return tree
}

type ancestorFact struct {
	kind  item.Kind
	tmdb  string
	index int
}

func ancestorFacts(ancestors []library.RefreshAncestor, limit int) []ancestorFact {
	var out []ancestorFact
	for _, ancestor := range ancestors {
		if len(out) == limit {
			break
		}
		fact := ancestorFact{kind: ancestor.Kind, index: -1}
		fact.tmdb, _ = ancestor.ProviderIDs.Get("Tmdb")
		if ancestor.IndexNumber != nil {
			fact.index = *ancestor.IndexNumber
		}
		out = append(out, fact)
	}
	return out
}

// TestRemoteRefreshPassesSeriesAncestorsToSeasonAndEpisode proves that the
// scanner gives each Season and Episode refresh the resolved series identity,
// nearest ancestor first, including a provider id the series refresh found.
func TestRemoteRefreshPassesSeriesAncestorsToSeasonAndEpisode(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	writeTVHierarchy(t, harness, "")

	var mu sync.Mutex
	seen := make(map[item.Kind][]ancestorFact)
	refresher := scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if candidate.Kind == item.KindSeries {
			candidate.ProviderIDs.Set("Tmdb", "1396")
		}
		mu.Lock()
		seen[candidate.Kind] = ancestorFacts(refresh.Ancestors, 2)
		mu.Unlock()
		// A provider that writes to its snapshot changes nothing else.
		for index := range refresh.Ancestors {
			refresh.Ancestors[index].ProviderIDs.Set("Tmdb", "mutated")
		}
		return nil
	})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: refresher})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %v, errors %v", err, result.Errors)
	}
	season := []ancestorFact{{kind: item.KindSeries, tmdb: "1396", index: -1}}
	if got := seen[item.KindSeason]; len(got) == 0 || got[0] != season[0] {
		t.Errorf("season ancestors = %+v, want the series with Tmdb 1396 first", got)
	}
	episode := []ancestorFact{{kind: item.KindSeason, index: 1}, {kind: item.KindSeries, tmdb: "1396", index: -1}}
	if got := seen[item.KindEpisode]; len(got) != 2 || got[0] != episode[0] || got[1] != episode[1] {
		t.Errorf("episode ancestors = %+v, want %+v", got, episode)
	}
}

// TestScanStoresPresentationKeysForNextUp proves that a scan stores
// PresentationUniqueKey on every item and the series key on its Season and
// Episode, as upstream BeforeSave does. NextUp groups played episodes by it.
func TestScanStoresPresentationKeysForNextUp(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	tree := writeTVHierarchy(t, harness, "")
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %v, errors %v", err, result.Errors)
	}
	series := scannerItemAtPath(t, harness.items, tree.show)
	season := scannerItemAtPath(t, harness.items, tree.season)
	episode := scannerItemAtPath(t, harness.items, tree.episode)
	seriesKey := series.ID.String()
	for _, check := range []struct {
		name      string
		got, want *string
	}{
		{"series key", series.PresentationUniqueKey, &seriesKey},
		{"season key", season.PresentationUniqueKey, stringValue(seriesKey + "-001")},
		{"season series key", season.SeriesPresentationUniqueKey, &seriesKey},
		{"episode key", episode.PresentationUniqueKey, stringValue(episode.ID.String())},
		{"episode series key", episode.SeriesPresentationUniqueKey, &seriesKey},
	} {
		if check.got == nil || *check.got != *check.want {
			t.Errorf("%s = %v, want %q", check.name, check.got, *check.want)
		}
	}
}

func stringValue(value string) *string { return &value }

// multiCassette replays the first cassette that holds the request.
type multiCassette []*remotetest.Cassette

func (cassettes multiCassette) Do(request *http.Request) (*http.Response, error) {
	var err error
	for _, cassette := range cassettes {
		var response *http.Response
		if response, err = cassette.Do(request); err == nil {
			return response, nil
		}
	}
	return nil, err
}

// TestRemoteRefreshEnrichesSeasonAndEpisodeFromTMDb runs the real NFO and
// TMDb providers over a recorded TMDb cassette. The series id comes from
// tvshow.nfo; the season and episode refreshes reach TMDb through it and the
// enriched fields persist.
func TestRemoteRefreshEnrichesSeasonAndEpisodeFromTMDb(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	tree := writeTVHierarchy(t, harness, `<tvshow><title>Breaking Bad</title><tmdbid>1396</tmdbid></tvshow>`)
	enableInternetProviders(t, harness)

	var cassettes multiCassette
	for _, path := range []string{"../providers/tmdb/testdata/breaking-bad.json", "../providers/tmdb/testdata/breaking-bad-seasons.json"} {
		cassette, err := remotetest.Load(path)
		if err != nil {
			t.Fatalf("load cassette: %v", err)
		}
		cassettes = append(cassettes, cassette)
	}
	config := tmdb.DefaultConfig()
	config.APIKey = "test-key"
	registry := providers.NewRegistry(
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			return os.Open(path)
		}}),
		tmdb.NewProvider(tmdb.Options{HTTP: remote.NewClient(remote.Config{Doer: cassettes, CacheEntries: 32}), Config: config}),
	)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %v, errors %v", err, result.Errors)
	}

	season := scannerItemAtPath(t, harness.items, tree.season)
	if id, ok := season.ProviderIDs.Get("Tvdb"); !ok || id != "30272" {
		t.Errorf("season Tvdb = %q, want 30272 from TMDb", id)
	}
	if season.Overview == nil || *season.Overview == "" {
		t.Error("season Overview is empty, want the TMDb overview")
	}
	episode := scannerItemAtPath(t, harness.items, tree.episode)
	if episode.Name == nil || *episode.Name != "Pilot" {
		t.Errorf("episode Name = %v, want Pilot", episode.Name)
	}
	if id, ok := episode.ProviderIDs.Get("Tvdb"); !ok || id != "349232" {
		t.Errorf("episode Tvdb = %q, want 349232", id)
	}
	hasStill := false
	for _, image := range episode.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == "https://image.tmdb.org/t/p/original/s1e1.jpg" && image.Width == 1920 && image.Height == 1080 {
			hasStill = true
		}
	}
	if !hasStill {
		t.Errorf("episode ImageInfos = %+v, want the TMDb still with dimensions", episode.ImageInfos)
	}
}

// TestRemoteRefreshSkipsEpisodeWithoutSeriesTMDbID proves that a series with
// no TMDb id leaves its seasons and episodes unenriched and the scan clean.
func TestRemoteRefreshSkipsEpisodeWithoutSeriesTMDbID(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	tree := writeTVHierarchy(t, harness, "")
	enableInternetProviders(t, harness)

	var mu sync.Mutex
	var requests []string
	doer := doerFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, request.URL.Path)
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(http.NoBody), Header: http.Header{}, Request: request}, nil
	})
	config := tmdb.DefaultConfig()
	config.APIKey = "test-key"
	registry := providers.NewRegistry(tmdb.NewProvider(tmdb.Options{HTTP: remote.NewClient(remote.Config{Doer: doer}), Config: config}))
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %v, errors %v", err, result.Errors)
	}
	for _, path := range requests {
		if len(path) > len("/3/tv/") && path[:len("/3/tv/")] == "/3/tv/" {
			t.Errorf("request %s, want no season or episode request without a series TMDb id", path)
		}
	}
	if episode := scannerItemAtPath(t, harness.items, tree.episode); episode.Overview != nil {
		t.Errorf("episode Overview = %q, want none", *episode.Overview)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (do doerFunc) Do(request *http.Request) (*http.Response, error) { return do(request) }
