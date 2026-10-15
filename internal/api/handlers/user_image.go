// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// userImageQuery binds the GET/HEAD /UserImage parameter set. userId selects
// the profile owner; tag and format only shape a 200 response, and no
// reachable state produces one, so they are accepted and unused.
type userImageQuery struct {
	UserID *dto.UUID `query:"userId"`
	Tag    *string   `query:"tag"`
	Format *string   `query:"format"`
}

// RegisterUserImage registers GET/HEAD /UserImage with stock wire behavior for
// the current data model.
//
// The routes are anonymous so the login user picker loads profile images
// before authentication (upstream marks the action [AllowAnonymous]; the same
// reasoning applies to item-image serving in images.go). The authorizer
// installs no identity on anonymous routes, so the identity fallback is
// inert today; it stays so behavior upgrades if identity ever reaches
// anonymous routes.
//
// Deadfin stores no profile image, so every reachable request ends in the 400
// branch (absent or all-zero userId) or the 404 branch (a userId that names no
// stored image). The handler takes no dependencies, so the route-ledger
// composition needs only the registration call.
func RegisterUserImage(rt *router.Router) {
	h := http.HandlerFunc(serveUserImage)

	rt.Handle(http.MethodGet, "/UserImage", router.Route{OperationID: "GetUserImage", Anonymous: true}, h)
	rt.Handle(http.MethodHead, "/UserImage", router.Route{OperationID: "HeadUserImage", Anonymous: true}, h)
}

// serveUserImage answers GET/HEAD /UserImage. It matches stock behavior for a
// server whose users carry no profile image: an absent or all-zero effective
// userId returns 400, and any other userId returns 404.
func serveUserImage(w http.ResponseWriter, r *http.Request) {
	var query userImageQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}

	var effectiveID dto.UUID
	if query.UserID != nil {
		effectiveID = *query.UserID
	} else if identity, ok := middleware.IdentityFromContext(r.Context()); ok && identity.Authenticated {
		effectiveID = identity.UserID
	}

	if effectiveID.IsNil() {
		http.Error(w, "UserId is required if unauthenticated", http.StatusBadRequest)
		return
	}

	http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
}
