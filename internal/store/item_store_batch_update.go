// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"strings"
)

// batchUpdateMaxArgs bounds the arguments bound into one batched UPDATE. It
// stays below SQLite's conservative 999-variable limit.
const batchUpdateMaxArgs = 900

// batchColumn names one column of a batched UPDATE and its PostgreSQL type.
// PostgreSQL needs the type to compare and assign the VALUES columns.
type batchColumn struct {
	name     string
	postgres string
}

// updateFromValues sets columns[1:] of table from each row, where the row's
// columns[0] value matches. Each row holds one argument per column. The rows
// are written with one UPDATE ... FROM (VALUES ...) statement per batch, so
// the cost is one round trip per batch, not one per row. Both SQLite and
// PostgreSQL accept the statement.
func (s *ItemStore) updateFromValues(ctx context.Context, tx *sql.Tx, table string, columns []batchColumn, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	postgres := s.ph(1) != "?"
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = column.name
	}
	assignments := make([]string, 0, len(columns)-1)
	for _, column := range columns[1:] {
		assignments = append(assignments, column.name+" = batch_values."+column.name)
	}
	prefix := "WITH batch_values(" + strings.Join(names, ", ") + ") AS (VALUES "
	suffix := ") UPDATE " + table + " SET " + strings.Join(assignments, ", ") + " FROM batch_values WHERE " + table + "." + columns[0].name + " = batch_values." + columns[0].name
	perBatch := max(1, batchUpdateMaxArgs/len(columns))
	for start := 0; start < len(rows); start += perBatch {
		batch := rows[start:min(start+perBatch, len(rows))]
		values := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(columns))
		marks := make([]string, len(columns))
		for index, row := range batch {
			for column := range columns {
				marks[column] = s.ph(len(args) + 1)
				if postgres {
					marks[column] += "::" + columns[column].postgres
				}
				args = append(args, row[column])
			}
			values[index] = "(" + strings.Join(marks, ", ") + ")"
		}
		if _, err := tx.ExecContext(ctx, prefix+strings.Join(values, ", ")+suffix, args...); err != nil {
			return err
		}
	}
	return nil
}
