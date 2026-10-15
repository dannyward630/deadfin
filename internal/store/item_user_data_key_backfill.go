// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// BackfillMissingItemUserDataKeys derives mappings only for items that have no
// persisted key rows. Store openers invoke it after migrations so an upgrade
// gets canonical shared-user-data correlation without rebuilding keys on every
// subsequent start.
func BackfillMissingItemUserDataKeys(ctx context.Context, stores Stores) (err error) {
	s := NewItemStore(stores)
	tx, err := stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)

	for {
		page, err := s.userDataKeyBackfillPage(ctx, tx)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		if err := s.loadPageSatellites(ctx, tx, page); err != nil {
			return err
		}

		keys, err := s.userDataKeyRowsForPage(ctx, tx, page)
		if err != nil {
			return err
		}
		if err := s.insertUserDataKeyRows(ctx, tx, keys); err != nil {
			return err
		}
		if len(page) < pageSatelliteBatchSize {
			break
		}
	}
	return tx.Commit()
}

func (s *ItemStore) userDataKeyBackfillPage(ctx context.Context, tx *sql.Tx) ([]item.Item, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE NOT EXISTS (SELECT 1 FROM item_user_data_keys AS item_key WHERE item_key.item_id = items.id) ORDER BY id LIMIT "+s.ph(1), pageSatelliteBatchSize)
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

func (s *ItemStore) userDataKeyPageSeries(ctx context.Context, tx *sql.Tx, page []item.Item) (map[dto.UUID]item.Item, error) {
	seriesByID := make(map[dto.UUID]item.Item)
	pageByID := make(map[dto.UUID]item.Item, len(page))
	for _, it := range page {
		pageByID[it.ID] = it
	}
	required := make(map[dto.UUID]struct{})
	for _, it := range page {
		if it.SeriesID == nil {
			continue
		}
		if series, ok := pageByID[*it.SeriesID]; ok {
			seriesByID[*it.SeriesID] = series
			continue
		}
		required[*it.SeriesID] = struct{}{}
	}
	if len(required) == 0 {
		return seriesByID, nil
	}

	ids := make([]dto.UUID, 0, len(required))
	for id := range required {
		ids = append(ids, id)
	}
	marks, args := s.sourceIDArgs(ids)
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id IN ("+marks+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	seriesPage := make([]item.Item, 0, len(ids))
	for rows.Next() {
		series, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		seriesPage = append(seriesPage, series)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	if err := s.loadPageSatellites(ctx, tx, seriesPage); err != nil {
		return nil, err
	}
	for _, series := range seriesPage {
		seriesByID[series.ID] = series
	}
	for id := range required {
		if _, ok := seriesByID[id]; !ok {
			return nil, sql.ErrNoRows
		}
	}
	return seriesByID, nil
}

func (s *ItemStore) userDataKeyRowsForPage(ctx context.Context, tx *sql.Tx, page []item.Item) ([]userDataKeyRow, error) {
	seriesByID, err := s.userDataKeyPageSeries(ctx, tx, page)
	if err != nil {
		return nil, err
	}
	keys := make([]userDataKeyRow, 0, len(page))
	for _, it := range page {
		inputs := item.UserDataInputs{}
		if it.SeriesID != nil {
			series, ok := seriesByID[*it.SeriesID]
			if !ok {
				return nil, sql.ErrNoRows
			}
			inputs.Series = &series
		}
		keys = append(keys, userDataKeyRows(it.ID, it, inputs)...)
	}
	return keys, nil
}
