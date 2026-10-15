// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/config"
)

func TestLocalizationParentalRatingsRetainsStockZeroScoresWithBothCasingProfiles(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "US"})
	handlers.RegisterLocalization(rt, service, manager)

	for _, tc := range []struct {
		name, accept, nameKey, want string
	}{
		{
			name:    "Pascal",
			nameKey: "Name",
			want:    `{"Name":"Approved","Value":0,"RatingScore":{"score":0,"subScore":0}}`,
		},
		{
			name:    "Camel",
			accept:  `application/json; profile="CamelCase"`,
			nameKey: "name",
			want:    `{"name":"Approved","value":0,"ratingScore":{"score":0,"subScore":0}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/Localization/ParentalRatings", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)

			if got := string(rawJSONRatingByName(t, rec.Body.Bytes(), tc.nameKey, "Approved")); got != tc.want {
				t.Errorf("Approved wire shape = %s, want non-nil stock zero values and nested subScore preserved as %s", got, tc.want)
			}
		})
	}
}
