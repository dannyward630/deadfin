// SPDX-License-Identifier: GPL-2.0-only

package anime_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/anime"
	"github.com/deadfin-org/deadfin/internal/clock"
)

// fakeHost serves fixed seed bodies and records each request.
type fakeHost struct {
	mu       sync.Mutex
	bodies   map[string][]byte
	etags    map[string]string
	requests []*http.Request
}

func (host *fakeHost) Do(request *http.Request) (*http.Response, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.requests = append(host.requests, request)
	url := request.URL.String()
	etag := host.etags[url]
	if etag != "" && request.Header.Get("If-None-Match") == etag {
		return &http.Response{StatusCode: http.StatusNotModified, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	body, found := host.bodies[url]
	if !found {
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	header := http.Header{"Last-Modified": {"Sat, 10 Oct 2026 03:00:08 GMT"}}
	if etag != "" {
		header.Set("ETag", etag)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (host *fakeHost) count() int {
	host.mu.Lock()
	defer host.mu.Unlock()
	return len(host.requests)
}

func testSources() []anime.Source {
	return []anime.Source{
		anime.ManamiSource("https://seed.test/manami.json"),
		anime.AniDBTitlesSource("https://seed.test/anime-titles.xml.gz"),
		anime.AnimeListsSource("https://seed.test/anime-list-master.xml"),
	}
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	read := func(name string) []byte {
		contents, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return contents
	}
	return &fakeHost{
		bodies: map[string][]byte{
			"https://seed.test/manami.json":           read("anime-offline-database-minified.json"),
			"https://seed.test/anime-titles.xml.gz":   read("anime-titles.xml.gz"),
			"https://seed.test/anime-list-master.xml": read("anime-list-master.xml"),
		},
		etags: map[string]string{
			"https://seed.test/manami.json":           `"m1"`,
			"https://seed.test/anime-titles.xml.gz":   `"t1"`,
			"https://seed.test/anime-list-master.xml": `"l1"`,
		},
	}
}

// TestServiceRefreshDownloadsOncePerDay checks the AniDB rule: at most one
// request per source per day, conditional requests after that, and a
// crosswalk built from the cached files.
func TestServiceRefreshDownloadsOncePerDay(t *testing.T) {
	host := newFakeHost(t)
	fake := clock.NewFake(time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	service := anime.NewService(anime.ServiceOptions{
		Fetcher: anime.NewFetcher(anime.FetcherOptions{Doer: host, Clock: fake, Dir: dir, UserAgent: "Deadfin/test (+https://github.com/deadfin-org/deadfin)"}),
		Dir:     dir,
		Sources: testSources(),
	})

	if err := service.Refresh(t.Context(), nil); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if got := host.count(); got != 3 {
		t.Fatalf("first refresh made %d requests, want 3", got)
	}
	for _, request := range host.requests {
		if request.Header.Get("User-Agent") != "Deadfin/test (+https://github.com/deadfin-org/deadfin)" {
			t.Fatalf("request %s User-Agent = %q", request.URL, request.Header.Get("User-Agent"))
		}
		if request.Header.Get("If-None-Match") != "" {
			t.Fatalf("first request %s is conditional", request.URL)
		}
	}
	crosswalk, err := service.Crosswalk(t.Context())
	if err != nil || crosswalk == nil {
		t.Fatalf("Crosswalk = %v, %v", crosswalk, err)
	}
	if identity, found := crosswalk.Identify(anime.Query{TVDB: 267440}); !found || identity.Anime.AniList != 16498 {
		t.Fatalf("Identify after refresh = %+v found %v", identity, found)
	}

	fake.Advance(23 * time.Hour)
	if err := service.Refresh(t.Context(), nil); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := host.count(); got != 3 {
		t.Fatalf("refresh inside a day made %d requests, want none", got-3)
	}

	fake.Advance(2 * time.Hour)
	if err := service.Refresh(t.Context(), nil); err != nil {
		t.Fatalf("third Refresh: %v", err)
	}
	if got := host.count(); got != 6 {
		t.Fatalf("refresh after a day made %d requests, want 3", got-3)
	}
	for _, request := range host.requests[3:] {
		if request.Header.Get("If-None-Match") == "" || request.Header.Get("If-Modified-Since") == "" {
			t.Fatalf("request %s is not conditional: %v", request.URL, request.Header)
		}
	}
}

// TestFetcherRejectsInvalidDownload keeps the cached file when a download
// does not parse, and counts the failed attempt against the daily limit.
func TestFetcherRejectsInvalidDownload(t *testing.T) {
	host := newFakeHost(t)
	fake := clock.NewFake(time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	fetcher := anime.NewFetcher(anime.FetcherOptions{Doer: host, Clock: fake, Dir: dir})
	source := testSources()[2]
	if changed, err := fetcher.Fetch(t.Context(), source); err != nil || !changed {
		t.Fatalf("first Fetch = %v, %v", changed, err)
	}
	original, err := os.ReadFile(filepath.Join(dir, source.File))
	if err != nil {
		t.Fatal(err)
	}

	host.bodies[source.URL] = []byte("<html>rate limited</html>")
	host.etags[source.URL] = `"l2"`
	fake.Advance(25 * time.Hour)
	if changed, err := fetcher.Fetch(t.Context(), source); err == nil || changed {
		t.Fatalf("invalid Fetch = %v, %v, want a rejection", changed, err)
	}
	current, err := os.ReadFile(filepath.Join(dir, source.File))
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("cached file changed after a rejected download")
	}
	before := host.count()
	if changed, err := fetcher.Fetch(t.Context(), source); err != nil || changed || host.count() != before {
		t.Fatalf("retry inside the interval = %v, %v, %d requests", changed, err, host.count()-before)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestServiceWithoutSeedData(t *testing.T) {
	service := anime.NewService(anime.ServiceOptions{Dir: t.TempDir(), Sources: testSources()})
	crosswalk, err := service.Crosswalk(t.Context())
	if err != nil || crosswalk != nil {
		t.Fatalf("Crosswalk without data = %v, %v, want nil, nil", crosswalk, err)
	}
	if _, found := crosswalk.Identify(anime.Query{TVDB: 267440}); found {
		t.Fatal("nil crosswalk identified a series")
	}
}
