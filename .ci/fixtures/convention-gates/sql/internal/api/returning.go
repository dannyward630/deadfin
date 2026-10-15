// SPDX-License-Identifier: GPL-2.0-only

// Package api is an inert fixture for verify-convention-gate-fixtures.
package api

// DELETE ... RETURNING is statement-shaped SQL; the post-identifier whitelist
// must recognize RETURNING so this is rejected outside internal/store.
const rawDeleteReturningOutsideStore = "DELETE FROM cache RETURNING id"
