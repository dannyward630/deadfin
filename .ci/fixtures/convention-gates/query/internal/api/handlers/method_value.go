// SPDX-License-Identifier: GPL-2.0-only

// Package handlers is an inert fixture for verify-convention-gate-fixtures.
package handlers

import "net/http"

func queryParsedThroughMethodValue(req *http.Request) {
	query := req.URL.Query
	_ = query()
}
