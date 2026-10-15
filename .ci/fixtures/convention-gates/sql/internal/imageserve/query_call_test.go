// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. The string below is SQL-shaped but is not a
// direct testing diagnostic: it is passed to db.QueryContext, not to a testing
// receiver method. The exemption must not cover it, so the gate must reject it
// as raw SQL. This file holds exactly one raw-SQL report so its assertion fails
// when that report is absent.
package imageserve_test

import (
	"context"
	"database/sql"
)

// queryCall passes SQL to db.QueryContext, not to a testing diagnostic.
func queryCall(ctx context.Context, db *sql.DB) {
	rows, _ := db.QueryContext(ctx, "SELECT id FROM cache WHERE active = 1")
	_ = rows
}
