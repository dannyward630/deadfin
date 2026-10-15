// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryAiringScalarBindings keeps repeated logical values
// bound at every SQLite positional placeholder while PostgreSQL reuses its
// numbered parameters. It exercises the combined predicates, where a single
// clock value appears three times and a positive season value appears three
// times in the generated SQL.
func TestTranslateItemQueryAiringScalarBindings(t *testing.T) {
	reference := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	isAiring, hasNotAired := true, false
	isNotAiring, hasAired := false, true
	positiveSeason, negativeSeason := 7, -1

	cases := []struct {
		name         string
		query        store.ItemQuery
		sqliteArgs   []any
		postgresSQL  []string
		postgresArgs []any
	}{
		{
			name: "airing true has aired false positive season",
			query: store.ItemQuery{
				IsAiring:          &isAiring,
				HasAired:          &hasNotAired,
				AiredDuringSeason: &positiveSeason,
				ReferenceTime:     &reference,
			},
			sqliteArgs: []any{
				sqlite.Dialect{}.TimeValue(reference), sqlite.Dialect{}.TimeValue(reference), sqlite.Dialect{}.TimeValue(reference),
				positiveSeason, positiveSeason, positiveSeason,
			},
			postgresSQL: []string{
				"start_date <= $1 AND end_date >= $1",
				"end_date > $1",
				"parent_index_number = $2 OR airs_after_season_number = $2 OR airs_before_season_number = $2",
			},
			postgresArgs: []any{postgres.Dialect{}.TimeValue(reference), positiveSeason},
		},
		{
			name: "airing false has aired true negative season",
			query: store.ItemQuery{
				IsAiring:          &isNotAiring,
				HasAired:          &hasAired,
				AiredDuringSeason: &negativeSeason,
				ReferenceTime:     &reference,
			},
			sqliteArgs: []any{
				sqlite.Dialect{}.TimeValue(reference), sqlite.Dialect{}.TimeValue(reference), sqlite.Dialect{}.TimeValue(reference), negativeSeason,
			},
			postgresSQL: []string{
				"start_date > $1 AND end_date < $1",
				"end_date <= $1",
				"parent_index_number = $2",
			},
			postgresArgs: []any{postgres.Dialect{}.TimeValue(reference), negativeSeason},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqliteTranslation, err := store.Translate(tc.query, sqlite.Dialect{})
			if err != nil {
				t.Fatalf("translating SQLite query: %v", err)
			}
			for _, sql := range []string{sqliteTranslation.SQL, sqliteTranslation.CountSQL} {
				if got, want := strings.Count(sql, "?"), len(tc.sqliteArgs); got != want {
					t.Fatalf("SQLite placeholder count = %d, want %d for SQL %q", got, want, sql)
				}
			}
			if !reflect.DeepEqual(sqliteTranslation.Args, tc.sqliteArgs) {
				t.Fatalf("SQLite page args = %#v, want %#v", sqliteTranslation.Args, tc.sqliteArgs)
			}
			if !reflect.DeepEqual(sqliteTranslation.CountArgs, tc.sqliteArgs) {
				t.Fatalf("SQLite count args = %#v, want %#v", sqliteTranslation.CountArgs, tc.sqliteArgs)
			}

			postgresTranslation, err := store.Translate(tc.query, postgres.Dialect{})
			if err != nil {
				t.Fatalf("translating PostgreSQL query: %v", err)
			}
			for _, sql := range []string{postgresTranslation.SQL, postgresTranslation.CountSQL} {
				for _, fragment := range tc.postgresSQL {
					if !strings.Contains(sql, fragment) {
						t.Fatalf("PostgreSQL SQL = %q, want fragment %q", sql, fragment)
					}
				}
			}
			if !reflect.DeepEqual(postgresTranslation.Args, tc.postgresArgs) {
				t.Fatalf("PostgreSQL page args = %#v, want %#v", postgresTranslation.Args, tc.postgresArgs)
			}
			if !reflect.DeepEqual(postgresTranslation.CountArgs, tc.postgresArgs) {
				t.Fatalf("PostgreSQL count args = %#v, want %#v", postgresTranslation.CountArgs, tc.postgresArgs)
			}
		})
	}
}
