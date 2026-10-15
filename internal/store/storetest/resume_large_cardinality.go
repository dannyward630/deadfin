// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"context"
	"fmt"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// SeedResumeLargeCardinality inserts the filler items and user data used by
// Resume performance tests. Item inserts share one transaction. User-data
// inserts use the writer directly, matching the exercised production path.
func SeedResumeLargeCardinality(ctx context.Context, stores store.Stores, userID dto.UUID, fillerCount, userDataCount int) error {
	dialect := stores.Dialect()
	falseValue := dialect.Boolean(false)
	writer := stores.Writer()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin filler insert: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO items (id, kind, sort_name, source_type, playlist_owner_user_id, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access) VALUES ("+dialect.Placeholder(1)+", "+dialect.Placeholder(2)+", "+dialect.Placeholder(3)+", "+dialect.Placeholder(4)+", "+dialect.Placeholder(5)+", "+falseValue+", "+falseValue+", "+falseValue+", "+falseValue+", "+dialect.Boolean(true)+", "+falseValue+", "+falseValue+", "+falseValue+", "+falseValue+")")
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare filler insert: %w", err)
	}
	for i := 1; i <= fillerCount; i++ {
		id, err := resumeCardinalityUUID(i)
		if err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
		if _, err := stmt.ExecContext(ctx, resumeCardinalityUUIDArg(dialect, id), item.KindMovie.String(), "filler", "Library", resumeCardinalityUUIDArg(dialect, dto.NilUUID)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("insert filler item %d: %w", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("close filler insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit filler items: %w", err)
	}

	userDataStmt, err := writer.PrepareContext(ctx, "INSERT INTO user_data (user_id, custom_key, item_id, played, position_ticks, last_played) VALUES ("+dialect.Placeholder(1)+", "+dialect.Placeholder(2)+", "+dialect.Placeholder(3)+", "+dialect.Boolean(false)+", "+dialect.Placeholder(4)+", "+dialect.Placeholder(5)+")")
	if err != nil {
		return fmt.Errorf("prepare user data insert: %w", err)
	}
	for i := 1; i <= userDataCount; i++ {
		id, err := resumeCardinalityUUID(i)
		if err != nil {
			_ = userDataStmt.Close()
			return err
		}
		last := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		if _, err := userDataStmt.ExecContext(ctx, resumeCardinalityUUIDArg(dialect, userID), "library/"+id.String(), resumeCardinalityUUIDArg(dialect, id), int64(1), dialect.TimeValue(last)); err != nil {
			_ = userDataStmt.Close()
			return fmt.Errorf("insert filler user data %d: %w", i, err)
		}
	}
	if err := userDataStmt.Close(); err != nil {
		return fmt.Errorf("close user data insert: %w", err)
	}
	return nil
}

func resumeCardinalityUUID(value int) (dto.UUID, error) {
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		return dto.NilUUID, fmt.Errorf("parse resume UUID %d: %w", value, err)
	}
	return id, nil
}

func resumeCardinalityUUIDArg(dialect store.Dialect, id dto.UUID) any {
	if dialect.Placeholder(1) == "?" {
		return id[:]
	}
	return id.Dashed()
}
