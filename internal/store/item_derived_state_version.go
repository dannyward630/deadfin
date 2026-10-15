// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// derivedStateVersion is the code's current item-derived-state version. The
// store openers stamp this value after a successful repair. Increment this
// constant for every future change to the output of repairDerivedTx.
//
// Version 2 forces one repair of rows whose TopParentID, SeriesID, and
// SeasonID an earlier metadata backfill wrote as NULL.
const derivedStateVersion = 2

// itemDerivedStateVersionKey is the store_metadata key that records the
// derived-state version a database was built under.
const itemDerivedStateVersionKey = "item_derived_state_version"

// RepairOutdatedDerivedState runs the global derived-state repair when the
// stored version is lower than the code version. Store openers call it once
// after migrate.Up and before BackfillMissingItemUserDataKeys.
//
// An absent stamp identifies a database from older code and is treated as
// version 0, so the first open after an upgrade repairs every item. When the
// stored version is current, the function returns without derived-state
// writes. A malformed, negative, or greater stamp is rejected with an error.
func RepairOutdatedDerivedState(ctx context.Context, stores Stores) (err error) {
	s := NewItemStore(stores)
	d := stores.Dialect()
	tx, err := stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)

	if err := s.lockDerivedGlobal(ctx, tx); err != nil {
		return err
	}

	stored, hasStamp, err := readDerivedStateVersionStamp(ctx, tx, d)
	if err != nil {
		return err
	}
	if hasStamp && stored == derivedStateVersion {
		return tx.Commit()
	}
	if hasStamp && (stored < 0 || stored > derivedStateVersion) {
		return fmt.Errorf("store: item derived state version %d is malformed", stored)
	}

	if err := s.repairDerivedTx(ctx, tx); err != nil {
		return err
	}
	if err := writeDerivedStateVersionStamp(ctx, tx, d, derivedStateVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func readDerivedStateVersionStamp(ctx context.Context, tx *sql.Tx, d Dialect) (int, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM store_metadata WHERE key = "+d.Placeholder(1), itemDerivedStateVersionKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read item derived state version stamp: %w", err)
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("parse item derived state version %q: %w", raw, err)
	}
	return version, true, nil
}

func writeDerivedStateVersionStamp(ctx context.Context, tx *sql.Tx, d Dialect, version int) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO store_metadata (key, value) VALUES ("+d.Placeholder(1)+", "+d.Placeholder(2)+") ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		itemDerivedStateVersionKey, strconv.Itoa(version))
	if err != nil {
		return fmt.Errorf("write item derived state version stamp: %w", err)
	}
	return nil
}
