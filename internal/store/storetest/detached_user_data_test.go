// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestDetachedUserDataSQLite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Stores {
		stores, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("opening factory SQLite stores: %v", err)
		}
		return stores
	})
}

func TestDeleteDetachedUserDataRemovesOrphansPreservesAttached(t *testing.T) {
	ctx := context.Background()

	// SQLite twin.
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer func() { _ = stores.Close() }()
	seedAndVerifyDetachedUserData(t, ctx, stores)
	// PostgreSQL twin (skips when DSN unset — manual check to avoid
	// skipping the entire test, which would hide the SQLite result).
	if dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN"); dsn != "" {
		pgStores := openPostgresFromDSN(t, dsn)
		seedAndVerifyDetachedUserData(t, ctx, pgStores)
	}
}

func seedAndVerifyDetachedUserData(t *testing.T, ctx context.Context, stores store.Stores) {
	t.Helper()
	w := stores.Writer()
	d := stores.Dialect()

	// Dialect-aware argument helpers: SQLite stores UUIDs as raw bytes and
	// booleans as 0/1; PostgreSQL stores UUIDs as dashed strings and booleans
	// as true/false. Binary blobs (policy, configuration) are []byte on both.
	uuidArg := func(b [16]byte) any {
		if d.Placeholder(1) == "?" {
			return b[:] // SQLite: BLOB
		}
		return dto.UUID(b).Dashed() // PostgreSQL: UUID text
	}
	boolArg := func(v bool) any {
		if d.Boolean(v) == "1" {
			if v {
				return 1
			}
			return 0
		}
		return v
	}
	ph := d.Placeholder

	// Fixed UUIDs for the fixture.
	userID := [16]byte{0x01}
	itemID := [16]byte{0x02}
	orphanUserID := [16]byte{0xFF}

	// Insert a valid user.
	_, err := w.ExecContext(ctx,
		`INSERT INTO users (id, name, normalized_name, policy, configuration) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`, `+ph(4)+`, `+ph(5)+`)`,
		uuidArg(userID), "alice", "alice", []byte("{}"), []byte("{}"))
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Insert a valid item (all NOT NULL columns supplied).
	_, err = w.ExecContext(ctx,
		`INSERT INTO items (id, kind, playlist_owner_user_id, source_type, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access, sort_name, clean_name) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`, `+ph(4)+`, `+ph(5)+`, `+ph(6)+`, `+ph(7)+`, `+ph(8)+`, `+ph(9)+`, `+ph(10)+`, `+ph(11)+`, `+ph(12)+`, `+ph(13)+`, `+ph(14)+`, `+ph(15)+`)`,
		uuidArg(itemID), "Movie", uuidArg([16]byte{}), "Library",
		boolArg(false), boolArg(false), boolArg(false), boolArg(false),
		boolArg(true), boolArg(false), boolArg(false), boolArg(false),
		boolArg(false), "Item", "item")
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}

	// Insert a valid item_user_data_keys row.
	_, err = w.ExecContext(ctx,
		`INSERT INTO item_user_data_keys (item_id, custom_key, is_live_tv_channel_name) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`)`,
		uuidArg(itemID), "mykey", boolArg(false))
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// Insert an attached user_data row (valid user + valid key).
	_, err = w.ExecContext(ctx,
		`INSERT INTO user_data (user_id, custom_key, item_id) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`)`,
		uuidArg(userID), "mykey", uuidArg(itemID))
	if err != nil {
		t.Fatalf("seed attached user_data: %v", err)
	}

	// Insert a user-orphan row (user_id not in users).
	_, err = w.ExecContext(ctx,
		`INSERT INTO user_data (user_id, custom_key, item_id) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`)`,
		uuidArg(orphanUserID), "orphan", uuidArg(itemID))
	if err != nil {
		t.Fatalf("seed user-orphan user_data: %v", err)
	}

	// Insert a key-orphan row (item_id+custom_key not in item_user_data_keys).
	_, err = w.ExecContext(ctx,
		`INSERT INTO user_data (user_id, custom_key, item_id) VALUES (`+
			ph(1)+`, `+ph(2)+`, `+ph(3)+`)`,
		uuidArg(userID), "orphankey", uuidArg(itemID))
	if err != nil {
		t.Fatalf("seed key-orphan user_data: %v", err)
	}

	// Delete detached rows.
	deleted, err := store.DeleteDetachedUserData(ctx, stores)
	if err != nil {
		t.Fatalf("DeleteDetachedUserData: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted %d rows, want 2", deleted)
	}

	// Verify the attached row survives.
	var count int
	err = w.QueryRowContext(ctx,
		`SELECT count(*) FROM user_data WHERE user_id = `+ph(1)+` AND custom_key = `+ph(2),
		uuidArg(userID), "mykey").Scan(&count)
	if err != nil {
		t.Fatalf("count attached: %v", err)
	}
	if count != 1 {
		t.Errorf("attached user_data count = %d, want 1 (row was wrongly deleted)", count)
	}

	// Verify both orphan rows are gone.
	err = w.QueryRowContext(ctx,
		`SELECT count(*) FROM user_data WHERE custom_key IN (`+ph(1)+`, `+ph(2)+`)`,
		"orphan", "orphankey").Scan(&count)
	if err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if count != 0 {
		t.Errorf("orphan user_data count = %d, want 0", count)
	}
}

func openPostgresFromDSN(t *testing.T, dsn string) store.Stores {
	t.Helper()
	suffix := make([]byte, 8)
	if _, err := cryptorand.Read(suffix); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	schema := "storetest_" + hex.EncodeToString(suffix)
	stores, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open PostgreSQL store schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		_ = stores.Close()
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		_, _ = admin.ExecContext(t.Context(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = admin.Close()
	})
	return stores
}
