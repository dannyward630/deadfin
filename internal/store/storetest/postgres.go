// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
)

// OpenPostgres creates an isolated PostgreSQL store from DEADFIN_TEST_POSTGRES_DSN.
// It skips when the opt-in live-test DSN is absent and drops its schema at cleanup.
func OpenPostgres(t *testing.T) store.Stores {
	t.Helper()
	return openPostgres(t)
}

// OpenPostgresBenchmark is OpenPostgres for a benchmark.
func OpenPostgresBenchmark(b *testing.B) store.Stores {
	b.Helper()
	return openPostgres(b)
}

func openPostgres(t testing.TB) store.Stores {
	t.Helper()
	dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEADFIN_TEST_POSTGRES_DSN is not set")
	}
	schema := postgresTestSchema(t)
	stores, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open PostgreSQL store schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close PostgreSQL store schema %q: %v", schema, err)
		}
		if err := dropPostgresSchema(context.Background(), dsn, schema); err != nil {
			t.Errorf("drop PostgreSQL store schema %q: %v", schema, err)
		}
	})
	return stores
}

func postgresTestSchema(t testing.TB) string {
	t.Helper()
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		t.Fatalf("generate PostgreSQL test schema suffix: %v", err)
	}
	return "storetest_" + hex.EncodeToString(suffix[:])
}

func dropPostgresSchema(ctx context.Context, dsn, schema string) (err error) {
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open administrator: %w", err)
	}
	defer func() { err = errors.Join(err, admin.Close()) }()
	if _, err := admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		return err
	}
	return nil
}

// PostgresSchemaURL creates an empty schema from DEADFIN_TEST_POSTGRES_DSN and
// returns a URL whose search_path selects it, so a caller that opens the store
// itself from a URL gets an isolated database. It skips when the DSN is absent
// and drops the schema at cleanup. The DSN must be in URL form.
func PostgresSchemaURL(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEADFIN_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("DEADFIN_TEST_POSTGRES_DSN must be a postgres:// URL")
	}
	schema := postgresTestSchema(t)
	if err := createPostgresSchema(t.Context(), dsn, schema); err != nil {
		t.Fatalf("create PostgreSQL schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := dropPostgresSchema(context.Background(), dsn, schema); err != nil {
			t.Errorf("drop PostgreSQL store schema %q: %v", schema, err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func createPostgresSchema(ctx context.Context, dsn, schema string) (err error) {
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open administrator: %w", err)
	}
	defer func() { err = errors.Join(err, admin.Close()) }()
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	return err
}
