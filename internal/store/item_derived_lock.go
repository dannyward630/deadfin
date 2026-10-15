// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// PostgreSQL derived-state locking.
//
// SQLite has one writer connection, so every write transaction is already
// serialized and these functions do nothing there.
//
// On PostgreSQL several writers run at once. A writer that repairs derived
// state reads the parent chain of each affected item and writes rows for the
// affected subtree. Two writers conflict only when one changes the hierarchy
// or the satellites that the other reads or writes. The locks below express
// that conflict directly instead of serializing every write:
//
//   - The global key (reconcileGlobalLock*) is taken in shared mode by every
//     scoped writer and in exclusive mode by the global repair
//     (repairDerivedTx), which rewrites every item.
//   - Each item a writer changes or deletes takes its item key in exclusive
//     mode. Each ancestor of such an item, and each new parent with its
//     ancestors, takes its item key in shared mode. A writer that changes item
//     X therefore excludes every writer inside the subtree of X (they hold X
//     shared as an ancestor), and every writer that changes an ancestor of X.
//     Writers in disjoint subtrees hold only shared keys on their common
//     ancestors and run at the same time. The affected sets of two concurrent
//     scoped repairs are then disjoint, and the parent chain that each repair
//     reads cannot change under it.
//   - People rows and item_values rows are shared between items. A writer
//     takes an exclusive key for each person and each (kind, value) it inserts,
//     updates, or may delete as an orphan. This keeps the shared-row writes of
//     concurrent writers in one global order, so they cannot deadlock, and an
//     orphan delete cannot race an insert that reuses the value.
//
// Keys are acquired in ascending order in one statement, after the shared
// global key, so two writers cannot wait on each other's upfront keys. The
// writer reads its key set before it holds the keys; it then reads the set
// again and takes any new keys, until the set is stable. A writer whose key
// set exceeds derivedLockKeyLimit takes the global key exclusively instead,
// which bounds the advisory lock table use of a large batch such as an
// initial library scan.

// derivedLockKeyLimit is the largest scoped key set one transaction takes. A
// larger writer falls back to the exclusive global key. PostgreSQL shares one
// lock table of max_locks_per_transaction * max_connections entries (6400 by
// default) between all transactions.
const derivedLockKeyLimit = 256

// derivedLockScope names the rows a writer changes.
type derivedLockScope struct {
	// items are written, reparented, or deleted. Each takes an exclusive key;
	// its current ancestors take shared keys.
	items []dto.UUID
	// parents are the new parents of items. Each takes a shared key, and so
	// do its ancestors.
	parents []dto.UUID
	// writes are the item values whose people and item values the writer
	// inserts or updates.
	writes []item.Item
	// replaced items have their satellites replaced: their current people
	// and item values take exclusive keys.
	replaced []dto.UUID
	// deleted items lose their item value mappings: their current item
	// values may become orphans and take exclusive keys.
	deleted []dto.UUID
}

type derivedLockPlan func(context.Context, *sql.Tx) (derivedLockScope, error)

// lockDerivedScope takes the PostgreSQL locks for a scoped derived-state
// writer. plan returns the writer's scope from the current committed state;
// it runs again after each acquisition until the key set is stable.
func (s *ItemStore) lockDerivedScope(ctx context.Context, tx *sql.Tx, plan derivedLockPlan) error {
	if s.ph(1) == "?" {
		return nil
	}
	held := make(map[int64]bool)
	for attempt := 0; ; attempt++ {
		scope, err := plan(ctx, tx)
		if err != nil {
			return err
		}
		keys, err := s.derivedLockKeys(ctx, tx, scope)
		if err != nil {
			return err
		}
		if attempt == 0 {
			if len(keys) > derivedLockKeyLimit {
				return s.lockDerivedGlobal(ctx, tx)
			}
			if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared("+s.ph(1)+", "+s.ph(2)+")", reconcileGlobalLockFirst, reconcileGlobalLockSecond); err != nil {
				return fmt.Errorf("item derived shared lock: %w", err)
			}
		}
		var need []int64
		for key, exclusive := range keys {
			if previous, found := held[key]; !found || (exclusive && !previous) {
				need = append(need, key)
			}
		}
		if len(need) == 0 {
			return nil
		}
		sort.Slice(need, func(left, right int) bool { return need[left] < need[right] })
		modes := make([]bool, len(need))
		for index, key := range need {
			modes[index] = keys[key]
		}
		rows, err := tx.QueryContext(ctx, "SELECT CASE WHEN x THEN pg_advisory_xact_lock(k)::text ELSE pg_advisory_xact_lock_shared(k)::text END FROM unnest("+s.ph(1)+"::bigint[], "+s.ph(2)+"::boolean[]) AS t(k, x)", need, modes)
		if err != nil {
			return fmt.Errorf("item derived scoped lock: %w", err)
		}
		for rows.Next() {
			// Each row is one acquired key; draining the rows acquires them all.
		}
		if err := closeRows(rows, rows.Err()); err != nil {
			return fmt.Errorf("item derived scoped lock: %w", err)
		}
		for index, key := range need {
			held[key] = held[key] || modes[index]
		}
	}
}

// lockDerivedItems is the scope of a writer that changes the given items in
// place, with no new parent and no satellite rewrite.
func (s *ItemStore) lockDerivedItems(ctx context.Context, tx *sql.Tx, ids ...dto.UUID) error {
	return s.lockDerivedScope(ctx, tx, func(context.Context, *sql.Tx) (derivedLockScope, error) {
		return derivedLockScope{items: ids}, nil
	})
}

// lockDerivedWrite is the scope of a writer that upserts the given items with
// their satellites.
func (s *ItemStore) lockDerivedWrite(ctx context.Context, tx *sql.Tx, values ...item.Item) error {
	scope := derivedLockScope{writes: values}
	for _, value := range values {
		scope.items = append(scope.items, value.ID)
		scope.replaced = append(scope.replaced, value.ID)
		if value.ParentID != nil {
			scope.parents = append(scope.parents, *value.ParentID)
		}
	}
	return s.lockDerivedScope(ctx, tx, func(context.Context, *sql.Tx) (derivedLockScope, error) { return scope, nil })
}

// lockDerivedGlobal serializes PostgreSQL writers against a transaction that
// rewrites the derived state of every item (repairDerivedTx).
func (s *ItemStore) lockDerivedGlobal(ctx context.Context, tx *sql.Tx) error {
	if s.ph(1) == "?" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock("+s.ph(1)+", "+s.ph(2)+")", reconcileGlobalLockFirst, reconcileGlobalLockSecond); err != nil {
		return fmt.Errorf("item derived mutation lock: %w", err)
	}
	return nil
}

const (
	derivedLockItem   byte = 'i'
	derivedLockPerson byte = 'p'
	derivedLockValue  byte = 'v'
)

func derivedLockKey(namespace byte, parts ...[]byte) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte{namespace})
	for _, part := range parts {
		_, _ = hash.Write(part)
		_, _ = hash.Write([]byte{0})
	}
	return int64(hash.Sum64())
}

func itemLockKey(id dto.UUID) int64 { return derivedLockKey(derivedLockItem, id[:]) }

func personLockKey(id dto.UUID) int64 { return derivedLockKey(derivedLockPerson, id[:]) }

func valueLockKey(kind, value string) int64 {
	return derivedLockKey(derivedLockValue, []byte(kind), []byte(value))
}

// derivedLockKeys maps each key of scope to its mode: true for exclusive.
func (s *ItemStore) derivedLockKeys(ctx context.Context, tx *sql.Tx, scope derivedLockScope) (map[int64]bool, error) {
	keys := make(map[int64]bool)
	share := func(key int64) {
		if _, found := keys[key]; !found {
			keys[key] = false
		}
	}
	items := uniqueUUIDs(scope.items)
	parents := uniqueUUIDs(scope.parents)
	for _, id := range items {
		keys[itemLockKey(id)] = true
	}
	for _, id := range parents {
		share(itemLockKey(id))
	}
	err := s.forUUIDChunks(uniqueUUIDs(append(append([]dto.UUID(nil), items...), parents...)), func(_ []dto.UUID, marks string, args []any) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT ancestor_id FROM ancestors WHERE item_id IN ("+marks+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return closeRows(rows, err)
			}
			id, err := scanUUID(raw)
			if err != nil {
				return closeRows(rows, err)
			}
			share(itemLockKey(id))
		}
		return closeRows(rows, rows.Err())
	})
	if err != nil {
		return nil, err
	}
	for _, value := range scope.writes {
		for _, person := range value.People {
			keys[personLockKey(person.PersonID)] = true
		}
		for kind, list := range itemValueLists(value.Values) {
			for _, entry := range list {
				keys[valueLockKey(kind, entry)] = true
			}
		}
	}
	err = s.forUUIDChunks(uniqueUUIDs(scope.replaced), func(_ []dto.UUID, marks string, args []any) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT person_id FROM people_map WHERE item_id IN ("+marks+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return closeRows(rows, err)
			}
			id, err := scanUUID(raw)
			if err != nil {
				return closeRows(rows, err)
			}
			keys[personLockKey(id)] = true
		}
		return closeRows(rows, rows.Err())
	})
	if err != nil {
		return nil, err
	}
	err = s.forUUIDChunks(uniqueUUIDs(append(append([]dto.UUID(nil), scope.replaced...), scope.deleted...)), func(_ []dto.UUID, marks string, args []any) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT v.kind, v.value FROM item_value_map m JOIN item_values v ON v.id = m.value_id WHERE m.item_id IN ("+marks+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, value string
			if err := rows.Scan(&kind, &value); err != nil {
				return closeRows(rows, err)
			}
			keys[valueLockKey(kind, value)] = true
		}
		return closeRows(rows, rows.Err())
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
