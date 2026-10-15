// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// RegisterBuild registers the native build identity route. The route is anonymous: a client must
// read compatibility before it has a token, and the body has no secrets.
// build runs on every request so callers never share a Capabilities slice.
func RegisterBuild(rt *router.Router, build func() dto.BuildInfo) {
	rt.Handle(http.MethodGet, "/deadfin/v1/build", router.Route{OperationID: "NATIVE:GetBuild", Anonymous: true}, func(w http.ResponseWriter, r *http.Request) {
		_ = render.JSON(w, r, http.StatusOK, build())
	})
}
