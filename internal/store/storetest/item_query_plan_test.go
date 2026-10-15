// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreQueryPlanSQLiteHotRelations pins the access paths for the
// relation families ItemQuery translates. Each query is a scoped source-shaped
// lookup, so a broad scan here means a hot request scales with an unrelated
// library rather than its supplied relation key.
func TestItemStoreQueryPlanSQLiteHotRelations(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite query-plan fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite query-plan fixture: %v", err)
		}
	})
	assertItemQueryHotPlans(t, stores)
}

// TestItemStoreQueryPlanPostgresHotRelations applies the identical index and
// access-path contract to the opt-in PostgreSQL backend.
func TestItemStoreQueryPlanPostgresHotRelations(t *testing.T) {
	assertItemQueryHotPlans(t, storetestpkg.OpenPostgres(t))
}

// TestDerivedScopePlanSQLite pins the plan of the scoped-derived subtree read
// on an ANALYZEd SQLite items table. See assertDerivedScopePlan.
func TestDerivedScopePlanSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertDerivedScopePlan(t, stores)
}

// TestDerivedScopePlanPostgres applies the same contract to PostgreSQL.
func TestDerivedScopePlanPostgres(t *testing.T) {
	assertDerivedScopePlan(t, storetestpkg.OpenPostgres(t))
}

// assertDerivedScopePlan seeds about 5,000 items (one root, 1,000 folders
// below it, four movies below each folder), runs ANALYZE, and asserts that the
// subtree read of one folder does not scan items. When the statement joined
// its recursive CTE back to items, both planners read the whole table for a
// five-row subtree: PostgreSQL hashed a sequential scan of items and SQLite
// scanned items against a bloom filter on the CTE. The PostgreSQL check keeps
// sequential plans enabled, so the planner chooses by its costs.
func assertDerivedScopePlan(t *testing.T, stores store.Stores) {
	t.Helper()
	const folders, moviesPerFolder = 1000, 4
	placeholder := stores.Dialect().Placeholder
	postgres := placeholder(1) != "?"
	uuidArg := func(n int) any {
		id := itemQueryPlanUUID(t, n)
		if postgres {
			return id.Dashed()
		}
		return id[:]
	}
	marks := make([]string, 5)
	for index := range marks {
		marks[index] = placeholder(index + 1)
	}
	insert := "INSERT INTO items (id, kind, parent_id, name, playlist_owner_user_id, source_type, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access) " +
		"VALUES (" + strings.Join(marks, ", ") + ", '0', " + placeholder(6) + ", FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE)"
	tx, err := stores.Writer().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	add := func(n int, kind item.Kind, parent any, folder bool) {
		if _, err := tx.ExecContext(t.Context(), insert, uuidArg(n), kind.String(), parent, fmt.Sprintf("item %d", n), uuidArg(0), folder); err != nil {
			t.Fatalf("seed item %d: %v", n, err)
		}
	}
	add(1, item.KindFolder, nil, true)
	next := 2 + folders
	for folder := 2; folder < 2+folders; folder++ {
		add(folder, item.KindFolder, uuidArg(1), true)
		for range moviesPerFolder {
			add(next, item.KindMovie, uuidArg(folder), false)
			next++
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if _, err := stores.Writer().ExecContext(t.Context(), "ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	statement, args := store.NewItemStore(stores).DerivedScopeStatement([]dto.UUID{itemQueryPlanUUID(t, 2+folders/2)})
	if !postgres {
		assertScopedPlanAvoidsFullScans(t, stores, statement, args, []string{"items"})
		return
	}
	rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN (COSTS FALSE) "+statement, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", statement, err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close plan rows: %v", err)
	}
	joined := strings.Join(plan, "\n")
	if strings.Contains(joined, "Seq Scan on items") {
		t.Fatalf("subtree read scans all of items:\n%s", joined)
	}
	if !strings.Contains(joined, "items_pkey") {
		t.Fatalf("subtree read does not read the roots through items_pkey:\n%s", joined)
	}
}

type itemQueryPlanFixture struct {
	root, series, movie, playlist dto.UUID
	user, person                  dto.UUID
	customKey                     string
	valueID                       int64
}

func assertItemQueryHotPlans(t *testing.T, stores store.Stores) {
	t.Helper()
	fixture := newItemQueryPlanFixture(t, stores)
	placeholder := stores.Dialect().Placeholder
	uuidArg := func(id dto.UUID) any {
		if placeholder(1) == "?" {
			return id[:]
		}
		return id.Dashed()
	}
	cases := []struct {
		name   string
		sql    string
		args   []any
		tables []string
	}{
		{name: "parent scope", sql: "SELECT id FROM items WHERE parent_id = " + placeholder(1), args: []any{uuidArg(fixture.root)}, tables: []string{"items"}},
		{name: "provider value", sql: "SELECT item_id FROM provider_ids WHERE provider = " + placeholder(1) + " AND value = " + placeholder(2), args: []any{"Tmdb", "plan-provider"}, tables: []string{"provider_ids"}},
		{name: "user data item state", sql: "SELECT item_id FROM user_data WHERE user_id = " + placeholder(1) + " AND item_id = " + placeholder(2), args: []any{uuidArg(fixture.user), uuidArg(fixture.movie)}, tables: []string{"user_data"}},
		{name: "shared user data key", sql: "SELECT item_id FROM item_user_data_keys WHERE custom_key = " + placeholder(1), args: []any{fixture.customKey}, tables: []string{"item_user_data_keys"}},
		{name: "normalized value", sql: "SELECT item_id FROM item_value_map WHERE value_id = " + placeholder(1), args: []any{fixture.valueID}, tables: []string{"item_value_map"}},
		{name: "person credit", sql: "SELECT item_id FROM people_map WHERE person_id = " + placeholder(1) + " AND type = " + placeholder(2), args: []any{uuidArg(fixture.person), item.PersonKindActor.String()}, tables: []string{"people_map"}},
		{name: "media stream", sql: "SELECT item_id FROM media_streams WHERE stream_type = " + placeholder(1) + " AND codec = " + placeholder(2), args: []any{fmt.Sprintf("%d", playback.StreamTypeVideo), "h264"}, tables: []string{"media_streams"}},
		{name: "ancestor closure", sql: "SELECT item_id FROM ancestors WHERE ancestor_id = " + placeholder(1), args: []any{uuidArg(fixture.root)}, tables: []string{"ancestors"}},
		{name: "series grouping", sql: "SELECT id FROM items WHERE series_id = " + placeholder(1), args: []any{uuidArg(fixture.series)}, tables: []string{"items"}},
		{name: "share access", sql: "SELECT item_id FROM item_shares WHERE user_id = " + placeholder(1), args: []any{uuidArg(fixture.user)}, tables: []string{"item_shares"}},
	}
	mediaSourcePage, mediaSourceArgs := store.NewItemStore(stores).MediaSourceRecordsPageStatement([]dto.UUID{fixture.movie, fixture.series})
	cases = append(cases, struct {
		name   string
		sql    string
		args   []any
		tables []string
	}{name: "media source page", sql: mediaSourcePage, args: mediaSourceArgs, tables: []string{"items"}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertScopedPlanAvoidsFullScans(t, stores, tc.sql, tc.args, tc.tables)
		})
	}
	t.Run("migration indexes", func(t *testing.T) {
		assertItemQueryPlanIndexes(t, stores)
	})
}

func newItemQueryPlanFixture(t *testing.T, stores store.Stores) itemQueryPlanFixture {
	t.Helper()
	items := store.NewItemStore(stores)
	fixture := itemQueryPlanFixture{
		root:     itemQueryPlanUUID(t, 80_001),
		series:   itemQueryPlanUUID(t, 80_002),
		movie:    itemQueryPlanUUID(t, 80_003),
		playlist: itemQueryPlanUUID(t, 80_006),
		user:     itemQueryPlanUUID(t, 80_004),
		person:   itemQueryPlanUUID(t, 80_005),
	}
	root := item.Item{ID: fixture.root, Kind: item.KindFolder, Name: itemQueryPlanString("plan root")}
	series := item.Item{ID: fixture.series, Kind: item.KindSeries, ParentID: &fixture.root, Name: itemQueryPlanString("plan series")}
	movie := item.Item{
		ID:       fixture.movie,
		Kind:     item.KindMovie,
		ParentID: &fixture.series,
		Name:     itemQueryPlanString("Plan Movie"),
		SortName: itemQueryPlanString("Plan Movie"),
		Values:   item.ItemValues{Genres: []string{"Plan Genre"}},
		People:   []item.PersonCredit{{PersonID: fixture.person, Name: "Plan Person", Type: item.PersonKindActor}},
	}
	movie.ProviderIDs.Set("Tmdb", "plan-provider")
	playlist := item.Item{ID: fixture.playlist, Kind: item.KindPlaylist, PlaylistOwnerUserID: fixture.user, Name: itemQueryPlanString("plan playlist")}
	for _, it := range []item.Item{root, series, movie, playlist} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert query-plan item %s: %v", it.ID, err)
		}
	}
	if err := items.ReplaceStreams(t.Context(), fixture.movie, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}}); err != nil {
		t.Fatalf("replace query-plan streams: %v", err)
	}
	if err := items.ReplaceShares(t.Context(), fixture.playlist, []store.ItemShare{{UserID: fixture.user, CanEdit: true}}); err != nil {
		t.Fatalf("replace query-plan shares: %v", err)
	}
	for _, key := range item.UserDataKeys(movie, item.UserDataInputs{}) {
		if key != fixture.movie.String() {
			fixture.customKey = key
			break
		}
	}
	if fixture.customKey == "" {
		t.Fatal("query-plan fixture has no shared user-data key")
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: fixture.user, CustomKey: fixture.customKey, ItemID: fixture.movie, Played: true}); err != nil {
		t.Fatalf("upsert query-plan user data: %v", err)
	}
	placeholder := stores.Dialect().Placeholder
	if err := stores.Reader().QueryRowContext(t.Context(), "SELECT id FROM item_values WHERE kind = "+placeholder(1)+" AND value = "+placeholder(2), "genre", "Plan Genre").Scan(&fixture.valueID); err != nil {
		t.Fatalf("read normalized query-plan genre ID: %v", err)
	}
	return fixture
}

func assertScopedPlanAvoidsFullScans(t *testing.T, stores store.Stores, statement string, args []any, tables []string) {
	t.Helper()
	if stores.Dialect().Placeholder(1) == "?" {
		rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, args...)
		if err != nil {
			t.Fatalf("explain SQLite query %q: %v", statement, err)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				t.Errorf("close SQLite query-plan rows: %v", err)
			}
		}()
		var detail []string
		for rows.Next() {
			var id, parent, unused int
			var value string
			if err := rows.Scan(&id, &parent, &unused, &value); err != nil {
				t.Fatalf("scan SQLite query plan: %v", err)
			}
			detail = append(detail, value)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read SQLite query plan: %v", err)
		}
		for _, table := range tables {
			for _, step := range detail {
				if strings.Contains(strings.ToUpper(step), "SCAN "+strings.ToUpper(table)) {
					t.Fatalf("SQLite plan broadly scans %s for scoped query %q: %s", table, statement, strings.Join(detail, " | "))
				}
			}
		}
		return
	}

	tx, err := stores.Reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin PostgreSQL plan transaction: %v", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Errorf("rollback PostgreSQL query-plan transaction: %v", err)
		}
	}()
	if _, err := tx.ExecContext(t.Context(), "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable PostgreSQL sequential plans: %v", err)
	}
	rows, err := tx.QueryContext(t.Context(), "EXPLAIN (COSTS FALSE) "+statement, args...)
	if err != nil {
		t.Fatalf("explain PostgreSQL query %q: %v", statement, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close PostgreSQL query-plan rows: %v", err)
		}
	}()
	var detail []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan PostgreSQL query plan: %v", err)
		}
		detail = append(detail, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read PostgreSQL query plan: %v", err)
	}
	for _, table := range tables {
		if strings.Contains(strings.ToLower(strings.Join(detail, "\n")), "seq scan on "+strings.ToLower(table)) {
			t.Fatalf("PostgreSQL plan broadly scans %s for scoped query %q: %s", table, statement, strings.Join(detail, " | "))
		}
	}
}

func assertItemQueryPlanIndexes(t *testing.T, stores store.Stores) {
	t.Helper()
	requirements := map[string][]string{
		"provider_ids":        {"ix_provider_ids_provider_value_item"},
		"item_value_map":      {"ix_item_value_map_value_item"},
		"people_map":          {"ix_people_map_person_type_item"},
		"media_streams":       {"ix_media_streams_type_codec_item"},
		"items":               {"ix_items_series", "ix_items_top_parent"},
		"item_shares":         {"ix_item_shares_user_item"},
		"item_user_data_keys": {"ix_item_user_data_keys_key"},
	}
	for table, indexes := range requirements {
		for _, index := range indexes {
			t.Run(table+"/"+index, func(t *testing.T) {
				var count int
				var err error
				if stores.Dialect().Placeholder(1) == "?" {
					err = stores.Reader().QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_index_list(?) WHERE name = ?", table, index).Scan(&count)
				} else {
					err = stores.Reader().QueryRowContext(t.Context(), "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1 AND indexname = $2", table, index).Scan(&count)
				}
				if err != nil {
					t.Fatalf("inspect index %q for %s: %v", index, table, err)
				}
				if count != 1 {
					t.Fatalf("migration index %q for %s count = %d, want 1", index, table, count)
				}
			})
		}
	}
}

func itemQueryPlanUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse query-plan UUID %d: %v", value, err)
	}
	return id
}

func itemQueryPlanString(value string) *string { return &value }
