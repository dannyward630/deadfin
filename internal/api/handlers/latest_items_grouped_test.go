// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"fmt"
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
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestLatestItemsGroupsBeforeLimit pins UserViewManager.GetItemsForLatestItems
// for a tvshows library: the limit applies to series groups
// (BaseItemRepository.GetLatestTvShowItems), not to a limit*2 page of
// episodes. The newest 40 episodes belong to 4 of the 20 series, so grouping
// a 32-episode page returns only 4 entries.
func TestLatestItemsGroupsBeforeLimit(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	identity := store.NewIdentity(stores)
	users := user.New(identity, clock.NewFake(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)), bytes.NewReader(nil))
	viewer := latestItemsExcludesUser(t, identity, 10, "viewer", []byte(`{"IsAdministrator":true}`), []byte(`{}`))
	items := store.NewItemStore(stores)

	next := 1000
	id := func() dto.UUID { next++; return latestItemsExcludesUUID(t, next) }
	text := func(value string) *string { return &value }
	number := func(value int) *int { return &value }
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) *time.Time { value := base.Add(time.Duration(minutes) * time.Minute); return &value }
	shows := item.CollectionTypeTVShows

	aggregate, library := id(), id()
	values := []item.Item{
		{ID: aggregate, Kind: item.KindAggregateFolder, Name: text("root"), IsFolder: true},
		{ID: library, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: text("Shows"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &shows}}},
	}
	var want []dto.UUID
	for index := 0; index < 20; index++ {
		seriesID, seasonID := id(), id()
		name := fmt.Sprintf("Series %02d", index)
		if index < 16 {
			want = append(want, seriesID)
		}
		values = append(values,
			item.Item{ID: seriesID, Kind: item.KindSeries, ParentID: &library, Name: text(name), IsFolder: true, PresentationUniqueKey: text(name), DateCreated: at(0)},
			item.Item{ID: seasonID, Kind: item.KindSeason, ParentID: &seriesID, Name: text("Season 1"), IsFolder: true, IndexNumber: number(1),
				SeriesID: &seriesID, SeriesName: text(name), DateCreated: at(0)},
		)
		episodes, newest := 2, 10_000-index*100
		if index < 4 {
			episodes, newest = 10, 20_000-index*100
		}
		for episode := 0; episode < episodes; episode++ {
			values = append(values, item.Item{ID: id(), Kind: item.KindEpisode, ParentID: &seasonID, Name: text(name + " episode"),
				IndexNumber: number(episode + 1), ParentIndexNumber: number(1), SeriesID: &seriesID, SeasonID: &seasonID,
				SeriesName: text(name), SeriesPresentationUniqueKey: text(name), DateCreated: at(newest - episode)})
		}
	}
	for _, value := range values {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert fixture item %s: %v", value.ID, err)
		}
	}

	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: viewer.ID}},
		latestItemsExcludesEvaluator{},
		users,
	)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, nil), clock.NewReal(), handlers.ItemServices{Users: users, LatestPreferences: handlers.NewLatestUserPreferences(users)})

	path := "/Users/" + viewer.ID.Dashed() + "/Items/Latest?parentId=" + library.Dashed() + "&limit=16"
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body=%s", path, record.Code, record.Body.String())
	}
	if got := latestItemsExcludesResponseIDs(t, record.Body.Bytes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("GET %s returned %d items %v, want the 16 newest series %v", path, len(got), got, want)
	}
}
