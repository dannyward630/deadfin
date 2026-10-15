// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// rebuildUserDataKeys persists the canonical item.UserDataKeys candidates used
// to correlate shared user data. It runs inside the writer transaction after
// hierarchy and normalized satellites have been repaired, so it has the same
// series context and metadata facts as the persisted item graph.
func (s *ItemStore) rebuildUserDataKeys(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM item_user_data_keys"); err != nil {
		return err
	}

	var after *dto.UUID
	for {
		page, err := s.userDataKeyRebuildPage(ctx, tx, after)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		if err := s.loadPageSatellites(ctx, tx, page); err != nil {
			return err
		}
		keys, err := s.userDataKeyRowsForPage(ctx, tx, page)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("rebuild user data keys: derived series is missing")
			}
			return err
		}
		if err := s.insertUserDataKeyRows(ctx, tx, keys); err != nil {
			return err
		}
		if len(page) < pageSatelliteBatchSize {
			return nil
		}
		last := page[len(page)-1].ID
		after = &last
	}
}

func (s *ItemStore) userDataKeyRebuildPage(ctx context.Context, tx *sql.Tx, after *dto.UUID) ([]item.Item, error) {
	query := "SELECT " + strings.Join(itemColumns, ", ") + " FROM items"
	args := make([]any, 0, 2)
	if after != nil {
		query += " WHERE id > " + s.ph(1)
		args = append(args, s.uuidArg(*after))
	}
	query += " ORDER BY id LIMIT " + s.ph(len(args)+1)
	args = append(args, pageSatelliteBatchSize)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	page := make([]item.Item, 0, pageSatelliteBatchSize)
	for rows.Next() {
		it, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		page = append(page, it)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return page, nil
}

const itemUserDataKeyInsertBatchSize = 300

type userDataKeyRow struct {
	itemID              dto.UUID
	key                 string
	isLiveTVChannelName bool
}

func userDataKeyRows(id dto.UUID, it item.Item, inputs item.UserDataInputs) []userDataKeyRow {
	disabledInputs := inputs
	disabledInputs.DisableLiveTVChannelUserDataName = true
	disabled := make(map[string]struct{})
	for _, key := range item.UserDataKeys(it, disabledInputs) {
		disabled[key] = struct{}{}
	}

	seen := make(map[string]struct{})
	keys := item.UserDataKeys(it, inputs)
	rows := make([]userDataKeyRow, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		_, isLiveTVChannelName := disabled[key]
		rows = append(rows, userDataKeyRow{itemID: id, key: key, isLiveTVChannelName: !isLiveTVChannelName})
	}
	return rows
}

func (s *ItemStore) insertUserDataKeyRows(ctx context.Context, tx *sql.Tx, rows []userDataKeyRow) error {
	for start := 0; start < len(rows); start += itemUserDataKeyInsertBatchSize {
		end := min(start+itemUserDataKeyInsertBatchSize, len(rows))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*3)
		for index, row := range rows[start:end] {
			placeholder := index*3 + 1
			values = append(values, "("+s.ph(placeholder)+", "+s.ph(placeholder+1)+", "+s.ph(placeholder+2)+")")
			args = append(args, s.uuidArg(row.itemID), row.key, boolArg(s.stores.Dialect(), row.isLiveTVChannelName))
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO item_user_data_keys(item_id,custom_key,is_live_tv_channel_name) VALUES "+strings.Join(values, ", ")+" ON CONFLICT(item_id,custom_key) DO NOTHING", args...); err != nil {
			return err
		}
	}
	return nil
}
