// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreResumeLargeCardinality exercises the Resume query path against a
// database that matches the measured production scale: at least 11,124 items and
// 10,000 user-data rows. It prints separate count and page durations, prints
// EXPLAIN QUERY PLAN output for both statements, reports full scans and
// correlated loops, and asserts the result IDs, total count, paging, direct
// match, shared custom-key match, and alternate-version winner.
func TestItemStoreResumeLargeCardinality(t *testing.T) {
	items, stores := openLargeCardinalityStore(t)
	ctx := t.Context()
	dialect := stores.Dialect()

	userID := largeCardUUID(t, 9000)

	// Build a library with 11,124 items: 11,118 filler movies and 6 test
	// items that exercise direct matches, shared custom-key matches, and
	// alternate-version selection. Insert fillers via raw SQL to avoid the
	// per-item repair-derived overhead; test items use the public API.
	const fillerCount = 11118
	const testItemCount = 6
	const totalItems = fillerCount + testItemCount
	if totalItems < 11124 {
		t.Fatalf("fixture item count %d is below the required 11,124", totalItems)
	}

	// Insert filler items via raw SQL. The Resume query only needs id, kind,
	// sort_name, is_folder, is_virtual_item, and source_type (NOT NULL).
	fillerName := "filler"
	sourceType := "Library"
	falseValue := dialect.Boolean(false)
	writer := stores.Writer()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning filler insert tx: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO items (id, kind, sort_name, source_type, playlist_owner_user_id, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access) VALUES ("+dialect.Placeholder(1)+", "+dialect.Placeholder(2)+", "+dialect.Placeholder(3)+", "+dialect.Placeholder(4)+", "+dialect.Placeholder(5)+", "+falseValue+", "+falseValue+", "+falseValue+", "+falseValue+", "+dialect.Boolean(true)+", "+falseValue+", "+falseValue+", "+falseValue+", "+falseValue+")")
	if err != nil {
		t.Fatalf("preparing filler insert: %v", err)
	}
	for i := 1; i <= fillerCount; i++ {
		id := largeCardUUID(t, i)
		if _, err := stmt.ExecContext(ctx, id[:], item.KindMovie.String(), fillerName, sourceType, make([]byte, 16)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("inserting filler item %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("closing filler insert stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing filler items: %v", err)
	}

	// Test items: direct match, shared-key match, and alternate versions.
	// These use the public Upsert API so item_user_data_keys are populated.
	directID := largeCardUUID(t, 10001)
	sharedSourceID := largeCardUUID(t, 20002)
	sharedCandidateID := largeCardUUID(t, 10003)
	alternatePrimaryID := largeCardUUID(t, 10004)
	alternateFirstID := largeCardUUID(t, 10005)
	alternateSecondID := largeCardUUID(t, 10006)

	testItemSlice := []item.Item{
		{ID: directID, Kind: item.KindMovie, Name: stringPtr("direct"), SortName: stringPtr("same")},
		{ID: sharedCandidateID, Kind: item.KindMovie, Name: stringPtr("shared candidate"), SortName: stringPtr("same")},
		{ID: alternatePrimaryID, Kind: item.KindMovie, Name: stringPtr("alt primary"), SortName: stringPtr("same")},
		{ID: alternateFirstID, Kind: item.KindMovie, Name: stringPtr("alt first"), SortName: stringPtr("same"), PrimaryVersionID: &alternatePrimaryID},
		{ID: alternateSecondID, Kind: item.KindMovie, Name: stringPtr("alt second"), SortName: stringPtr("same"), PrimaryVersionID: &alternatePrimaryID},
	}
	// sharedCandidateID shares a Tmdb provider ID with sharedSourceID. Only
	// sharedCandidateID is in the items table; sharedSourceID exists only as
	// the item_id in user_data, so the shared custom-key match brings in
	// sharedCandidateID without also matching sharedSourceID as a direct item.
	testItemSlice[1].ProviderIDs.Set("Tmdb", "shared-user-data-key")
	for _, it := range testItemSlice {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upserting test item %s: %v", it.ID, err)
		}
	}

	// Create 10,000 filler user-data rows via raw SQL. The Resume query starts
	// from user_data rows with position_ticks > 0, so all fillers are in-progress.
	const userDataFillerCount = 10000
	userDataStmt, err := writer.PrepareContext(ctx, "INSERT INTO user_data (user_id, custom_key, item_id, played, position_ticks, last_played) VALUES ("+dialect.Placeholder(1)+", "+dialect.Placeholder(2)+", "+dialect.Placeholder(3)+", "+dialect.Boolean(false)+", "+dialect.Placeholder(4)+", "+dialect.Placeholder(5)+")")
	if err != nil {
		t.Fatalf("preparing user_data insert: %v", err)
	}
	for i := 1; i <= userDataFillerCount; i++ {
		id := largeCardUUID(t, i)
		last := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		lastValue := dialect.TimeValue(last)
		if _, err := userDataStmt.ExecContext(ctx, userID[:], largeCardCustomKey(id), id[:], int64(1), lastValue); err != nil {
			t.Fatalf("inserting filler user data %d: %v", i, err)
		}
	}
	if err := userDataStmt.Close(); err != nil {
		t.Fatalf("closing user_data insert stmt: %v", err)
	}

	// Direct match: in-progress user data on the direct item.
	directLast := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(ctx, store.UserData{
		UserID:        userID,
		CustomKey:     largeCardCustomKey(directID),
		ItemID:        directID,
		PositionTicks: 1,
		LastPlayed:    &directLast,
	}); err != nil {
		t.Fatalf("upserting direct user data: %v", err)
	}

	// Shared custom-key match: user data on sharedSourceID (not in the items
	// table) whose custom key matches sharedCandidateID through
	// item_user_data_keys. The custom key is the Tmdb provider value that
	// both items would share.
	sharedKey := "shared-user-data-key"
	sharedLast := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(ctx, store.UserData{
		UserID:        userID,
		CustomKey:     sharedKey,
		ItemID:        sharedSourceID,
		PositionTicks: 1,
		LastPlayed:    &sharedLast,
	}); err != nil {
		t.Fatalf("upserting shared user data: %v", err)
	}

	// Alternate versions: primary and two alternates. The first alternate has
	// the greatest last_played, so it wins the alternate-version selection.
	altPrimaryLast := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	altFirstLast := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	altSecondLast := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: largeCardCustomKey(alternatePrimaryID), ItemID: alternatePrimaryID, PositionTicks: 1, LastPlayed: &altPrimaryLast}); err != nil {
		t.Fatalf("upserting alt primary user data: %v", err)
	}
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: largeCardCustomKey(alternateFirstID), ItemID: alternateFirstID, PositionTicks: 1, LastPlayed: &altFirstLast}); err != nil {
		t.Fatalf("upserting alt first user data: %v", err)
	}
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: largeCardCustomKey(alternateSecondID), ItemID: alternateSecondID, PositionTicks: 1, LastPlayed: &altSecondLast}); err != nil {
		t.Fatalf("upserting alt second user data: %v", err)
	}

	resumable := true
	query := store.ItemQuery{
		UserID:                 &userID,
		IsResumable:            &resumable,
		Recursive:              true,
		IncludeOwnedItems:      true,
		SortBy:                 []store.ItemSortBy{store.ItemSortByDatePlayed},
		SortOrder:              []store.SortOrder{store.SortOrderDescending},
		EnableTotalRecordCount: true,
	}

	// Translate to get the fixed SQL for EXPLAIN.
	translated, err := store.Translate(query, dialect)
	if err != nil {
		t.Fatalf("translating resume query: %v", err)
	}

	// Print the PRE-FIX plan: the unbounded correlated Resume relation the
	// translator produced before this change. The pre-fix shape starts from
	// every item and runs a per-item correlated alternate-version subquery
	// whose COALESCE join key cannot seek an index, plus a repeated per-item
	// user_data MAX(last_played) lookup. Report every full scan and
	// correlated loop it contains.
	preFixSQL, preFixArgs := preFixResumeCountSQL(t, dialect, userID)
	t.Log("=== EXPLAIN QUERY PLAN: PRE-FIX count statement ===")
	preFixRows, err := stores.Reader().QueryContext(ctx, "EXPLAIN QUERY PLAN "+preFixSQL, preFixArgs...)
	if err != nil {
		t.Fatalf("explaining pre-fix count statement: %v", err)
	}
	preFixFindings := printExplainSection(t, preFixRows, "PRE-FIX")
	if preFixFindings == 0 {
		t.Fatalf("PRE-FIX plan reported no full scan or correlated loop; the pre-fix query must show the unbounded defect")
	}
	t.Logf("PRE-FIX plan reported %d prohibited scans or loops", preFixFindings)

	// Print the FIXED plan for the count statement and reject prohibited
	// scans or loops: no item-table full scan and no unbounded per-item
	// user-data loop in the Resume relation.
	t.Log("=== EXPLAIN QUERY PLAN: FIXED count statement ===")
	countRows, err := stores.Reader().QueryContext(ctx, "EXPLAIN QUERY PLAN "+translated.CountSQL, translated.CountArgs...)
	if err != nil {
		t.Fatalf("explaining fixed count statement: %v", err)
	}
	assertResumePlanRejectsProhibited(t, countRows)

	// Print the FIXED plan for the page statement and reject prohibited
	// scans or loops.
	pageQuery := query
	pageLimit := 12
	pageStart := 0
	pageQuery.Limit = &pageLimit
	pageQuery.StartIndex = &pageStart
	pageTranslated, err := store.Translate(pageQuery, dialect)
	if err != nil {
		t.Fatalf("translating page query: %v", err)
	}
	t.Log("=== EXPLAIN QUERY PLAN: FIXED page statement ===")
	pageRows, err := stores.Reader().QueryContext(ctx, "EXPLAIN QUERY PLAN "+pageTranslated.SQL, pageTranslated.Args...)
	if err != nil {
		t.Fatalf("explaining fixed page statement: %v", err)
	}
	assertResumePlanRejectsProhibited(t, pageRows)

	// Measure the count duration separately.
	realClock := clock.NewReal()
	countStart := realClock.Now()
	var countResult int64
	err = stores.Reader().QueryRowContext(ctx, translated.CountSQL, translated.CountArgs...).Scan(&countResult)
	countDuration := realClock.Now().Sub(countStart)
	if err != nil {
		t.Fatalf("running count statement: %v", err)
	}
	t.Logf("count_duration=%s", countDuration)

	// Measure the page duration separately.
	pageStartTimer := realClock.Now()
	pageResult, err := items.Query(ctx, pageQuery)
	pageDuration := realClock.Now().Sub(pageStartTimer)
	if err != nil {
		t.Fatalf("running page query: %v", err)
	}
	t.Logf("page_duration=%s", pageDuration)

	// Assert the total count. The expected set is: 10,000 filler items + 1
	// direct + 1 shared candidate + 1 alternate winner (alternateFirst).
	const expectedTotal = userDataFillerCount + 3
	if int(countResult) != expectedTotal {
		t.Errorf("count = %d, want %d", countResult, expectedTotal)
	}
	if pageResult.TotalRecordCount != expectedTotal {
		t.Errorf("page TotalRecordCount = %d, want %d", pageResult.TotalRecordCount, expectedTotal)
	}

	// Assert the page limit.
	if len(pageResult.Items) != pageLimit {
		t.Errorf("page item count = %d, want %d (limit)", len(pageResult.Items), pageLimit)
	}

	// The first three items in DatePlayed DESC order are: direct (Jul 10),
	// sharedCandidate (Jul 9), alternateFirst (Jul 8). After that, fillers
	// descend by timestamp.
	pageIDs := make([]dto.UUID, len(pageResult.Items))
	for i, it := range pageResult.Items {
		pageIDs[i] = it.ID
	}

	if len(pageIDs) < 3 {
		t.Fatalf("page returned %d items, need at least 3 for assertion", len(pageIDs))
	}

	if pageIDs[0] != directID {
		t.Errorf("page[0] = %s, want %s (direct match, greatest last_played)", pageIDs[0], directID)
	}
	if pageIDs[1] != sharedCandidateID {
		t.Errorf("page[1] = %s, want %s (shared custom-key match)", pageIDs[1], sharedCandidateID)
	}
	if pageIDs[2] != alternateFirstID {
		t.Errorf("page[2] = %s, want %s (alternate-version winner)", pageIDs[2], alternateFirstID)
	}

	// Verify offset behavior: request startIndex=1, limit=2 and assert that
	// the page returns exactly the two expected IDs.
	offsetQuery := query
	offsetLimit := 2
	offsetStart := 1
	offsetQuery.Limit = &offsetLimit
	offsetQuery.StartIndex = &offsetStart
	offsetResult, err := items.Query(ctx, offsetQuery)
	if err != nil {
		t.Fatalf("running offset query: %v", err)
	}
	if len(offsetResult.Items) != offsetLimit {
		t.Fatalf("offset query returned %d items, want exactly %d for StartIndex=%d Limit=%d", len(offsetResult.Items), offsetLimit, offsetStart, offsetLimit)
	}
	if offsetResult.Items[0].ID != sharedCandidateID {
		t.Errorf("offset[0] = %s, want %s (shared candidate at position 1)", offsetResult.Items[0].ID, sharedCandidateID)
	}
	if offsetResult.Items[1].ID != alternateFirstID {
		t.Errorf("offset[1] = %s, want %s (alternate winner at position 2)", offsetResult.Items[1].ID, alternateFirstID)
	}

	// Assert completion within 5 seconds.
	if countDuration >= 5*time.Second {
		t.Errorf("count_duration %s exceeds the 5-second limit", countDuration)
	}
	if pageDuration >= 5*time.Second {
		t.Errorf("page_duration %s exceeds the 5-second limit", pageDuration)
	}
}

// openLargeCardinalityStore opens an in-memory SQLite store for the
// large-cardinality test. The in-memory database is fast enough to build
// 11,124 items and 10,000 user-data rows within the test timeout.
func openLargeCardinalityStore(t *testing.T) (*store.ItemStore, store.Stores) {
	t.Helper()
	var stores store.Stores
	storetest.Run(t, func(t *testing.T) store.Stores {
		opened, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("opening large-cardinality SQLite fixture: %v", err)
		}
		stores = opened
		return opened
	})
	return store.NewItemStore(stores), stores
}

func largeCardUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing large-cardinality UUID %d: %v", value, err)
	}
	return id
}

func largeCardCustomKey(id dto.UUID) string {
	return "library/" + id.String()
}

// printExplainSection prints every EXPLAIN QUERY PLAN row under the given
// label and returns the count of prohibited findings: a full scan of a base
// table (items or user_data) or a correlated loop. CTE materialization scans
// (SCAN progress, SCAN ranked, SCAN (subquery-N)) are not base-table scans
// and do not count as findings.
func printExplainSection(t *testing.T, rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}, label string) int {
	t.Helper()
	defer rows.Close()
	findings := 0
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scanning %s explain row: %v", label, err)
		}
		t.Log(detail)
		lower := strings.ToLower(detail)
		if isBaseTableFullScan(lower) {
			t.Logf("%s FINDING: full scan of base table: %s", label, detail)
			findings++
		}
		if strings.Contains(lower, "correlated") {
			t.Logf("%s FINDING: correlated loop: %s", label, detail)
			findings++
		}
	}
	return findings
}

// assertResumePlanRejectsProhibited prints the FIXED Resume plan and fails the
// test if it contains a prohibited access path: a full scan of the items or
// user_data base table, or an unbounded per-item correlated user_data loop.
// Indexed SEARCH steps and CTE materialization scans are permitted.
func assertResumePlanRejectsProhibited(t *testing.T, rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}) {
	t.Helper()
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scanning fixed explain row: %v", err)
		}
		t.Log(detail)
		lower := strings.ToLower(detail)
		if isBaseTableFullScan(lower) {
			t.Fatalf("FIXED plan has a prohibited base-table full scan: %s", detail)
		}
		if strings.Contains(lower, "correlated") && strings.Contains(lower, "user_data") {
			t.Fatalf("FIXED plan has a prohibited correlated user_data loop: %s", detail)
		}
	}
}

// isBaseTableFullScan reports whether a plan detail line describes a full scan
// of the items or user_data base table. A SEARCH step that uses an index is
// not a full scan. A SCAN of a CTE subquery (progress, ranked, or a numbered
// subquery) is not a base-table scan.
func isBaseTableFullScan(lower string) bool {
	if !strings.Contains(lower, "scan") {
		return false
	}
	if strings.Contains(lower, "using index") || strings.Contains(lower, "using covering index") {
		return false
	}
	for _, table := range []string{"items", "user_data"} {
		if strings.Contains(lower, "scan "+table) {
			return true
		}
	}
	return false
}

// preFixResumeCountSQL builds the count statement the translator produced
// before this change: it starts from every item and applies a per-item
// correlated alternate-version subquery whose COALESCE join key cannot seek
// an index, plus a repeated per-item user_data MAX(last_played) lookup.
// The shape mirrors the old addResumableUserDataFilter non-series branch
// and the old datePlayedExpression. It is the unbounded defect the fix
// replaces.
func preFixResumeCountSQL(t *testing.T, dialect store.Dialect, userID dto.UUID) (string, []any) {
	t.Helper()
	var args []any
	ph := func() string {
		args = append(args, userID[:])
		return dialect.Placeholder(len(args))
	}
	userDataCandidate := func(alias string) string {
		return "(" + alias + ".item_id = items.id OR EXISTS (SELECT 1 FROM item_user_data_keys AS item_key WHERE item_key.item_id = items.id AND item_key.custom_key = " + alias + ".custom_key))"
	}
	inProgress := func(itemAlias string) string {
		return "EXISTS (SELECT 1 FROM user_data AS user_data WHERE user_data.user_id = " + ph() + " AND " + strings.ReplaceAll(userDataCandidate("user_data"), "items.id", itemAlias+".id") + " AND position_ticks > 0)"
	}
	maxLastPlayed := func(itemAlias string) string {
		return "(SELECT MAX(user_data.last_played) FROM user_data AS user_data WHERE user_data.user_id = " + ph() + " AND " + strings.ReplaceAll(userDataCandidate("user_data"), "items.id", itemAlias+".id") + " AND user_data.position_ticks > 0)"
	}
	maxAlt := func() string { return maxLastPlayed("alternate_sibling") }
	maxItems := func() string { return maxLastPlayed("items") }
	alternateWinner := "NOT EXISTS (SELECT 1 FROM items AS alternate_sibling WHERE alternate_sibling.id <> items.id AND COALESCE(alternate_sibling.primary_version_id, alternate_sibling.id) = COALESCE(items.primary_version_id, items.id) AND " + inProgress("alternate_sibling") + " AND ((" + maxAlt() + " IS NOT NULL AND (" + maxItems() + " IS NULL OR " + maxAlt() + " > " + maxItems() + ")) OR ((" + maxAlt() + " = " + maxItems() + " OR (" + maxAlt() + " IS NULL AND " + maxItems() + " IS NULL)) AND alternate_sibling.id < items.id)))"
	where := "(" + inProgress("items") + " AND " + alternateWinner + ")"
	// Restrict to movies so the plan matches the measured production shape.
	sql := "SELECT COUNT(*) FROM items WHERE items.kind = 'Movie' AND " + where
	return sql, args
}
