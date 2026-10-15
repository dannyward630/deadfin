// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"context"
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// SQLiteItemUpdateObserver records direct item-row updates. It lets scanner
// integration tests verify that an unchanged scan does not write persisted
// items, including synthetic folders.
type SQLiteItemUpdateObserver struct {
	stores store.Stores
}

// NewSQLiteItemUpdateObserver installs an update trigger on the supplied
// SQLite stores. It is intentionally SQLite-specific because the scanner
// fixtures use the in-memory SQLite backend.
func NewSQLiteItemUpdateObserver(ctx context.Context, stores store.Stores) (*SQLiteItemUpdateObserver, error) {
	if stores.Dialect().Placeholder(1) != "?" {
		return nil, fmt.Errorf("item update observer requires SQLite stores")
	}
	if _, err := stores.Writer().ExecContext(ctx, "CREATE TABLE storetest_item_update_log (id INTEGER PRIMARY KEY)"); err != nil {
		return nil, fmt.Errorf("create item update log: %w", err)
	}
	if _, err := stores.Writer().ExecContext(ctx, "CREATE TRIGGER storetest_item_update AFTER UPDATE ON items BEGIN INSERT INTO storetest_item_update_log (id) VALUES (NULL); END"); err != nil {
		return nil, fmt.Errorf("create item update trigger: %w", err)
	}
	return &SQLiteItemUpdateObserver{stores: stores}, nil
}

// Count returns the number of item writes observed after construction.
func (o *SQLiteItemUpdateObserver) Count(ctx context.Context) (int, error) {
	var count int
	if err := o.stores.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM storetest_item_update_log").Scan(&count); err != nil {
		return 0, fmt.Errorf("count item updates: %w", err)
	}
	return count, nil
}

// InstallSQLiteItemKindInsertFailure makes all future attempts to materialize
// an item of the requested kind fail atomically. It supports rollback tests
// that must assert the public store transaction contract.
func InstallSQLiteItemKindInsertFailure(ctx context.Context, stores store.Stores, kind item.Kind) error {
	if stores.Dialect().Placeholder(1) != "?" {
		return fmt.Errorf("item kind insert failure requires SQLite stores")
	}
	kindLiteral := "'" + strings.ReplaceAll(kind.String(), "'", "''") + "'"
	statement := "CREATE TRIGGER storetest_fail_item_kind_insert BEFORE INSERT ON items WHEN NEW.kind = " + kindLiteral + " BEGIN SELECT RAISE(ABORT, 'injected item kind materialization failure'); END"
	if _, err := stores.Writer().ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("install item kind insert failure: %w", err)
	}
	return nil
}
