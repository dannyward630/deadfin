// SPDX-License-Identifier: GPL-2.0-only

package migrate_test

import (
	"testing"
	"testing/fstest"

	"github.com/deadfin-org/deadfin/internal/store/migrate"
)

func TestValidateTwinsRejectsMissingPostgresMigrationIndependently(t *testing.T) {
	migrations := fstest.MapFS{
		"sqlite/00001_create_metadata.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;")},
	}

	if err := migrate.ValidateTwinsFS(migrations); err == nil {
		t.Fatal("validating a SQLite-only migration succeeded, want a missing PostgreSQL twin error")
	}
}

func TestValidateTwinsAcceptsMatchingDialectMigrations(t *testing.T) {
	migrations := fstest.MapFS{
		"migrations/sqlite/00001_create_metadata.sql":   &fstest.MapFile{Data: []byte("-- sqlite")},
		"migrations/postgres/00001_create_metadata.sql": &fstest.MapFile{Data: []byte("-- postgres")},
	}

	if err := migrate.ValidateTwinsFS(migrations); err != nil {
		t.Fatalf("validating matching migration twins: %v", err)
	}
}
