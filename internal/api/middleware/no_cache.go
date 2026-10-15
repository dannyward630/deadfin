// SPDX-License-Identifier: GPL-2.0-only

package middleware

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/router"
)

// NoCache returns a RouteWrapper that stamps
// "Cache-Control: no-cache" before dispatch for the named operation IDs.
// A matching route sets the header before it calls next, so every
// downstream status — including the authorizer's bare 401 — carries the
// captured stock header. A non-matching route returns next unchanged and
// pays no per-request cost.
func NoCache(operationIDs ...string) router.RouteWrapper {
	matches := make(map[string]struct{}, len(operationIDs))
	for _, id := range operationIDs {
		matches[id] = struct{}{}
	}
	return func(route router.Route, next http.Handler) http.Handler {
		if _, ok := matches[route.OperationID]; !ok {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			next.ServeHTTP(w, r)
		})
	}
}
