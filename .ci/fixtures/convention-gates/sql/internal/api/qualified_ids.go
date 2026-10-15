// SPDX-License-Identifier: GPL-2.0-only

// Package api is an inert fixture for verify-convention-gate-fixtures.
package api

// Schema-qualified identifiers must be recognized on both sides of the dot in
// every advertised form (bare, double-quoted, bracketed, backticked); these
// must be rejected outside internal/store.
const rawBracketQualifiedTableOutsideStore = "CREATE TABLE main.[items] (id INTEGER)"
const rawBracketBracketViewOutsideStore = "CREATE VIEW [main].[active] AS SELECT id FROM cache"
const rawBacktickQualifiedTableOutsideStore = "CREATE TABLE main.`items` (id INTEGER)"
