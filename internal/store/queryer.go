// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
)

// queryer is satisfied by both *sql.DB and *sql.Tx, keeping multi-query reads
// on one snapshot without exposing database/sql outside the store package.
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
