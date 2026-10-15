// SPDX-License-Identifier: GPL-2.0-only

// Package storetest provides the backend conformance-suite entry point.
package storetest

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// Factory constructs an isolated store for one conformance-suite invocation.
type Factory func(t *testing.T) store.Stores

// Run owns the common store lifecycle. Backend conformance cases are added here
// as typed store APIs are introduced.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest factory is required")
	}
	stores := factory(t)
	if stores == nil {
		t.Fatal("storetest factory returned nil stores")
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close stores: %v", err)
		}
	})
}

var registeredDrivers = struct {
	sync.Mutex
	names map[string]struct{}
}{names: make(map[string]struct{})}

// RegisterDriver makes a test driver available exactly once for a test process.
// A shared test driver name is safe when package tests run concurrently.
func RegisterDriver(name string, value driver.Driver) {
	registeredDrivers.Lock()
	defer registeredDrivers.Unlock()
	if _, exists := registeredDrivers.names[name]; exists {
		return
	}
	sql.Register(name, value)
	registeredDrivers.names[name] = struct{}{}
}

// OpenSQLiteReader opens a migrated SQLite store whose reader uses driverName.
// It owns both connections and closes them when t finishes.
func OpenSQLiteReader(t *testing.T, driverName, databaseName string) store.Stores {
	t.Helper()
	path := filepath.Join(t.TempDir(), databaseName)
	dsn := "file:" + path + "?_pragma=foreign_keys(ON)"
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open SQLite fixture writer: %v", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	reader, err := sql.Open(driverName, dsn+"&_pragma=query_only(ON)")
	if err != nil {
		_ = writer.Close()
		t.Fatalf("open SQLite fixture reader: %v", err)
	}
	opened := sqliteReaderStores{writer: writer, reader: reader}
	if err := migrate.Up(t.Context(), writer, migrate.SQLite); err != nil {
		_ = opened.Close()
		t.Fatalf("migrate SQLite fixture: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), opened); err != nil {
		_ = opened.Close()
		t.Fatalf("backfill SQLite fixture item user-data keys: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close SQLite fixture: %v", err)
		}
	})
	return opened
}

type sqliteReaderStores struct {
	writer *sql.DB
	reader *sql.DB
}

func (s sqliteReaderStores) Writer() *sql.DB      { return s.writer }
func (s sqliteReaderStores) Reader() *sql.DB      { return s.reader }
func (sqliteReaderStores) Dialect() store.Dialect { return storesqlite.Dialect{} }
func (s sqliteReaderStores) Close() error         { return errors.Join(s.reader.Close(), s.writer.Close()) }

// AssertAdditionalPartsCount verifies the durable additional-part relation for
// one owner without exposing SQL outside the store boundary.
func AssertAdditionalPartsCount(t *testing.T, stores store.Stores, itemID dto.UUID, want int) {
	t.Helper()
	query := "SELECT COUNT(*) FROM item_additional_parts WHERE item_id = " + stores.Dialect().Placeholder(1)
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query, itemID[:]).Scan(&got); err != nil {
		t.Fatalf("count additional parts for %s: %v", itemID, err)
	}
	if got != want {
		t.Fatalf("additional-part rows for %s = %d, want %d", itemID, got, want)
	}
}

// AssertNoAdditionalPartOrphans verifies that no durable additional-part row
// references an item that has been removed.
func AssertNoAdditionalPartOrphans(t *testing.T, stores store.Stores) {
	t.Helper()
	const query = `
SELECT COUNT(*)
FROM item_additional_parts AS additional_parts
LEFT JOIN items ON items.id = additional_parts.item_id
WHERE items.id IS NULL`
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatalf("count orphan additional parts: %v", err)
	}
	if got != 0 {
		t.Fatalf("orphan additional-part rows = %d, want 0", got)
	}
}
