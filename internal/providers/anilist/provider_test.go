// SPDX-License-Identifier: GPL-2.0-only

package anilist

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/anime"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/store"
)

// staticCrosswalks serves the anime package's trimmed seed fixtures.
type staticCrosswalks struct{ crosswalk *anime.Crosswalk }

func (s staticCrosswalks) Crosswalk(context.Context) (*anime.Crosswalk, error) {
	return s.crosswalk, nil
}

func fixtureCrosswalks(t *testing.T) staticCrosswalks {
	t.Helper()
	crosswalk, found, err := anime.Load(t.Context(), filepath.Join("..", "..", "anime", "testdata"), anime.DefaultSources())
	if err != nil || !found {
		t.Fatalf("load seed fixtures: found %v, error %v", found, err)
	}
	return staticCrosswalks{crosswalk: crosswalk}
}

type recordingStore struct {
	mu      sync.Mutex
	media   []store.AnimeMedia
	seasons map[string]map[int][]store.AnimeSegment
}

func (s *recordingStore) SaveMedia(_ context.Context, media store.AnimeMedia) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media = append(s.media, media)
	return nil
}

func (s *recordingStore) ReplaceSeasonMap(_ context.Context, numbering string, series int, seasons map[int][]store.AnimeSegment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seasons == nil {
		s.seasons = make(map[string]map[int][]store.AnimeSegment)
	}
	s.seasons[numbering+":"+strconv.Itoa(series)] = seasons
	return nil
}

// animeLibrary returns library options that enable the provider for series.
func animeLibrary() library.LibraryOptions {
	return library.LibraryOptions{TypeOptions: []library.TypeOptions{
		{Type: "Series", MetadataFetchers: []string{"TheMovieDb", "AniList"}},
	}}
}

func newCassetteProvider(t *testing.T, records *recordingStore) *Provider {
	t.Helper()
	cassette, err := remotetest.Load(filepath.Join("testdata", "media.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake := clock.NewFake(time.Date(2026, 10, 11, 2, 30, 0, 0, time.UTC))
	return NewProvider(Options{
		HTTP:       remote.NewClient(remote.Config{Doer: cassette, Clock: fake}),
		Crosswalks: fixtureCrosswalks(t),
		Store:      records,
		Clock:      fake,
	})
}

func stringValue(value string) *string { return &value }

func TestRefreshSeriesIdentifiesByTVDBAndAppliesAniList(t *testing.T) {
	records := &recordingStore{}
	provider := newCassetteProvider(t, records)
	series := item.Item{
		ID: dto.UUID{1}, Kind: item.KindSeries,
		Name: stringValue("Shingeki no Kyojin"), SortName: stringValue("shingeki no kyojin"),
		Path:        stringValue("/media/animu/Shingeki no Kyojin"),
		ImageInfos:  []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/animu/Shingeki no Kyojin/poster.jpg"}},
		ProviderIDs: providerIDs("Tvdb", "267440"),
	}
	handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary(), LibraryRoot: "/media/animu"}, &series)
	if err != nil || !handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	for key, want := range map[string]string{"AniList": "16498", "AniDB": "9541", "MyAnimeList": "16498", "Tvdb": "267440"} {
		if got, _ := series.ProviderIDs.Get(key); got != want {
			t.Errorf("provider id %s = %q, want %q", key, got, want)
		}
	}
	if *series.Name != "Attack on Titan" || *series.OriginalTitle != "進撃の巨人" || *series.SortName != "attack on titan" {
		t.Errorf("titles = %q / %q / %q", *series.Name, *series.OriginalTitle, *series.SortName)
	}
	if series.Overview == nil || !strings.HasPrefix(*series.Overview, "Several hundred years ago") || strings.Contains(*series.Overview, "<br>") {
		t.Errorf("overview = %v", series.Overview)
	}
	if strings.Join(series.Values.Genres, ",") != "Action,Drama,Fantasy,Mystery" || strings.Join(series.Values.Studios, ",") != "WIT STUDIO" {
		t.Errorf("genres %v studios %v", series.Values.Genres, series.Values.Studios)
	}
	if series.CommunityRating == nil || *series.CommunityRating != 8.5 || series.ProductionYear == nil || *series.ProductionYear != 2013 {
		t.Errorf("rating %v year %v", series.CommunityRating, series.ProductionYear)
	}
	if series.SeriesStatus == nil || *series.SeriesStatus != item.SeriesStatusEnded {
		t.Errorf("status = %v", series.SeriesStatus)
	}
	// The local poster stays; the AniList banner fills the missing type.
	if len(series.ImageInfos) != 2 || series.ImageInfos[0].Path != "/media/animu/Shingeki no Kyojin/poster.jpg" ||
		series.ImageInfos[1].Type != item.ImageTypeBanner || !strings.HasPrefix(series.ImageInfos[1].Path, "https://s4.anilist.co/") {
		t.Errorf("images = %+v", series.ImageInfos)
	}

	if len(records.media) != 1 {
		t.Fatalf("SaveMedia calls = %d, want 1", len(records.media))
	}
	media := records.media[0]
	if media.AniListID != 16498 || media.AniDBID != 9541 || media.Format != "TV" || media.Season != "SPRING" || media.SeasonYear != 2013 {
		t.Errorf("media = %+v", media)
	}
	sequel := false
	for _, relation := range media.Relations {
		if relation.RelationType == "SEQUEL" && relation.TargetAniListID == 20958 && relation.TargetType == "ANIME" {
			sequel = true
		}
	}
	if !sequel || len(media.Relations) != 11 {
		t.Errorf("relations = %+v, want 11 edges with SEQUEL 20958", media.Relations)
	}
	if len(media.TitleSets) != 1 || media.TitleSets[0].Provider != "AniList" || media.TitleSets[0].ExternalID != "16498" {
		t.Fatalf("title sets = %+v", media.TitleSets)
	}
	titles := map[string]bool{}
	for _, title := range media.TitleSets[0].Titles {
		titles[title.Title] = true
	}
	for _, want := range []string{"Attack on Titan", "Shingeki no Kyojin", "進撃の巨人", "AoT", "진격의 거인", "Atak Tytanów"} {
		if !titles[want] {
			t.Errorf("stored titles lack %q", want)
		}
	}

	seasons := records.seasons["tvdb:267440"]
	if len(seasons[3]) != 2 || seasons[3][1].AniDBID != 14444 || seasons[3][1].EpisodeOffset != 12 || len(seasons[4]) != 3 {
		t.Errorf("TVDB season map = %+v", seasons)
	}
	tmdbSeasons := records.seasons["tmdb:1429"]
	if len(tmdbSeasons[3]) != 2 || tmdbSeasons[3][1].EpisodeOffset != 12 || len(tmdbSeasons[4]) != 3 {
		t.Errorf("TMDb season map = %+v", tmdbSeasons)
	}
}

func TestRefreshSeasonWritesFirstSegmentIDs(t *testing.T) {
	provider := newCassetteProvider(t, &recordingStore{})
	three := 3
	season := item.Item{ID: dto.UUID{2}, Kind: item.KindSeason, IndexNumber: &three}
	refresh := library.RefreshContext{Options: animeLibrary(), Ancestors: []library.RefreshAncestor{
		{ID: dto.UUID{1}, Kind: item.KindSeries, ProviderIDs: providerIDs("Tvdb", "267440")},
	}}
	handled, err := provider.Refresh(t.Context(), refresh, &season)
	if err != nil || !handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	for key, want := range map[string]string{"AniDB": "13241", "AniList": "99147", "MyAnimeList": "35760"} {
		if got, _ := season.ProviderIDs.Get(key); got != want {
			t.Errorf("season provider id %s = %q, want %q", key, got, want)
		}
	}
}

// TestRefreshRequiresLibraryOptIn keeps non-anime libraries unchanged, also
// when a series there carries a TVDB id that Anime-Lists knows.
func TestRefreshRequiresLibraryOptIn(t *testing.T) {
	provider := newCassetteProvider(t, &recordingStore{})
	for _, options := range []library.LibraryOptions{
		{},
		{TypeOptions: []library.TypeOptions{{Type: "Series", MetadataFetchers: []string{"TheMovieDb"}}}},
		{TypeOptions: []library.TypeOptions{{Type: "Movie", MetadataFetchers: []string{"AniList"}}}},
	} {
		series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Shingeki no Kyojin"), ProviderIDs: providerIDs("Tvdb", "267440")}
		handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: options}, &series)
		if err != nil || handled || *series.Name != "Shingeki no Kyojin" || len(series.ProviderIDs.Snapshot()) != 1 {
			t.Errorf("options %+v: Refresh = %v, %v; item %+v", options, handled, err, series)
		}
	}
}

// TestRefreshSeriesByTMDbID covers the live animu shape: plain-title
// folders and a TMDb id from the TMDb provider, no TVDB id.
func TestRefreshSeriesByTMDbID(t *testing.T) {
	records := &recordingStore{}
	provider := newCassetteProvider(t, records)
	series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Attack on Titan"), ProviderIDs: providerIDs("Tmdb", "1429")}
	handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary()}, &series)
	if err != nil || !handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	for key, want := range map[string]string{"AniList": "16498", "AniDB": "9541", "Tvdb": "267440"} {
		if got, _ := series.ProviderIDs.Get(key); got != want {
			t.Errorf("provider id %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := records.seasons["tmdb:1429"]; !ok {
		t.Errorf("TMDb season map not stored: %v", records.seasons)
	}
}

func TestRefreshSeriesByTitleWithoutIDs(t *testing.T) {
	records := &recordingStore{}
	provider := newCassetteProvider(t, records)
	series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Attack on Titan (2013)")}
	handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary()}, &series)
	if err != nil || !handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	if got, _ := series.ProviderIDs.Get("AniList"); got != "16498" {
		t.Fatalf("AniList id = %q", got)
	}
	if got, _ := series.ProviderIDs.Get("Tvdb"); got != "267440" {
		t.Fatalf("Tvdb id from Anime-Lists = %q", got)
	}
}

// TestRefreshSeriesReplacesWrongSeriesIDs covers "Hundred" labelled
// "Cain's Hundred": the TMDb provider matched another show of the same name.
// The folder title identifies the anime, the TVDB and TMDb ids that map to no
// anime are replaced by the Anime-Lists ids, and the run logs both.
func TestRefreshSeriesReplacesWrongSeriesIDs(t *testing.T) {
	records := &recordingStore{}
	provider := newCassetteProvider(t, records)
	var logs strings.Builder
	provider.logger = slog.New(slog.NewTextHandler(&logs, nil))
	series := item.Item{
		ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("The Simpsons"),
		Path:        stringValue("/media/animu/Shingeki no Kyojin"),
		ProviderIDs: providerIDs("Tvdb", "71663", "Tmdb", "456"),
	}
	handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary(), LibraryRoot: "/media/animu"}, &series)
	if err != nil || !handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	for key, want := range map[string]string{"AniList": "16498", "AniDB": "9541", "Tvdb": "267440", "Tmdb": "1429"} {
		if got, _ := series.ProviderIDs.Get(key); got != want {
			t.Errorf("provider id %s = %q, want %q", key, got, want)
		}
	}
	if *series.Name != "Attack on Titan" {
		t.Errorf("name = %q", *series.Name)
	}
	if _, ok := records.seasons["tvdb:267440"]; !ok {
		t.Errorf("season map not stored under the corrected TVDB id: %v", records.seasons)
	}
	line := logs.String()
	for _, want := range []string{"level=INFO", `msg="anilist refresh"`, "outcome=identified", "method=title", "replacedTvdb=71663", "replacedTmdb=456", "metadata=fetched"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q lacks %q", line, want)
		}
	}
}

// TestRefreshLogsSkipReason makes a provider run that changes nothing
// observable at INFO.
func TestRefreshLogsSkipReason(t *testing.T) {
	provider := newCassetteProvider(t, &recordingStore{})
	var logs strings.Builder
	provider.logger = slog.New(slog.NewTextHandler(&logs, nil))
	series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Breaking Bad")}
	if handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary()}, &series); err != nil || handled {
		t.Fatalf("Refresh = %v, %v", handled, err)
	}
	if line := logs.String(); !strings.Contains(line, "level=INFO") || !strings.Contains(line, "outcome=skipped") ||
		!strings.Contains(line, `reason="no id or title matches the anime crosswalk"`) || !strings.Contains(line, `name="Breaking Bad"`) {
		t.Errorf("log = %q", line)
	}
}

// TestRegistryFullRefreshRunsAniList is the regression for the live animu
// library: an item or library refresh with FullRefresh reaches the provider
// through the registry when MetadataFetchers lists AniList, also when the
// MetadataFetcherOrder imported from Jellyfin does not name it.
func TestRegistryFullRefreshRunsAniList(t *testing.T) {
	provider := newCassetteProvider(t, &recordingStore{})
	var ran []string
	tmdb := fakeRemote{name: "TheMovieDb", ran: &ran}
	registry := providers.NewRegistry(tmdb, provider)
	options := library.LibraryOptions{EnableInternetProviders: true, TypeOptions: []library.TypeOptions{
		{Type: "Series", MetadataFetchers: []string{"AniList", "TheMovieDb", "The Open Movie Database"},
			MetadataFetcherOrder: []string{"TheMovieDb", "The Open Movie Database"}},
		{Type: "Season", MetadataFetchers: []string{"AniList", "TheMovieDb"}, MetadataFetcherOrder: []string{"TheMovieDb"}},
	}}
	series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Attack on Titan"), ProviderIDs: providerIDs("Tmdb", "1429")}
	refresh := library.RefreshContext{Mode: library.RefreshModeFullRefresh, Options: options, LibraryRoot: "/media/animu"}
	if err := registry.Refresh(t.Context(), refresh, &series); err != nil {
		t.Fatalf("registry refresh: %v", err)
	}
	if got, _ := series.ProviderIDs.Get("AniList"); got != "16498" {
		t.Fatalf("AniList id = %q; the registry did not run the provider", got)
	}
	if strings.Join(ran, ",") != "TheMovieDb" {
		t.Fatalf("other remote providers ran %v, want TheMovieDb", ran)
	}

	// A library that does not list AniList never reaches it.
	options.TypeOptions[0].MetadataFetchers = []string{"TheMovieDb"}
	other := item.Item{ID: dto.UUID{2}, Kind: item.KindSeries, ProviderIDs: providerIDs("Tmdb", "1429")}
	if err := registry.Refresh(t.Context(), library.RefreshContext{Mode: library.RefreshModeFullRefresh, Options: options}, &other); err != nil {
		t.Fatalf("registry refresh: %v", err)
	}
	if _, has := other.ProviderIDs.Get("AniList"); has {
		t.Fatal("AniList ran in a library that does not list it")
	}
}

type fakeRemote struct {
	name string
	ran  *[]string
}

func (f fakeRemote) Name() string                 { return f.name }
func (fakeRemote) Stage() providers.ProviderStage { return providers.ProviderStageRemoteMetadata }
func (f fakeRemote) Refresh(context.Context, library.RefreshContext, *item.Item) (bool, error) {
	*f.ran = append(*f.ran, f.name)
	return true, nil
}

func TestRefreshUnknownAniListIDIsNotAnError(t *testing.T) {
	provider := newCassetteProvider(t, &recordingStore{})
	series := item.Item{ID: dto.UUID{1}, Kind: item.KindSeries, Name: stringValue("Missing"), ProviderIDs: providerIDs("AniList", "999999999")}
	handled, err := provider.Refresh(t.Context(), library.RefreshContext{Options: animeLibrary()}, &series)
	if err != nil || !handled || *series.Name != "Missing" {
		t.Fatalf("Refresh = %v, %v, name %q", handled, err, *series.Name)
	}
}

// sequenceDoer answers 429 first, then delegates.
type sequenceDoer struct {
	mu       sync.Mutex
	limited  int
	inner    remote.Doer
	requests int
}

func (d *sequenceDoer) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.requests++
	limited := d.limited > 0
	if limited {
		d.limited--
	}
	d.mu.Unlock()
	if limited {
		_, _ = io.Copy(io.Discard, request.Body)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {"30"}, "X-Ratelimit-Remaining": {"0"}},
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"Too Many Requests.","status":429}],"data":null}`)),
		}, nil
	}
	return d.inner.Do(request)
}

// TestAPIClientHonorsRetryAfter pauses for Retry-After on HTTP 429 and then
// retries, on fake time.
func TestAPIClientHonorsRetryAfter(t *testing.T) {
	cassette, err := remotetest.Load(filepath.Join("testdata", "media.json"))
	if err != nil {
		t.Fatal(err)
	}
	doer := &sequenceDoer{limited: 1, inner: cassette}
	fake := clock.NewFake(time.Date(2026, 10, 11, 2, 30, 0, 0, time.UTC))
	api := newAPIClient(remote.NewClient(remote.Config{Doer: doer, Clock: fake}), fake, Endpoint, time.Hour, slog.Default())

	type result struct {
		media *media
		err   error
	}
	done := make(chan result, 1)
	started := fake.Now()
	go func() {
		value, err := api.media(t.Context(), 16498)
		done <- result{value, err}
	}()
	var got result
	for waiting := true; waiting; {
		select {
		case got = <-done:
			waiting = false
		default:
			fake.Advance(time.Second)
			runtime.Gosched()
		}
	}
	if got.err != nil || got.media == nil || got.media.ID != 16498 {
		t.Fatalf("media = %+v, %v", got.media, got.err)
	}
	if elapsed := fake.Now().Sub(started); elapsed < 30*time.Second {
		t.Fatalf("retried after %s of fake time, want at least Retry-After 30s", elapsed)
	}
	if doer.requests != 2 {
		t.Fatalf("requests = %d, want 2", doer.requests)
	}
	// A cached series costs no request.
	if value, err := api.media(t.Context(), 16498); err != nil || value == nil || doer.requests != 2 {
		t.Fatalf("cached media = %v, %v after %d requests", value, err, doer.requests)
	}
}

func TestCleanDescription(t *testing.T) {
	got := cleanDescription("First line.<br><br>\n<i>Second</i> &amp; third.<br>\n<br>\n<br>\n(Source: AniList)")
	want := "First line.\n\nSecond & third.\n\n(Source: AniList)"
	if got != want {
		t.Fatalf("cleanDescription = %q, want %q", got, want)
	}
}

func providerIDs(entries ...string) item.ProviderIDs {
	var ids item.ProviderIDs
	for index := 0; index < len(entries); index += 2 {
		ids.Set(entries[index], entries[index+1])
	}
	return ids
}
