// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// A recursive ParentId query reads only the parent's subtree. ItemStore
// resolves the parent kind first and scopes the base relation through an
// index keyed by the parent. These tests pin that the scoped statements
// return exactly what the generic relation returns, and that their plans
// never walk the whole item table.

func TestItemQueryRecursiveParentScopeSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertRecursiveParentScope(t, stores)
}

func TestItemQueryRecursiveParentScopePostgres(t *testing.T) {
	assertRecursiveParentScope(t, storetestpkg.OpenPostgres(t))
}

type recursiveParentFixture struct {
	topLibrary, nestedLibrary, series, boxSet, playlist, missing dto.UUID
	user                                                         store.User
}

func newRecursiveParentFixture(t *testing.T, stores store.Stores) recursiveParentFixture {
	t.Helper()
	ctx := t.Context()
	items := store.NewItemStore(stores)
	id := func(n int) dto.UUID { return itemQueryPlanUUID(t, 90_000+n) }
	name := itemQueryPlanString
	intp := func(n int) *int { return &n }
	f := recursiveParentFixture{topLibrary: id(2), nestedLibrary: id(4), series: id(11), boxSet: id(30), playlist: id(31), missing: id(99)}
	f.user = store.User{ID: id(50), Policy: []byte(`{"IsAdministrator":true}`)}
	aggregate, holder, physical, season := id(1), id(3), id(10), id(12)
	values := []item.Item{
		{ID: aggregate, Kind: item.KindAggregateFolder, Name: name("root")},
		{ID: f.topLibrary, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Shows")},
		{ID: holder, Kind: item.KindFolder, ParentID: &aggregate, Name: name("Holder")},
		{ID: f.nestedLibrary, Kind: item.KindCollectionFolder, ParentID: &holder, Name: name("Nested")},
		{ID: physical, Kind: item.KindFolder, ParentID: &f.topLibrary, Name: name("shows")},
		{ID: f.series, Kind: item.KindSeries, ParentID: &physical, Name: name("Show")},
		{ID: season, Kind: item.KindSeason, ParentID: &f.series, Name: name("Season 1"), IndexNumber: intp(1)},
		{ID: id(13), Kind: item.KindEpisode, ParentID: &season, Name: name("Episode 1"), IndexNumber: intp(1), ParentIndexNumber: intp(1)},
		{ID: id(14), Kind: item.KindEpisode, ParentID: &season, Name: name("Episode 2"), IndexNumber: intp(2), ParentIndexNumber: intp(1)},
		{ID: id(15), Kind: item.KindMovie, ParentID: &physical, Name: name("Movie A")},
		{ID: id(20), Kind: item.KindMovie, ParentID: &f.nestedLibrary, Name: name("Movie B")},
		{ID: id(21), Kind: item.KindMovie, ParentID: &holder, Name: name("Movie C")},
		{ID: f.boxSet, Kind: item.KindBoxSet, Name: name("Box")},
		{ID: id(40), Kind: item.KindPerson, Name: name("Person")},
	}
	for _, value := range values {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	if err := items.AddToCollection(ctx, f.boxSet, []dto.UUID{id(15), id(20)}); err != nil {
		t.Fatalf("add to collection: %v", err)
	}
	playlist := item.Item{ID: f.playlist, Kind: item.KindPlaylist, PlaylistOwnerUserID: f.user.ID, Name: name("List"),
		LinkedChildren: []item.LinkedChildRef{{ItemID: id(14), Type: item.LinkedChildTypeManual}, {ItemID: id(21), Type: item.LinkedChildTypeManual}}}
	if err := items.CreatePlaylist(ctx, playlist, nil); err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if err := items.UpsertUserData(ctx, store.UserData{UserID: f.user.ID, CustomKey: id(13).String(), ItemID: id(13), Played: true}); err != nil {
		t.Fatalf("upsert user data: %v", err)
	}
	return f
}

func assertRecursiveParentScope(t *testing.T, stores store.Stores) {
	t.Helper()
	f := newRecursiveParentFixture(t, stores)
	items := store.NewItemStore(stores)
	notFolder, notPlayed := false, false
	two, one := 2, 1
	parents := map[string]dto.UUID{
		"top library": f.topLibrary, "nested library": f.nestedLibrary, "series": f.series,
		"box set": f.boxSet, "playlist": f.playlist, "missing": f.missing,
	}
	shapes := map[string]func(dto.UUID) store.ItemQuery{
		"all": func(parent dto.UUID) store.ItemQuery {
			return store.ItemQuery{Recursive: true, ParentID: &parent, EnableTotalRecordCount: true}
		},
		"typed page": func(parent dto.UUID) store.ItemQuery {
			return store.ItemQuery{Recursive: true, ParentID: &parent, IncludeItemTypes: []item.Kind{item.KindEpisode, item.KindMovie}, StartIndex: &one, Limit: &two, EnableTotalRecordCount: true}
		},
		"latest": func(parent dto.UUID) store.ItemQuery {
			return store.ItemQuery{Recursive: true, ParentID: &parent, IsFolder: &notFolder, IsPlayed: &notPlayed, User: &f.user, UserID: &f.user.ID,
				SortBy: []store.ItemSortBy{store.ItemSortByDateCreated}, SortOrder: []store.SortOrder{store.SortOrderDescending}}
		},
	}
	for parentName, parent := range parents {
		for shapeName, shape := range shapes {
			t.Run(parentName+"/"+shapeName, func(t *testing.T) {
				query := shape(parent)
				generic, err := store.Translate(query, stores.Dialect())
				if err != nil {
					t.Fatalf("translate generic: %v", err)
				}
				scoped, err := items.TranslateQuery(t.Context(), query)
				if err != nil {
					t.Fatalf("translate scoped: %v", err)
				}
				if scoped.SQL == generic.SQL {
					t.Fatalf("scoped statement equals the generic statement: %s", scoped.SQL)
				}
				want, wantCount := recursiveParentIDs(t, stores, generic)
				got, gotCount := recursiveParentIDs(t, stores, scoped)
				if !reflect.DeepEqual(got, want) || gotCount != wantCount {
					t.Fatalf("scoped ids = %v (count %d), generic ids = %v (count %d)", got, gotCount, want, wantCount)
				}
				result, err := items.Query(t.Context(), query)
				if err != nil {
					t.Fatalf("query: %v", err)
				}
				var queried []string
				for _, value := range result.Items {
					queried = append(queried, value.ID.String())
				}
				if !reflect.DeepEqual(queried, got) {
					t.Fatalf("Query ids = %v, scoped statement ids = %v", queried, got)
				}
				if parentName != "missing" {
					assertRecursiveParentPlan(t, stores, scoped.SQL, scoped.Args)
					assertRecursiveParentPlan(t, stores, scoped.CountSQL, scoped.CountArgs)
				}
			})
		}
	}
	t.Run("fixture has rows", func(t *testing.T) {
		query := shapes["all"](f.topLibrary)
		result, err := items.Query(t.Context(), query)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		// The library itself (its own top parent, as in the generic relation),
		// the physical folder, series, season, two episodes, and a movie.
		if result.TotalRecordCount != 7 {
			t.Fatalf("top library count = %d, want 7", result.TotalRecordCount)
		}
	})
}

func recursiveParentIDs(t *testing.T, stores store.Stores, translated store.TranslatedItemQuery) ([]string, int) {
	t.Helper()
	rows, err := stores.Reader().QueryContext(t.Context(), translated.SQL, translated.Args...)
	if err != nil {
		t.Fatalf("run %s: %v", translated.SQL, err)
	}
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var ids []string
	for rows.Next() {
		values := make([]any, len(columns))
		for index := range values {
			values[index] = new(any)
		}
		if err := rows.Scan(values...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, recursiveParentRowID(t, *values[0].(*any)))
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	var count int
	if err := stores.Reader().QueryRowContext(t.Context(), translated.CountSQL, translated.CountArgs...).Scan(&count); err != nil {
		t.Fatalf("run %s: %v", translated.CountSQL, err)
	}
	return ids, count
}

func recursiveParentRowID(t *testing.T, raw any) string {
	t.Helper()
	switch value := raw.(type) {
	case []byte:
		if len(value) == 16 {
			var id dto.UUID
			copy(id[:], value)
			return id.String()
		}
		id, err := dto.ParseUUID(string(value))
		if err != nil {
			t.Fatalf("parse id %q: %v", value, err)
		}
		return id.String()
	case string:
		id, err := dto.ParseUUID(value)
		if err != nil {
			t.Fatalf("parse id %q: %v", value, err)
		}
		return id.String()
	default:
		t.Fatalf("id has type %T", raw)
		return ""
	}
}

// assertRecursiveParentPlan rejects a plan that walks the item table. On
// SQLite this includes a search on primary_version_id alone: without
// statistics the planner takes "primary_version_id IS NULL" as selective,
// although it matches almost every item. The plan must also read an index
// keyed by the parent.
func assertRecursiveParentPlan(t *testing.T, stores store.Stores, statement string, args []any) {
	t.Helper()
	if stores.Dialect().Placeholder(1) != "?" {
		assertScopedPlanAvoidsFullScans(t, stores, statement, args, []string{"items"})
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
		if strings.HasPrefix(step, "SCAN items") || strings.Contains(step, "(primary_version_id=?)") {
			t.Fatalf("plan walks the item table at %q:\n%s", step, strings.Join(detail, "\n"))
		}
		for _, key := range []string{"top_parent_id=?", "ancestor_id=?", "parent_item_id=?"} {
			if strings.Contains(step, key) {
				keyed = true
			}
		}
	}
	if !keyed {
		t.Fatalf("plan reads no index keyed by the parent:\n%s", strings.Join(detail, "\n"))
	}
}
