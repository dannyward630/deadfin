// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestLatestExclusionRootsConstrainCandidateQueryBeforePaging captures the
// candidate query emitted by the public Latest endpoint. ItemsController first
// derives the user's allowed roots, then LibraryManager applies that scope to
// the repository query before its source candidate limit. A post-page filter
// would return no items here because the three newest rows are excluded.
func TestLatestExclusionRootsConstrainCandidateQueryBeforePaging(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	includedRoot := *fixture.includedMovie.TopParentID
	older, newestExcluded := latestExclusionPagingMovies(t, includedRoot, fixture.excludedLibrary.ID)
	for _, value := range append(older, newestExcluded...) {
		if err := fixture.items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert paging fixture item %s: %v", value.ID, err)
		}
	}

	items := &latestExclusionPagingItems{items: fixture.items}
	routes := latestExclusionPagingRoutes(t, fixture, items, fixture.items)
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?userId="+fixture.target.ID.Dashed()+"&includeItemTypes=Movie&isPlayed=false&groupItems=false&limit=1", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET Latest status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if got, want := latestItemsExcludesResponseIDs(t, record.Body.Bytes()), []dto.UUID{older[0].ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Latest limit=1 IDs = %v, want newest allowed item after exclusions %v; body=%s", got, want, record.Body.String())
	}

	candidate, found := latestExclusionPagingLatestCandidate(items.queries)
	if !found {
		t.Fatalf("Latest candidate query = %#v, want DateCreated-desc source query with limit 2", items.queries)
	}
	if !latestExclusionPagingScopesRoot(candidate, includedRoot) || latestExclusionPagingScopesRoot(candidate, fixture.excludedLibrary.ID) {
		t.Fatalf("Latest source candidate query = %#v, want allowed root %s and no excluded root %s before limit", candidate, includedRoot, fixture.excludedLibrary.ID)
	}
}

// TestNextUpExclusionRootsConstrainCandidateSelectionBeforePaging pins the
// TVSeriesManager behavior where excluded library roots are removed before
// selecting, counting, ordering, and paging NextUp candidates. The two newest
// watched series are excluded, so handler-side filtering after limit would
// yield an empty page and an inflated total instead of the older allowed next
// episode.
func TestNextUpExclusionRootsConstrainCandidateSelectionBeforePaging(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	excludedSeries, excludedWatched, excludedNext := latestItemsExcludesSeries(t, 50, fixture.excludedLibrary.ID, "excluded-paging-series")
	for _, value := range []item.Item{excludedSeries, excludedWatched, excludedNext} {
		if err := fixture.items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert excluded NextUp paging item %s: %v", value.ID, err)
		}
	}

	newest, middle, oldest := time.Date(2026, time.July, 26, 3, 0, 0, 0, time.UTC), time.Date(2026, time.July, 26, 2, 0, 0, 0, time.UTC), time.Date(2026, time.July, 26, 1, 0, 0, 0, time.UTC)
	for _, data := range []store.UserData{
		{UserID: fixture.target.ID, CustomKey: "excluded-first", ItemID: latestItemsExcludesUUID(t, 41), Played: true, LastPlayed: &newest},
		{UserID: fixture.target.ID, CustomKey: "excluded-paging-first", ItemID: excludedWatched.ID, Played: true, LastPlayed: &middle},
		{UserID: fixture.target.ID, CustomKey: "included-first", ItemID: latestItemsExcludesUUID(t, 31), Played: true, LastPlayed: &oldest},
	} {
		if err := fixture.items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert NextUp paging user data %q: %v", data.CustomKey, err)
		}
	}

	nextUp := &latestExclusionPagingNextUp{items: fixture.items}
	routes := latestExclusionPagingRoutes(t, fixture, fixture.items, nextUp)
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Shows/NextUp?userId="+fixture.target.ID.Dashed()+"&limit=1&enableTotalRecordCount=true", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET NextUp status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode NextUp response: %v; body=%s", err, record.Body.String())
	}
	if got, want := latestItemsExcludesItemIDs(page.Items), []dto.UUID{fixture.includedNextUp.ID}; !reflect.DeepEqual(got, want) || page.TotalRecordCount != 1 {
		t.Fatalf("NextUp limit=1 IDs/count = %v/%d, want older allowed %v/1; body=%s", got, page.TotalRecordCount, want, record.Body.String())
	}

	if len(nextUp.queries) != 1 || nextUp.queries[0].Limit == nil || *nextUp.queries[0].Limit != 1 || !reflect.DeepEqual(nextUp.queries[0].ExcludedTopParentIDs, []dto.UUID{fixture.excludedLibrary.ID}) {
		t.Fatalf("NextUp source query = %#v, want excluded root %s and limit 1 before candidate paging", nextUp.queries, fixture.excludedLibrary.ID)
	}
}

func latestExclusionPagingMovies(t *testing.T, includedRoot, excludedRoot dto.UUID) ([]item.Item, []item.Item) {
	t.Helper()
	oldest, older := time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC)
	included := []item.Item{
		latestExclusionPagingMovie(t, 100, includedRoot, "allowed older movie", oldest),
		latestExclusionPagingMovie(t, 101, includedRoot, "allowed newer movie", older),
	}
	excluded := []item.Item{
		latestExclusionPagingMovie(t, 110, excludedRoot, "excluded newest movie one", time.Date(2026, time.July, 26, 3, 0, 0, 0, time.UTC)),
		latestExclusionPagingMovie(t, 111, excludedRoot, "excluded newest movie two", time.Date(2026, time.July, 26, 2, 0, 0, 0, time.UTC)),
		latestExclusionPagingMovie(t, 112, excludedRoot, "excluded newest movie three", time.Date(2026, time.July, 26, 1, 0, 0, 0, time.UTC)),
	}
	return []item.Item{included[1], included[0]}, excluded
}

func latestExclusionPagingMovie(t *testing.T, value int, root dto.UUID, name string, created time.Time) item.Item {
	t.Helper()
	movie := latestItemsExcludesItem(t, value, item.KindMovie, &root, name)
	movie.TopParentID = &root
	movie.DateCreated = &created
	return movie
}

func latestExclusionPagingLatestCandidate(queries []store.ItemQuery) (store.ItemQuery, bool) {
	for _, query := range queries {
		if query.Limit != nil && *query.Limit == 2 && len(query.OrderBy) > 0 && query.OrderBy[0] == (store.ItemOrder{SortBy: store.ItemSortByDateCreated, SortOrder: store.SortOrderDescending}) {
			return query, true
		}
	}
	return store.ItemQuery{}, false
}

func latestExclusionPagingScopesRoot(query store.ItemQuery, root dto.UUID) bool {
	for _, value := range append(append([]dto.UUID(nil), query.AncestorIDs...), query.TopParentIDs...) {
		if value == root {
			return true
		}
	}
	return false
}

type latestExclusionPagingItems struct {
	items   *store.ItemStore
	queries []store.ItemQuery
}

func (reader *latestExclusionPagingItems) Query(ctx context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.Query(ctx, query)
}

type latestExclusionPagingNextUp struct {
	items   *store.ItemStore
	queries []store.NextUpQuery
}

func (reader *latestExclusionPagingNextUp) NextUp(ctx context.Context, query store.NextUpQuery) (store.NextUpResult, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.NextUp(ctx, query)
}

func latestExclusionPagingRoutes(t *testing.T, fixture latestItemsExcludesFixture, items handlers.ItemReader, nextUp handlers.NextUpReader) *router.Router {
	t.Helper()
	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: fixture.admin.ID}},
		latestItemsExcludesEvaluator{},
		fixture.users,
	)
	routes := router.New(authorizer.Wrap)
	builder := dtobuild.NewBuilder(fixture.items, nil)
	handlers.RegisterItems(routes, items, builder, clock.NewReal(), handlers.ItemServices{Users: fixture.users, LatestPreferences: handlers.NewLatestUserPreferences(fixture.users)})
	handlers.RegisterShows(routes, items, builder, nextUp, fixture.users, clock.NewReal())
	return routes
}
