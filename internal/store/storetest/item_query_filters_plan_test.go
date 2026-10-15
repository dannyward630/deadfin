// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// GET /Items/Filters2 reads the genre values and the stream languages of a
// recursive ParentId query. These tests pin that both reads stay inside the
// parent's subtree: the plans never walk the item table, read an index keyed
// by the parent, and reach the value and stream rows by item id.

func TestItemQueryFiltersPlanSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertItemQueryFiltersPlan(t, stores)
}

func TestItemQueryFiltersPlanPostgres(t *testing.T) {
	assertItemQueryFiltersPlan(t, storetestpkg.OpenPostgres(t))
}

func assertItemQueryFiltersPlan(t *testing.T, stores store.Stores) {
	t.Helper()
	f := newRecursiveParentFixture(t, stores)
	items := store.NewItemStore(stores)
	isSeries := true
	for parentName, parent := range map[string]dto.UUID{"top library": f.topLibrary, "nested library": f.nestedLibrary, "series": f.series} {
		for shapeName, query := range map[string]store.ItemQuery{
			"series":         {Recursive: true, ParentID: &parent, IncludeItemTypes: []item.Kind{item.KindSeries}, User: &f.user, UserID: &f.user.ID, IsSeries: &isSeries},
			"series streams": {Recursive: true, ParentID: &parent, IncludeItemTypes: []item.Kind{item.KindSeries, item.KindEpisode}, IncludeOwnedItems: true, User: &f.user, UserID: &f.user.ID},
			"untyped":        {Recursive: true, ParentID: &parent, User: &f.user, UserID: &f.user.ID},
		} {
			t.Run(parentName+"/"+shapeName, func(t *testing.T) {
				values, valueArgs, err := items.TranslateItemValues(t.Context(), query, "genre")
				if err != nil {
					t.Fatalf("translate item values: %v", err)
				}
				assertItemQueryFiltersStatementPlan(t, stores, values, valueArgs, "item_value_map")
				for _, streamType := range []string{"Audio", "Subtitle"} {
					streams, streamArgs, err := items.TranslateStreamLanguages(t.Context(), query, streamType)
					if err != nil {
						t.Fatalf("translate stream languages: %v", err)
					}
					assertItemQueryFiltersStatementPlan(t, stores, streams, streamArgs, "media_streams")
					if _, err := items.QueryStreamLanguages(t.Context(), query, streamType); err != nil {
						t.Fatalf("query stream languages: %v", err)
					}
				}
			})
		}
	}
}

// assertItemQueryFiltersStatementPlan applies the recursive-parent plan rule
// and also rejects a plan that reads the value or stream table other than by
// item id.
func assertItemQueryFiltersStatementPlan(t *testing.T, stores store.Stores, statement string, args []any, satellite string) {
	t.Helper()
	assertRecursiveParentPlan(t, stores, statement, args)
	if stores.Dialect().Placeholder(1) != "?" {
		assertScopedPlanAvoidsFullScans(t, stores, statement, args, []string{"items", satellite})
		return
	}
	rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", statement, err)
	}
	var detail []string
	for rows.Next() {
		var id, parent, unused int
		var step string
		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		detail = append(detail, step)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close plan: %v", err)
	}
	keyed := false
	for _, step := range detail {
		if !strings.Contains(step, satellite) && !strings.Contains(step, " m ") && !strings.Contains(step, " stream ") {
			continue
		}
		if strings.HasPrefix(step, "SCAN") {
			t.Fatalf("plan scans %s at %q:\n%s", satellite, step, strings.Join(detail, "\n"))
		}
		if strings.Contains(step, "(item_id=?") {
			keyed = true
		}
	}
	if !keyed {
		t.Fatalf("plan reads %s by no item id:\n%s", satellite, strings.Join(detail, "\n"))
	}
}
