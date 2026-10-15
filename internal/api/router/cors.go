// SPDX-License-Identifier: GPL-2.0-only

// CORS implements the shipped default policy of pinned Jellyfin 12.0.0:
// any origin, any method, any header, no credentials. Upstream registers
// this as ASP.NET Core CORS middleware that runs before routing and before
// authentication (Jellyfin.Server/Startup.cs:191, UseCors), so deadfin answers
// a preflight in Router.ServeHTTP before dispatching to chi. This reproduces
// upstream's before-routing ordering, including answering preflights for paths
// whose method is not registered and for paths that do not exist at all.
//
// Only the default wildcard branch is implemented. The configurable CorsHosts
// case (specific origins + AllowCredentials) is deferred — see the companion
// divergence in docs/divergences.md.
package router

import "net/http"

// corsHeaders mirrors the header set emitted by upstream's wildcard policy
// (CorsPolicyProvider.cs:29-38 with AllowAnyOrigin/Method/Header). The exact
// emitted set is settled empirically against the pinned image.
const (
	headerAllowOrigin  = "Access-Control-Allow-Origin"
	headerAllowMethods = "Access-Control-Allow-Methods"
	headerAllowHeaders = "Access-Control-Allow-Headers"
	headerReqMethod    = "Access-Control-Request-Method"
	headerReqHeaders   = "Access-Control-Request-Headers"
)

// applyCORS handles cross-origin requests with upstream's default policy.
//
//   - No Origin header: same-origin traffic; dispatch unchanged with no CORS
//     headers.
//   - Origin present + OPTIONS + Access-Control-Request-Method present: a
//     preflight. Write the preflight headers, 204 No Content, and return
//     without dispatching to chi (and therefore without any route wrapper,
//     matching middleware-before-authentication ordering).
//   - Origin present, otherwise: a simple cross-origin request. Set
//     Access-Control-Allow-Origin: * on the response and dispatch normally.
//     This must apply to error responses too, matching middleware-before-routing
//     ordering, so the header is written before the downstream handler runs.
//
// Case-normalization (router.ServeHTTP) runs before this; a preflight for a
// mis-cased path therefore behaves like the real request.
func applyCORS(w http.ResponseWriter, r *http.Request) (handled bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}

	if r.Method == http.MethodOptions && r.Header.Get(headerReqMethod) != "" {
		// Preflight. Upstream's AllowAnyMethod/AllowAnyHeader echoes the
		// requested method and requested headers rather than a fixed list.
		w.Header().Set(headerAllowOrigin, "*")
		if m := r.Header.Get(headerReqMethod); m != "" {
			w.Header().Set(headerAllowMethods, m)
		}
		if h := r.Header.Get(headerReqHeaders); h != "" {
			w.Header().Set(headerAllowHeaders, h)
		}
		// No Access-Control-Allow-Credentials (wildcard branch), and no
		// Access-Control-Max-Age or Vary: Origin unless the differential run
		// against the pinned image shows otherwise.
		w.WriteHeader(http.StatusNoContent)
		return true
	}

	// Simple cross-origin request: allow the origin on whatever response
	// (including errors) the downstream handler produces.
	w.Header().Set(headerAllowOrigin, "*")
	return false
}
