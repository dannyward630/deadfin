// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestMediaSourceRecordsPagePlanWithStaleStatistics pins the plan of the DTO
// media-source statement under the sqlite_stat1 rows of a live library that
// grew from about 10,000 items without a new ANALYZE. With those statistics,
// "id IN (...) OR presentation_unique_key IN (...) ORDER BY id" walked the
// whole primary-key index, so each DTO page read the whole items table.
func TestMediaSourceRecordsPagePlanWithStaleStatistics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	stores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	for _, statement := range []string{
		"ANALYZE",
		"DELETE FROM sqlite_stat1",
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES
			('items', 'ix_items_primary_version_id', '10287 125 1'),
			('items', 'ix_items_series', '10287 95'),
			('items', 'ix_items_path', '10287 1'),
			('items', 'ix_items_presentation_unique_key', '10287 61'),
			('items', 'ix_items_clean_name', '10287 1'),
			('items', 'ix_items_kind_top', '10287 618 463'),
			('items', 'ix_items_parent', '10287 6'),
			('items', 'sqlite_autoindex_items_1', '10287 1')`,
	} {
		if _, err := stores.Writer().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seed statistics %q: %v", statement, err)
		}
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("close SQLite: %v", err)
	}
	// Reopen, so every connection loads the seeded statistics.
	stores, err = sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	statement, args := store.NewItemStore(stores).MediaSourceRecordsPageStatement([]dto.UUID{itemQueryPlanUUID(t, 1), itemQueryPlanUUID(t, 2)})
	assertScopedPlanAvoidsFullScans(t, stores, statement, args, []string{"items"})
}
