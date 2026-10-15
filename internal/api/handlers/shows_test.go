// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestShowRoutesUseDedicatedOperationsAndDefaultAuthorization(t *testing.T) {
	rt := router.New()
	handlers.RegisterShows(rt, &showsReader{}, &showsBuilder{}, &showsNextUpReader{}, &showsUserResolver{}, clock.NewReal())
	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Shows/{seriesId}/Episodes", OperationID: "GetEpisodes", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/{seriesId}/Seasons", OperationID: "GetSeasons", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/NextUp", OperationID: "GetNextUp", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		got, found := routeByOperation(rt.Routes(), want.OperationID)
		if !found || got != want {
			t.Fatalf("route %q = %#v, found %t, want %#v", want.OperationID, got, found, want)
		}
	}
}

func TestShowSeasonsValidatesSeriesThenSelectsAndProjectsFilteredAdjacency(t *testing.T) {
	series, before, current, after := showsID(t, 1), showsID(t, 2), showsID(t, 3), showsID(t, 4)
	reader := &showsReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: series, Kind: item.KindSeries}}},
		{Items: []item.Item{{ID: before, Kind: item.KindSeason}, {ID: current, Kind: item.KindSeason}, {ID: after, Kind: item.KindSeason}}},
	}}
	builder := &showsBuilder{items: []dto.BaseItemDto{{ID: before}, {ID: current}, {ID: after}}}
	rt := router.New()
	handlers.RegisterShows(rt, reader, builder, nil, nil, clock.NewReal())

	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Shows/"+series.Dashed()+"/Seasons?userId="+showsID(t, 5).Dashed()+"&isMissing=false&isSpecialSeason=true&adjacentTo="+current.Dashed()+"&fields=ParentId,AlternateMediaSources,people&enableImages=true&enableImageTypes=Primary", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET Seasons status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if len(reader.queries) != 2 || len(builder.values) != 1 || !reflect.DeepEqual(builder.values[0], []item.Item{{ID: before, Kind: item.KindSeason}, {ID: current, Kind: item.KindSeason}, {ID: after, Kind: item.KindSeason}}) {
		t.Fatalf("reader/build calls = %d/%d values=%#v, want validation + filtered seasons", len(reader.queries), len(builder.values), builder.values)
	}
	selection := reader.queries[1]
	if selection.ParentID == nil || *selection.ParentID != series || !reflect.DeepEqual(selection.IncludeItemTypes, []item.Kind{item.KindSeason}) || selection.IsMissing == nil || *selection.IsMissing || selection.IsSpecialSeason == nil || !*selection.IsSpecialSeason || !reflect.DeepEqual(selection.DtoOptions.Fields, []string{"ParentId", "People"}) || !selection.DtoOptions.EnableImages {
		t.Fatalf("season selection = %#v", selection)
	}
	var body dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode seasons: %v", err)
	}
	if body.TotalRecordCount != 3 || len(body.Items) != 3 {
		t.Fatalf("season page = %#v, want adjacency page with three items", body)
	}
}

func TestShowEpisodesCoverSeasonPrecedenceNumberAndAllEpisodesBranches(t *testing.T) {
	series, season, first, second, third := showsID(t, 10), showsID(t, 11), showsID(t, 12), showsID(t, 13), showsID(t, 14)
	index := 2
	for _, tc := range []struct {
		name    string
		path    string
		results []store.ItemQueryResult
		check   func(t *testing.T, queries []store.ItemQuery)
	}{
		{
			name: "season id takes precedence over route series and season number",
			path: "/Shows/" + series.Dashed() + "/Episodes?seasonId=" + season.Dashed() + "&season=99",
			results: []store.ItemQueryResult{
				{Items: []item.Item{{ID: season, Kind: item.KindSeason}}},
				{Items: []item.Item{{ID: first, Kind: item.KindEpisode}, {ID: second, Kind: item.KindEpisode}}},
			},
			check: func(t *testing.T, queries []store.ItemQuery) {
				t.Helper()
				if len(queries) != 2 || queries[0].ItemIDs[0] != season || queries[1].ParentID != nil || queries[1].EpisodesOfSeasonID == nil || *queries[1].EpisodesOfSeasonID != season {
					t.Fatalf("season-id query sequence = %#v", queries)
				}
			},
		},
		{
			name: "season number selects matching season before episodes",
			path: "/Shows/" + series.Dashed() + "/Episodes?season=2",
			results: []store.ItemQueryResult{
				{Items: []item.Item{{ID: series, Kind: item.KindSeries}}},
				{Items: []item.Item{{ID: season, Kind: item.KindSeason, IndexNumber: &index}}},
				{Items: []item.Item{{ID: first, Kind: item.KindEpisode}}},
			},
			check: func(t *testing.T, queries []store.ItemQuery) {
				t.Helper()
				if len(queries) != 3 || queries[2].ParentID != nil || queries[2].EpisodesOfSeasonID == nil || *queries[2].EpisodesOfSeasonID != season {
					t.Fatalf("season-number query sequence = %#v", queries)
				}
			},
		},
		{
			name: "all episodes orders filters start cursor adjacency then pages",
			path: "/Shows/" + series.Dashed() + "/Episodes?startItemId=" + second.Dashed() + "&adjacentTo=" + second.Dashed() + "&startIndex=1&limit=1",
			results: []store.ItemQueryResult{
				{Items: []item.Item{{ID: series, Kind: item.KindSeries}}},
				{Items: []item.Item{{ID: first, Kind: item.KindEpisode}, {ID: second, Kind: item.KindEpisode}, {ID: third, Kind: item.KindEpisode}}},
				{Items: []item.Item{{ID: second, Kind: item.KindEpisode}}},
			},
			check: func(t *testing.T, queries []store.ItemQuery) {
				t.Helper()
				if len(queries) != 3 || !reflect.DeepEqual(queries[1].AncestorIDs, []dto.UUID{series}) || queries[1].StartIndex != nil || queries[1].Limit != nil || !reflect.DeepEqual(queries[1].SortBy, []store.ItemSortBy{store.ItemSortByParentIndexNumber, store.ItemSortByIndexNumber}) {
					t.Fatalf("all-episodes selection = %#v", queries)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &showsReader{results: tc.results}
			builder := &showsBuilder{items: []dto.BaseItemDto{{ID: first}, {ID: second}, {ID: third}}}
			rt := router.New()
			handlers.RegisterShows(rt, reader, builder, nil, nil, clock.NewReal())
			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", tc.path, record.Code, record.Body.String())
			}
			tc.check(t, reader.queries)
		})
	}
}

func TestShowNextUpMapsSourceDefaultsAndProjectsStoreOrder(t *testing.T) {
	user, series, first, second := showsID(t, 30), showsID(t, 31), showsID(t, 32), showsID(t, 33)
	cutoff := "2025-01-03T00:00:00Z"
	nextUp := &showsNextUpReader{result: store.NextUpResult{Items: []item.Item{{ID: first, Kind: item.KindEpisode}, {ID: second, Kind: item.KindEpisode}}, TotalRecordCount: 7}}
	builder := &showsBuilder{items: []dto.BaseItemDto{{ID: first}, {ID: second}}}
	rt := router.New()
	handlers.RegisterShows(rt, &showsReader{}, builder, nextUp, &showsUserResolver{}, clock.NewReal())
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Shows/NextUp?userId="+user.Dashed()+"&seriesId="+series.Dashed()+"&startIndex=2&limit=1&nextUpDateCutoff="+cutoff+"&enableResumable=false&enableRewatching=true&fields=ParentId,AlternateMediaSources,people", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET NextUp status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if len(nextUp.queries) != 1 {
		t.Fatalf("next up reads = %d, want 1", len(nextUp.queries))
	}
	query := nextUp.queries[0]
	if query.UserID != user || query.User == nil || query.User.ID != user || query.SeriesID == nil || *query.SeriesID != series || query.StartIndex == nil || *query.StartIndex != 2 || query.Limit == nil || *query.Limit != 1 || query.NextUpDateCutoff == nil || query.NextUpDateCutoff.Format(time.RFC3339) != cutoff || query.EnableResumable || !query.EnableRewatching || !query.EnableTotalRecordCount {
		t.Fatalf("NextUp request = %#v", query)
	}
	if len(builder.values) != 1 || !reflect.DeepEqual(builder.values[0], nextUp.result.Items) {
		t.Fatalf("NextUp projection values = %#v, want store order %#v", builder.values, nextUp.result.Items)
	}
	var body dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode NextUp response: %v", err)
	}
	if body.TotalRecordCount != 7 || body.StartIndex != 2 {
		t.Fatalf("NextUp response page = %#v", body)
	}
}

// TestShowNextUpPassesFullTargetPolicyToCandidateSelection establishes the
// route-to-store access fact required for NextUp to exclude candidates the
// selected user cannot read. Episodes themselves cannot carry playlist-only
// OpenAccess facts, so this test pins forwarding the source user policy.
func TestShowNextUpPassesFullTargetPolicyToCandidateSelection(t *testing.T) {
	target := store.User{ID: showsID(t, 41), Policy: []byte(`{"IsAdministrator":false,"EnableAllFolders":false,"EnabledFolders":["00000000-0000-0000-0000-000000000042"]}`)}
	watched, candidate := showsID(t, 43), showsID(t, 44)
	nextUp := &showsNextUpReader{result: store.NextUpResult{Items: []item.Item{{ID: watched, Kind: item.KindEpisode}, {ID: candidate, Kind: item.KindEpisode}}}}
	builder := &showsBuilder{items: []dto.BaseItemDto{{ID: watched}, {ID: candidate}}}
	rt := router.New()
	handlers.RegisterShows(rt, &showsReader{}, builder, nextUp, &showsUserResolver{user: target}, clock.NewReal())

	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Shows/NextUp?userId="+target.ID.Dashed(), nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET NextUp status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if len(nextUp.queries) != 1 || nextUp.queries[0].UserID != target.ID || nextUp.queries[0].User == nil || !reflect.DeepEqual(*nextUp.queries[0].User, target) {
		t.Fatalf("NextUp candidate access query = %#v, want full target user %#v", nextUp.queries, target)
	}
	if len(builder.values) != 1 || !reflect.DeepEqual(builder.values[0], nextUp.result.Items) {
		t.Fatalf("NextUp candidate projection = %#v, want selected candidates %#v", builder.values, nextUp.result.Items)
	}
}

// TestShowNextUpAcceptsBothDateCutoffShapes pins the two nextUpDateCutoff
// spellings real clients send. jellyfin-web sends a date-only value and
// Swiftfin sends a full RFC 3339 timestamp; a stdlib time.Time on the query
// struct accepts only the second and answered the first with 400, which broke
// the jellyfin-web home screen. Both must bind, and both must
// reach the store as the same instant.
func TestShowNextUpAcceptsBothDateCutoffShapes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		cutoff string
		want   time.Time
	}{
		{name: "date only (jellyfin-web)", cutoff: "2025-08-12", want: time.Date(2025, 8, 12, 0, 0, 0, 0, time.UTC)},
		{name: "rfc3339 (Swiftfin)", cutoff: "2025-08-10T17:40:57Z", want: time.Date(2025, 8, 10, 17, 40, 57, 0, time.UTC)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			user := store.User{ID: showsID(t, 41)}
			nextUp := &showsNextUpReader{}
			rt := router.New()
			handlers.RegisterShows(rt, &showsReader{}, &showsBuilder{}, nextUp, &showsUserResolver{user: user}, clock.NewReal())

			record := httptest.NewRecorder()
			target := "/Shows/NextUp?userId=" + user.ID.Dashed() + "&nextUpDateCutoff=" + url.QueryEscape(testCase.cutoff)
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, target, nil))

			if record.Code != http.StatusOK {
				t.Fatalf("cutoff %q status = %d, want 200; body=%s", testCase.cutoff, record.Code, record.Body.String())
			}
			if len(nextUp.queries) != 1 {
				t.Fatalf("cutoff %q reached the store %d times, want 1", testCase.cutoff, len(nextUp.queries))
			}
			got := nextUp.queries[0].NextUpDateCutoff
			if got == nil {
				t.Fatalf("cutoff %q bound to nil", testCase.cutoff)
			}
			if !got.Equal(testCase.want) {
				t.Fatalf("cutoff %q bound to %s, want %s", testCase.cutoff, got.UTC(), testCase.want)
			}
		})
	}
}

func TestShowNextUpReturnsNotFoundBeforeQueryingUnknownUser(t *testing.T) {
	user := showsID(t, 40)
	nextUp := &showsNextUpReader{}
	rt := router.New()
	handlers.RegisterShows(rt, &showsReader{}, &showsBuilder{}, nextUp, &showsUserResolver{err: store.ErrNotFound}, clock.NewReal())
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Shows/NextUp?userId="+user.Dashed(), nil))
	if record.Code != http.StatusNotFound || len(nextUp.queries) != 0 {
		t.Fatalf("unknown-user NextUp status/queries = %d/%d, want 404/0", record.Code, len(nextUp.queries))
	}
}

func TestShowRoutesMapBindingLookupReaderAndProjectionFailures(t *testing.T) {
	series := showsID(t, 20)
	for _, tc := range []struct {
		name    string
		path    string
		reader  *showsReader
		builder *showsBuilder
		status  int
	}{
		{name: "malformed route id", path: "/Shows/not-a-guid/Episodes", reader: &showsReader{}, builder: &showsBuilder{}, status: http.StatusBadRequest},
		{name: "malformed query", path: "/Shows/" + series.Dashed() + "/Seasons?isMissing=no", reader: &showsReader{}, builder: &showsBuilder{}, status: http.StatusBadRequest},
		{name: "missing series", path: "/Shows/" + series.Dashed() + "/Seasons", reader: &showsReader{results: []store.ItemQueryResult{{}}}, builder: &showsBuilder{}, status: http.StatusNotFound},
		{name: "lookup reader failure", path: "/Shows/" + series.Dashed() + "/Episodes", reader: &showsReader{errAt: 0, err: errors.New("unavailable")}, builder: &showsBuilder{}, status: http.StatusInternalServerError},
		{name: "projection failure", path: "/Shows/" + series.Dashed() + "/Seasons", reader: &showsReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: series, Kind: item.KindSeries}}}, {}}}, builder: &showsBuilder{err: errors.New("projection unavailable")}, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterShows(rt, tc.reader, tc.builder, nil, nil, clock.NewReal())
			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if record.Code != tc.status {
				t.Fatalf("GET %s status = %d, want %d; body=%s", tc.path, record.Code, tc.status, record.Body.String())
			}
		})
	}
}

type showsReader struct {
	results []store.ItemQueryResult
	queries []store.ItemQuery
	errAt   int
	err     error
}

func (r *showsReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	r.queries = append(r.queries, query)
	if r.err != nil && len(r.queries)-1 == r.errAt {
		return store.ItemQueryResult{}, r.err
	}
	index := len(r.queries) - 1
	if index >= len(r.results) {
		return store.ItemQueryResult{}, nil
	}
	return r.results[index], nil
}

type showsNextUpReader struct {
	result  store.NextUpResult
	err     error
	queries []store.NextUpQuery
}

func (r *showsNextUpReader) NextUp(_ context.Context, query store.NextUpQuery) (store.NextUpResult, error) {
	r.queries = append(r.queries, query)
	return r.result, r.err
}

type showsUserResolver struct {
	err  error
	user store.User
}

func (r *showsUserResolver) Get(_ context.Context, id dto.UUID) (store.User, error) {
	if r.user.ID.IsNil() {
		r.user.ID = id
	}
	return r.user, r.err
}

type showsBuilder struct {
	items  []dto.BaseItemDto
	values [][]item.Item
	err    error
}

func (b *showsBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	b.values = append(b.values, append([]item.Item(nil), values...))
	return dto.QueryResult[dto.BaseItemDto]{Items: b.items}, b.err
}

func (b *showsBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return b.Build(ctx, values, options, userID)
}

func showsID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse fixture ID: %v", err)
	}
	return id
}
