// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/displaypreferences"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// RegisterDisplayPreferences registers the current user's display-preference
// endpoints on rt.
func RegisterDisplayPreferences(rt *router.Router, service *displaypreferences.Service) {
	h := displayPreferencesHandler{service: service}
	rt.Handle(http.MethodGet, "/DisplayPreferences/{displayPreferencesId}", router.Route{
		OperationID: "GetDisplayPreferences",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.get)
	rt.Handle(http.MethodPost, "/DisplayPreferences/{displayPreferencesId}", router.Route{
		OperationID: "UpdateDisplayPreferences",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.put)
}

type displayPreferencesHandler struct {
	service *displaypreferences.Service
}

// Jellyfin supplies these managed CustomPrefs values for every HTTP preference
// update. They belong at the request boundary: the persisted display-preference
// service deliberately preserves directly stored opaque documents unchanged.
var (
	stockChromecastVersion          = "stable"
	stockSkipLength                 = "15000"
	stockEnableNextVideoInfoOverlay = "True"
	stockEmptyTheme                 = ""
)

type displayPreferencesQuery struct {
	UserID *string `query:"userId"`
	Client *string `query:"client"`
}

func (h displayPreferencesHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, client, ok := displayPreferencesRequest(w, r)
	if !ok {
		return
	}
	preferences, err := h.service.Get(r.Context(), userID, chi.URLParam(r, "displayPreferencesId"), client)
	if err != nil {
		writeDisplayPreferencesError(w, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, preferences)
}

func (h displayPreferencesHandler) put(w http.ResponseWriter, r *http.Request) {
	userID, client, ok := displayPreferencesRequest(w, r)
	if !ok {
		return
	}
	var preferences *dto.DisplayPreferences
	if err := bind.JSONBody(r, &preferences); err != nil {
		if errors.Is(err, bind.ErrUnsupportedMediaType) {
			http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		} else {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		}
		return
	}
	if preferences == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	preferences.CustomPrefs = mergeManagedDisplayPreferenceCustomPrefs(preferences.CustomPrefs)
	if err := h.service.Put(r.Context(), userID, chi.URLParam(r, "displayPreferencesId"), client, *preferences); err != nil {
		writeDisplayPreferencesError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mergeManagedDisplayPreferenceCustomPrefs(request map[string]*string) map[string]*string {
	merged := map[string]*string{
		"chromecastVersion":          &stockChromecastVersion,
		"dashboardTheme":             &stockEmptyTheme,
		"enableNextVideoInfoOverlay": &stockEnableNextVideoInfoOverlay,
		"skipBackLength":             &stockSkipLength,
		"skipForwardLength":          &stockSkipLength,
		"tvhome":                     &stockEmptyTheme,
	}
	for key, value := range request {
		merged[key] = value
	}
	return merged
}

func displayPreferencesRequest(w http.ResponseWriter, r *http.Request) (dto.UUID, string, bool) {
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return dto.NilUUID, "", false
	}

	var query displayPreferencesQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, "", false
	}
	if query.Client == nil || strings.TrimSpace(*query.Client) == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, "", false
	}

	userID := identity.UserID
	if query.UserID != nil {
		parsed, err := dto.ParseUUID(*query.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return dto.NilUUID, "", false
		}
		if parsed.IsNil() {
			parsed = identity.UserID
		}
		if parsed != identity.UserID && !identity.Administrator {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return dto.NilUUID, "", false
		}
		userID = parsed
	}
	return userID, *query.Client, true
}

func writeDisplayPreferencesError(w http.ResponseWriter, err error) {
	var validation *displaypreferences.ValidationError
	if errors.As(err, &validation) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
