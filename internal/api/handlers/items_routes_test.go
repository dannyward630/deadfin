// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
)

// TestItemRoutesPinPrimaryAndLegacyLedger pins the first P2-08 route slice.
// The legacy routes are intentionally retained in the local route ledger under
// semantic SHADOW IDs because upstream hides them from its OpenAPI document.
func TestItemRoutesPinPrimaryAndLegacyLedger(t *testing.T) {
	rt := router.New()
	handlers.RegisterItems(rt, nil, nil, clock.NewReal())
	handlers.RegisterShows(rt, nil, nil, nil, nil, clock.NewReal())

	primary := []router.Route{
		{Method: http.MethodGet, Pattern: "/Items", OperationID: "GetItems", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/UserItems/Resume", OperationID: "GetResumeItems", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}", OperationID: "GetItem", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Download", OperationID: "GetDownload", Policy: string(auth.PolicyDownload)},
		{Method: http.MethodGet, Pattern: "/Items/Latest", OperationID: "GetLatestMedia", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Search/Hints", OperationID: "GetSearchHints", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Similar", OperationID: "GetSimilarItems", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Artists/{itemId}/Similar", OperationID: "GetSimilarArtists", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Albums/{itemId}/Similar", OperationID: "GetSimilarAlbums", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/{itemId}/Similar", OperationID: "GetSimilarShows", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Movies/{itemId}/Similar", OperationID: "GetSimilarMovies", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Trailers/{itemId}/Similar", OperationID: "GetSimilarTrailers", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/SpecialFeatures", OperationID: "GetSpecialFeatures", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/LocalTrailers", OperationID: "GetLocalTrailers", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Intros", OperationID: "GetIntros", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/AdditionalParts", OperationID: "GetAdditionalPart", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/NextUp", OperationID: "GetNextUp", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/{seriesId}/Episodes", OperationID: "GetEpisodes", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Shows/{seriesId}/Seasons", OperationID: "GetSeasons", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Genres", OperationID: "GetGenres", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Persons", OperationID: "GetPersons", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Studios", OperationID: "GetStudios", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/Filters", OperationID: "GetQueryFiltersLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/Filters2", OperationID: "GetQueryFilters", Policy: string(auth.PolicyDefaultAuthorization)},
	}
	shadows := []router.Route{
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items", OperationID: "SHADOW:GetItemsLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/Resume", OperationID: "SHADOW:GetResumeItemsLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}", OperationID: "SHADOW:GetItemLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/Latest", OperationID: "SHADOW:GetLatestMediaLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/LocalTrailers", OperationID: "SHADOW:GetLocalTrailersLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/SpecialFeatures", OperationID: "SHADOW:GetSpecialFeaturesLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/Intros", OperationID: "SHADOW:GetIntrosLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
	}
	for _, want := range append(primary, shadows...) {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy || got.Anonymous {
				t.Fatalf("route %q = %#v, want non-anonymous %#v", want.OperationID, got, want)
			}
		})
	}
}
