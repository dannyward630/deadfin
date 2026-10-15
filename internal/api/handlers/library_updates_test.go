// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

type countingReporter struct{ reports int }

func (r *countingReporter) Report() { r.reports++ }

type fixedRoots []library.VirtualFolder

func (f fixedRoots) VirtualFolders(context.Context) ([]library.VirtualFolder, error) { return f, nil }

// providerItems answers a provider-id query with one item when the query names
// a known kind/provider/id.
type providerItems struct {
	known map[string]bool
	last  store.ItemQuery
}

func (p *providerItems) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	p.last = query
	for provider, id := range query.HasAnyProviderID {
		if len(query.IncludeItemTypes) == 1 && p.known[query.IncludeItemTypes[0].String()+"/"+provider+"/"+id] {
			return store.ItemQueryResult{Items: []item.Item{{Kind: query.IncludeItemTypes[0]}}, TotalRecordCount: 1}, nil
		}
	}
	return store.ItemQueryResult{}, nil
}

func libraryUpdatesRouter(t *testing.T) (*router.Router, *countingReporter, *providerItems) {
	t.Helper()
	changes := &countingReporter{}
	items := &providerItems{known: map[string]bool{"Series/Tvdb/81189": true, "Movie/Imdb/tt0133093": true, "Movie/Tmdb/603": true}}
	rt := router.New()
	handlers.RegisterLibraryUpdates(rt, changes, fixedRoots{{Name: "Shows", MediaPaths: []string{"/media/tv"}}}, items)
	return rt, changes, items
}

func post(rt http.Handler, path, body string) int {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec.Code
}

func TestPostUpdatedMediaReportsPathsInsideALibrary(t *testing.T) {
	rt, changes, _ := libraryUpdatesRouter(t)
	for _, tc := range []struct {
		name, body string
		status     int
		reports    int
	}{
		{"path in a library", `{"Updates":[{"Path":"/media/tv/Show/Season 01/Show S01E01.mkv","UpdateType":"Created"}]}`, http.StatusNoContent, 1},
		{"path outside every library", `{"Updates":[{"Path":"/elsewhere/x.mkv"}]}`, http.StatusNoContent, 0},
		{"library prefix that is not a parent", `{"Updates":[{"Path":"/media/tvshows/x.mkv"}]}`, http.StatusNoContent, 0},
		{"several updates report once", `{"Updates":[{"Path":"/media/tv/a.mkv"},{"Path":"/media/tv/b.mkv"}]}`, http.StatusNoContent, 1},
		{"no updates", `{"Updates":[]}`, http.StatusNoContent, 0},
		{"null path", `{"Updates":[{"Path":null}]}`, http.StatusBadRequest, 0},
		{"malformed", `{"Updates":`, http.StatusBadRequest, 0},
	} {
		changes.reports = 0
		if got := post(rt, "/Library/Media/Updated", tc.body); got != tc.status || changes.reports != tc.reports {
			t.Errorf("%s: status %d reports %d, want %d and %d", tc.name, got, changes.reports, tc.status, tc.reports)
		}
	}
}

func TestPostUpdatedSeriesAndMoviesReportOnlyKnownItems(t *testing.T) {
	rt, changes, items := libraryUpdatesRouter(t)
	for _, tc := range []struct {
		path    string
		reports int
	}{
		{"/Library/Series/Updated?tvdbId=81189", 1},
		{"/Library/Series/Added?tvdbId=81189", 1},
		{"/Library/Series/Updated?tvdbId=999", 0},
		{"/Library/Series/Updated", 0},
		{"/Library/Movies/Updated?imdbId=tt0133093", 1},
		{"/Library/Movies/Added?tmdbId=603", 1},
		// imdbId wins over tmdbId, as upstream checks it first.
		{"/Library/Movies/Updated?imdbId=tt0000000&tmdbId=603", 0},
		{"/Library/Movies/Updated", 0},
	} {
		changes.reports = 0
		if got := post(rt, tc.path, ""); got != http.StatusNoContent || changes.reports != tc.reports {
			t.Errorf("%s: status %d reports %d, want 204 and %d", tc.path, got, changes.reports, tc.reports)
		}
	}
	if !items.last.Recursive || items.last.Limit == nil || *items.last.Limit != 1 {
		t.Errorf("query = %+v, want a recursive limit-1 lookup", items.last)
	}
}
