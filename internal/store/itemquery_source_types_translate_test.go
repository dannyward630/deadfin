// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQuerySourceTypesBindsInCallerOrder defends the SQL boundary
// used by scheduled media work: SourceTypes narrows the common candidate set
// before its count and page statements, while duplicate values are removed in
// stable first-seen order for dialect placeholders and arguments.
func TestTranslateItemQuerySourceTypesBindsInCallerOrder(t *testing.T) {
	limit, start := 1, 2
	query := store.ItemQuery{
		SourceTypes:            []item.SourceType{item.SourceTypeLiveTV, item.SourceTypeLibrary, item.SourceTypeLiveTV, item.SourceTypeChannel, item.SourceTypeLibrary},
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		StartIndex:             &start,
		Limit:                  &limit,
		EnableTotalRecordCount: true,
	}

	cases := []struct {
		name          string
		dialect       store.Dialect
		filter        string
		page          string
		wantCountArgs []any
		wantPageArgs  []any
	}{
		{
			name:          "SQLite positional placeholders",
			dialect:       sqlite.Dialect{},
			filter:        "source_type IN (?, ?, ?)",
			page:          "LIMIT ? OFFSET ?",
			wantCountArgs: []any{"2", "0", "1"},
			wantPageArgs:  []any{"2", "0", "1", limit, start},
		},
		{
			name:          "PostgreSQL numbered placeholders",
			dialect:       postgres.Dialect{},
			filter:        "source_type IN ($1, $2, $3)",
			page:          "LIMIT $4 OFFSET $5",
			wantCountArgs: []any{"2", "0", "1"},
			wantPageArgs:  []any{"2", "0", "1", limit, start},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			for _, statement := range []struct {
				name string
				sql  string
			}{{name: "page", sql: translated.SQL}, {name: "count", sql: translated.CountSQL}} {
				if !strings.Contains(statement.sql, tc.filter) {
					t.Errorf("%s SQL = %q, want SourceTypes predicate %q", statement.name, statement.sql, tc.filter)
				}
			}
			if !strings.Contains(translated.SQL, tc.page) {
				t.Errorf("page SQL = %q, want paging placeholders %q after SourceTypes", translated.SQL, tc.page)
			}
			if strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Errorf("count SQL = %q, want SourceTypes before pagination", translated.CountSQL)
			}
			if got := translated.CountArgs; !reflect.DeepEqual(got, tc.wantCountArgs) {
				t.Errorf("count args = %#v, want deduplicated persisted SourceType values in caller order %#v", got, tc.wantCountArgs)
			}
			if got := translated.Args; !reflect.DeepEqual(got, tc.wantPageArgs) {
				t.Errorf("page args = %#v, want SourceTypes then limit and offset %#v", got, tc.wantPageArgs)
			}
		})
	}
}

// TestTranslateItemQueryEmptySourceTypesDoesNotNarrowCandidates defends the
// caller contract that an empty SourceTypes slice means no source restriction.
func TestTranslateItemQueryEmptySourceTypesDoesNotNarrowCandidates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect store.Dialect
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}},
		{name: "PostgreSQL", dialect: postgres.Dialect{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(store.ItemQuery{SourceTypes: []item.SourceType{}}, tc.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			for _, statement := range []struct {
				name string
				sql  string
			}{{name: "page", sql: translated.SQL}, {name: "count", sql: translated.CountSQL}} {
				if strings.Contains(statement.sql, "source_type IN") {
					t.Errorf("%s SQL = %q, empty SourceTypes must not constrain candidates", statement.name, statement.sql)
				}
			}
			if len(translated.CountArgs) != 0 || len(translated.Args) != 0 {
				t.Errorf("empty SourceTypes arguments = count %#v page %#v, want none", translated.CountArgs, translated.Args)
			}
		})
	}
}

// TestTranslateItemQuerySourceTypesRejectsUnknownValues defends the input
// validation boundary: an unrecognized source enum must not broaden a query or
// produce a bound empty string.
func TestTranslateItemQuerySourceTypesRejectsUnknownValues(t *testing.T) {
	unknown := item.SourceType(99)
	for _, tc := range []struct {
		name    string
		dialect store.Dialect
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}},
		{name: "PostgreSQL", dialect: postgres.Dialect{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Translate(store.ItemQuery{SourceTypes: []item.SourceType{unknown}}, tc.dialect); err == nil || !strings.Contains(err.Error(), "unsupported source type") {
				t.Fatalf("Translate(unknown SourceTypes) error = %v, want unsupported source type rejection", err)
			}
		})
	}
}
