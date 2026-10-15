// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/localization"
)

// RegisterLocalization registers the stock read-only localization endpoints.
// Authentication enforcement is applied by route wrappers; this registration
// deliberately only declares the upstream policy metadata.
func RegisterLocalization(rt *router.Router, service *localization.Service, manager *config.Manager) {
	h := localizationHandler{service: service, manager: manager}

	rt.Handle(http.MethodGet, "/Localization/Countries", router.Route{
		OperationID: "GetCountries",
		Policy:      string(auth.PolicyFirstTimeSetupOrDefault),
	}, h.countries)
	rt.Handle(http.MethodGet, "/Localization/Cultures", router.Route{
		OperationID: "GetCultures",
		Policy:      string(auth.PolicyFirstTimeSetupOrDefault),
	}, h.cultures)
	rt.Handle(http.MethodGet, "/Localization/ParentalRatings", router.Route{
		OperationID: "GetParentalRatings",
		Policy:      string(auth.PolicyFirstTimeSetupOrDefault),
	}, h.parentalRatings)
	rt.Handle(http.MethodGet, "/Localization/Options", router.Route{
		OperationID: "GetLocalizationOptions",
		Policy:      string(auth.PolicyFirstTimeSetupOrDefault),
	}, h.options)
}

type localizationHandler struct {
	service *localization.Service
	manager *config.Manager
}

func (h localizationHandler) countries(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, h.service.Countries())
}

func (h localizationHandler) cultures(w http.ResponseWriter, r *http.Request) {
	all := h.service.Cultures()
	unique := make([]dto.CultureDto, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, culture := range all {
		key := strings.ToLower(culture.DisplayName)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, culture)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return unique[i].DisplayName < unique[j].DisplayName
	})
	_ = render.JSON(w, r, http.StatusOK, unique)
}

func (h localizationHandler) parentalRatings(w http.ResponseWriter, r *http.Request) {
	country := ""
	if h.manager != nil {
		country = h.manager.System().MetadataCountryCode
	}
	_ = render.JSON(w, r, http.StatusOK, h.service.ParentalRatings(country))
}

func (h localizationHandler) options(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, h.service.Options())
}
