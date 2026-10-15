// SPDX-License-Identifier: GPL-2.0-only

package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	_ "modernc.org/sqlite"
)

const readerConnections = 4

// Open opens a SQLite store, configures every connection, and applies the
// embedded SQLite migrations before making the store available.
func Open(ctx context.Context, path string, options ...Option) (store.Stores, error) {
	var opened openOptions
	for _, option := range options {
		option(&opened)
	}
	var runner *statisticsRunner
	if opened.statistics != nil {
		var err error
		if runner, err = newStatisticsRunner(nil, *opened.statistics); err != nil {
			return nil, err
		}
	}
	baseDSN, err := connectionString(path)
	if err != nil {
		return nil, err
	}
	cache, err := cachePragmas(opened.cache)
	if err != nil {
		return nil, err
	}
	for _, pragma := range cache {
		if baseDSN, err = connectionStringWithPragma(baseDSN, pragma); err != nil {
			return nil, err
		}
	}
	writerDSN, err := connectionStringWithParamOverride(baseDSN, "_txlock", "immediate")
	if err != nil {
		return nil, err
	}
	readerDSN, err := connectionStringWithPragma(baseDSN, "query_only(ON)")
	if err != nil {
		return nil, err
	}

	writer, err := sql.Open("sqlite", writerDSN)
	if err != nil {
		return nil, fmt.Errorf("open SQLite writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	reader, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open SQLite reader: %w", err)
	}
	reader.SetMaxOpenConns(readerConnections)
	reader.SetMaxIdleConns(readerConnections)

	if err := writer.PingContext(ctx); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("connect SQLite writer: %w", err)
	}
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("connect SQLite reader: %w", err)
	}
	if err := migrate.Up(ctx, writer, migrate.SQLite); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("migrate SQLite store: %w", err)
	}
	result := &stores{writer: writer, reader: reader, dialect: Dialect{}}
	if err := store.RepairOutdatedDerivedState(ctx, result); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("repair SQLite outdated derived state: %w", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(ctx, result); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("backfill SQLite user data keys: %w", err)
	}
	if runner != nil {
		runner.writer = writer
		result.statistics = runner
		runner.start()
	}
	return result, nil
}

type stores struct {
	writer  *sql.DB
	reader  *sql.DB
	dialect store.Dialect
	// statistics is nil when Open had no WithStatistics option.
	statistics *statisticsRunner
	closed     bool
}

func (s *stores) Writer() *sql.DB { return s.writer }

func (s *stores) Reader() *sql.DB { return s.reader }

func (s *stores) Dialect() store.Dialect { return s.dialect }

// Close stops the statistics runner, refreshes the planner statistics one
// last time, and closes both pools. The final refresh leaves fresh
// statistics after an import or a scan run from the command line. A failed
// refresh is logged and does not fail Close. Without a runner a successful
// refresh logs nothing, so command-line tools keep their output unchanged.
func (s *stores) Close() error {
	if !s.closed {
		s.closed = true
		if s.statistics != nil {
			s.statistics.stop()
			logStatisticsRun(s.statistics.logger, refreshStatistics(context.Background(), s.writer, s.statistics.clock, "close"))
		} else if result := refreshStatistics(context.Background(), s.writer, clock.NewReal(), "close"); result.err != nil {
			logStatisticsRun(slog.Default(), result)
		}
	}
	return errors.Join(s.reader.Close(), s.writer.Close())
}

// RequestStatisticsRefresh asks the statistics runner for a run soon. It
// returns at once. Without a runner it does nothing.
func (s *stores) RequestStatisticsRefresh() {
	if s.statistics != nil {
		s.statistics.request()
	}
}

// Maintain runs SQLite maintenance on the writer: PRAGMA wal_checkpoint(TRUNCATE)
// → PRAGMA optimize → VACUUM → PRAGMA wal_checkpoint(TRUNCATE), in upstream
// order (SqliteDatabaseProvider.cs:107-110). Writer MaxOpenConns=1 serializes
// against all other writes — no lock coordination needed.
func (s *stores) Maintain(ctx context.Context) error {
	return store.Maintain(ctx, s)
}

func connectionString(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("sqlite path is required")
	}
	if path == ":memory:" {
		name, err := memoryDatabaseName()
		if err != nil {
			return "", err
		}
		path = "file:" + name + "?mode=memory&cache=shared"
	}

	var dsn *url.URL
	var err error
	if strings.HasPrefix(path, "file:") {
		dsn, err = url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("parse SQLite DSN: %w", err)
		}
	} else {
		dsn = &url.URL{Scheme: "file", Path: path}
	}

	pragmas := dsn.Query()
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "journal_size_limit(134217728)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "foreign_keys(ON)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")
	pragmas.Add("_pragma", "temp_store(2)")
	dsn.RawQuery = pragmas.Encode()
	return dsn.String(), nil
}

func connectionStringWithPragma(dsn, pragma string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse SQLite connection string: %w", err)
	}
	pragmas := parsed.Query()
	pragmas.Add("_pragma", pragma)
	parsed.RawQuery = pragmas.Encode()
	return parsed.String(), nil
}

// connectionStringWithParamOverride sets one DSN query parameter, replacing
// any value the input DSN already carries for that key. The writer DSN uses
// this for _txlock so an input _txlock=deferred cannot defeat the immediate
// transaction mode that reserves the SQLite writer lock at BeginTx.
func connectionStringWithParamOverride(dsn, key, value string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse SQLite connection string: %w", err)
	}
	params := parsed.Query()
	params.Set(key, value)
	parsed.RawQuery = params.Encode()
	return parsed.String(), nil
}

func memoryDatabaseName() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate SQLite memory database name: %w", err)
	}
	return fmt.Sprintf("deadfin-%x", bytes), nil
}
