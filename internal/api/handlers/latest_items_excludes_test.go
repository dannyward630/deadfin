// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestLatestItemsExcludesFilterRootRoutesForTargetUser pins the root-scope
// exclusion behavior of ItemsController.cs:953-996, UserViewManager.cs:277-283,
// and TVSeriesManager.cs:69-77. Each route reaches the real ItemStore, whose
// ancestor closure is populated by ItemStore.Upsert rather than fabricated by
// a helper. The administrator's inverse configuration makes a caller/target
// mix-up observable as well as a missing exclusion filter.
func TestLatestItemsExcludesFilterRootRoutesForTargetUser(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	routes := fixture.routes(t)

	for _, test := range []struct {
		name string
		path string
		want []dto.UUID
	}{
		{
			name: "resume uses target excluded library",
			path: "/UserItems/Resume?userId=" + fixture.target.ID.Dashed() + "&includeItemTypes=Movie",
			want: []dto.UUID{fixture.includedMovie.ID},
		},
		{
			name: "latest uses target excluded collection",
			path: "/Items/Latest?userId=" + fixture.target.ID.Dashed() + "&includeItemTypes=Movie&isPlayed=false&groupItems=false",
			want: []dto.UUID{fixture.includedMovie.ID},
		},
		{
			name: "next up uses target excluded library",
			path: "/Shows/NextUp?userId=" + fixture.target.ID.Dashed(),
			want: []dto.UUID{fixture.includedNextUp.ID},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, test.path, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", test.path, record.Code, record.Body.String())
			}
			if got := latestItemsExcludesResponseIDs(t, record.Body.Bytes()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("GET %s item IDs = %v, want %v; body=%s", test.path, got, test.want, record.Body.String())
			}
		})
	}
}

type latestItemsExcludesFixture struct {
	items           *store.ItemStore
	users           *user.Service
	admin           store.User
	target          store.User
	includedMovie   item.Item
	includedNextUp  item.Item
	excludedLibrary item.Item
}

func newLatestItemsExcludesFixture(t *testing.T) latestItemsExcludesFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})

	identity := store.NewIdentity(stores)
	users := user.New(identity, clock.NewFake(time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)), bytes.NewReader(nil))
	includedLibrary := latestItemsExcludesItem(t, 1, item.KindCollectionFolder, nil, "included library")
	includedLibrary.IsFolder = true
	excludedLibrary := latestItemsExcludesItem(t, 2, item.KindCollectionFolder, nil, "excluded collection")
	excludedLibrary.IsFolder = true

	adminConfiguration := latestItemsExcludesConfiguration(t, includedLibrary.ID)
	targetConfiguration := latestItemsExcludesConfiguration(t, excludedLibrary.ID)
	admin := latestItemsExcludesUser(t, identity, 10, "administrator", []byte(`{"IsAdministrator":true}`), adminConfiguration)
	target := latestItemsExcludesUser(t, identity, 11, "target", []byte(`{}`), targetConfiguration)

	includedMovie := latestItemsExcludesItem(t, 20, item.KindMovie, &includedLibrary.ID, "included movie")
	includedMovie.TopParentID = &includedLibrary.ID
	excludedMovie := latestItemsExcludesItem(t, 21, item.KindMovie, &excludedLibrary.ID, "excluded movie")
	excludedMovie.TopParentID = &excludedLibrary.ID

	includedSeries, includedFirst, includedNext := latestItemsExcludesSeries(t, 30, includedLibrary.ID, "included-series")
	excludedSeries, excludedFirst, excludedNext := latestItemsExcludesSeries(t, 40, excludedLibrary.ID, "excluded-series")
	items := store.NewItemStore(stores)
	for _, value := range []item.Item{
		includedLibrary,
		excludedLibrary,
		includedMovie,
		excludedMovie,
		includedSeries,
		includedFirst,
		includedNext,
		excludedSeries,
		excludedFirst,
		excludedNext,
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert fixture item %s: %v", value.ID, err)
		}
	}

	lastPlayed := time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)
	for _, data := range []store.UserData{
		{UserID: target.ID, CustomKey: "included-movie", ItemID: includedMovie.ID, PositionTicks: 1, LastPlayed: &lastPlayed},
		{UserID: target.ID, CustomKey: "excluded-movie", ItemID: excludedMovie.ID, PositionTicks: 1, LastPlayed: &lastPlayed},
		{UserID: target.ID, CustomKey: "included-first", ItemID: includedFirst.ID, Played: true, LastPlayed: &lastPlayed},
		{UserID: target.ID, CustomKey: "excluded-first", ItemID: excludedFirst.ID, Played: true, LastPlayed: &lastPlayed},
	} {
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert fixture user data %q: %v", data.CustomKey, err)
		}
	}
	return latestItemsExcludesFixture{items: items, users: users, admin: admin, target: target, includedMovie: includedMovie, includedNextUp: includedNext, excludedLibrary: excludedLibrary}
}

func (fixture latestItemsExcludesFixture) routes(t *testing.T) *router.Router {
	t.Helper()
	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: fixture.admin.ID}},
		latestItemsExcludesEvaluator{},
		fixture.users,
	)
	routes := router.New(authorizer.Wrap)
	builder := dtobuild.NewBuilder(fixture.items, nil)
	handlers.RegisterItems(routes, fixture.items, builder, clock.NewReal(), handlers.ItemServices{Users: fixture.users, LatestPreferences: handlers.NewLatestUserPreferences(fixture.users)})
	handlers.RegisterShows(routes, fixture.items, builder, fixture.items, fixture.users, clock.NewReal())
	return routes
}

func latestItemsExcludesUser(t *testing.T, identity *store.Identity, value int, name string, policy, configuration []byte) store.User {
	t.Helper()
	created, err := identity.CreateUser(t.Context(), store.User{
		ID:             latestItemsExcludesUUID(t, value),
		Name:           name,
		NormalizedName: name,
		Policy:         policy,
		Configuration:  configuration,
	})
	if err != nil {
		t.Fatalf("create %s user: %v", name, err)
	}
	return created
}

func latestItemsExcludesConfiguration(t *testing.T, excluded dto.UUID) []byte {
	t.Helper()
	configuration, err := json.Marshal(dto.UserConfiguration{LatestItemsExcludes: []dto.UUID{excluded}})
	if err != nil {
		t.Fatalf("marshal user configuration: %v", err)
	}
	return configuration
}

func latestItemsExcludesSeries(t *testing.T, start int, library dto.UUID, key string) (item.Item, item.Item, item.Item) {
	t.Helper()
	series := latestItemsExcludesItem(t, start, item.KindSeries, &library, key)
	series.IsFolder = true
	series.TopParentID = &library
	series.PresentationUniqueKey = &key
	one, two := 1, 2
	first := latestItemsExcludesItem(t, start+1, item.KindEpisode, &series.ID, key+" episode one")
	first.TopParentID, first.SeriesID, first.SeriesPresentationUniqueKey = &library, &series.ID, &key
	first.ParentIndexNumber, first.IndexNumber = &one, &one
	next := latestItemsExcludesItem(t, start+2, item.KindEpisode, &series.ID, key+" episode two")
	next.TopParentID, next.SeriesID, next.SeriesPresentationUniqueKey = &library, &series.ID, &key
	next.ParentIndexNumber, next.IndexNumber = &one, &two
	return series, first, next
}

func latestItemsExcludesItem(t *testing.T, value int, kind item.Kind, parent *dto.UUID, name string) item.Item {
	t.Helper()
	return item.Item{ID: latestItemsExcludesUUID(t, value), Kind: kind, ParentID: parent, Name: &name, SortName: &name}
}

func latestItemsExcludesUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse fixture ID %d: %v", value, err)
	}
	return id
}

func latestItemsExcludesResponseIDs(t *testing.T, body []byte) []dto.UUID {
	t.Helper()
	var page struct {
		Items []dto.BaseItemDto `json:"Items"`
	}
	if err := json.Unmarshal(body, &page); err == nil && page.Items != nil {
		return latestItemsExcludesItemIDs(page.Items)
	}
	var latest []dto.BaseItemDto
	if err := json.Unmarshal(body, &latest); err != nil {
		t.Fatalf("decode item response: %v; body=%s", err, body)
	}
	return latestItemsExcludesItemIDs(latest)
}

func latestItemsExcludesItemIDs(values []dto.BaseItemDto) []dto.UUID {
	result := make([]dto.UUID, len(values))
	for index := range values {
		result[index] = values[index].ID
	}
	return result
}

type latestItemsExcludesResolver struct{ identity auth.Identity }

func (resolver latestItemsExcludesResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return resolver.identity, nil
}

type latestItemsExcludesEvaluator struct{}

func (latestItemsExcludesEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}
