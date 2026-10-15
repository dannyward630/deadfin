// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// PrimaryImages returns the first Primary image record of each listed item
// that has one. DtoService.AttachPeople reads it to project
// BaseItemPerson.PrimaryImageTag; IDs are read in bounded chunks so a page of
// credits makes one query per chunk.
func (s *ItemStore) PrimaryImages(ctx context.Context, ids []dto.UUID) (result map[dto.UUID]item.ImageInfo, err error) {
	result = make(map[dto.UUID]item.ImageInfo)
	if len(ids) == 0 {
		return result, nil
	}
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	for start := 0; start < len(ids); start += pageSatelliteBatchSize {
		marks, args := s.sourceIDArgs(ids[start:min(start+pageSatelliteBatchSize, len(ids))])
		if err = s.primaryImages(ctx, tx, marks, args, result); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ItemStore) primaryImages(ctx context.Context, db queryer, marks string, args []any, out map[dto.UUID]item.ImageInfo) error {
	args = append(args, item.ImageTypePrimary.String())
	rows, err := db.QueryContext(ctx, "SELECT item_id,idx,path,date_modified,width,height,blurhash FROM item_images WHERE item_id IN ("+marks+") AND type = "+s.ph(len(args))+" ORDER BY item_id,idx", args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw, modified, blur any
		info := item.ImageInfo{Type: item.ImageTypePrimary}
		if err := rows.Scan(&raw, &info.Index, &info.Path, &modified, &info.Width, &info.Height, &blur); err != nil {
			return closeRows(rows, err)
		}
		id, err := scanUUID(raw)
		if err != nil {
			return closeRows(rows, err)
		}
		if _, found := out[id]; found {
			continue
		}
		info.DateModified = optionalTime(modified, s.stores.Dialect())
		if !isNil(blur) {
			value := asString(blur)
			info.BlurHash = &value
		}
		out[id] = info
	}
	return closeRows(rows, rows.Err())
}
