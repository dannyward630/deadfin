// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

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

// TestShowsServeVirtualSeasonAndItsEpisodes proves the public Seasons and
// Episodes routes for a virtual (pathless) season over the real store. Its
// episodes are children of the series and link to it through SeasonId, as
// upstream Season.GetEpisodes returns them, and /Items?ParentId=<season>
// lists the same episodes (Season.GetItemsInternal).
func TestShowsServeVirtualSeasonAndItsEpisodes(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	series := *fixture.includedNextUp.SeriesID
	first, next := latestItemsExcludesUUID(t, 31), latestItemsExcludesUUID(t, 32)
	one, name := 1, "Season 1"
	season := item.Item{ID: latestItemsExcludesUUID(t, 60), Kind: item.KindSeason, ParentID: &series, SeriesID: &series, IsFolder: true, Name: &name, SortName: &name, IndexNumber: &one}
	if err := fixture.items.ApplySeriesSeasons(t.Context(), series, store.SeriesSeasonPlan{
		Create: []item.Item{season},
		Assign: []store.SeriesSeasonAssignment{{EpisodeID: first, SeasonID: season.ID, SeasonName: &name}, {EpisodeID: next, SeasonID: season.ID, SeasonName: &name}},
	}); err != nil {
		t.Fatalf("apply virtual season: %v", err)
	}
	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: fixture.admin.ID}},
		latestItemsExcludesEvaluator{},
		fixture.users,
	)
	routes := router.New(authorizer.Wrap)
	builder := dtobuild.NewBuilder(fixture.items, dtobuild.NewStoreEnricher(fixture.items, nil, nil))
	handlers.RegisterItems(routes, fixture.items, builder, clock.NewReal(), handlers.ItemServices{Users: fixture.users, LatestPreferences: handlers.NewLatestUserPreferences(fixture.users)})
	handlers.RegisterShows(routes, fixture.items, builder, fixture.items, fixture.users, clock.NewReal())
	user := "userId=" + fixture.target.ID.Dashed()

	get := func(path string) []byte {
		t.Helper()
		record := httptest.NewRecorder()
		routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body=%s", path, record.Code, record.Body.String())
		}
		return record.Body.Bytes()
	}

	body := get("/Shows/" + series.Dashed() + "/Seasons?fields=ChildCount&" + user)
	var seasons dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(body, &seasons); err != nil {
		t.Fatalf("decode seasons: %v; body=%s", err, body)
	}
	if len(seasons.Items) != 1 || seasons.Items[0].ID != season.ID {
		t.Fatalf("Seasons = %s, want only the virtual season %s", body, season.ID)
	}
	got := seasons.Items[0]
	if got.LocationType == nil || *got.LocationType != "Virtual" {
		t.Fatalf("season LocationType = %v, want Virtual for a season with no path; body=%s", got.LocationType, body)
	}
	if got.ChildCount == nil || *got.ChildCount != 2 {
		t.Fatalf("season ChildCount = %v, want its 2 episodes; body=%s", got.ChildCount, body)
	}
	if got.UserData == nil || got.UserData.Played || got.UserData.UnplayedItemCount == nil || *got.UserData.UnplayedItemCount != 1 {
		t.Fatalf("season UserData = %+v, want one of two episodes unplayed; body=%s", got.UserData, body)
	}

	want := []dto.UUID{first, next}
	for _, path := range []string{
		"/Shows/" + series.Dashed() + "/Episodes?seasonId=" + season.ID.Dashed() + "&" + user,
		"/Shows/" + series.Dashed() + "/Episodes?season=1&" + user,
		// Season.GetItemsInternal lists a virtual season's episodes too.
		"/Items?ParentId=" + season.ID.Dashed() + "&" + user,
	} {
		body := get(path)
		if ids := latestItemsExcludesResponseIDs(t, body); !reflect.DeepEqual(ids, want) {
			t.Fatalf("GET %s IDs = %v, want %v; body=%s", path, ids, want, body)
		}
		var page dto.QueryResult[dto.BaseItemDto]
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("decode episodes: %v", err)
		}
		for _, episode := range page.Items {
			if episode.SeasonID == nil || *episode.SeasonID != season.ID || episode.SeasonName != name {
				t.Fatalf("episode %s season = %v %v, want %s %q", episode.ID, episode.SeasonID, episode.SeasonName, season.ID, name)
			}
		}
	}
}
