// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"

	"github.com/deadfin-org/deadfin/internal/item"
)

// cleanSatelliteTarget names a satellite table whose clean column is
// item.CleanName of its value column, and the map table that links it to
// items.
type cleanSatelliteTarget struct {
	table       string
	idColumn    batchColumn
	valueColumn string
	cleanColumn string
	mapTable    string
	mapColumn   string
}

func cleanSatelliteTargets() []cleanSatelliteTarget {
	return []cleanSatelliteTarget{
		{table: "item_values", idColumn: batchColumn{"id", "bigint"}, valueColumn: "value", cleanColumn: "clean_value", mapTable: "item_value_map", mapColumn: "value_id"},
		{table: "people", idColumn: batchColumn{"id", "uuid"}, valueColumn: "name", cleanColumn: "clean_name", mapTable: "people_map", mapColumn: "person_id"},
	}
}

func (s *ItemStore) repairCleanSatellites(ctx context.Context, tx *sql.Tx) error {
	for _, target := range cleanSatelliteTargets() {
		query := "SELECT " + target.idColumn.name + ", " + target.valueColumn + ", " + target.cleanColumn + " FROM " + target.table
		changed, err := scanChangedCleanText(ctx, tx, query, nil, nil)
		if err != nil {
			return err
		}
		if err := s.updateFromValues(ctx, tx, target.table, []batchColumn{target.idColumn, {target.cleanColumn, "text"}}, changed); err != nil {
			return err
		}
	}
	return nil
}

// scanChangedCleanText reads (id, value, clean) rows and appends an
// (id, item.CleanName(value)) row for each stored clean text that differs.
func scanChangedCleanText(ctx context.Context, tx *sql.Tx, query string, args []any, changed [][]any) ([][]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return changed, err
	}
	for rows.Next() {
		var id any
		var value, stored string
		if err := rows.Scan(&id, &value, &stored); err != nil {
			return changed, closeRows(rows, err)
		}
		if clean := item.CleanName(value); clean != stored {
			changed = append(changed, []any{id, clean})
		}
	}
	return changed, closeRows(rows, rows.Err())
}
