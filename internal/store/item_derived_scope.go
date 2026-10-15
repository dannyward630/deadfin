// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// derivedChunkSize bounds the identifiers bound into one IN list. It stays
// below SQLite's conservative 999-variable limit.
const derivedChunkSize = 400

// derivedNode is the hierarchy-relevant part of one item row: the inputs of
// the closure, the derived hierarchy columns, and clean_name, plus the stored
// derived values so that the repair writes only changed rows.
type derivedNode struct {
	id       dto.UUID
	kind     string
	parentID *dto.UUID
	name     *string
	clean    *string
	top      *dto.UUID
	series   *dto.UUID
	season   *dto.UUID
}

// repairDerivedForTx recomputes the derived encodings that repairDerivedTx
// owns, but only for the affected set. The affected set is every item in roots
// that still exists, plus all of its current descendants. The descendants are
// read through a recursive parent_id walk after the transaction's writes, so a
// reparent, a kind change, and a new child are covered. The derived encodings
// are ancestors, top_parent_id, series_id, season_id, clean_name, the clean
// values of the item_values and people rows the set references, and
// item_user_data_keys.
//
// Every derived value of an item is a function of the item row, its own
// satellites, and its ancestor chain (the series that user-data keys read is
// the nearest Series on that chain). No derived value reads a child or a
// sibling. A write to item X therefore changes derived state only in the
// subtree of X, and the affected set above is complete. deleted lists every
// item the transaction removed; their closure and key rows are removed too.
//
// The result equals repairDerivedTx on the same data. The equivalence test in
// storetest runs randomized write sequences and compares the two.
func (s *ItemStore) repairDerivedForTx(ctx context.Context, tx *sql.Tx, roots, deleted []dto.UUID) error {
	if err := s.deleteDerivedRows(ctx, tx, deleted); err != nil {
		return err
	}
	scope, err := s.derivedScope(ctx, tx, roots)
	if err != nil {
		return err
	}
	if len(scope) == 0 {
		return nil
	}
	nodes, err := s.derivedNodes(ctx, tx, scope)
	if err != nil {
		return err
	}
	ids := make([]dto.UUID, 0, len(scope))
	for id := range scope {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].String() < ids[right].String() })
	if err := s.repairScopedAncestors(ctx, tx, ids, nodes); err != nil {
		return err
	}
	if err := s.repairScopedColumns(ctx, tx, ids, nodes); err != nil {
		return err
	}
	if err := s.repairScopedCleanSatellites(ctx, tx, ids); err != nil {
		return err
	}
	return s.repairScopedUserDataKeys(ctx, tx, ids)
}

// deleteDerivedRows removes the closure and user-data-key rows of deleted
// items. An item_user_data_keys row also cascades with its item; the explicit
// delete keeps the result independent of foreign-key enforcement.
func (s *ItemStore) deleteDerivedRows(ctx context.Context, tx *sql.Tx, deleted []dto.UUID) error {
	return s.forUUIDChunks(deleted, func(_ []dto.UUID, marks string, args []any) error {
		for _, statement := range []string{
			"DELETE FROM ancestors WHERE item_id IN (" + marks + ")",
			"DELETE FROM ancestors WHERE ancestor_id IN (" + marks + ")",
			"DELETE FROM item_user_data_keys WHERE item_id IN (" + marks + ")",
		} {
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// derivedScope returns the existing roots and all of their descendants, with
// the hierarchy row of each.
func (s *ItemStore) derivedScope(ctx context.Context, tx *sql.Tx, roots []dto.UUID) (map[dto.UUID]derivedNode, error) {
	scope := make(map[dto.UUID]derivedNode)
	err := s.forUUIDChunks(uniqueUUIDs(roots), func(_ []dto.UUID, marks string, args []any) error {
		return s.scanDerivedNodes(ctx, tx, derivedScopeQuery(marks), args, scope)
	})
	return scope, err
}

// DerivedScopeStatement returns the subtree read of derivedScope for ids,
// with its arguments. The plan tests read the statement here.
func (s *ItemStore) DerivedScopeStatement(ids []dto.UUID) (string, []any) {
	marks, args := s.sourceIDArgs(ids)
	return derivedScopeQuery(marks), args
}

// derivedScopeQuery reads the roots in marks and all of their descendants.
// The recursive CTE carries the hierarchy columns, so no join back to items
// follows it. With a join back, both planners read the whole table on an
// ANALYZEd library: PostgreSQL estimated the CTE at 931 rows for a 16-row
// subtree and hashed a sequential scan of items, and SQLite scanned items
// against a bloom filter on the CTE. Each row of the CTE is a
// function of its id, so UNION over all columns removes the same rows as
// UNION over id and still ends a parent_id cycle.
func derivedScopeQuery(marks string) string {
	const columns = "id, kind, parent_id, name, clean_name, top_parent_id, series_id, season_id"
	return "WITH RECURSIVE scope(" + columns + ") AS (SELECT " + columns + " FROM items WHERE id IN (" + marks + ") " +
		"UNION SELECT items.id, items.kind, items.parent_id, items.name, items.clean_name, items.top_parent_id, items.series_id, items.season_id FROM items JOIN scope ON items.parent_id = scope.id) " +
		"SELECT " + columns + " FROM scope"
}

// derivedNodes returns the hierarchy rows of the scope and of every ancestor
// of the scope. It walks parent_id upward one level per query, so it does not
// depend on the closure rows it is about to repair.
func (s *ItemStore) derivedNodes(ctx context.Context, tx *sql.Tx, scope map[dto.UUID]derivedNode) (map[dto.UUID]derivedNode, error) {
	nodes := make(map[dto.UUID]derivedNode, len(scope))
	for id, node := range scope {
		nodes[id] = node
	}
	frontier := make([]dto.UUID, 0, len(scope))
	for _, node := range scope {
		if node.parentID != nil {
			frontier = append(frontier, *node.parentID)
		}
	}
	for len(frontier) != 0 {
		missing := make([]dto.UUID, 0, len(frontier))
		seen := make(map[dto.UUID]struct{}, len(frontier))
		for _, id := range frontier {
			if _, found := nodes[id]; found {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			missing = append(missing, id)
		}
		if len(missing) == 0 {
			break
		}
		loaded := make(map[dto.UUID]derivedNode, len(missing))
		err := s.forUUIDChunks(missing, func(_ []dto.UUID, marks string, args []any) error {
			query := "SELECT id, kind, parent_id, name, clean_name, top_parent_id, series_id, season_id FROM items WHERE id IN (" + marks + ")"
			return s.scanDerivedNodes(ctx, tx, query, args, loaded)
		})
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, id := range missing {
			node, found := loaded[id]
			if !found {
				// repairDerivedTx fails on the same missing parent.
				return nil, fmt.Errorf("repair derived state: parent %s: %w", id, sql.ErrNoRows)
			}
			nodes[id] = node
			if node.parentID != nil {
				frontier = append(frontier, *node.parentID)
			}
		}
	}
	return nodes, nil
}

func (s *ItemStore) scanDerivedNodes(ctx context.Context, tx *sql.Tx, query string, args []any, into map[dto.UUID]derivedNode) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rawID, rawParent, rawTop, rawSeries, rawSeason any
		var node derivedNode
		if err := rows.Scan(&rawID, &node.kind, &rawParent, &node.name, &node.clean, &rawTop, &rawSeries, &rawSeason); err != nil {
			return closeRows(rows, err)
		}
		if node.id, err = scanUUID(rawID); err != nil {
			return closeRows(rows, err)
		}
		for _, pair := range []struct {
			raw any
			dst **dto.UUID
		}{{rawParent, &node.parentID}, {rawTop, &node.top}, {rawSeries, &node.series}, {rawSeason, &node.season}} {
			if isNil(pair.raw) {
				continue
			}
			value, err := scanUUID(pair.raw)
			if err != nil {
				return closeRows(rows, err)
			}
			*pair.dst = &value
		}
		into[node.id] = node
	}
	return closeRows(rows, rows.Err())
}

// ancestorChain returns the parent chain of id, nearest first. It matches the
// recursive closure in repairDerivedTx.
func ancestorChain(nodes map[dto.UUID]derivedNode, id dto.UUID) ([]dto.UUID, error) {
	var chain []dto.UUID
	current := nodes[id]
	for current.parentID != nil {
		parentID := *current.parentID
		if parentID == id || len(chain) > len(nodes) {
			return nil, fmt.Errorf("repair derived state: item %s has a parent cycle", id)
		}
		chain = append(chain, parentID)
		parent, found := nodes[parentID]
		if !found {
			return nil, fmt.Errorf("repair derived state: parent %s: %w", parentID, sql.ErrNoRows)
		}
		current = parent
	}
	return chain, nil
}

// derivedHierarchy returns the top parent, the nearest Series, and the
// nearest Season of id from the loaded rows. An Episode with no Season
// ancestor keeps its stored season.
func derivedHierarchy(nodes map[dto.UUID]derivedNode, id dto.UUID) (*dto.UUID, *dto.UUID, *dto.UUID, error) {
	var top, series, season *dto.UUID
	current := nodes[id]
	for steps := 0; ; steps++ {
		if steps > len(nodes) {
			return nil, nil, nil, fmt.Errorf("repair derived state: item %s has a parent cycle", id)
		}
		if current.kind == item.KindSeries.String() && series == nil {
			value := current.id
			series = &value
		}
		if current.kind == item.KindSeason.String() && season == nil {
			value := current.id
			season = &value
		}
		if current.parentID == nil {
			value := current.id
			top = &value
			break
		}
		parent, found := nodes[*current.parentID]
		if !found {
			return nil, nil, nil, fmt.Errorf("repair derived state: parent %s: %w", *current.parentID, sql.ErrNoRows)
		}
		// A folder whose parent is the AggregateFolder is the library boundary
		// and the top parent for its descendants, per upstream
		// BaseItem.IsTopParent (BaseItem.cs:755-758: GetParent() is
		// AggregateFolder). Stop here and write the current ID, not the
		// AggregateFolder ID, so TopParentIDs queries for a CollectionFolder
		// return its descendants.
		if parent.kind == item.KindAggregateFolder.String() {
			value := current.id
			top = &value
			break
		}
		current = parent
	}
	// An Episode outside a season folder has no Season ancestor. Its season
	// is the series' virtual season that the scanner assigned (upstream
	// SeriesMetadataService.CreateSeasonsAsync sets Episode.SeasonId). The
	// stored value is kept, so the value is still a function of the row.
	if season == nil && nodes[id].kind == item.KindEpisode.String() && nodes[id].season != nil {
		value := *nodes[id].season
		season = &value
	}
	return top, series, season, nil
}

type ancestorRow struct {
	itemID     dto.UUID
	ancestorID dto.UUID
}

// repairScopedAncestors makes the closure rows of the scope equal to the
// parent chains. It reads the stored rows and writes only the difference, so
// a write that changes no hierarchy writes no closure row.
func (s *ItemStore) repairScopedAncestors(ctx context.Context, tx *sql.Tx, ids []dto.UUID, nodes map[dto.UUID]derivedNode) error {
	var remove, insert []ancestorRow
	err := s.forUUIDChunks(ids, func(chunk []dto.UUID, marks string, args []any) error {
		stored := make(map[ancestorRow]struct{})
		rows, err := tx.QueryContext(ctx, "SELECT item_id, ancestor_id FROM ancestors WHERE item_id IN ("+marks+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rawItem, rawAncestor any
			if err := rows.Scan(&rawItem, &rawAncestor); err != nil {
				return closeRows(rows, err)
			}
			itemID, err := scanUUID(rawItem)
			if err != nil {
				return closeRows(rows, err)
			}
			ancestorID, err := scanUUID(rawAncestor)
			if err != nil {
				return closeRows(rows, err)
			}
			stored[ancestorRow{itemID, ancestorID}] = struct{}{}
		}
		if err := closeRows(rows, rows.Err()); err != nil {
			return err
		}
		for _, id := range chunk {
			chain, err := ancestorChain(nodes, id)
			if err != nil {
				return err
			}
			for _, ancestorID := range chain {
				row := ancestorRow{id, ancestorID}
				if _, found := stored[row]; found {
					delete(stored, row)
					continue
				}
				insert = append(insert, row)
			}
		}
		for row := range stored {
			remove = append(remove, row)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(remove) != 0 {
		statement, err := tx.PrepareContext(ctx, "DELETE FROM ancestors WHERE item_id = "+s.ph(1)+" AND ancestor_id = "+s.ph(2))
		if err != nil {
			return err
		}
		for _, row := range remove {
			if _, err := statement.ExecContext(ctx, s.uuidArg(row.itemID), s.uuidArg(row.ancestorID)); err != nil {
				return errors.Join(err, statement.Close())
			}
		}
		if err := statement.Close(); err != nil {
			return err
		}
	}
	for start := 0; start < len(insert); start += derivedChunkSize / 2 {
		end := min(start+derivedChunkSize/2, len(insert))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for index, row := range insert[start:end] {
			values = append(values, "("+s.ph(index*2+1)+", "+s.ph(index*2+2)+")")
			args = append(args, s.uuidArg(row.itemID), s.uuidArg(row.ancestorID))
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO ancestors(item_id, ancestor_id) VALUES "+strings.Join(values, ", "), args...); err != nil {
			return err
		}
	}
	return nil
}

// repairScopedColumns writes clean_name, top_parent_id, series_id, and
// season_id for the scope rows whose stored value differs. The changed rows
// are written in batches, so the cost is not one round trip per item.
func (s *ItemStore) repairScopedColumns(ctx context.Context, tx *sql.Tx, ids []dto.UUID, nodes map[dto.UUID]derivedNode) error {
	d := s.stores.Dialect()
	var changed [][]any
	for _, id := range ids {
		node := nodes[id]
		top, series, season, err := derivedHierarchy(nodes, id)
		if err != nil {
			return err
		}
		var clean *string
		if node.name != nil {
			value := item.CleanName(*node.name)
			clean = &value
		}
		if equalUUIDPtrFields(top, node.top) && equalUUIDPtrFields(series, node.series) && equalUUIDPtrFields(season, node.season) && equalStringPtrFields(clean, node.clean) {
			continue
		}
		var cleanArg any
		if clean != nil {
			cleanArg = *clean
		}
		changed = append(changed, []any{s.uuidArg(id), cleanArg, uuidPtrArg(d, top), uuidPtrArg(d, series), uuidPtrArg(d, season)})
	}
	return s.updateFromValues(ctx, tx, "items", []batchColumn{
		{"id", "uuid"}, {"clean_name", "text"}, {"top_parent_id", "uuid"}, {"series_id", "uuid"}, {"season_id", "uuid"},
	}, changed)
}

// repairScopedCleanSatellites recomputes clean_value and clean_name for the
// item_values and people rows that the scope references.
func (s *ItemStore) repairScopedCleanSatellites(ctx context.Context, tx *sql.Tx, ids []dto.UUID) error {
	for _, target := range cleanSatelliteTargets() {
		var changed [][]any
		err := s.forUUIDChunks(ids, func(_ []dto.UUID, marks string, args []any) error {
			query := "SELECT DISTINCT t." + target.idColumn.name + ", t." + target.valueColumn + ", t." + target.cleanColumn + " FROM " + target.mapTable + " m JOIN " + target.table + " t ON t." + target.idColumn.name + " = m." + target.mapColumn + " WHERE m.item_id IN (" + marks + ")"
			var err error
			changed, err = scanChangedCleanText(ctx, tx, query, args, changed)
			return err
		})
		if err != nil {
			return err
		}
		if err := s.updateFromValues(ctx, tx, target.table, []batchColumn{target.idColumn, {target.cleanColumn, "text"}}, changed); err != nil {
			return err
		}
	}
	return nil
}

// repairScopedUserDataKeys recomputes the item_user_data_keys rows of the
// scope. item.UserDataKeys reads the item row, its provider IDs, its item
// values, and its series; it reads no other satellite, so only those load. An
// item whose stored key set already matches is not written.
func (s *ItemStore) repairScopedUserDataKeys(ctx context.Context, tx *sql.Tx, ids []dto.UUID) error {
	for start := 0; start < len(ids); start += derivedChunkSize {
		chunk := ids[start:min(start+derivedChunkSize, len(ids))]
		marks, args := s.sourceIDArgs(chunk)
		page, err := s.userDataKeyItems(ctx, tx, marks, args)
		if err != nil {
			return err
		}
		seriesByID, err := s.userDataKeySeries(ctx, tx, page)
		if err != nil {
			return err
		}
		want := make(map[dto.UUID][]userDataKeyRow, len(page))
		for _, it := range page {
			inputs := item.UserDataInputs{}
			if it.SeriesID != nil {
				series, ok := seriesByID[*it.SeriesID]
				if !ok {
					return errors.New("repair user data keys: derived series is missing")
				}
				inputs.Series = &series
			}
			want[it.ID] = userDataKeyRows(it.ID, it, inputs)
		}
		stored, err := s.storedUserDataKeys(ctx, tx, marks, args)
		if err != nil {
			return err
		}
		var stale []dto.UUID
		var insert []userDataKeyRow
		for _, it := range page {
			if sameUserDataKeyRows(want[it.ID], stored[it.ID]) {
				continue
			}
			if len(stored[it.ID]) != 0 {
				stale = append(stale, it.ID)
			}
			insert = append(insert, want[it.ID]...)
		}
		if len(stale) != 0 {
			staleMarks, staleArgs := s.sourceIDArgs(stale)
			if _, err := tx.ExecContext(ctx, "DELETE FROM item_user_data_keys WHERE item_id IN ("+staleMarks+")", staleArgs...); err != nil {
				return err
			}
		}
		if err := s.insertUserDataKeyRows(ctx, tx, insert); err != nil {
			return err
		}
	}
	return nil
}

func (s *ItemStore) userDataKeyItems(ctx context.Context, tx *sql.Tx, marks string, args []any) ([]item.Item, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id IN ("+marks+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	var page []item.Item
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
	if err := s.loadUserDataKeySatellites(ctx, tx, page); err != nil {
		return nil, err
	}
	return page, nil
}

// loadUserDataKeySatellites loads the satellites item.UserDataKeys reads:
// provider IDs and item values.
func (s *ItemStore) loadUserDataKeySatellites(ctx context.Context, db queryer, items []item.Item) error {
	for start := 0; start < len(items); start += derivedChunkSize {
		batch := items[start:min(start+derivedChunkSize, len(items))]
		ids := make([]dto.UUID, len(batch))
		byID := make(map[dto.UUID]*item.Item, len(batch))
		for index := range batch {
			ids[index] = batch[index].ID
			byID[batch[index].ID] = &batch[index]
		}
		marks, args := s.sourceIDArgs(ids)
		if err := s.batchProviders(ctx, db, marks, args, byID); err != nil {
			return err
		}
		if err := s.batchItemValues(ctx, db, marks, args, byID); err != nil {
			return err
		}
	}
	return nil
}

func (s *ItemStore) userDataKeySeries(ctx context.Context, tx *sql.Tx, page []item.Item) (map[dto.UUID]item.Item, error) {
	byID := make(map[dto.UUID]item.Item)
	for _, it := range page {
		byID[it.ID] = it
	}
	var required []dto.UUID
	requested := make(map[dto.UUID]struct{})
	for _, it := range page {
		if it.SeriesID == nil {
			continue
		}
		if _, ok := byID[*it.SeriesID]; ok {
			continue
		}
		if _, ok := requested[*it.SeriesID]; ok {
			continue
		}
		requested[*it.SeriesID] = struct{}{}
		required = append(required, *it.SeriesID)
	}
	err := s.forUUIDChunks(required, func(_ []dto.UUID, marks string, args []any) error {
		loaded, err := s.userDataKeyItems(ctx, tx, marks, args)
		for _, series := range loaded {
			byID[series.ID] = series
		}
		return err
	})
	return byID, err
}

func (s *ItemStore) storedUserDataKeys(ctx context.Context, tx *sql.Tx, marks string, args []any) (map[dto.UUID][]userDataKeyRow, error) {
	rows, err := tx.QueryContext(ctx, "SELECT item_id, custom_key, is_live_tv_channel_name FROM item_user_data_keys WHERE item_id IN ("+marks+")", args...)
	if err != nil {
		return nil, err
	}
	stored := make(map[dto.UUID][]userDataKeyRow)
	for rows.Next() {
		var rawID, rawLive any
		var key string
		if err := rows.Scan(&rawID, &key, &rawLive); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(rawID)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		stored[id] = append(stored[id], userDataKeyRow{itemID: id, key: key, isLiveTVChannelName: asBool(rawLive)})
	}
	return stored, closeRows(rows, rows.Err())
}

func sameUserDataKeyRows(want, stored []userDataKeyRow) bool {
	if len(want) != len(stored) {
		return false
	}
	set := make(map[userDataKeyRow]struct{}, len(stored))
	for _, row := range stored {
		set[row] = struct{}{}
	}
	for _, row := range want {
		if _, found := set[row]; !found {
			return false
		}
	}
	return true
}

// forUUIDChunks calls fn with IN-list placeholders and arguments for each
// chunk of ids.
func (s *ItemStore) forUUIDChunks(ids []dto.UUID, fn func(chunk []dto.UUID, marks string, args []any) error) error {
	for start := 0; start < len(ids); start += derivedChunkSize {
		chunk := ids[start:min(start+derivedChunkSize, len(ids))]
		marks, args := s.sourceIDArgs(chunk)
		if err := fn(chunk, marks, args); err != nil {
			return err
		}
	}
	return nil
}

func uniqueUUIDs(ids []dto.UUID) []dto.UUID {
	seen := make(map[dto.UUID]struct{}, len(ids))
	out := make([]dto.UUID, 0, len(ids))
	for _, id := range ids {
		if id.IsNil() {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
