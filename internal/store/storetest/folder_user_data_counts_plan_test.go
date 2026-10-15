// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestFolderUserDataCountsPlanStartsFromRequestedFolders pins the plan of
// the folder played-count statement under the sqlite_stat1 rows of the
// library-scale benchmark (about 47,000 items). With plain JOINs, SQLite
// drove the linked-folder branch from a scan of all of ancestors and the
// season branch from every Season, and probed the requested folders last.
// Each Series or MusicAlbum on a DTO page then cost about 20 ms.
func TestFolderUserDataCountsPlanStartsFromRequestedFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	stores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	for _, statement := range []string{
		"ANALYZE",
		"DELETE FROM sqlite_stat1",
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES
			('items', 'ix_items_top_parent_latest', '47056 2 2 2 2 1'),
			('items', 'ix_items_top_parent', '47056 2'),
			('items', 'ix_items_primary_version_id', '47056 47056 1'),
			('items', 'ix_items_series', '47056 233'),
			('items', 'ix_items_path', '47056 47056'),
			('items', 'ix_items_presentation_unique_key', '47056 40'),
			('items', 'ix_items_clean_name', '47056 2'),
			('items', 'ix_items_kind_top', '47056 4278 2'),
			('items', 'ix_items_parent', '47056 23'),
			('items', 'sqlite_autoindex_items_1', '47056 1'),
			('ancestors', 'ix_ancestors_item', '59906 4 1'),
			('ancestors', 'sqlite_autoindex_ancestors_1', '59906 30 1'),
			('item_user_data_keys', 'ix_item_user_data_keys_key', '104306 1 1'),
			('item_user_data_keys', 'ix_item_user_data_keys_lookup', '104306 3 3 1'),
			('item_user_data_keys', 'sqlite_autoindex_item_user_data_keys_1', '104306 3 1'),
			('user_data', 'ix_user_data_user_item', '2000 2000 1'),
			('user_data', 'ix_user_data_user_in_progress', '2000 2000 2000 1 1 1'),
			('user_data', 'sqlite_autoindex_user_data_1', '2000 2000 1')`,
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
	user := store.User{ID: itemQueryPlanUUID(t, 9), Policy: []byte(`{"IsAdministrator":true}`)}
	statement, args := store.NewItemStore(stores).FolderUserDataCountsStatement(user.ID, []dto.UUID{itemQueryPlanUUID(t, 1), itemQueryPlanUUID(t, 2)}, &user)
	rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var detail []string
	for rows.Next() {
		var id, parent, unused int
		var value string
		if err := rows.Scan(&id, &parent, &unused, &value); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		detail = append(detail, value)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close plan rows: %v", err)
	}
	plan := strings.Join(detail, " | ")
	for _, step := range detail {
		// The season and linked-folder rows must come from lookups by the
		// requested folder, not from a scan or a search by kind.
		if strings.HasPrefix(step, "SCAN descendant") || strings.HasPrefix(step, "SCAN linked") || strings.Contains(step, "ix_items_kind_top") {
			t.Fatalf("plan does not start from the requested folders: %s", plan)
		}
	}
	for _, want := range []string{
		"SEARCH season USING INDEX sqlite_autoindex_items_1 (id=?)",
		"SEARCH descendant USING COVERING INDEX sqlite_autoindex_ancestors_1 (ancestor_id=?)",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan lacks %q: %s", want, plan)
		}
	}
}
