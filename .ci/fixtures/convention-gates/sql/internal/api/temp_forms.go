// SPDX-License-Identifier: GPL-2.0-only

// Package api is an inert fixture for verify-convention-gate-fixtures.
package api

// Both TEMP and TEMPORARY spellings are real DDL and must be rejected outside
// internal/store; the whitespace grouping must consume the separator for
// either spelling.
const rawTempTableOutsideStore = "CREATE TEMP TABLE scratch (id INTEGER)"
const rawTemporaryTableOutsideStore = "CREATE TEMPORARY TABLE scratch (id INTEGER)"
const rawTempViewOutsideStore = "CREATE TEMP VIEW active AS SELECT id FROM cache"
const rawTemporaryViewOutsideStore = "CREATE TEMPORARY VIEW active AS SELECT id FROM cache"
