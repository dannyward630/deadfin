// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"fmt"
)

// DeleteDetachedUserData deletes user_data rows whose user_id is absent from
// users or whose custom_key is absent from item_user_data_keys (schema:
// migrations/sqlite/00004_items.sql:100, 00005, 00003_identity.sql:5).
// The SQL is valid on both SQLite and PostgreSQL.
//
// Immediate deletion (no RetentionDate column) — divergence-noted vs upstream's
// 90-day hold (CleanupUserDataTask.cs:53-66).
func DeleteDetachedUserData(ctx context.Context, stores Stores) (int64, error) {
	if stores == nil {
		return 0, fmt.Errorf("store: stores is required")
	}
	const query = `
DELETE FROM user_data
WHERE user_id NOT IN (SELECT id FROM users)
   OR (item_id, custom_key) NOT IN (SELECT item_id, custom_key FROM item_user_data_keys)
`
	result, err := stores.Writer().ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("store: delete detached user data: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: count deleted user data rows: %w", err)
	}
	return n, nil
}

// Maintain runs SQLite maintenance on the writer: PRAGMA wal_checkpoint(TRUNCATE)
// → PRAGMA optimize → VACUUM → PRAGMA wal_checkpoint(TRUNCATE), in upstream order
// (SqliteDatabaseProvider.cs:107-110). Writer MaxOpenConns=1 serializes against
// all other writes — no lock coordination needed.
func Maintain(ctx context.Context, stores Stores) error {
	if stores == nil {
		return fmt.Errorf("store: stores is required")
	}
	steps := []string{
		`PRAGMA wal_checkpoint(TRUNCATE)`,
		`PRAGMA optimize`,
		`VACUUM`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	}
	w := stores.Writer()
	for _, step := range steps {
		if _, err := w.ExecContext(ctx, step); err != nil {
			return fmt.Errorf("store: maintain step %q: %w", step, err)
		}
	}
	return nil
}
