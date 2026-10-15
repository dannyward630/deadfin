// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

// TestSQLiteOpenRepairsOutdatedDerivedStateVersion proves that sqlite.Open
// repairs outdated item-derived state when the stored version is lower than
// the code version, and that it leaves derived state untouched when the
// stored version is current or when the repair fails.
//
// The test uses a file-backed SQLite database so that each reopen reads the
// on-disk state that a prior close persisted.
func TestSQLiteOpenRepairsOutdatedDerivedStateVersion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "deadfin.db")

	// Seed the fixture: an AggregateFolder, a CollectionFolder, a source
	// Folder, and a Movie. Upsert runs repairDerivedTx, so the initial
	// top_parent_id values are correct. The test then corrupts them to the
	// old AggregateFolder value to simulate a database from before the
	// CollectionFolder boundary fix.
	aggregateID, err := dto.ParseUUID(fmt.Sprintf("%032x", 1))
	if err != nil {
		t.Fatalf("parse aggregate UUID: %v", err)
	}
	collectionID, err := dto.ParseUUID(fmt.Sprintf("%032x", 2))
	if err != nil {
		t.Fatalf("parse collection UUID: %v", err)
	}
	sourceID, err := dto.ParseUUID(fmt.Sprintf("%032x", 3))
	if err != nil {
		t.Fatalf("parse source UUID: %v", err)
	}
	movieID, err := dto.ParseUUID(fmt.Sprintf("%032x", 4))
	if err != nil {
		t.Fatalf("parse movie UUID: %v", err)
	}

	aggregate := derivedVersionItem(aggregateID, item.KindAggregateFolder, true, nil, "default")
	collection := derivedVersionItem(collectionID, item.KindCollectionFolder, true, &aggregateID, "Movies")
	source := derivedVersionItem(sourceID, item.KindFolder, true, &collectionID, "Source")
	movie := derivedVersionItem(movieID, item.KindMovie, false, &sourceID, "Film (2024)")
	movie.MediaType = mediaType(item.MediaTypeVideo)

	stores := openDerivedVersionStore(t, dbPath)
	items := store.NewItemStore(stores)
	for _, it := range []item.Item{aggregate, collection, source, movie} {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upsert %s %s: %v", it.Kind, it.ID, err)
		}
	}

	// Corrupt top_parent_id to the old AggregateFolder value without an item
	// mutation, simulating a database built before the boundary fix.
	corruptTopParentID(t, stores.Writer(), aggregateID)

	// Remove the version stamp so the next open treats the database as
	// version 0 and runs the repair.
	deleteDerivedStateStamp(t, stores.Writer())
	if err := stores.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Reopen: the gate must repair the outdated derived state and stamp
	// version 1.
	stores = openDerivedVersionStore(t, dbPath)
	items = store.NewItemStore(stores)

	// The CollectionFolder and Movie must now self-reference the
	// CollectionFolder ID, not the old AggregateFolder ID.
	assertTopParentID(t, items, ctx, collectionID, collectionID, "CollectionFolder")
	assertTopParentID(t, items, ctx, movieID, collectionID, "Movie")

	// A TopParentIDs query for the CollectionFolder must return the Movie.
	page, err := items.Query(ctx, store.ItemQuery{
		TopParentIDs:           []dto.UUID{collectionID},
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("Query(TopParentIDs=[collection]) after repair: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != movieID {
		t.Fatalf("TopParentIDs query after repair returned %v, want [Movie %v]", ids(page.Items), movieID)
	}

	// The reopened database must store version 1.
	assertDerivedStateStamp(t, stores.Writer(), "2")

	// Insert a sentinel ancestor row that repairDerivedTx would delete. If the
	// next open runs the repair, the sentinel is removed. If the gate takes the
	// early-return path, the sentinel survives.
	insertSentinelAncestor(t, stores.Writer(), collectionID, movieID)
	if err := stores.Close(); err != nil {
		t.Fatalf("close repaired store: %v", err)
	}

	// Reopen with a current stamp: the gate must not run the repair.
	stores = openDerivedVersionStore(t, dbPath)
	assertSentinelAncestor(t, stores.Reader(), collectionID, movieID, true)
	if err := stores.Close(); err != nil {
		t.Fatalf("close current-stamp store: %v", err)
	}

	// Inject a repair failure: corrupt top_parent_id, remove the stamp, drop
	// the ancestors table so repairDerivedTx fails on DELETE FROM ancestors,
	// then reopen. The open must fail and leave the old stamp and derived rows
	// unchanged.
	failurePath := filepath.Join(t.TempDir(), "failure.db")
	stores = openDerivedVersionStore(t, failurePath)
	items = store.NewItemStore(stores)
	for _, it := range []item.Item{aggregate, collection, source, movie} {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upsert failure fixture %s %s: %v", it.Kind, it.ID, err)
		}
	}
	corruptTopParentID(t, stores.Writer(), aggregateID)
	deleteDerivedStateStamp(t, stores.Writer())
	dropAncestorsTable(t, stores.Writer())
	if err := stores.Close(); err != nil {
		t.Fatalf("close failure seed store: %v", err)
	}

	if _, err := sqlite.Open(ctx, failurePath); err == nil {
		t.Fatalf("sqlite.Open with a broken repair succeeded, want error")
	}
	rawFailure := openRawSQLite(t, failurePath)
	assertDerivedStateStampAbsent(t, rawFailure)
	assertRawTopParentID(t, rawFailure, collectionID, aggregateID, "CollectionFolder after failed repair")
	assertRawTopParentID(t, rawFailure, movieID, aggregateID, "Movie after failed repair")
	if err := rawFailure.Close(); err != nil {
		t.Fatalf("close raw failure db: %v", err)
	}

	// Set a malformed stamp and reopen: the open must fail without a
	// derived-state write.
	malformedPath := filepath.Join(t.TempDir(), "malformed.db")
	stores = openDerivedVersionStore(t, malformedPath)
	items = store.NewItemStore(stores)
	for _, it := range []item.Item{aggregate, collection, source, movie} {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upsert malformed fixture %s %s: %v", it.Kind, it.ID, err)
		}
	}
	setDerivedStateStamp(t, stores.Writer(), "abc")
	// Insert a sentinel ancestor row that repairDerivedTx would delete. If
	// the rejected open ran the repair, the sentinel is removed. If the gate
	// rejects the stamp before the repair, the sentinel survives.
	insertSentinelAncestor(t, stores.Writer(), collectionID, movieID)
	if err := stores.Close(); err != nil {
		t.Fatalf("close malformed seed store: %v", err)
	}
	if _, err := sqlite.Open(ctx, malformedPath); err == nil {
		t.Fatalf("sqlite.Open with a malformed stamp succeeded, want error")
	}
	rawMalformed := openRawSQLite(t, malformedPath)
	assertRawTopParentID(t, rawMalformed, collectionID, collectionID, "CollectionFolder after malformed stamp")
	assertRawTopParentID(t, rawMalformed, movieID, collectionID, "Movie after malformed stamp")
	assertSentinelAncestor(t, rawMalformed, collectionID, movieID, true)
	if err := rawMalformed.Close(); err != nil {
		t.Fatalf("close raw malformed db: %v", err)
	}

	// Set a greater stamp and reopen: the open must fail without a
	// derived-state write.
	greaterPath := filepath.Join(t.TempDir(), "greater.db")
	stores = openDerivedVersionStore(t, greaterPath)
	items = store.NewItemStore(stores)
	for _, it := range []item.Item{aggregate, collection, source, movie} {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upsert greater fixture %s %s: %v", it.Kind, it.ID, err)
		}
	}
	setDerivedStateStamp(t, stores.Writer(), "99")
	// Insert a sentinel ancestor row that repairDerivedTx would delete. If
	// the rejected open ran the repair, the sentinel is removed. If the gate
	// rejects the stamp before the repair, the sentinel survives.
	insertSentinelAncestor(t, stores.Writer(), collectionID, movieID)
	if err := stores.Close(); err != nil {
		t.Fatalf("close greater seed store: %v", err)
	}
	if _, err := sqlite.Open(ctx, greaterPath); err == nil {
		t.Fatalf("sqlite.Open with a greater stamp succeeded, want error")
	}
	rawGreater := openRawSQLite(t, greaterPath)
	assertRawTopParentID(t, rawGreater, collectionID, collectionID, "CollectionFolder after greater stamp")
	assertRawTopParentID(t, rawGreater, movieID, collectionID, "Movie after greater stamp")
	assertSentinelAncestor(t, rawGreater, collectionID, movieID, true)
	if err := rawGreater.Close(); err != nil {
		t.Fatalf("close raw greater db: %v", err)
	}
}

func openDerivedVersionStore(t *testing.T, path string) store.Stores {
	t.Helper()
	stores, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open SQLite store %q: %v", path, err)
	}
	return stores
}

func derivedVersionItem(id dto.UUID, kind item.Kind, isFolder bool, parentID *dto.UUID, name string) item.Item {
	return item.Item{
		ID:       id,
		Kind:     kind,
		IsFolder: isFolder,
		ParentID: parentID,
		Name:     &name,
		SortName: &name,
	}
}

// corruptTopParentID writes the old parent ID into top_parent_id for every
// item without an item mutation. This simulates a database built before the
// CollectionFolder boundary fix.
func corruptTopParentID(t *testing.T, db *sql.DB, oldID dto.UUID) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "UPDATE items SET top_parent_id = ?", oldID[:]); err != nil {
		t.Fatalf("corrupt top_parent_id: %v", err)
	}
}

func deleteDerivedStateStamp(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "DELETE FROM store_metadata WHERE key = 'item_derived_state_version'"); err != nil {
		t.Fatalf("delete derived state stamp: %v", err)
	}
}

func setDerivedStateStamp(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "INSERT INTO store_metadata (key, value) VALUES ('item_derived_state_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", value); err != nil {
		t.Fatalf("set derived state stamp %q: %v", value, err)
	}
}

func assertDerivedStateStamp(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var got string
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM store_metadata WHERE key = 'item_derived_state_version'").Scan(&got); err != nil {
		t.Fatalf("read derived state stamp: %v", err)
	}
	if got != want {
		t.Fatalf("derived state stamp = %q, want %q", got, want)
	}
}

func assertDerivedStateStampAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	var got string
	err := db.QueryRowContext(context.Background(), "SELECT value FROM store_metadata WHERE key = 'item_derived_state_version'").Scan(&got)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("derived state stamp present after failed repair = %q, want absent", got)
	}
}

func dropAncestorsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "DROP TABLE ancestors"); err != nil {
		t.Fatalf("drop ancestors table: %v", err)
	}
}

func insertSentinelAncestor(t *testing.T, db *sql.DB, itemID, ancestorID dto.UUID) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "INSERT OR IGNORE INTO ancestors (item_id, ancestor_id) VALUES (?, ?)", itemID[:], ancestorID[:]); err != nil {
		t.Fatalf("insert sentinel ancestor: %v", err)
	}
}

func assertSentinelAncestor(t *testing.T, db *sql.DB, itemID, ancestorID dto.UUID, want bool) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM ancestors WHERE item_id = ? AND ancestor_id = ?", itemID[:], ancestorID[:]).Scan(&n); err != nil {
		t.Fatalf("read sentinel ancestor: %v", err)
	}
	if (n > 0) != want {
		t.Fatalf("sentinel ancestor present = %v, want %v", n > 0, want)
	}
}

func assertTopParentID(t *testing.T, items *store.ItemStore, ctx context.Context, id, want dto.UUID, label string) {
	t.Helper()
	got, err := items.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", label, err)
	}
	if got.TopParentID == nil {
		t.Fatalf("%s TopParentID is nil, want %v", label, want)
	}
	if *got.TopParentID != want {
		t.Fatalf("%s TopParentID = %v, want %v", label, *got.TopParentID, want)
	}
}

func assertRawTopParentID(t *testing.T, db *sql.DB, id, want dto.UUID, label string) {
	t.Helper()
	var raw []byte
	if err := db.QueryRowContext(context.Background(), "SELECT top_parent_id FROM items WHERE id = ?", id[:]).Scan(&raw); err != nil {
		t.Fatalf("read %s top_parent_id: %v", label, err)
	}
	var got dto.UUID
	copy(got[:], raw)
	if got != want {
		t.Fatalf("%s top_parent_id = %v, want %v", label, got, want)
	}
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open raw SQLite %q: %v", path, err)
	}
	return db
}
