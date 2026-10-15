// SPDX-License-Identifier: GPL-2.0-only

package tmdb_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

const seasonsCassette = "testdata/breaking-bad-seasons.json"

// recordingDoer records every request path before it delegates.
type recordingDoer struct {
	inner remote.Doer
	mu    sync.Mutex
	paths []string
}

func (doer *recordingDoer) Do(request *http.Request) (*http.Response, error) {
	doer.mu.Lock()
	doer.paths = append(doer.paths, request.URL.Path)
	doer.mu.Unlock()
	return doer.inner.Do(request)
}

// apiPaths returns the recorded paths without the configuration request.
func (doer *recordingDoer) apiPaths() []string {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	var out []string
	for _, path := range doer.paths {
		if path != "/3/configuration" {
			out = append(out, path)
		}
	}
	return out
}

type failingDoer struct{ err error }

func (doer failingDoer) Do(*http.Request) (*http.Response, error) { return nil, doer.err }

func newTVProvider(t *testing.T, doer remote.Doer, configure func(*tmdb.Config)) *tmdb.Provider {
	t.Helper()
	config := tmdb.DefaultConfig()
	config.APIKey = "test-key"
	if configure != nil {
		configure(&config)
	}
	return tmdb.NewProvider(tmdb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: doer, CacheEntries: 32}),
		Config: config,
	})
}

func recordingCassette(t *testing.T) *recordingDoer {
	t.Helper()
	cassette, err := remotetest.Load(seasonsCassette)
	if err != nil {
		t.Fatalf("load cassette: %v", err)
	}
	return &recordingDoer{inner: cassette}
}

func seriesAncestor(tmdbID, displayOrder string) library.RefreshAncestor {
	series := item.Item{Kind: item.KindSeries, Name: stringPtr("Breaking Bad")}
	if tmdbID != "" {
		series.ProviderIDs.Set("Tmdb", tmdbID)
	}
	if displayOrder != "" {
		series.Extra.Series = &item.SeriesExtra{DisplayOrder: &displayOrder}
	}
	return library.NewRefreshAncestor(&series)
}

func tvRefreshContext(ancestors ...library.RefreshAncestor) library.RefreshContext {
	refresh := movieRefreshContext()
	refresh.Ancestors = ancestors
	return refresh
}

func seasonAncestor(number int) library.RefreshAncestor {
	season := item.Item{Kind: item.KindSeason, IndexNumber: intPtr(number)}
	return library.NewRefreshAncestor(&season)
}

func TestProviderSeasonMapsMetadataFromCassette(t *testing.T) {
	doer := recordingCassette(t)
	provider := newTVProvider(t, doer, nil)
	target := item.Item{Kind: item.KindSeason, Name: stringPtr("Season 1"), IndexNumber: intPtr(1)}

	handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target)
	if err != nil || !handled {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", handled, err)
	}
	if got, want := doer.apiPaths(), []string{"/3/tv/1396/season/1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if target.Overview == nil || *target.Overview != "High school chemistry teacher Walter White's life is suddenly transformed." {
		t.Errorf("Overview = %v", target.Overview)
	}
	if target.PremiereDate == nil || target.PremiereDate.Format("2006-01-02") != "2008-01-20" {
		t.Errorf("PremiereDate = %v, want 2008-01-20", target.PremiereDate)
	}
	if target.ProductionYear == nil || *target.ProductionYear != 2008 {
		t.Errorf("ProductionYear = %v, want 2008", target.ProductionYear)
	}
	if target.IndexNumber == nil || *target.IndexNumber != 1 {
		t.Errorf("IndexNumber = %v, want 1", target.IndexNumber)
	}
	// ImportSeasonName defaults to false: the local name stays.
	if target.Name == nil || *target.Name != "Season 1" {
		t.Errorf("Name = %v, want the local Season 1", target.Name)
	}
	if id, ok := target.ProviderIDs.Get("Tvdb"); !ok || id != "30272" {
		t.Errorf("Tvdb = %q, want 30272", id)
	}
	if _, ok := target.ProviderIDs.Get("Tmdb"); ok {
		t.Error("season gained a Tmdb id; upstream sets only Tvdb")
	}
	// Cast by billing order, then the wanted crew kind; the grip is not wanted.
	var got []string
	for _, person := range target.People {
		got = append(got, person.Type.String()+":"+person.Name)
	}
	if want := []string{"Actor:Aaron Paul", "Actor:Bryan Cranston", "Producer:Vince Gilligan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("People = %v, want %v", got, want)
	}
	assertImageDimensions(t, target.ImageInfos, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/season1.jpg", 1000, 1500)
}

func TestProviderSeasonImportSeasonName(t *testing.T) {
	provider := newTVProvider(t, recordingCassette(t), func(config *tmdb.Config) { config.ImportSeasonName = true })
	target := item.Item{Kind: item.KindSeason, Name: stringPtr("Season 01"), IndexNumber: intPtr(1)}
	if _, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target); err != nil {
		t.Fatal(err)
	}
	if target.Name == nil || *target.Name != "Season 1" {
		t.Errorf("Name = %v, want the TMDb Season 1", target.Name)
	}
}

func TestProviderSeasonAndEpisodeSkipWithoutParentOrNumber(t *testing.T) {
	cases := []struct {
		name    string
		target  item.Item
		refresh library.RefreshContext
	}{
		{"season without series", item.Item{Kind: item.KindSeason, IndexNumber: intPtr(1)}, tvRefreshContext()},
		{"season with series lacking Tmdb", item.Item{Kind: item.KindSeason, IndexNumber: intPtr(1)}, tvRefreshContext(seriesAncestor("", ""))},
		{"season with invalid Tmdb", item.Item{Kind: item.KindSeason, IndexNumber: intPtr(1)}, tvRefreshContext(seriesAncestor("abc", ""))},
		{"season with zero Tmdb", item.Item{Kind: item.KindSeason, IndexNumber: intPtr(1)}, tvRefreshContext(seriesAncestor("0", ""))},
		{"season without number", item.Item{Kind: item.KindSeason}, tvRefreshContext(seriesAncestor("1396", ""))},
		{"episode without series", item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(1)}, tvRefreshContext(seasonAncestor(1))},
		{"episode with invalid Tmdb", item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(1)}, tvRefreshContext(seasonAncestor(1), seriesAncestor("-4", ""))},
		{"episode without number", item.Item{Kind: item.KindEpisode, ParentIndexNumber: intPtr(1)}, tvRefreshContext(seasonAncestor(1), seriesAncestor("1396", ""))},
		{"missing episode", item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(1), IsVirtualItem: true}, tvRefreshContext(seriesAncestor("1396", ""))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := recordingCassette(t)
			provider := newTVProvider(t, doer, nil)
			target := tc.target
			handled, err := provider.Refresh(t.Context(), tc.refresh, &target)
			if handled || err != nil {
				t.Fatalf("Refresh = (%v, %v), want (false, nil)", handled, err)
			}
			if paths := doer.apiPaths(); len(paths) != 0 {
				t.Errorf("requests = %v, want none", paths)
			}
		})
	}
}

func TestProviderEpisodeMapsMetadataFromCassette(t *testing.T) {
	doer := recordingCassette(t)
	provider := newTVProvider(t, doer, nil)
	target := item.Item{Kind: item.KindEpisode, Name: stringPtr("S01E01"), IndexNumber: intPtr(1), ParentIndexNumber: intPtr(1)}

	handled, err := provider.Refresh(t.Context(), tvRefreshContext(seasonAncestor(1), seriesAncestor("1396", "")), &target)
	if err != nil || !handled {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", handled, err)
	}
	if got, want := doer.apiPaths(), []string{"/3/tv/1396/season/1/episode/1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if target.Name == nil || *target.Name != "Pilot" {
		t.Errorf("Name = %v, want Pilot", target.Name)
	}
	if target.Overview == nil || *target.Overview != "Walter White is diagnosed with cancer." {
		t.Errorf("Overview = %v", target.Overview)
	}
	if target.PremiereDate == nil || target.PremiereDate.Format("2006-01-02") != "2008-01-20" {
		t.Errorf("PremiereDate = %v", target.PremiereDate)
	}
	if target.ProductionYear == nil || *target.ProductionYear != 2008 {
		t.Errorf("ProductionYear = %v", target.ProductionYear)
	}
	if target.CommunityRating == nil || *target.CommunityRating != float32(8.2) {
		t.Errorf("CommunityRating = %v, want 8.2", target.CommunityRating)
	}
	for key, want := range map[string]string{"Tvdb": "349232", "Imdb": "tt0959621", "TvRage": "637041"} {
		if got, ok := target.ProviderIDs.Get(key); !ok || got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if len(target.Extra.RemoteTrailers) != 1 || *target.Extra.RemoteTrailers[0].URL != "https://www.youtube.com/watch?v=pilotTrailer" {
		t.Errorf("RemoteTrailers = %+v, want the one YouTube trailer", target.Extra.RemoteTrailers)
	}
	// Cast, then guest stars by billing order, then wanted crew.
	var got []string
	for _, person := range target.People {
		got = append(got, person.Type.String()+":"+person.Name)
	}
	want := []string{"Actor:Bryan Cranston", "GuestStar:Max Arciniega", "GuestStar:John Koyama", "Writer:Vince Gilligan", "Director:Vince Gilligan"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("People = %v, want %v", got, want)
	}
	if guest := target.People[1]; guest.Role == nil || *guest.Role != "Krazy-8" || guest.SortOrder == nil || *guest.SortOrder != 0 {
		t.Errorf("guest star = %+v, want Krazy-8 at order 0", guest)
	}
	assertImageDimensions(t, target.ImageInfos, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/s1e1.jpg", 1920, 1080)
}

func TestProviderEpisodeDefaultsToSeasonOne(t *testing.T) {
	doer := recordingCassette(t)
	provider := newTVProvider(t, doer, nil)
	target := item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(2)}

	if handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target); err != nil || !handled {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", handled, err)
	}
	if got, want := doer.apiPaths(), []string{"/3/tv/1396/season/1/episode/2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if target.ParentIndexNumber == nil || *target.ParentIndexNumber != 1 {
		t.Errorf("ParentIndexNumber = %v, want 1", target.ParentIndexNumber)
	}
}

func TestProviderEpisodeRangeRequestsEveryNumberAndJoins(t *testing.T) {
	doer := recordingCassette(t)
	provider := newTVProvider(t, doer, nil)
	target := item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(1), ParentIndexNumber: intPtr(1)}
	target.Extra.Episode = &item.EpisodeExtra{IndexNumberEnd: intPtr(3)}

	if handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target); err != nil || !handled {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", handled, err)
	}
	want := []string{"/3/tv/1396/season/1/episode/1", "/3/tv/1396/season/1/episode/2", "/3/tv/1396/season/1/episode/3"}
	if got := doer.apiPaths(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if target.Name == nil || *target.Name != "Pilot / Cat's in the Bag... / ...And the Bag's in the River" {
		t.Errorf("Name = %v", target.Name)
	}
	if target.Overview == nil || *target.Overview != "Walter White is diagnosed with cancer. / Walt and Jesse clean up. / Walt decides." {
		t.Errorf("Overview = %v", target.Overview)
	}
	// The first episode supplies every other field.
	if id, _ := target.ProviderIDs.Get("Tvdb"); id != "349232" {
		t.Errorf("Tvdb = %q, want the first episode's 349232", id)
	}
	if target.Extra.Episode.IndexNumberEnd == nil || *target.Extra.Episode.IndexNumberEnd != 3 {
		t.Errorf("IndexNumberEnd = %v, want 3", target.Extra.Episode.IndexNumberEnd)
	}
}

func TestProviderEpisodeNotFoundIsUnhandled(t *testing.T) {
	provider := newTVProvider(t, recordingCassette(t), nil)
	target := item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(9), ParentIndexNumber: intPtr(1)}
	handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target)
	if handled || err != nil {
		t.Fatalf("Refresh = (%v, %v), want (false, nil)", handled, err)
	}
	if target.Name != nil {
		t.Errorf("Name = %v, want unchanged nil", *target.Name)
	}
}

func TestProviderEpisodeDisplayOrderUsesEpisodeGroup(t *testing.T) {
	doer := recordingCassette(t)
	provider := newTVProvider(t, doer, nil)
	// Absolute order: group order 1, position 8 is aired S02E01.
	target := item.Item{Kind: item.KindEpisode, IndexNumber: intPtr(8), ParentIndexNumber: intPtr(1)}

	if handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "absolute")), &target); err != nil || !handled {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", handled, err)
	}
	want := []string{"/3/tv/1396/episode_groups", "/3/tv/episode_group/5d0e6ec5b29d1d001e93e0b1", "/3/tv/1396/season/2/episode/1"}
	if got := doer.apiPaths(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if target.Name == nil || *target.Name != "Seven Thirty-Seven" {
		t.Errorf("Name = %v, want Seven Thirty-Seven", target.Name)
	}
	// The local numbers stay in display order.
	if *target.IndexNumber != 8 || *target.ParentIndexNumber != 1 {
		t.Errorf("numbers = S%dE%d, want S1E8", *target.ParentIndexNumber, *target.IndexNumber)
	}
}

// The remote client replaces transport errors with a credential-safe error, so
// the test asserts that an error reaches the caller, not its identity.
func TestProviderSeasonAndEpisodeReturnRequestErrors(t *testing.T) {
	transport := errors.New("connection refused")
	for _, target := range []item.Item{
		{Kind: item.KindSeason, IndexNumber: intPtr(1)},
		{Kind: item.KindEpisode, IndexNumber: intPtr(1), ParentIndexNumber: intPtr(1)},
	} {
		provider := newTVProvider(t, failingDoer{err: transport}, nil)
		handled, err := provider.Refresh(t.Context(), tvRefreshContext(seriesAncestor("1396", "")), &target)
		if handled || err == nil {
			t.Errorf("%v: Refresh = (%v, %v), want (false, an error)", target.Kind, handled, err)
		}
	}
}

func TestProviderSeasonAndEpisodeReturnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, target := range []item.Item{
		{Kind: item.KindSeason, IndexNumber: intPtr(1)},
		{Kind: item.KindEpisode, IndexNumber: intPtr(1), ParentIndexNumber: intPtr(1)},
	} {
		doer := recordingCassette(t)
		provider := newTVProvider(t, doer, nil)
		handled, err := provider.Refresh(ctx, tvRefreshContext(seriesAncestor("1396", "")), &target)
		if handled || !errors.Is(err, context.Canceled) {
			t.Errorf("%v: Refresh = (%v, %v), want (false, context.Canceled)", target.Kind, handled, err)
		}
		if paths := doer.apiPaths(); len(paths) != 0 {
			t.Errorf("%v: requests = %v, want none", target.Kind, paths)
		}
	}
}
