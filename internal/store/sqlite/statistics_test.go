// SPDX-License-Identifier: GPL-2.0-only

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
	"go.uber.org/goleak"
)

const statisticsTestInterval = time.Hour

// statisticsHarness opens a file store with a statistics runner on a fake
// clock and records every run and every log line.
type statisticsHarness struct {
	stores *stores
	clock  *clock.Fake
	runs   chan statisticsRun
	logs   *lockedBuffer
}

func openStatisticsHarness(t *testing.T) *statisticsHarness {
	t.Helper()
	h := &statisticsHarness{
		clock: clock.NewFake(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)),
		runs:  make(chan statisticsRun, 8),
		logs:  &lockedBuffer{},
	}
	opened, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"), WithStatistics(StatisticsOptions{
		Clock:    h.clock,
		Interval: statisticsTestInterval,
		Logger:   slog.New(slog.NewTextHandler(h.logs, nil)),
		observe:  func(run statisticsRun) { h.runs <- run },
	}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.stores = opened.(*stores)
	t.Cleanup(func() { _ = h.stores.Close() })
	if run := h.nextRun(t); run.reason != "start" || run.err != nil {
		t.Fatalf("first run = %+v, want a clean start run", run)
	}
	return h
}

func (h *statisticsHarness) nextRun(t *testing.T) statisticsRun {
	t.Helper()
	select {
	case run := <-h.runs:
		return run
	case <-t.Context().Done():
		t.Fatal("no statistics run before the test deadline")
		return statisticsRun{}
	}
}

func TestStatisticsPeriodicRunRefreshesStaleStatisticsForReaders(t *testing.T) {
	t.Parallel()
	h := openStatisticsHarness(t)
	ctx := t.Context()

	// Real data makes ia the selective index. The stale statistics claim
	// the opposite, from a time when the table was 10,000x larger.
	execAll(t, h.stores.writer,
		`CREATE TABLE planner_probe (a INTEGER, b INTEGER)`,
		`CREATE INDEX planner_probe_a ON planner_probe (a)`,
		`CREATE INDEX planner_probe_b ON planner_probe (b)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200)
		 INSERT INTO planner_probe (a, b) SELECT i, i % 2 FROM n`,
		`ANALYZE planner_probe`,
		`INSERT OR REPLACE INTO sqlite_stat1 (tbl, idx, stat) VALUES
		 ('planner_probe', 'planner_probe_a', '2000000 1000000'),
		 ('planner_probe', 'planner_probe_b', '2000000 1')`,
		// The writer loads the stale statistics, as if it had analyzed them.
		`ANALYZE sqlite_schema`,
	)
	if err := reloadSchemaOnWriter(ctx, h.stores.writer); err != nil {
		t.Fatalf("load stale statistics: %v", err)
	}

	reader, err := h.stores.reader.Conn(ctx)
	if err != nil {
		t.Fatalf("reader connection: %v", err)
	}
	defer reader.Close()
	const query = `SELECT a FROM planner_probe WHERE a = 7 AND b = 1`
	if plan := queryPlan(t, reader, query); !strings.Contains(plan, "planner_probe_b") {
		t.Fatalf("reader plan with stale statistics = %q, want planner_probe_b", plan)
	}

	h.clock.Advance(statisticsTestInterval - time.Second)
	select {
	case run := <-h.runs:
		t.Fatalf("run before the interval elapsed: %+v", run)
	default:
	}
	h.clock.Advance(time.Second)
	run := h.nextRun(t)
	if run.reason != "periodic" || run.full || !run.changed || run.err != nil {
		t.Fatalf("periodic run = %+v, want a changing optimize run", run)
	}
	if stat := statRow(t, h.stores.writer, "planner_probe", "planner_probe_a"); !strings.HasPrefix(stat, "200 ") {
		t.Fatalf("planner_probe_a stat after the run = %q, want the real row count 200", stat)
	}
	// The same reader connection must plan with the new statistics.
	if plan := queryPlan(t, reader, query); !strings.Contains(plan, "planner_probe_a") {
		t.Fatalf("reader plan after the run = %q, want planner_probe_a", plan)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "reason=periodic") || !strings.Contains(logs, "duration=") {
		t.Fatalf("logs = %q, want one INFO line with reason and duration per run", logs)
	}

	// The next periodic run finds nothing to change.
	h.clock.Advance(statisticsTestInterval)
	if run := h.nextRun(t); run.reason != "periodic" || run.changed || run.err != nil {
		t.Fatalf("second periodic run = %+v, want an unchanged run", run)
	}
}

func TestStatisticsRequestedRunAnalyzesFullyAfterItemsDrift(t *testing.T) {
	t.Parallel()
	h := openStatisticsHarness(t)

	seedItems(t, h.stores.writer, 300)
	// The live database on 2026-10-11 stored 10,287 for 47,336 items.
	execAll(t, h.stores.writer,
		`ANALYZE`,
		`UPDATE sqlite_stat1 SET stat = '60 1' WHERE tbl = 'items'`,
	)

	var refresher store.StatisticsRefresher = h.stores
	refresher.RequestStatisticsRefresh()
	run := h.nextRun(t)
	if run.reason != "requested" || !run.full || !run.changed || run.err != nil {
		t.Fatalf("requested run = %+v, want a full ANALYZE that changed statistics", run)
	}
	if stat := statRow(t, h.stores.writer, "items", "sqlite_autoindex_items_1"); !strings.HasPrefix(stat, "300 ") {
		t.Fatalf("items primary key stat = %q, want the real row count 300", stat)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "mode=analyze") {
		t.Fatalf("logs = %q, want the full ANALYZE mode", logs)
	}
}

func TestStatisticsNeedFullAnalyze(t *testing.T) {
	t.Parallel()
	opened, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s := opened.(*stores)
	t.Cleanup(func() { _ = s.Close() })
	conn, err := s.writer.Conn(t.Context())
	if err != nil {
		t.Fatalf("writer connection: %v", err)
	}
	defer conn.Close()

	check := func(name string, want bool) {
		t.Helper()
		got, err := statisticsNeedFullAnalyze(t.Context(), conn)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: need full ANALYZE = %v, want %v", name, got, want)
		}
	}
	check("empty items without statistics", false)
	seedItems(t, conn, 100)
	check("items without statistics", true)
	execAll(t, conn, `ANALYZE`)
	check("fresh statistics", false)
	execAll(t, conn, `UPDATE sqlite_stat1 SET stat = '51 1' WHERE tbl = 'items'`)
	check("stored count within 2x", false)
	execAll(t, conn, `UPDATE sqlite_stat1 SET stat = '49 1' WHERE tbl = 'items'`)
	check("stored count below half", true)
	execAll(t, conn, `UPDATE sqlite_stat1 SET stat = '201 1' WHERE tbl = 'items'`)
	check("stored count above double", true)
	execAll(t, conn, `DELETE FROM sqlite_stat1 WHERE tbl = 'items'`)
	check("items rows removed from statistics", true)
}

func TestStatisticsCloseStopsRunnerAndRefreshes(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	path := filepath.Join(t.TempDir(), "deadfin.db")
	logs := &lockedBuffer{}
	runs := make(chan statisticsRun, 8)
	opened, err := Open(t.Context(), path, WithStatistics(StatisticsOptions{
		Clock:   clock.NewFake(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)),
		Logger:  slog.New(slog.NewTextHandler(logs, nil)),
		observe: func(run statisticsRun) { runs <- run },
	}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	<-runs
	seedItems(t, opened.Writer(), 50)
	if err := opened.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := strings.Count(logs.String(), "reason=close"); got != 1 {
		t.Fatalf("close runs logged = %d, want 1; logs %q", got, logs.String())
	}

	// The final refresh analyzed the new items, so the next process starts
	// with statistics.
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if stat := statRow(t, reopened.Writer(), "items", "sqlite_autoindex_items_1"); !strings.HasPrefix(stat, "50 ") {
		t.Fatalf("items stat after Close = %q, want the real row count 50", stat)
	}
}

func TestStatisticsRunnerStopsDuringStartRun(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	opened, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"), WithStatistics(StatisticsOptions{
		Clock:  clock.NewFake(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)),
		Logger: slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)),
	}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Close without waiting for the start run.
	if err := opened.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestStatisticsRequestsCoalesceWithoutBlocking(t *testing.T) {
	t.Parallel()
	runner, err := newStatisticsRunner(nil, StatisticsOptions{Clock: clock.NewFake(time.Time{})})
	if err != nil {
		t.Fatalf("newStatisticsRunner: %v", err)
	}
	for range 3 {
		runner.request()
	}
	if pending := len(runner.requests); pending != 1 {
		t.Fatalf("pending requests = %d, want 1", pending)
	}
}

func TestStatisticsRequiresClock(t *testing.T) {
	t.Parallel()
	if _, err := Open(t.Context(), ":memory:", WithStatistics(StatisticsOptions{})); err == nil {
		t.Fatal("Open without a statistics clock succeeded")
	}
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func execAll(t *testing.T, db execer, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func seedItems(t *testing.T, db execer, count int) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO items (id, kind, source_type, playlist_owner_user_id, is_folder, is_virtual_item, is_in_mixed_folder,
			is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access)
		SELECT randomblob(16), 'Movie', 'Library', zeroblob(16), 0, 0, 0, 0, 1, 0, 0, 0, 0 FROM n`, count); err != nil {
		t.Fatalf("seed items: %v", err)
	}
}

func reloadSchemaOnWriter(ctx context.Context, writer *sql.DB) error {
	conn, err := writer.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return reloadSchemaEverywhere(ctx, conn)
}

func statRow(t *testing.T, db queryer, table, index string) string {
	t.Helper()
	var stat string
	if err := db.QueryRowContext(t.Context(), `SELECT stat FROM sqlite_stat1 WHERE tbl = ? AND idx = ?`, table, index).Scan(&stat); err != nil {
		t.Fatalf("read %s.%s stat: %v", table, index, err)
	}
	return stat
}

// queryPlan runs query once, as a real read checks the schema cookie and an
// EXPLAIN does not, and then returns its plan.
func queryPlan(t *testing.T, db queryer, query string) string {
	t.Helper()
	ran, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatalf("run %s: %v", query, err)
	}
	if err := errors.Join(ran.Close(), ran.Err()); err != nil {
		t.Fatalf("run %s: %v", query, err)
	}
	rows, err := db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+query)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return strings.Join(plan, "; ")
}

// lockedBuffer is a log sink safe for the runner goroutine and the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
