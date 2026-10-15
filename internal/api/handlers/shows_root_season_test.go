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
)

// TestSeasonFolderListsRootFolderEpisodesOfTheSeason proves the season
// episode list over the real store. A physical "Season 2" folder holds one
// episode; the other season 2 episodes are files in the series root.
// Upstream Season.GetEpisodes
// (Series.FilterEpisodesBySeason) lists all of them: the episodes linked to
// the season, the episodes of season number 2, and a special that airs
// before season 2. /Shows/{id}/Episodes and /Items?ParentId=<season> both
// return the set, and the season counts include it.
func TestSeasonFolderListsRootFolderEpisodesOfTheSeason(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	series := *fixture.includedNextUp.SeriesID
	library := *fixture.includedNextUp.TopParentID
	zero, two := 0, 2
	seasonPath, seasonName := "/tv/included-series/Season 2", "Season 2"
	season := item.Item{ID: latestItemsExcludesUUID(t, 70), Kind: item.KindSeason, ParentID: &series, TopParentID: &library, SeriesID: &series, IsFolder: true, Path: &seasonPath, Name: &seasonName, SortName: &seasonName, IndexNumber: &two}
	episode := func(value int, parent dto.UUID, seasonID *dto.UUID, seasonNumber, number int, sortName string) item.Item {
		ep := latestItemsExcludesItem(t, value, item.KindEpisode, &parent, sortName)
		ep.TopParentID, ep.SeriesID, ep.SeasonID = &library, &series, seasonID
		ep.ParentIndexNumber, ep.IndexNumber = &seasonNumber, &number
		return ep
	}
	inFolder := episode(71, season.ID, &season.ID, 2, 25, "002 - 0025 - Mortal Recoil")
	rootFirst := episode(72, series, &season.ID, 2, 1, "002 - 0001 - It Came from the Nightosphere")
	rootSecond := episode(73, series, &season.ID, 2, 2, "002 - 0002 - The Eyes")
	// A root episode with no SeasonId matches by its season number.
	rootNumbered := episode(74, series, nil, 2, 3, "002 - 0003 - Loyalty to the King")
	// A special that airs before season 2 belongs to season 2 while
	// DisplaySpecialsWithinSeasons is on (the default).
	special := episode(75, series, nil, zero, 1, "000 - 0001 - Special")
	special.AirsBeforeSeasonNumber = &two
	for _, value := range []item.Item{season, inFolder, rootFirst, rootSecond, rootNumbered, special} {
		if err := fixture.items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
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
	user := "userId=" + fixture.admin.ID.Dashed()
	get := func(path string) []byte {
		t.Helper()
		record := httptest.NewRecorder()
		routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body=%s", path, record.Code, record.Body.String())
		}
		return record.Body.Bytes()
	}

	// /Shows/{id}/Episodes sorts by ParentIndexNumber, IndexNumber.
	shows := []dto.UUID{special.ID, rootFirst.ID, rootSecond.ID, rootNumbered.ID, inFolder.ID}
	for _, path := range []string{
		"/Shows/" + series.Dashed() + "/Episodes?seasonId=" + season.ID.Dashed() + "&" + user,
		"/Shows/" + series.Dashed() + "/Episodes?season=2&" + user,
	} {
		if ids := latestItemsExcludesResponseIDs(t, get(path)); !reflect.DeepEqual(ids, shows) {
			t.Fatalf("GET %s IDs = %v, want %v", path, ids, shows)
		}
	}

	// /Items?ParentId=<season> sorts by SortName by default.
	items := []dto.UUID{special.ID, rootFirst.ID, rootSecond.ID, rootNumbered.ID, inFolder.ID}
	for _, path := range []string{
		"/Items?ParentId=" + season.ID.Dashed() + "&" + user,
		"/Items?ParentId=" + season.ID.Dashed() + "&Recursive=true&IncludeItemTypes=Episode&" + user,
		"/Users/" + fixture.admin.ID.Dashed() + "/Items?ParentId=" + season.ID.Dashed(),
	} {
		if ids := latestItemsExcludesResponseIDs(t, get(path)); !reflect.DeepEqual(ids, items) {
			t.Fatalf("GET %s IDs = %v, want %v", path, ids, items)
		}
	}
	body := get("/Items?ParentId=" + season.ID.Dashed() + "&StartIndex=1&Limit=2&EnableTotalRecordCount=true&" + user)
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if ids := latestItemsExcludesItemIDs(page.Items); page.TotalRecordCount != 5 || !reflect.DeepEqual(ids, items[1:3]) {
		t.Fatalf("paged season items = %v total %d, want %v total 5; body=%s", ids, page.TotalRecordCount, items[1:3], body)
	}

	body = get("/Shows/" + series.Dashed() + "/Seasons?fields=ChildCount,RecursiveItemCount&" + user)
	var seasons dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(body, &seasons); err != nil {
		t.Fatalf("decode seasons: %v", err)
	}
	if len(seasons.Items) != 1 || seasons.Items[0].ID != season.ID {
		t.Fatalf("Seasons = %s, want season 2", body)
	}
	got := seasons.Items[0]
	if got.ChildCount == nil || *got.ChildCount != 5 || got.RecursiveItemCount == nil || *got.RecursiveItemCount != 5 {
		t.Fatalf("season counts = %v / %v, want 5 / 5; body=%s", got.ChildCount, got.RecursiveItemCount, body)
	}
	if got.UserData == nil || got.UserData.UnplayedItemCount == nil || *got.UserData.UnplayedItemCount != 5 {
		t.Fatalf("season UserData = %+v, want 5 unplayed; body=%s", got.UserData, body)
	}
}
