// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
)

// RegisterSetupUnlock registers the native first-run unlock route (D-53). The
// composition root logs the full link once at startup while setup is pending.
// A browser that opens the link receives the setup cookie and continues to the
// unmodified web client wizard, which then works through a proxy or from a
// remote network. The route is anonymous because it is the credential.
func RegisterSetupUnlock(rt *router.Router, gate *middleware.SetupGate) {
	rt.Handle(http.MethodGet, "/deadfin/v1/setup", router.Route{OperationID: "NATIVE:UnlockSetup", Anonymous: true}, func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			Token *string `query:"token"`
		}
		if err := bind.QueryRequest(r, &query); err != nil || query.Token == nil {
			badRequest(w)
			return
		}
		if !gate.Pending() {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		if !gate.ValidToken(*query.Token) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     middleware.SetupCookieName,
			Value:    *query.Token,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
		// A relative target keeps any BaseUrl prefix: the browser resolves it
		// against the link it opened, /<base>/deadfin/v1/setup.
		w.Header().Set("Location", "../../web/")
		w.WriteHeader(http.StatusSeeOther)
	})
}
