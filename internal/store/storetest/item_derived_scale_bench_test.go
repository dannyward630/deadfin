// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// BenchmarkReconcileAtLibraryScale measures one changed scanner folder
// reconcile on a library of the operator's size: about 47,000 items (1,000
// movies, 9,000 episodes, 4,000 tracks, 31,000 Person items) and 151,000
// person credits. The fixture is written with bulk SQL and one global repair,
// so the same benchmark runs against code that repairs globally on every
// write. It runs on file-backed SQLite and, when DEADFIN_TEST_POSTGRES_DSN is
// set, on PostgreSQL.
//
//	go test -run '^$' -bench ReconcileAtLibraryScale -benchtime 10x ./internal/store/storetest/
func BenchmarkReconcileAtLibraryScale(b *testing.B) {
	forEachScaleBackend(b, func(b *testing.B, stores store.Stores) {
		ctx := b.Context()
		items := store.NewItemStore(stores)
		fixture := writeScaleFixture(b, ctx, stores)
		if err := items.RepairDerived(ctx); err != nil {
			b.Fatalf("initial global repair: %v", err)
		}
		analyzeScaleFixture(b, ctx, stores)

		for _, target := range []struct {
			name   string
			folder dto.UUID
		}{{"movie folder", fixture.movieFolder}, {"season folder", fixture.season}} {
			b.Run(target.name, func(b *testing.B) {
				children, err := items.ReconciliationChildren(ctx, target.folder)
				if err != nil || len(children) == 0 {
					b.Fatalf("children of %s: %d, %v", target.folder, len(children), err)
				}
				b.ResetTimer()
				for iteration := range b.N {
					name := fmt.Sprintf("Renamed %d", iteration)
					children[0].Name = &name
					if _, err := items.ReconcileScannedPlan(ctx, target.folder, children, nil, nil); err != nil {
						b.Fatalf("reconcile: %v", err)
					}
				}
			})
		}
	})
}

// forEachScaleBackend runs body as a sub-benchmark on a fresh file-backed
// SQLite store, and on a fresh PostgreSQL schema when
// DEADFIN_TEST_POSTGRES_DSN is set.
func forEachScaleBackend(b *testing.B, body func(*testing.B, store.Stores)) {
	b.Run("sqlite", func(b *testing.B) {
		stores, err := sqlite.Open(b.Context(), filepath.Join(b.TempDir(), "scale.db"))
		if err != nil {
			b.Fatalf("open SQLite: %v", err)
		}
		b.Cleanup(func() { _ = stores.Close() })
		body(b, stores)
	})
	if os.Getenv("DEADFIN_TEST_POSTGRES_DSN") == "" {
		return
	}
	b.Run("postgres", func(b *testing.B) {
		body(b, storetestpkg.OpenPostgresBenchmark(b))
	})
}

// scaleSQL adapts the fixture SQL, written with "?" placeholders, to the
// store dialect.
type scaleSQL struct{ dialect store.Dialect }

func (s scaleSQL) postgres() bool { return s.dialect.Placeholder(1) != "?" }

// query numbers the "?" placeholders for the dialect.
func (s scaleSQL) query(query string) string {
	if !s.postgres() {
		return query
	}
	var out strings.Builder
	index := 0
	for _, r := range query {
		if r == '?' {
			index++
			out.WriteString("$" + strconv.Itoa(index))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// id encodes a UUID as the dialect stores it: a 16-byte blob on SQLite, a
// uuid value on PostgreSQL.
func (s scaleSQL) id(id dto.UUID) any {
	if s.postgres() {
		return id.Dashed()
	}
	return id[:]
}

// analyzeScaleFixture refreshes the planner statistics, so the plans match
// a maintained database rather than a freshly loaded one. On SQLite it then
// changes the schema cookie, as the store's statistics runner does: a reader
// connection loads sqlite_stat1 only at schema load, so without the reload
// the readers that earlier scenarios opened keep the old statistics.
func analyzeScaleFixture(b *testing.B, ctx context.Context, stores store.Stores) {
	b.Helper()
	statements := []string{"ANALYZE"}
	if !(scaleSQL{stores.Dialect()}).postgres() {
		statements = append(statements, "CREATE TABLE IF NOT EXISTS scale_statistics_reload (id INTEGER)", "DROP TABLE scale_statistics_reload")
	}
	for _, statement := range statements {
		if _, err := stores.Writer().ExecContext(ctx, statement); err != nil {
			b.Fatalf("analyze fixture: %s: %v", statement, err)
		}
	}
}

type scaleFixture struct {
	movieFolder dto.UUID
	season      dto.UUID
}

func scaleUUID(n int) dto.UUID {
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", n))
	if err != nil {
		panic(err)
	}
	return id
}

func writeScaleFixture(b *testing.B, ctx context.Context, stores store.Stores) scaleFixture {
	b.Helper()
	q := scaleSQL{stores.Dialect()}
	tx, err := stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	batch := func(insert, row string) *scaleBatch {
		return &scaleBatch{b: b, ctx: ctx, tx: tx, q: q, insert: insert, row: row}
	}
	insertItem := batch("INSERT INTO items (id, kind, parent_id, name, playlist_owner_user_id, source_type, index_number, parent_index_number, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access)", "(?, ?, ?, ?, ?, '0', ?, ?, ?, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE)")
	insertProvider := batch("INSERT INTO provider_ids (item_id, provider, value)", "(?, ?, ?)")
	insertPerson := batch("INSERT INTO people (id, name, clean_name)", "(?, ?, ?)")
	insertCredit := batch("INSERT INTO people_map (item_id, person_id, role, type, sort_order)", "(?, ?, NULL, 'Actor', ?)")
	insertValueMap := batch("INSERT INTO item_value_map (item_id, value_id, sort_order)", "(?, ?, ?)")
	zero := q.id(dto.UUID{})
	next := 1
	add := func(kind item.Kind, parent *dto.UUID, name string, index, parentIndex *int, folder bool) dto.UUID {
		id := scaleUUID(next)
		next++
		var parentArg any
		if parent != nil {
			parentArg = q.id(*parent)
		}
		insertItem.add(q.id(id), kind.String(), parentArg, name, zero, index, parentIndex, folder)
		return id
	}
	const persons = 31000
	personIDs := make([]dto.UUID, persons)
	for index := range personIDs {
		name := fmt.Sprintf("Person %d", index)
		personIDs[index] = add(item.KindPerson, nil, name, nil, nil, false)
		insertPerson.add(q.id(personIDs[index]), name, item.CleanName(name))
	}
	// people_map references people, so the people go in before any credit.
	insertPerson.flush()
	genres := []string{"Drama", "Comedy", "Action", "Documentary", "Animation", "Horror"}
	for index, genre := range genres {
		if _, err := tx.ExecContext(ctx, q.query("INSERT INTO item_values (id, kind, value, clean_value) VALUES (?, 'genre', ?, ?)"), index+1, genre, item.CleanName(genre)); err != nil {
			b.Fatalf("insert genre: %v", err)
		}
	}
	credit := 0
	describe := func(id dto.UUID, credits int, providers ...string) {
		for index := 0; index < len(providers); index += 2 {
			insertProvider.add(q.id(id), providers[index], providers[index+1])
		}
		for order := range credits {
			credit++
			insertCredit.add(q.id(id), q.id(personIDs[(credit*7919)%persons]), order)
		}
		for order := range 2 {
			insertValueMap.add(q.id(id), 1+(credit+order)%len(genres), order)
		}
	}
	intp := func(value int) *int { return &value }
	root := add(item.KindAggregateFolder, nil, "root", nil, nil, true)
	movies := add(item.KindCollectionFolder, &root, "Movies", nil, nil, true)
	shows := add(item.KindCollectionFolder, &root, "Shows", nil, nil, true)
	music := add(item.KindCollectionFolder, &root, "Music", nil, nil, true)
	var fixture scaleFixture
	for index := range 1000 {
		folder := add(item.KindFolder, &movies, fmt.Sprintf("Movie %d (2000)", index), nil, nil, true)
		movie := add(item.KindMovie, &folder, fmt.Sprintf("Movie %d", index), nil, nil, false)
		describe(movie, 30, "Tmdb", fmt.Sprint(index), "Imdb", fmt.Sprintf("tt%07d", index))
		if index == 500 {
			fixture.movieFolder = folder
		}
	}
	for index := range 200 {
		series := add(item.KindSeries, &shows, fmt.Sprintf("Show %d", index), nil, nil, true)
		describe(series, 10, "Tvdb", fmt.Sprint(index), "Imdb", fmt.Sprintf("tt9%06d", index))
		for seasonIndex := 1; seasonIndex <= 3; seasonIndex++ {
			season := add(item.KindSeason, &series, fmt.Sprintf("Season %d", seasonIndex), intp(seasonIndex), nil, true)
			for episode := 1; episode <= 15; episode++ {
				id := add(item.KindEpisode, &season, fmt.Sprintf("Episode %d", episode), intp(episode), intp(seasonIndex), false)
				describe(id, 13)
			}
			if index == 100 && seasonIndex == 2 {
				fixture.season = season
			}
		}
	}
	for index := range 50 {
		artist := add(item.KindMusicArtist, &music, fmt.Sprintf("Artist %d", index), nil, nil, true)
		for albumIndex := range 4 {
			album := add(item.KindMusicAlbum, &artist, fmt.Sprintf("Album %d", albumIndex), nil, nil, true)
			for track := 1; track <= 20; track++ {
				add(item.KindAudio, &album, fmt.Sprintf("Track %d", track), intp(track), nil, false)
			}
		}
	}
	for _, pending := range []*scaleBatch{insertItem, insertProvider, insertCredit, insertValueMap} {
		pending.flush()
	}
	if q.postgres() {
		// The genre ids above bypass the BIGSERIAL sequence.
		if _, err := tx.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('item_values', 'id'), (SELECT MAX(id) FROM item_values))"); err != nil {
			b.Fatalf("advance the item_values sequence: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit fixture: %v", err)
	}
	b.Logf("fixture: %d items, %d credits", next-1, credit)
	return fixture
}

// scaleBatch collects fixture rows and writes them as multi-row INSERT
// statements, so a PostgreSQL fixture load does not pay one round trip per
// row.
type scaleBatch struct {
	b           *testing.B
	ctx         context.Context
	tx          *sql.Tx
	q           scaleSQL
	insert, row string
	rows        int
	args        []any
}

// scaleBatchRows keeps the widest batch (items, 8 arguments a row) well under
// the SQLite and PostgreSQL bound-parameter limits.
const scaleBatchRows = 500

func (batch *scaleBatch) add(args ...any) {
	batch.args = append(batch.args, args...)
	batch.rows++
	if batch.rows == scaleBatchRows {
		batch.flush()
	}
}

func (batch *scaleBatch) flush() {
	if batch.rows == 0 {
		return
	}
	rows := strings.TrimSuffix(strings.Repeat(batch.row+", ", batch.rows), ", ")
	if _, err := batch.tx.ExecContext(batch.ctx, batch.q.query(batch.insert+" VALUES "+rows), batch.args...); err != nil {
		batch.b.Fatalf("%s: %v", batch.insert, err)
	}
	batch.rows, batch.args = 0, batch.args[:0]
}
