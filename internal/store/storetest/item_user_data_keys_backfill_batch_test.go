// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
	moderncsqlite "modernc.org/sqlite"
)

const backfillBatchItemCount = 902

var errBackfillKeyInsert = errors.New("injected item user-data key insert failure")

// TestItemStoreBackfillBatchesLegacySQLite exercises the actual migration
// boundary with more IDs than SQLite can put in one conservative relation IN
// clause. The instrumented driver remains a real modernc SQLite connection: it
// observes the statements and largest item page without replacing SQL behavior.
func TestItemStoreBackfillBatchesLegacySQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-batch-user-data-keys.db")
	fixture := createLegacyBackfillFixtureSQLite(t, path)
	stats := &backfillStatementStats{failAfterKeyInserts: 1}
	stores := openBackfillCountingSQLite(t, path, stats)
	if err := migrate.Up(t.Context(), stores.Writer(), migrate.SQLite); err != nil {
		t.Fatalf("migrate legacy SQLite batch fixture: %v", err)
	}
	stats.reset()
	stats.failAfterKeyInserts = 1

	// Failing after one successful batch proves all already-written rows share
	// the one backfill transaction and are rolled back rather than partially
	// publishing migrated mappings.
	err := store.BackfillMissingItemUserDataKeys(t.Context(), stores)
	if !errors.Is(err, errBackfillKeyInsert) {
		t.Fatalf("backfill with injected key write failure = %v, want %v", err, errBackfillKeyInsert)
	}
	assertBackfillKeyCount(t, stores.Writer(), 0)

	stats.reset()
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		t.Fatalf("backfill migrated SQLite fixture: %v", err)
	}
	assertBackfillBatchSQLiteWork(t, stats, len(fixture.expected))
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
	if err := stores.Close(); err != nil {
		t.Fatalf("close counted SQLite batch fixture: %v", err)
	}

	// sqlite.Open is the production startup seam. A second real open must keep
	// the complete canonical set unchanged. A counted direct rerun then proves
	// the same no-op path does only its empty candidate-page query.
	reopened, err := storesqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen migrated SQLite batch fixture: %v", err)
	}
	assertBackfillKeyRows(t, reopened.Writer(), fixture.expected)
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened SQLite batch fixture: %v", err)
	}

	stats = &backfillStatementStats{}
	stores = openBackfillCountingSQLite(t, path, stats)
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		t.Fatalf("rerun complete SQLite backfill: %v", err)
	}
	if got := stats.readerStatements(); got != 0 {
		t.Fatalf("idempotent backfill reader statements = %d, want 0", got)
	}
	if got := stats.writerQueries(); got != 1 {
		t.Fatalf("idempotent backfill writer reads = %d, want one empty candidate-page query", got)
	}
	if got := stats.writerKeyInserts(); got != 0 {
		t.Fatalf("idempotent backfill key insert statements = %d, want 0", got)
	}
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
}

// TestItemStoreBackfillBatchesLegacyPostgres runs the same migrated-database
// behavior through PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is configured.
// The SQLite driver test owns physical statement and page measurements; this
// test protects PostgreSQL placeholders, boolean values, and multi-row insert
// execution at the real backend seam.
func TestItemStoreBackfillBatchesLegacyPostgres(t *testing.T) {
	stores := storetestpkg.OpenPostgres(t)
	fixture := seedBackfillFixture(t, stores)
	if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.Postgres, 4); err != nil {
		t.Fatalf("downgrade PostgreSQL batch fixture to pre-key schema: %v", err)
	}
	if err := migrate.Up(t.Context(), stores.Writer(), migrate.Postgres); err != nil {
		t.Fatalf("migrate PostgreSQL batch fixture: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		t.Fatalf("backfill migrated PostgreSQL fixture: %v", err)
	}
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		t.Fatalf("rerun complete PostgreSQL backfill: %v", err)
	}
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
}

// TestItemStoreRepairDerivedRebuildsUserDataKeysInBatchesSQLite protects the
// public mutation path whose one writer transaction rebuilds every key after
// hierarchy repair. It must retain only bounded item pages and use the same
// eight satellite reads and 300-row key insert batches as startup backfill.
func TestItemStoreRepairDerivedRebuildsUserDataKeysInBatchesSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-derived-user-data-keys.db")
	fixture := createCurrentBackfillFixtureSQLite(t, path)
	stats := &backfillStatementStats{}
	stores := openBackfillCountingSQLite(t, path, stats)
	if err := store.NewItemStore(stores).RepairDerived(t.Context()); err != nil {
		t.Fatalf("repair derived batch fixture: %v", err)
	}
	assertRebuildBatchSQLiteWork(t, stats, len(fixture.expected))
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
}

// TestItemStoreRepairDerivedRebuildsUserDataKeysInBatchesPostgres ensures the
// same full-library mutation path accepts PostgreSQL UUID, boolean, and
// multi-row parameter semantics when the opt-in backend is configured.
func TestItemStoreRepairDerivedRebuildsUserDataKeysInBatchesPostgres(t *testing.T) {
	stores := storetestpkg.OpenPostgres(t)
	fixture := seedBackfillFixture(t, stores)
	if err := store.NewItemStore(stores).RepairDerived(t.Context()); err != nil {
		t.Fatalf("repair derived PostgreSQL batch fixture: %v", err)
	}
	assertBackfillKeyRows(t, stores.Writer(), fixture.expected)
}

type backfillFixture struct {
	expected []backfillKeyRow
}

func createLegacyBackfillFixtureSQLite(t *testing.T, path string) backfillFixture {
	t.Helper()
	legacy, err := storesqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open SQLite legacy batch fixture: %v", err)
	}
	fixture := seedBackfillFixture(t, legacy)
	if err := migrate.DownTo(t.Context(), legacy.Writer(), migrate.SQLite, 4); err != nil {
		_ = legacy.Close()
		t.Fatalf("downgrade SQLite batch fixture to pre-key schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close SQLite pre-key batch fixture: %v", err)
	}
	return fixture
}

func createCurrentBackfillFixtureSQLite(t *testing.T, path string) backfillFixture {
	t.Helper()
	stores, err := storesqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open SQLite current batch fixture: %v", err)
	}
	fixture := seedBackfillFixture(t, stores)
	if err := stores.Close(); err != nil {
		t.Fatalf("close SQLite current batch fixture: %v", err)
	}
	return fixture
}

func seedBackfillFixture(t *testing.T, stores store.Stores) backfillFixture {
	t.Helper()
	items := store.NewItemStore(stores)
	parentID := backfillBatchUUID(t, 630_000)
	parent := item.Item{ID: parentID, Kind: item.KindFolder, Name: backfillBatchString("batch root")}
	if err := items.Upsert(t.Context(), parent); err != nil {
		t.Fatalf("upsert batch fixture parent: %v", err)
	}

	children := make([]item.Item, backfillBatchItemCount)
	for index := range children {
		id := backfillBatchUUID(t, 630_001+index)
		name := fmt.Sprintf("batch item %04d", index)
		children[index] = item.Item{
			ID:       id,
			Kind:     item.KindMovie,
			ParentID: &parentID,
			Name:     backfillBatchString(name),
		}
		children[index].ProviderIDs.Set("Tmdb", fmt.Sprintf("tmdb-batch-%04d", index))
	}

	// One fully populated item makes every satellite family carry a real row;
	// the remaining 901 rows make the loader cross its 900-ID SQLite boundary.
	children[0].ProviderIDs.Set("Imdb", "imdb-batch-sample")
	children[0].ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "batch-primary.jpg"}}
	children[0].LockedFields = []item.MetadataField{item.MetadataFieldCast}
	children[0].Values = item.ItemValues{Genres: []string{"batch genre"}, AlbumArtists: []string{"batch artist"}}
	children[0].People = []item.PersonCredit{{PersonID: backfillBatchUUID(t, 631_000), Name: "Batch Person", Type: item.PersonKindActor}}
	children[0].TrailerTypes = []item.TrailerType{item.TrailerTypeLocalTrailer}
	children[0].LinkedChildren = []item.LinkedChildRef{{ItemID: parentID, Type: item.LinkedChildTypeManual}}
	children[0].AdditionalParts = []string{"batch-part-1.mkv"}
	children[1].Kind = item.KindLiveTvChannel
	children[1].ProviderIDs = item.ProviderIDs{}

	if _, err := items.ReconcileChildren(t.Context(), parentID, children); err != nil {
		t.Fatalf("seed %d backfill fixture children: %v", backfillBatchItemCount, err)
	}

	all := make([]item.Item, 0, len(children)+1)
	all = append(all, parent)
	all = append(all, children...)
	return backfillFixture{expected: expectedBackfillKeyRows(all)}
}

type backfillKeyRow struct {
	itemID              dto.UUID
	customKey           string
	isLiveTVChannelName bool
}

func expectedBackfillKeyRows(items []item.Item) []backfillKeyRow {
	rows := make([]backfillKeyRow, 0, len(items)*2)
	for _, it := range items {
		disabledInputs := item.UserDataInputs{DisableLiveTVChannelUserDataName: true}
		disabled := make(map[string]struct{})
		for _, key := range item.UserDataKeys(it, disabledInputs) {
			disabled[key] = struct{}{}
		}
		seen := make(map[string]struct{})
		for _, key := range item.UserDataKeys(it, item.UserDataInputs{}) {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			_, remainsWhenNameDisabled := disabled[key]
			rows = append(rows, backfillKeyRow{itemID: it.ID, customKey: key, isLiveTVChannelName: !remainsWhenNameDisabled})
		}
	}
	sort.Slice(rows, func(left, right int) bool {
		if order := bytes.Compare(rows[left].itemID[:], rows[right].itemID[:]); order != 0 {
			return order < 0
		}
		return rows[left].customKey < rows[right].customKey
	})
	return rows
}

func assertBackfillBatchSQLiteWork(t *testing.T, stats *backfillStatementStats, keyCount int) {
	t.Helper()
	const pageSize = 900
	pages := (backfillBatchItemCount + 1 + pageSize - 1) / pageSize // + parent
	if got := stats.readerStatements(); got != 0 {
		t.Fatalf("backfill reader statements = %d, want 0 because all work is in its writer transaction", got)
	}
	if got, want := stats.writerQueries(), 9*pages; got != want {
		t.Fatalf("backfill writer reads = %d, want %d: one candidate page plus eight satellite reads per 900-item page", got, want)
	}
	if got := stats.peakItemPageRows(); got > pageSize {
		t.Fatalf("backfill peak retained item page = %d rows, want at most %d", got, pageSize)
	}
	if got, want := stats.peakItemPageRows(), pageSize; got != want {
		t.Fatalf("backfill peak item page = %d rows, want %d to prove the >900 fixture crossed a bounded page", got, want)
	}
	if got, want := stats.writerKeyInserts(), (keyCount+299)/300; got != want {
		t.Fatalf("backfill key insert statements = %d, want %d multi-row batches of at most 300 rows", got, want)
	}
}

func assertBackfillKeyCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM item_user_data_keys").Scan(&got); err != nil {
		t.Fatalf("count item user-data keys: %v", err)
	}
	if got != want {
		t.Fatalf("item user-data key count = %d, want %d", got, want)
	}
}

func assertRebuildBatchSQLiteWork(t *testing.T, stats *backfillStatementStats, keyCount int) {
	t.Helper()
	const pageSize = 900
	pages := (backfillBatchItemCount + 1 + pageSize - 1) / pageSize // + parent
	if got := stats.readerStatements(); got != 0 {
		t.Fatalf("rebuild reader statements = %d, want 0 because RepairDerived owns one writer transaction", got)
	}
	if got, want := stats.itemPageQueries(), pages; got != want {
		t.Fatalf("rebuild item page reads = %d, want %d bounded 900-item pages", got, want)
	}
	if got, want := stats.satelliteQueries(), 8*pages; got != want {
		t.Fatalf("rebuild satellite reads = %d, want %d: eight relation reads per item page", got, want)
	}
	if got, want := stats.peakItemPageRows(), pageSize; got != want {
		t.Fatalf("rebuild peak item page = %d rows, want %d", got, want)
	}
	if got, want := stats.writerKeyDeletes(), 1; got != want {
		t.Fatalf("rebuild key delete statements = %d, want %d", got, want)
	}
	if got, want := stats.writerKeyInserts(), (keyCount+299)/300; got != want {
		t.Fatalf("rebuild key insert statements = %d, want %d multi-row batches of at most 300 rows", got, want)
	}
}

func assertBackfillKeyRows(t *testing.T, db *sql.DB, want []backfillKeyRow) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT item_id,custom_key,is_live_tv_channel_name FROM item_user_data_keys ORDER BY item_id,custom_key")
	if err != nil {
		t.Fatalf("query item user-data keys: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close item user-data key rows: %v", err)
		}
	}()
	got := make([]backfillKeyRow, 0, len(want))
	for rows.Next() {
		var raw []byte
		var row backfillKeyRow
		if err := rows.Scan(&raw, &row.customKey, &row.isLiveTVChannelName); err != nil {
			t.Fatalf("scan item user-data key: %v", err)
		}
		id, err := parseBackfillKeyUUID(raw)
		if err != nil {
			t.Fatalf("parse item user-data key ID %q: %v", raw, err)
		}
		row.itemID = id
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate item user-data keys: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("item user-data key rows = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("item user-data key row %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func parseBackfillKeyUUID(raw []byte) (dto.UUID, error) {
	if len(raw) == len(dto.UUID{}) {
		return dto.ParseUUID(hex.EncodeToString(raw))
	}
	return dto.ParseUUID(string(raw))
}

func backfillBatchUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse batch fixture UUID %d: %v", value, err)
	}
	return id
}

func backfillBatchString(value string) *string { return &value }

type backfillCountingStores struct {
	writer, reader *sql.DB
}

func (s backfillCountingStores) Writer() *sql.DB { return s.writer }
func (s backfillCountingStores) Reader() *sql.DB { return s.reader }
func (backfillCountingStores) Dialect() store.Dialect {
	return storesqlite.Dialect{}
}

func (s backfillCountingStores) Close() error {
	return errors.Join(s.reader.Close(), s.writer.Close())
}

type backfillStatementStats struct {
	mu                   sync.Mutex
	readerQueryCount     int
	writerQueryCount     int
	itemPageQueryCount   int
	satelliteQueryCount  int
	writerKeyInsertCount int
	writerKeyDeleteCount int
	keyInsertAttempts    int
	peakItemRows         int
	failAfterKeyInserts  int
}

func (s *backfillStatementStats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readerQueryCount = 0
	s.writerQueryCount = 0
	s.itemPageQueryCount = 0
	s.satelliteQueryCount = 0
	s.writerKeyInsertCount = 0
	s.writerKeyDeleteCount = 0
	s.keyInsertAttempts = 0
	s.peakItemRows = 0
	s.failAfterKeyInserts = 0
}

func (s *backfillStatementStats) recordQuery(reader bool, query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reader {
		s.readerQueryCount++
		return
	}
	s.writerQueryCount++
	if backfillItemPageQuery(query) {
		s.itemPageQueryCount++
	}
	if backfillSatelliteQuery(query) {
		s.satelliteQueryCount++
	}
}

func (s *backfillStatementStats) recordItemRows(rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rows > s.peakItemRows {
		s.peakItemRows = rows
	}
}

func (s *backfillStatementStats) beforeKeyMutation(query string) error {
	normalized := strings.ToUpper(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(normalized, "DELETE FROM ITEM_USER_DATA_KEYS") {
		s.writerKeyDeleteCount++
	}
	if !strings.Contains(normalized, "INSERT INTO ITEM_USER_DATA_KEYS") {
		return nil
	}
	s.keyInsertAttempts++
	if s.failAfterKeyInserts > 0 && s.keyInsertAttempts > s.failAfterKeyInserts {
		return errBackfillKeyInsert
	}
	s.writerKeyInsertCount++
	return nil
}

func backfillItemPageQuery(query string) bool {
	return strings.Contains(query, "FROM items WHERE NOT EXISTS") || strings.Contains(query, "FROM items ORDER BY id LIMIT") || (strings.Contains(query, "FROM items WHERE id >") && strings.Contains(query, "ORDER BY id LIMIT"))
}

func backfillSatelliteQuery(query string) bool {
	return strings.Contains(query, "WHERE item_id IN (") || strings.Contains(query, "WHERE m.item_id IN (") || strings.Contains(query, "WHERE parent_item_id IN (")
}

func (s *backfillStatementStats) readerStatements() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readerQueryCount
}

func (s *backfillStatementStats) writerQueries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writerQueryCount
}

func (s *backfillStatementStats) itemPageQueries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.itemPageQueryCount
}

func (s *backfillStatementStats) satelliteQueries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.satelliteQueryCount
}

func (s *backfillStatementStats) writerKeyInserts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writerKeyInsertCount
}

func (s *backfillStatementStats) writerKeyDeletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writerKeyDeleteCount
}

func (s *backfillStatementStats) peakItemPageRows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peakItemRows
}

var (
	registerBackfillCountingDriver sync.Once
	backfillDriver                 = &backfillCountingDriver{driver: &moderncsqlite.Driver{}, statsByDSN: make(map[string]*backfillStatementStats)}
)

func openBackfillCountingSQLite(t *testing.T, path string, stats *backfillStatementStats) store.Stores {
	t.Helper()
	registerBackfillCountingDriver.Do(func() {
		sql.Register("storetest-backfill-counting-sqlite", backfillDriver)
	})
	writerDSN := "file:" + path + "?_pragma=foreign_keys(ON)"
	readerDSN := writerDSN + "&_pragma=query_only(ON)"
	backfillDriver.register(writerDSN, stats)
	backfillDriver.register(readerDSN, stats)
	writer, err := sql.Open("storetest-backfill-counting-sqlite", writerDSN)
	if err != nil {
		t.Fatalf("open counting SQLite writer: %v", err)
	}
	reader, err := sql.Open("storetest-backfill-counting-sqlite", readerDSN)
	if err != nil {
		_ = writer.Close()
		t.Fatalf("open counting SQLite reader: %v", err)
	}
	stores := backfillCountingStores{writer: writer, reader: reader}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close counting SQLite stores: %v", err)
		}
	})
	return stores
}

type backfillCountingDriver struct {
	driver     driver.Driver
	mu         sync.Mutex
	statsByDSN map[string]*backfillStatementStats
}

func (d *backfillCountingDriver) register(dsn string, stats *backfillStatementStats) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statsByDSN[dsn] = stats
}

func (d *backfillCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	stats := d.statsByDSN[name]
	d.mu.Unlock()
	if stats == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("missing backfill statement stats for SQLite DSN %q", name)
	}
	return &backfillCountingConn{
		Conn:   conn,
		stats:  stats,
		reader: strings.Contains(name, "query_only(ON)"),
	}, nil
}

type backfillCountingConn struct {
	driver.Conn
	stats  *backfillStatementStats
	reader bool
}

func (c *backfillCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	c.stats.recordQuery(c.reader, query)
	return &backfillCountingRows{Rows: rows, stats: c.stats, itemPage: backfillItemPageQuery(query)}, nil
}

func (c *backfillCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.stats.beforeKeyMutation(query); err != nil {
		return nil, err
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *backfillCountingConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return c.wrapStmt(query, stmt), nil
}

func (c *backfillCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	stmt, err := preparer.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return c.wrapStmt(query, stmt), nil
}

func (c *backfillCountingConn) wrapStmt(query string, stmt driver.Stmt) driver.Stmt {
	return &backfillCountingStmt{Stmt: stmt, stats: c.stats, reader: c.reader, query: query, itemPage: backfillItemPageQuery(query)}
}

type backfillCountingStmt struct {
	driver.Stmt
	stats    *backfillStatementStats
	reader   bool
	query    string
	itemPage bool
}

func (s *backfillCountingStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), backfillNamedValues(args))
}

func (s *backfillCountingStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), backfillNamedValues(args))
}

func (s *backfillCountingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.stats.beforeKeyMutation(s.query); err != nil {
		return nil, err
	}
	execer, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, args)
}

func (s *backfillCountingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	s.stats.recordQuery(s.reader, s.query)
	return &backfillCountingRows{Rows: rows, stats: s.stats, itemPage: s.itemPage}, nil
}

func backfillNamedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for index, value := range args {
		values[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return values
}

func (c *backfillCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return beginner.BeginTx(ctx, opts)
}

type backfillCountingRows struct {
	driver.Rows
	stats    *backfillStatementStats
	itemPage bool
	count    int
}

func (r *backfillCountingRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == nil && r.itemPage {
		r.count++
		r.stats.recordItemRows(r.count)
	}
	return err
}
