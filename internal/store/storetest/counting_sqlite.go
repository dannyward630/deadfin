// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	moderncsqlite "modernc.org/sqlite"
)

// CountingSQLiteStores is a migrated SQLite store whose reader counts the
// reader-side queries that pass through it. Each instance owns its own
// counter and connector, so the helper adds no package-level mutable state
// and no global sql.Register name.
type CountingSQLiteStores struct {
	stores  store.Stores
	queries *atomic.Int64
}

// OpenCountingSQLite opens a migrated SQLite fixture whose reader counts
// queries. The reader uses a per-instance driver.Connector with sql.OpenDB,
// so the helper does not add a global sql.Register name. It owns both
// connections and closes them when t finishes.
func OpenCountingSQLite(t *testing.T, databaseName string) *CountingSQLiteStores {
	t.Helper()
	path := filepath.Join(t.TempDir(), databaseName)
	dsn := "file:" + path + "?_pragma=foreign_keys(ON)"

	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open counting SQLite fixture writer: %v", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	queries := &atomic.Int64{}
	connector := &countingSQLiteConnector{
		base:    &moderncsqlite.Driver{},
		name:    dsn + "&_pragma=query_only(ON)",
		queries: queries,
	}
	reader := sql.OpenDB(connector)

	opened := sqliteReaderStores{writer: writer, reader: reader}
	if err := migrate.Up(t.Context(), writer, migrate.SQLite); err != nil {
		_ = opened.Close()
		t.Fatalf("migrate counting SQLite fixture: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), opened); err != nil {
		_ = opened.Close()
		t.Fatalf("backfill counting SQLite fixture item user-data keys: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close counting SQLite fixture: %v", err)
		}
	})
	return &CountingSQLiteStores{stores: opened, queries: queries}
}

// Stores returns the store.Stores seam for the counting fixture.
func (c *CountingSQLiteStores) Stores() store.Stores { return c.stores }

// ResetReaderQueries sets the reader query count to zero.
func (c *CountingSQLiteStores) ResetReaderQueries() { c.queries.Store(0) }

// ReaderQueries returns the number of reader-side queries observed since the
// last reset.
func (c *CountingSQLiteStores) ReaderQueries() int64 { return c.queries.Load() }

// countingSQLiteConnector is a per-instance driver.Connector that wraps the
// modernc SQLite driver. It returns counting connections without registering
// a global driver name.
type countingSQLiteConnector struct {
	base    driver.Driver
	name    string
	queries *atomic.Int64
}

// Connect opens one connection through the base driver and wraps it so that
// reader-side queries increment the per-instance counter.
func (c *countingSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.name)
	if err != nil {
		return nil, err
	}
	return countingSQLiteConn{Conn: conn, queries: c.queries}, nil
}

// Driver returns the underlying SQLite driver.
func (c *countingSQLiteConnector) Driver() driver.Driver { return c.base }

// countingSQLiteConn wraps a SQLite connection and counts reader-side
// queries. It implements the optional driver interfaces that the SQLite
// reader path uses.
type countingSQLiteConn struct {
	driver.Conn
	queries *atomic.Int64
}

// QueryContext counts and delegates the reader-side query.
func (c countingSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

// BeginTx delegates transaction begin so the wrapped connection keeps the
// driver.ConnBeginTx contract.
func (c countingSQLiteConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return beginner.BeginTx(ctx, options)
}
