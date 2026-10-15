// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// countsReader is a test-only ItemReader that counts items by kind.
type countsReader struct {
	items     []item.Item
	favorites map[dto.UUID]bool
	err       error
	calls     int
	seen      []store.ItemQuery
}

func (r *countsReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	r.calls++
	r.seen = append(r.seen, query)
	if r.err != nil {
		return store.ItemQueryResult{}, r.err
	}
	count := 0
	for _, it := range r.items {
		if len(query.IncludeItemTypes) > 0 {
			matched := false
			for _, kind := range query.IncludeItemTypes {
				if it.Kind == kind {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if query.IsVirtualItem != nil && !*query.IsVirtualItem && it.IsVirtualItem {
			continue
		}
		if query.IsFavorite != nil && *query.IsFavorite {
			if !r.favorites[it.ID] {
				continue
			}
		}
		count++
	}
	return store.ItemQueryResult{TotalRecordCount: count}, nil
}

// countsBuilder is a minimal ItemDTOBuilder stub.
type countsBuilder struct{}

func (countsBuilder) Build(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return dto.NewQueryResult[dto.BaseItemDto](), nil
}

func (countsBuilder) BuildDetailed(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return dto.NewQueryResult[dto.BaseItemDto](), nil
}

func TestItemCountsRouteRegistration(t *testing.T) {
	rt := router.New()
	handlers.RegisterItems(rt, &countsReader{}, countsBuilder{}, clock.NewReal())

	got, found := routeByOperation(rt.Routes(), "GetItemCounts")
	if !found {
		t.Fatal("GetItemCounts is not registered")
	}
	wantMethod := http.MethodGet
	wantPattern := "/Items/Counts"
	wantPolicy := string(auth.PolicyDefaultAuthorization)
	if got.Method != wantMethod || got.Pattern != wantPattern || got.Policy != wantPolicy {
		t.Fatalf("GetItemCounts route = {Method: %q, Pattern: %q, Policy: %q}, want {Method: %q, Pattern: %q, Policy: %q}",
			got.Method, got.Pattern, got.Policy, wantMethod, wantPattern, wantPolicy)
	}
	if got.Anonymous {
		t.Fatal("GetItemCounts route must not be anonymous")
	}
}

func TestItemCountsNilReaderReturns503(t *testing.T) {
	rt := router.New()
	handlers.RegisterItems(rt, nil, countsBuilder{}, clock.NewReal())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/Counts", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil reader status = %d, want %d; body: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestItemCountsBuckets(t *testing.T) {
	reader := &countsReader{
		items: []item.Item{
			{ID: dto.UUID{1}, Kind: item.KindMovie},
			{ID: dto.UUID{2}, Kind: item.KindMovie},
			{ID: dto.UUID{3}, Kind: item.KindSeries},
			{ID: dto.UUID{4}, Kind: item.KindEpisode},
			{ID: dto.UUID{5}, Kind: item.KindEpisode},
			{ID: dto.UUID{6}, Kind: item.KindEpisode, IsVirtualItem: true},
			{ID: dto.UUID{7}, Kind: item.KindAudio},
			{ID: dto.UUID{8}, Kind: item.KindMusicAlbum},
			{ID: dto.UUID{9}, Kind: item.KindMusicArtist},
			{ID: dto.UUID{10}, Kind: item.KindMusicVideo},
			{ID: dto.UUID{11}, Kind: item.KindBoxSet},
			{ID: dto.UUID{12}, Kind: item.KindBook},
			{ID: dto.UUID{13}, Kind: item.KindTrailer},
			{ID: dto.UUID{14}, Kind: item.KindLiveTvProgram},
		},
	}
	rt := router.New()
	handlers.RegisterItems(rt, reader, countsBuilder{}, clock.NewReal())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/Counts", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var result map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
	// 13 items, excluding the virtual episode.
	want := map[string]int{
		"MovieCount":      2,
		"SeriesCount":     1,
		"EpisodeCount":    2, // 3 episodes minus 1 virtual
		"ArtistCount":     1,
		"ProgramCount":    1,
		"TrailerCount":    1,
		"SongCount":       1,
		"AlbumCount":      1,
		"MusicVideoCount": 1,
		"BoxSetCount":     1,
		"BookCount":       1,
		"ItemCount":       13, // 14 items minus 1 virtual
	}
	for field, wantVal := range want {
		if result[field] != wantVal {
			t.Errorf("%s = %d, want %d", field, result[field], wantVal)
		}
	}
}

func TestItemCountsIsFavorite(t *testing.T) {
	favMovieID := dto.UUID{1}
	favSeriesID := dto.UUID{2}
	reader := &countsReader{
		items: []item.Item{
			{ID: favMovieID, Kind: item.KindMovie},
			{ID: dto.UUID{3}, Kind: item.KindMovie},
			{ID: favSeriesID, Kind: item.KindSeries},
		},
		favorites: map[dto.UUID]bool{favMovieID: true, favSeriesID: true},
	}
	rt := router.New()
	handlers.RegisterItems(rt, reader, countsBuilder{}, clock.NewReal())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/Counts?isFavorite=true", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var result map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
	if result["MovieCount"] != 1 {
		t.Errorf("MovieCount = %d, want 1 (only favorites)", result["MovieCount"])
	}
	if result["SeriesCount"] != 1 {
		t.Errorf("SeriesCount = %d, want 1 (only favorites)", result["SeriesCount"])
	}
	if result["ItemCount"] != 2 {
		t.Errorf("ItemCount = %d, want 2 (only favorites)", result["ItemCount"])
	}
}

func TestItemCountsDoesNotFallThroughToItemId(t *testing.T) {
	reader := &countsReader{}
	rt := router.New()
	handlers.RegisterItems(rt, reader, countsBuilder{}, clock.NewReal())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/Counts", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Items/Counts status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	// The response must be JSON (200), not a 400 from the item-ID parser.
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestItemCountsNonAdminForeignUserReturns403(t *testing.T) {
	stores, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	fakeClock := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	users := user.New(store.NewIdentity(stores), fakeClock, &p104Entropy{})
	actor, err := users.Create(context.Background(), "Actor", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := users.Create(context.Background(), "Target", "")
	if err != nil {
		t.Fatal(err)
	}

	identities := map[string]auth.Identity{
		"actor": {Authenticated: true, UserID: actor.ID},
	}
	authorizer := middleware.NewAuthorizer(
		stubResolver{identities: identities},
		stubEvaluator{},
		stubAuthorizerUsers{users: users},
	)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItems(rt, &countsReader{}, countsBuilder{}, clock.NewReal(), handlers.ItemServices{Users: users})

	rec := p104Call(rt, http.MethodGet, "/Items/Counts?userId="+target.ID.Dashed(), "", "", "", "actor", p104MediaBrowserMetadata)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin foreign user status = %d, want %d; body: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// Test stubs for the authorizer. These mirror the p104 test helpers but are
// local so the test is self-contained.

type stubResolver struct {
	identities map[string]auth.Identity
}

func (s stubResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	return s.identities[request.Header.Get("X-P104-Actor")], nil
}

type stubEvaluator struct{}

func (stubEvaluator) Evaluate(_ context.Context, _ auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}

type stubAuthorizerUsers struct {
	users *user.Service
}

func (s stubAuthorizerUsers) Get(ctx context.Context, id dto.UUID) (store.User, error) {
	return s.users.Get(ctx, id)
}
