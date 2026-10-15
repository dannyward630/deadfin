// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryPresentationGroupingGate pins the source gate in
// BaseItemRepository.EnableGroupByPresentationUniqueKey (b04614e:163-203).
// Grouping is a transformed relation, so both the page and count statements
// must contain (or omit) the same transformation.
func TestTranslateItemQueryPresentationGroupingGate(t *testing.T) {
	trueValue := true
	user := &store.User{ID: groupingUUID(t, 1)}
	cases := []struct {
		name       string
		query      store.ItemQuery
		groupBySQL string
	}{
		{
			name:       "untyped user query groups by presentation key",
			query:      store.ItemQuery{User: user, GroupByPresentationUniqueKey: true},
			groupBySQL: "GROUP BY presentation_unique_key",
		},
		{
			name:       "allowed movie kind groups by presentation key",
			query:      store.ItemQuery{User: user, GroupByPresentationUniqueKey: true, IncludeItemTypes: []item.Kind{item.KindMovie}},
			groupBySQL: "GROUP BY presentation_unique_key",
		},
		{
			name:  "flag disabled does not group",
			query: store.ItemQuery{User: user, IncludeItemTypes: []item.Kind{item.KindMovie}},
		},
		{
			name:  "anonymous query does not group",
			query: store.ItemQuery{GroupByPresentationUniqueKey: true, IncludeItemTypes: []item.Kind{item.KindMovie}},
		},
		{
			name:  "resumable query does not group",
			query: store.ItemQuery{User: user, UserID: &user.ID, GroupByPresentationUniqueKey: true, IsResumable: &trueValue, IncludeItemTypes: []item.Kind{item.KindMovie}},
		},
		{
			name:       "series grouping selects the series key instead",
			query:      store.ItemQuery{User: user, GroupByPresentationUniqueKey: true, GroupBySeriesPresentationUniqueKey: true, IncludeItemTypes: []item.Kind{item.KindMovie}},
			groupBySQL: "GROUP BY series_presentation_unique_key",
		},
		{
			name:  "explicit presentation key disables grouping",
			query: store.ItemQuery{User: user, GroupByPresentationUniqueKey: true, PresentationUniqueKey: "exact-key", IncludeItemTypes: []item.Kind{item.KindMovie}},
		},
		{
			name:  "unsupported kind does not group",
			query: store.ItemQuery{User: user, GroupByPresentationUniqueKey: true, IncludeItemTypes: []item.Kind{item.KindBoxSet}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, dialect := range groupingDialects() {
				t.Run(dialect.name, func(t *testing.T) {
					translated, err := store.Translate(tc.query, dialect.dialect)
					if err != nil {
						t.Fatalf("Translate: %v", err)
					}
					for _, sql := range []string{translated.SQL, translated.CountSQL} {
						gotGrouping := strings.Contains(sql, "GROUP BY presentation_unique_key") || strings.Contains(sql, "GROUP BY series_presentation_unique_key")
						if tc.groupBySQL == "" && gotGrouping {
							t.Fatalf("SQL unexpectedly grouped transformed rows: %s", sql)
						}
						if tc.groupBySQL != "" && !strings.Contains(sql, tc.groupBySQL) {
							t.Fatalf("SQL = %s\nwant transformed relation containing %q", sql, tc.groupBySQL)
						}
					}
				})
			}
		})
	}
}

// TestTranslateItemQueryAdjacentToUsesBoundTargetAndTransformedCount pins
// TranslateQuery.cs:1229-1253. The target and its immediate sort-name neighbors
// are selected before ordering and pagination, so the count must use the same
// adjacent relation and PostgreSQL must number the caller value.
func TestTranslateItemQueryAdjacentToUsesBoundTargetAndTransformedCount(t *testing.T) {
	target := groupingUUID(t, 42)
	for _, dialect := range groupingDialects() {
		t.Run(dialect.name, func(t *testing.T) {
			translated, err := store.Translate(store.ItemQuery{AdjacentTo: &target}, dialect.dialect)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				if !strings.Contains(sql, "sort_name") || !strings.Contains(sql, "items.id") {
					t.Fatalf("adjacency SQL = %s\nwant target-plus-neighbor sort-name relation", sql)
				}
				if strings.Contains(sql, target.String()) {
					t.Fatalf("adjacency SQL interpolated target %q: %s", target, sql)
				}
			}
			if len(translated.CountArgs) == 0 {
				t.Fatal("adjacency count has no bound target argument")
			}
			if !strings.Contains(translated.SQL, dialect.placeholder) || !strings.Contains(translated.CountSQL, dialect.placeholder) {
				t.Fatalf("adjacency SQL page=%q count=%q, want %s placeholders", translated.SQL, translated.CountSQL, dialect.placeholder)
			}
			if dialect.name == "PostgreSQL" && (!strings.Contains(translated.SQL, "$1") || !strings.Contains(translated.CountSQL, "$1")) {
				t.Fatalf("PostgreSQL adjacency SQL must begin numbered caller bindings: page=%q count=%q", translated.SQL, translated.CountSQL)
			}
		})
	}
}

type groupingDialect struct {
	name        string
	dialect     store.Dialect
	placeholder string
}

func groupingDialects() []groupingDialect {
	return []groupingDialect{
		{name: "SQLite", dialect: sqlite.Dialect{}, placeholder: "?"},
		{name: "PostgreSQL", dialect: postgres.Dialect{}, placeholder: "$"},
	}
}

func groupingUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing grouping UUID %d: %v", value, err)
	}
	return id
}
