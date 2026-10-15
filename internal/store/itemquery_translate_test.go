// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

var stableItemQuerySort = regexp.MustCompile(`(?i)ORDER BY[\s\S]*sort_name\s+COLLATE\s+(?:BINARY|"C")\s+ASC(?:\s+NULLS\s+(?:FIRST|LAST))?\s*,\s*name\s+COLLATE\s+(?:BINARY|"C")\s+ASC(?:\s+NULLS\s+(?:FIRST|LAST))?\s*,\s*(?:[a-z_]+\.)?id\s+ASC(?:\s|$)`)

// TestTranslateItemQueryUsesDialectPlaceholdersAndStableSort defends the SQL
// boundary that makes a paged, parent-scoped Items response deterministic with
// SortName, display Name, and ID tie-breakers on both supported dialects.
func TestTranslateItemQueryUsesDialectPlaceholdersAndStableSort(t *testing.T) {
	parent := translateUUID(t, 1)
	query := store.ItemQuery{
		ItemIDs:                []dto.UUID{translateUUID(t, 2), translateUUID(t, 3)},
		ParentID:               &parent,
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		StartIndex:             translateInt(4),
		Limit:                  translateInt(5),
		EnableTotalRecordCount: true,
	}

	cases := []struct {
		name               string
		dialect            store.Dialect
		itemIDPlaceholders string
		pagePlaceholders   string
	}{
		{
			name:               "SQLite positional placeholders",
			dialect:            sqlite.Dialect{},
			itemIDPlaceholders: "IN (?, ?)",
			pagePlaceholders:   "LIMIT ? OFFSET ?",
		},
		{
			name:               "PostgreSQL numbered placeholders",
			dialect:            postgres.Dialect{},
			itemIDPlaceholders: "IN ($1, $2)",
			pagePlaceholders:   "LIMIT $3 OFFSET $4",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("translating query: %v", err)
			}

			for _, fragment := range []string{tc.itemIDPlaceholders, tc.pagePlaceholders} {
				if !strings.Contains(translated.SQL, fragment) {
					t.Fatalf("translated page SQL = %q, want fragment %q", translated.SQL, fragment)
				}
			}
			if strings.Contains(translated.SQL, "parent_id =") {
				t.Fatalf("explicit IDs must take precedence over ParentID: %q", translated.SQL)
			}
			if got, want := len(translated.Args), 4; got != want {
				t.Fatalf("translated page argument count = %d, want %d for two IDs, limit, and offset", got, want)
			}
			if !stableItemQuerySort.MatchString(translated.SQL) {
				t.Fatalf("translated page SQL must order SortName, Name, then ID for stable ascending ties: %q", translated.SQL)
			}

			if !strings.Contains(strings.ToUpper(translated.CountSQL), "SELECT COUNT(") {
				t.Fatalf("translated count SQL = %q, want a COUNT query", translated.CountSQL)
			}
			if !strings.Contains(translated.CountSQL, tc.itemIDPlaceholders) {
				t.Fatalf("translated count SQL = %q, want pre-pagination ID filter fragment %q", translated.CountSQL, tc.itemIDPlaceholders)
			}
			if strings.Contains(translated.CountSQL, "parent_id =") {
				t.Fatalf("explicit IDs must take precedence over ParentID in count SQL: %q", translated.CountSQL)
			}
			if strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Fatalf("translated count SQL must precede pagination: %q", translated.CountSQL)
			}
			if got, want := len(translated.CountArgs), 2; got != want {
				t.Fatalf("translated count argument count = %d, want %d for two IDs", got, want)
			}
		})
	}
}

// TestTranslateItemQueryUsesPortableStockNullOrdering pins the captured stock
// SQLite behavior so PostgreSQL pagination sees the same nullable ordering.
func TestTranslateItemQueryUsesPortableStockNullOrdering(t *testing.T) {
	translated, err := store.Translate(store.ItemQuery{
		SortBy:    []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder: []store.SortOrder{store.SortOrderAscending},
	}, sqlite.Dialect{})
	if err != nil {
		t.Fatalf("translating nullable SortName query: %v", err)
	}

	want := "ORDER BY " + (sqlite.Dialect{}).TextCollation("sort_name") + " ASC NULLS FIRST, " + (sqlite.Dialect{}).TextCollation("name") + " ASC NULLS FIRST, id ASC"
	if !strings.Contains(translated.SQL, want) {
		t.Fatalf("translated SQL = %q, want stock null-order fragment %q", translated.SQL, want)
	}
}

func translateUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func translateInt(value int) *int { return &value }

// TestTranslateRecursiveParentExcludesKindsWithoutAncestors keeps people,
// studios, and the other kinds that never live under a parent out of the
// filtered set of a recursive parent scope, before the per-candidate scope
// check runs.
func TestTranslateRecursiveParentExcludesKindsWithoutAncestors(t *testing.T) {
	parent := translateUUID(t, 1)
	const clause = "kind NOT IN ('Genre', 'LiveTvProgram', 'MusicGenre', 'Person', 'Program', 'Studio', 'TvProgram', 'Year')"
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
		want  bool
	}{
		{"recursive parent", store.ItemQuery{ParentID: &parent, Recursive: true}, true},
		{"direct parent", store.ItemQuery{ParentID: &parent}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(tc.query, sqlite.Dialect{})
			if err != nil {
				t.Fatalf("translating query: %v", err)
			}
			filtered, _, _ := strings.Cut(translated.SQL, "recursive_parent AS")
			if got := strings.Contains(filtered, clause); got != tc.want {
				t.Fatalf("filtered_ids contains %q = %v, want %v: %s", clause, got, tc.want, filtered)
			}
		})
	}
}
