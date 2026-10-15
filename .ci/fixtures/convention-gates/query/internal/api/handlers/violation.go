// SPDX-License-Identifier: GPL-2.0-only

// Package handlers is an inert fixture for verify-convention-gate-fixtures.
package handlers

import "net/http"

func queryParsedByHand(r *http.Request) {
	_ = r.URL.Query()
}
