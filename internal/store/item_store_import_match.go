// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// ImportMatchIndex is the item lookup the Jellyfin importer matches user data
// against (docs/02-storage.md §Importer): by user-data key first, then by
// path. LibrariesByName maps a library (CollectionFolder) name to its id so
// folder ids in imported user policies can be translated.
type ImportMatchIndex struct {
	ByUserDataKey   map[string]dto.UUID
	ByPath          map[string]dto.UUID
	LibrariesByName map[string]dto.UUID
}

// ImportMatchIndex loads the match index. A key shared by several items maps
// to the lowest item id, so the result is deterministic.
func (s *ItemStore) ImportMatchIndex(ctx context.Context) (ImportMatchIndex, error) {
	index := ImportMatchIndex{ByUserDataKey: map[string]dto.UUID{}, ByPath: map[string]dto.UUID{}, LibrariesByName: map[string]dto.UUID{}}
	reader := s.stores.Reader()

	keys, err := reader.QueryContext(ctx, "SELECT custom_key, item_id FROM item_user_data_keys WHERE is_live_tv_channel_name = "+s.stores.Dialect().Boolean(false)+" ORDER BY custom_key, item_id")
	if err != nil {
		return ImportMatchIndex{}, fmt.Errorf("import match index: read keys: %w", err)
	}
	for keys.Next() {
		var key string
		var raw any
		if err := keys.Scan(&key, &raw); err != nil {
			return ImportMatchIndex{}, closeRows(keys, fmt.Errorf("import match index: scan key: %w", err))
		}
		id, err := scanUUID(raw)
		if err != nil {
			return ImportMatchIndex{}, closeRows(keys, err)
		}
		if _, seen := index.ByUserDataKey[key]; !seen {
			index.ByUserDataKey[key] = id
		}
	}
	if err := closeRows(keys, keys.Err()); err != nil {
		return ImportMatchIndex{}, fmt.Errorf("import match index: read keys: %w", err)
	}

	items, err := reader.QueryContext(ctx, "SELECT id, path, kind, name FROM items WHERE path IS NOT NULL OR kind = 'CollectionFolder' ORDER BY id")
	if err != nil {
		return ImportMatchIndex{}, fmt.Errorf("import match index: read items: %w", err)
	}
	for items.Next() {
		var raw any
		var path, name *string
		var kind string
		if err := items.Scan(&raw, &path, &kind, &name); err != nil {
			return ImportMatchIndex{}, closeRows(items, fmt.Errorf("import match index: scan item: %w", err))
		}
		id, err := scanUUID(raw)
		if err != nil {
			return ImportMatchIndex{}, closeRows(items, err)
		}
		if kind == "CollectionFolder" && name != nil {
			if _, seen := index.LibrariesByName[*name]; !seen {
				index.LibrariesByName[*name] = id
			}
			continue
		}
		if path != nil {
			if _, seen := index.ByPath[*path]; !seen {
				index.ByPath[*path] = id
			}
		}
	}
	if err := closeRows(items, items.Err()); err != nil {
		return ImportMatchIndex{}, fmt.Errorf("import match index: read items: %w", err)
	}
	return index, nil
}
