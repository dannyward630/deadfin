// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
)

// TestShowsNextUpAppliesDisplaySpecialsWithinSeasons exercises the public
// NextUp route with the real store selector. It keeps the source setting at
// the HTTP boundary: the same positioned season-zero special is either
// omitted or selected before S1E2 according to the server configuration.
func TestShowsNextUpAppliesDisplaySpecialsWithinSeasons(t *testing.T) {
	fixture := newLatestItemsExcludesFixture(t)
	series := *fixture.includedNextUp.SeriesID
	key := *fixture.includedNextUp.SeriesPresentationUniqueKey
	zero, one, two := 0, 1, 2
	positionedSpecial := item.Item{
		ID:                          latestItemsExcludesUUID(t, 50),
		Kind:                        item.KindEpisode,
		ParentID:                    &series,
		TopParentID:                 fixture.includedNextUp.TopParentID,
		SeriesID:                    &series,
		SeriesPresentationUniqueKey: &key,
		ParentIndexNumber:           &zero,
		IndexNumber:                 &two,
		AirsBeforeSeasonNumber:      &one,
		AirsBeforeEpisodeNumber:     &two,
	}
	if err := fixture.items.Upsert(t.Context(), positionedSpecial); err != nil {
		t.Fatalf("upsert positioned special: %v", err)
	}

	for _, test := range []struct {
		name            string
		displaySpecials bool
		want            []dto.UUID
	}{
		{name: "disabled omits positioned special", displaySpecials: false, want: []dto.UUID{fixture.includedNextUp.ID}},
		{name: "enabled selects positioned special", displaySpecials: true, want: []dto.UUID{positionedSpecial.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			routes := nextUpSpecialsRoutes(t, fixture, test.displaySpecials)
			path := "/Shows/NextUp?userId=" + fixture.target.ID.Dashed() + "&seriesId=" + series.Dashed()
			record := httptest.NewRecorder()
			routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", path, record.Code, record.Body.String())
			}
			if got := latestItemsExcludesResponseIDs(t, record.Body.Bytes()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("GET %s item IDs = %v, want %v; body=%s", path, got, test.want, record.Body.String())
			}
		})
	}
}

type nextUpSpecialsConfiguration struct {
	configuration config.ServerConfiguration
}

func (configuration nextUpSpecialsConfiguration) System() config.ServerConfiguration {
	return configuration.configuration
}

func nextUpSpecialsRoutes(t *testing.T, fixture latestItemsExcludesFixture, displaySpecials bool) *router.Router {
	t.Helper()
	authorizer := middleware.NewAuthorizer(
		latestItemsExcludesResolver{identity: auth.Identity{Authenticated: true, Administrator: true, UserID: fixture.admin.ID}},
		latestItemsExcludesEvaluator{},
		fixture.users,
	)
	routes := router.New(authorizer.Wrap)
	builder := dtobuild.NewBuilder(fixture.items, nil)
	handlers.RegisterShows(routes, fixture.items, builder, fixture.items, fixture.users, clock.NewReal(), nextUpSpecialsConfiguration{configuration: config.ServerConfiguration{DisplaySpecialsWithinSeasons: displaySpecials}})
	return routes
}
