// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ItemUserResolver loads the persisted user that governs item visibility and
// per-user overlays.
type ItemUserResolver interface {
	Get(context.Context, dto.UUID) (store.User, error)
}

// ItemServices groups the item-route dependencies that require the selected
// persisted user. It keeps selection and authorization at one HTTP seam.
type ItemServices struct {
	Users             ItemUserResolver
	LatestPreferences LatestUserPreferences
	DirectFiles       playback.DirectFileSourceService
	// Languages names stream languages in GET /Items/Filters2.
	Languages LanguageInfoFinder
}

// resolveItemUser ports RequestHelpers.GetUserId for item routes, then loads
// the selected persisted user so ItemQuery access filtering cannot be bypassed.
func resolveItemUser(w http.ResponseWriter, r *http.Request, requested string, users ItemUserResolver) (store.User, bool) {
	var requestedID *dto.UUID
	if requested != "" {
		parsed, err := dto.ParseUUID(requested)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return store.User{}, false
		}
		requestedID = &parsed
	}
	return resolveSelectedUser(w, r, requestedID, users)
}

// isGlobalAPIKeyItemRequest identifies the source API-key browse mode. Unlike
// device tokens, API keys are trusted administrators without an implicit user;
// an omitted or Guid.Empty target therefore leaves item query user facts nil.
func isGlobalAPIKeyItemRequest(r *http.Request, requested string) bool {
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated || !identity.APIKey {
		return false
	}
	if requested == "" {
		return true
	}
	id, err := dto.ParseUUID(requested)
	return err == nil && id.IsNil()
}

// resolveSelectedUser ports RequestHelpers.GetUserId for routes that already
// bind a typed optional user identifier.
func resolveSelectedUser(w http.ResponseWriter, r *http.Request, requested *dto.UUID, users ItemUserResolver) (store.User, bool) {
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return store.User{}, false
	}
	userID := identity.UserID
	if requested != nil && !requested.IsNil() {
		userID = *requested
	}
	administrator := identity.Administrator
	if !administrator && userID != identity.UserID && users != nil {
		actor, err := users.Get(r.Context(), identity.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return store.User{}, false
		}
		var policy struct{ IsAdministrator bool }
		if len(actor.Policy) != 0 && json.Unmarshal(actor.Policy, &policy) == nil {
			administrator = policy.IsAdministrator
		}
	}
	if userID != identity.UserID && !administrator {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return store.User{}, false
	}
	if users == nil {
		return store.User{ID: userID}, true
	}
	user, err := users.Get(r.Context(), userID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return store.User{}, false
	case err != nil:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return store.User{}, false
	}
	return user, true
}

func applyItemUser(query *store.ItemQuery, user store.User) {
	query.UserID = &user.ID
	query.User = &user
}
