// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// databaseURLEnv names the environment variable that selects the store. The
// --database-url flag of the server and of each subcommand overrides it.
const databaseURLEnv = "DEADFIN_DATABASE_URL"

const databaseURLUsage = "database URL: empty for SQLite in <program-data>/data/deadfin.db, or postgres://user:password@host:5432/dbname (defaults to " + databaseURLEnv + ")"

// database identifies the store that one process opens. A zero postgresDSN
// selects SQLite at sqlitePath.
type database struct {
	postgresDSN string
	sqlitePath  string
}

// resolveDatabaseURL returns the flag value when it is set, else the
// environment value.
func resolveDatabaseURL(flagValue, envValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return flagValue
	}
	return envValue
}

// parseDatabase selects the store from a database URL. An empty URL selects
// SQLite at <dataDir>/deadfin.db. A postgres:// or postgresql:// URL selects
// PostgreSQL with that URL as the pgx connection string. Errors never repeat
// the URL, because it can hold a password.
func parseDatabase(raw, dataDir string) (database, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return database{sqlitePath: filepath.Join(dataDir, databaseFile)}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return database{}, fmt.Errorf("%s is not a valid URL; use postgres://user:password@host:5432/dbname, or leave it empty for SQLite", databaseURLEnv)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
		return database{postgresDSN: raw}, nil
	default:
		return database{}, fmt.Errorf("%s scheme %q is not supported; use postgres:// or postgresql://, or leave it empty for SQLite", databaseURLEnv, parsed.Scheme)
	}
}

// String describes the store for logs. It hides the PostgreSQL password.
func (d database) String() string {
	if d.postgresDSN != "" {
		return "PostgreSQL " + redactDatabaseURL(d.postgresDSN)
	}
	return "SQLite " + d.sqlitePath
}

// redactDatabaseURL replaces the password in the userinfo and in a password
// query parameter with "xxxxx".
func redactDatabaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	query := parsed.Query()
	if query.Has("password") {
		query.Set("password", "xxxxx")
		parsed.RawQuery = query.Encode()
	}
	return parsed.Redacted()
}

// open opens and migrates the selected store. sqliteOptions apply only to
// SQLite.
func (d database) open(ctx context.Context, sqliteOptions ...sqlite.Option) (store.Stores, error) {
	if d.postgresDSN != "" {
		return postgres.Open(ctx, postgres.Config{DSN: d.postgresDSN})
	}
	return sqlite.Open(ctx, d.sqlitePath, sqliteOptions...)
}

// openProgramDatabase opens the store that rawURL selects, with SQLite in the
// data directory of programData paths when rawURL is empty.
func openProgramDatabase(ctx context.Context, rawURL, dataDir string, sqliteOptions ...sqlite.Option) (store.Stores, database, error) {
	selected, err := parseDatabase(rawURL, dataDir)
	if err != nil {
		return nil, database{}, err
	}
	stores, err := selected.open(ctx, sqliteOptions...)
	if err != nil {
		return nil, database{}, fmt.Errorf("open %s: %w", selected, err)
	}
	return stores, selected, nil
}
