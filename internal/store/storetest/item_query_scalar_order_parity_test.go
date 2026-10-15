// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryScalarAndOrderParitySQLite exercises the public ItemStore
// boundary against SQLite. The PostgreSQL twin below runs the exact same cases
// when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreQueryScalarAndOrderParitySQLite(t *testing.T) {
	runItemStoreQueryScalarAndOrderParity(t, openItemStore)
}

func TestItemStoreQueryScalarAndOrderParityPostgres(t *testing.T) {
	runItemStoreQueryScalarAndOrderParity(t, openPostgresItemStore)
}

// TestItemStoreQueryRepresentativeMultiOrderParitySQLite keeps nullable,
// tie-breaking, and page-boundary behavior aligned with the scanned corpus on
// SQLite without relying on generated identity order.
func TestItemStoreQueryRepresentativeMultiOrderParitySQLite(t *testing.T) {
	runItemStoreQueryRepresentativeMultiOrderParity(t, openItemStore)
}

// TestItemStoreQueryRepresentativeMultiOrderParityPostgres runs the exact
// ordering table against a real PostgreSQL store when DEADFIN_TEST_POSTGRES_DSN
// is configured.
func TestItemStoreQueryRepresentativeMultiOrderParityPostgres(t *testing.T) {
	runItemStoreQueryRepresentativeMultiOrderParity(t, openPostgresItemStore)
}

// TestItemStoreQuerySpecialSeasonNullableIndexSQLite verifies that season
// classification treats a missing index as non-special through the public
// query boundary.
func TestItemStoreQuerySpecialSeasonNullableIndexSQLite(t *testing.T) {
	runItemStoreQuerySpecialSeasonNullableIndex(t, openItemStore)
}

// TestItemStoreQuerySpecialSeasonNullableIndexPostgres runs the same nullable
// index contract against a real PostgreSQL store when configured.
func TestItemStoreQuerySpecialSeasonNullableIndexPostgres(t *testing.T) {
	runItemStoreQuerySpecialSeasonNullableIndex(t, openPostgresItemStore)
}

func runItemStoreQuerySpecialSeasonNullableIndex(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	zero, nonzero := 0, 7
	withoutIndex := scalarOrderStoreItem(t, 501, "a-no-index", 0)
	withoutIndex.Kind = item.KindSeason
	special := scalarOrderStoreItem(t, 502, "b-special", 0)
	special.Kind, special.IndexNumber = item.KindSeason, &zero
	regular := scalarOrderStoreItem(t, 503, "c-regular", 0)
	regular.Kind, regular.IndexNumber = item.KindSeason, &nonzero
	for _, it := range []item.Item{withoutIndex, special, regular} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting special-season fixture item %s: %v", it.ID, err)
		}
	}

	trueValue, falseValue := true, false
	cases := []struct {
		name  string
		value *bool
		want  []dto.UUID
	}{
		{name: "special seasons select zero index only", value: &trueValue, want: []dto.UUID{special.ID}},
		{name: "regular seasons retain missing and nonzero indexes", value: &falseValue, want: []dto.UUID{withoutIndex.ID, regular.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := items.Query(t.Context(), store.ItemQuery{
				IsSpecialSeason:        tc.value,
				SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
				SortOrder:              []store.SortOrder{store.SortOrderAscending},
				EnableTotalRecordCount: true,
			})
			if err != nil {
				t.Fatalf("querying special-season fixture: %v", err)
			}
			if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, tc.want) || got.TotalRecordCount != len(tc.want) {
				t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, tc.want, len(tc.want))
			}
		})
	}
}

// TestItemStoreQueryPremiereDateFallbackParitySQLite covers source-compatible
// PremiereDate fallback ordering at the date range boundaries on SQLite.
func TestItemStoreQueryPremiereDateFallbackParitySQLite(t *testing.T) {
	runItemStoreQueryPremiereDateFallbackParity(t, openItemStore)
}

// TestItemStoreQueryPremiereDateFallbackParityPostgres runs the same boundary
// fixture against PostgreSQL, including nil dates with invalid production years.
func TestItemStoreQueryPremiereDateFallbackParityPostgres(t *testing.T) {
	runItemStoreQueryPremiereDateFallbackParity(t, openPostgresItemStore)
}

func runItemStoreQueryScalarAndOrderParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newScalarOrderStoreFixture(t, open(t))
	trueValue, falseValue := true, false
	two := 2
	createdBoundary := scalarOrderStoreTime(2025, 2)
	savedBoundary := scalarOrderStoreTime(2025, 2)

	t.Run("direct scalar filters preserve null and inclusive boundary rules", func(t *testing.T) {
		cases := []struct {
			name  string
			query store.ItemQuery
			want  []dto.UUID
		}{
			{"locked true", store.ItemQuery{IsLocked: &trueValue}, []dto.UUID{fixture.delta.ID}},
			{"locked false", store.ItemQuery{IsLocked: &falseValue}, []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID}},
			{"overview present excludes null and empty", store.ItemQuery{HasOverview: &trueValue}, []dto.UUID{fixture.alpha.ID, fixture.delta.ID}},
			{"overview absent includes null and empty", store.ItemQuery{HasOverview: &falseValue}, []dto.UUID{fixture.bravo.ID, fixture.charlie.ID}},
			{"official rating present rejects null and empty", store.ItemQuery{HasOfficialRating: &trueValue}, []dto.UUID{fixture.alpha.ID}},
			{"minimum index includes its boundary", store.ItemQuery{MinIndexNumber: &two}, []dto.UUID{fixture.bravo.ID, fixture.delta.ID}},
			{"compound parent and index cursor includes same-parent boundary", store.ItemQuery{MinParentAndIndexNumber: &store.ItemQueryParentAndIndexNumber{ParentIndexNumber: 2, IndexNumber: 2}}, []dto.UUID{fixture.bravo.ID, fixture.delta.ID}},
			{"exact index", store.ItemQuery{IndexNumber: &two}, []dto.UUID{fixture.bravo.ID}},
			{"exact parent index", store.ItemQuery{ParentIndexNumber: &two}, []dto.UUID{fixture.bravo.ID}},
			{"parent index not equal retains null", store.ItemQuery{ParentIndexNumberNotEquals: &two}, []dto.UUID{fixture.alpha.ID, fixture.charlie.ID, fixture.delta.ID}},
			{"created lower bound is inclusive", store.ItemQuery{MinDateCreated: &createdBoundary}, []dto.UUID{fixture.bravo.ID, fixture.delta.ID}},
			{"saved lower bound excludes null and includes boundary", store.ItemQuery{MinDateLastSaved: &savedBoundary}, []dto.UUID{fixture.bravo.ID}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fixture.assertQuery(t, tc.query, tc.want)
			})
		}
	})

	t.Run("every pinned deterministic sort has exact ids in both directions", func(t *testing.T) {
		ascendingNumeric := []dto.UUID{fixture.charlie.ID, fixture.alpha.ID, fixture.bravo.ID, fixture.delta.ID}
		descendingNumeric := []dto.UUID{fixture.delta.ID, fixture.bravo.ID, fixture.alpha.ID, fixture.charlie.ID}
		ascendingValues := []dto.UUID{fixture.charlie.ID, fixture.bravo.ID, fixture.delta.ID, fixture.alpha.ID}
		descendingValues := []dto.UUID{fixture.alpha.ID, fixture.delta.ID, fixture.bravo.ID, fixture.charlie.ID}
		ascendingNames := []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID, fixture.delta.ID}
		descendingNames := []dto.UUID{fixture.delta.ID, fixture.charlie.ID, fixture.bravo.ID, fixture.alpha.ID}
		ascendingPremiere := []dto.UUID{fixture.charlie.ID, fixture.bravo.ID, fixture.alpha.ID, fixture.delta.ID}
		descendingPremiere := []dto.UUID{fixture.delta.ID, fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID}
		ascendingProductionYear := []dto.UUID{fixture.charlie.ID, fixture.bravo.ID, fixture.alpha.ID, fixture.delta.ID}
		descendingProductionYear := []dto.UUID{fixture.delta.ID, fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID}
		ascendingPlayCount := []dto.UUID{fixture.charlie.ID, fixture.alpha.ID, fixture.bravo.ID, fixture.delta.ID}
		descendingPlayCount := []dto.UUID{fixture.bravo.ID, fixture.delta.ID, fixture.alpha.ID, fixture.charlie.ID}
		ascendingSeriesPlayed := []dto.UUID{fixture.bravo.ID, fixture.charlie.ID, fixture.alpha.ID, fixture.delta.ID}
		descendingSeriesPlayed := []dto.UUID{fixture.delta.ID, fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID}
		ascendingPlayed := []dto.UUID{fixture.bravo.ID, fixture.charlie.ID, fixture.alpha.ID, fixture.delta.ID}
		descendingPlayed := []dto.UUID{fixture.alpha.ID, fixture.delta.ID, fixture.bravo.ID, fixture.charlie.ID}
		ascendingUnplayed := []dto.UUID{fixture.alpha.ID, fixture.delta.ID, fixture.bravo.ID, fixture.charlie.ID}
		descendingUnplayed := []dto.UUID{fixture.bravo.ID, fixture.charlie.ID, fixture.alpha.ID, fixture.delta.ID}
		ascendingFavorite := []dto.UUID{fixture.bravo.ID, fixture.charlie.ID, fixture.delta.ID, fixture.alpha.ID}
		descendingFavorite := []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID, fixture.delta.ID}
		ascendingFolder := []dto.UUID{fixture.alpha.ID, fixture.charlie.ID, fixture.delta.ID, fixture.bravo.ID}
		descendingFolder := []dto.UUID{fixture.bravo.ID, fixture.alpha.ID, fixture.charlie.ID, fixture.delta.ID}

		cases := []struct {
			sort store.ItemSortBy
			asc  []dto.UUID
			desc []dto.UUID
		}{
			{store.ItemSortByDefault, ascendingNames, ascendingNames},
			{store.ItemSortByAiredEpisodeOrder, ascendingNames, descendingNames},
			{store.ItemSortByAlbum, ascendingValues, descendingValues},
			{store.ItemSortByAlbumArtist, ascendingValues, descendingValues},
			{store.ItemSortByArtist, ascendingValues, descendingValues},
			{store.ItemSortByDateCreated, ascendingNumeric, descendingNumeric},
			{store.ItemSortByOfficialRating, ascendingNumeric, descendingNumeric},
			{store.ItemSortByDatePlayed, ascendingNumeric, descendingPlayCount},
			{store.ItemSortByPremiereDate, ascendingPremiere, descendingPremiere},
			{store.ItemSortByStartDate, ascendingNumeric, descendingNumeric},
			{store.ItemSortBySortName, ascendingNames, descendingNames},
			{store.ItemSortByName, ascendingNames, descendingNames},
			{store.ItemSortByRuntime, ascendingNumeric, descendingNumeric},
			{store.ItemSortByCommunityRating, ascendingNumeric, descendingNumeric},
			{store.ItemSortByProductionYear, ascendingProductionYear, descendingProductionYear},
			{store.ItemSortByPlayCount, ascendingPlayCount, descendingPlayCount},
			{store.ItemSortByCriticRating, ascendingNumeric, descendingNumeric},
			{store.ItemSortByIsFolder, ascendingFolder, descendingFolder},
			{store.ItemSortByIsUnplayed, ascendingUnplayed, descendingUnplayed},
			{store.ItemSortByIsPlayed, ascendingPlayed, descendingPlayed},
			{store.ItemSortBySeriesSortName, ascendingValues, descendingValues},
			{store.ItemSortByVideoBitRate, ascendingNumeric, descendingNumeric},
			{store.ItemSortByAirTime, ascendingNames, descendingNames},
			{store.ItemSortByStudio, ascendingValues, descendingValues},
			{store.ItemSortByIsFavoriteOrLiked, ascendingFavorite, descendingFavorite},
			{store.ItemSortByDateLastContentAdded, ascendingNumeric, descendingNumeric},
			{store.ItemSortBySeriesDatePlayed, ascendingSeriesPlayed, descendingSeriesPlayed},
			{store.ItemSortByParentIndexNumber, ascendingNumeric, descendingNumeric},
			{store.ItemSortByIndexNumber, ascendingNumeric, descendingNumeric},
		}
		if got, want := len(cases)+1, len(store.ItemSortByValues()); got != want {
			t.Fatalf("deterministic sort cases + Random = %d, want pinned inventory size %d", got, want)
		}
		expected := make([]store.ItemSortBy, 0, len(cases))
		for _, sort := range store.ItemSortByValues() {
			if sort != store.ItemSortByRandom {
				expected = append(expected, sort)
			}
		}
		gotSorts := make([]store.ItemSortBy, len(cases))
		for index, tc := range cases {
			gotSorts[index] = tc.sort
		}
		if !reflect.DeepEqual(gotSorts, expected) {
			t.Fatalf("deterministic sort cases = %v, want pinned inventory without Random %v", gotSorts, expected)
		}
		for _, tc := range cases {
			t.Run(string(tc.sort), func(t *testing.T) {
				fixture.assertQuery(t, store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: tc.sort, SortOrder: store.SortOrderAscending}}}, tc.asc)
				fixture.assertQuery(t, store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: tc.sort, SortOrder: store.SortOrderDescending}}}, tc.desc)
			})
		}
	})

	t.Run("random preserves unpaged fixture membership and total count without asserting order", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByRandom, SortOrder: store.SortOrderAscending}}, EnableTotalRecordCount: true})
		if got.TotalRecordCount != 4 || !sameScalarIDSet(got.Items, []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID, fixture.delta.ID}) {
			t.Fatalf("random result IDs/count = %v/%d, want each fixture item exactly once and count 4", scalarOrderStoreIDs(got.Items), got.TotalRecordCount)
		}
	})

	t.Run("multi-sort broadcasts, breaks ties, and counts before paging", func(t *testing.T) {
		limit, start := 1, 1
		got := fixture.query(t, store.ItemQuery{
			SortBy:                 []store.ItemSortBy{store.ItemSortByPlayCount, store.ItemSortByName},
			SortOrder:              []store.SortOrder{store.SortOrderDescending},
			Limit:                  &limit,
			StartIndex:             &start,
			EnableTotalRecordCount: true,
		})
		if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, []dto.UUID{fixture.bravo.ID}) || got.TotalRecordCount != 4 {
			t.Fatalf("broadcast/paged IDs/count = %v/%d, want [%s]/4", ids, got.TotalRecordCount, fixture.bravo.ID)
		}
	})
}

func runItemStoreQueryRepresentativeMultiOrderParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	cases := []multiOrderStoreCase{
		{
			name:  "sort name uses display name for equal source-compatible keys",
			sorts: []store.ItemSortBy{store.ItemSortBySortName},
			entries: []multiOrderStoreEntry{
				{name: "sort-null", sortName: nil},
				{name: "sort-alpha-first", sortName: scalarOrderStoreString("alpha")},
				{name: "sort-alpha-second", sortName: scalarOrderStoreString("alpha")},
				{name: "sort-bravo", sortName: scalarOrderStoreString("bravo")},
			},
			ascending:  []string{"sort-null", "sort-alpha-first", "sort-alpha-second", "sort-bravo"},
			descending: []string{"sort-bravo", "sort-alpha-second", "sort-alpha-first", "sort-null"},
		},
		{
			name:  "production year date and sort name break nullable ties",
			sorts: []store.ItemSortBy{store.ItemSortByProductionYear, store.ItemSortByPremiereDate, store.ItemSortBySortName},
			entries: []multiOrderStoreEntry{
				{name: "year-null", sortName: scalarOrderStoreString("null"), productionYear: nil, premiereDate: nil},
				{name: "year-2000-no-date", sortName: scalarOrderStoreString("a-no-date"), productionYear: scalarOrderStoreInt(2000), premiereDate: nil},
				{name: "year-2000-date-first", sortName: scalarOrderStoreString("a-date"), productionYear: scalarOrderStoreInt(2000), premiereDate: scalarOrderStoreTimePointer(2000, 2)},
				{name: "year-2000-date-second", sortName: scalarOrderStoreString("b-date"), productionYear: scalarOrderStoreInt(2000), premiereDate: scalarOrderStoreTimePointer(2000, 2)},
				{name: "year-2020-date", sortName: scalarOrderStoreString("z-date"), productionYear: scalarOrderStoreInt(2020), premiereDate: scalarOrderStoreTimePointer(2020, 1)},
			},
			ascending:  []string{"year-null", "year-2000-no-date", "year-2000-date-first", "year-2000-date-second", "year-2020-date"},
			descending: []string{"year-2020-date", "year-2000-date-second", "year-2000-date-first", "year-2000-no-date", "year-null"},
		},
		{
			name:  "rating index and sort name break nullable ties",
			sorts: []store.ItemSortBy{store.ItemSortByOfficialRating, store.ItemSortByIndexNumber, store.ItemSortBySortName},
			entries: []multiOrderStoreEntry{
				{name: "rating-null", sortName: scalarOrderStoreString("null"), rating: nil, indexNumber: nil},
				{name: "rating-ten-no-index", sortName: scalarOrderStoreString("no-index"), rating: scalarOrderStoreInt(10), indexNumber: nil},
				{name: "rating-ten-index-first", sortName: scalarOrderStoreString("a-index"), rating: scalarOrderStoreInt(10), indexNumber: scalarOrderStoreInt(2)},
				{name: "rating-ten-index-second", sortName: scalarOrderStoreString("b-index"), rating: scalarOrderStoreInt(10), indexNumber: scalarOrderStoreInt(2)},
				{name: "rating-twenty-index", sortName: scalarOrderStoreString("z-index"), rating: scalarOrderStoreInt(20), indexNumber: scalarOrderStoreInt(1)},
			},
			ascending:  []string{"rating-null", "rating-ten-no-index", "rating-ten-index-first", "rating-ten-index-second", "rating-twenty-index"},
			descending: []string{"rating-twenty-index", "rating-ten-index-second", "rating-ten-index-first", "rating-ten-no-index", "rating-null"},
		},
	}

	for caseIndex, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := open(t)
			parentID := scalarOrderStoreUUID(t, 600+caseIndex)
			parentName := "multi-order-parent"
			if err := items.Upsert(t.Context(), item.Item{ID: parentID, Kind: item.KindFolder, Name: &parentName, SortName: &parentName, IsFolder: true}); err != nil {
				t.Fatalf("upserting multi-order parent: %v", err)
			}
			for entryIndex, entry := range tc.entries {
				child := item.Item{
					ID:                           scalarOrderStoreUUID(t, 700+caseIndex*10+entryIndex),
					Kind:                         item.KindMovie,
					ParentID:                     &parentID,
					Name:                         scalarOrderStoreString(entry.name),
					SortName:                     entry.sortName,
					ProductionYear:               entry.productionYear,
					PremiereDate:                 entry.premiereDate,
					InheritedParentalRatingValue: entry.rating,
					IndexNumber:                  entry.indexNumber,
				}
				if err := items.Upsert(t.Context(), child); err != nil {
					t.Fatalf("upserting multi-order item %q: %v", entry.name, err)
				}
			}
			for _, direction := range []struct {
				name  string
				order store.SortOrder
				want  []string
			}{
				{name: "ascending", order: store.SortOrderAscending, want: tc.ascending},
				{name: "descending", order: store.SortOrderDescending, want: tc.descending},
			} {
				t.Run(direction.name, func(t *testing.T) {
					orders := make([]store.ItemOrder, len(tc.sorts))
					for index, sort := range tc.sorts {
						orders[index] = store.ItemOrder{SortBy: sort, SortOrder: direction.order}
					}
					query := store.ItemQuery{ParentID: &parentID, OrderBy: orders, EnableTotalRecordCount: true}
					assertMultiOrderPage(t, items, query, direction.want)
				})
			}
		})
	}
}

func assertMultiOrderPage(t *testing.T, items *store.ItemStore, query store.ItemQuery, want []string) {
	t.Helper()
	assertPage := func(name string, start, limit int, wantPage []string) {
		t.Helper()
		paged := query
		paged.StartIndex, paged.Limit = &start, &limit
		got, err := items.Query(t.Context(), paged)
		if err != nil {
			t.Fatalf("querying %s page: %v", name, err)
		}
		if names := multiOrderStoreNames(got.Items); !reflect.DeepEqual(names, wantPage) || got.TotalRecordCount != len(want) {
			t.Fatalf("%s page names/count = %v/%d, want %v/%d", name, names, got.TotalRecordCount, wantPage, len(want))
		}
	}
	assertPage("unpaged", 0, len(want), want)
	assertPage("first", 0, 2, want[:2])
	assertPage("middle", 1, 2, want[1:3])
	assertPage("last", len(want)-2, 2, want[len(want)-2:])
	assertPage("past-end", len(want), 2, []string{})
}

func runItemStoreQueryPremiereDateFallbackParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newPremiereDateFallbackFixture(t, open(t))

	canonicalAscending := []dto.UUID{fixture.invalidNegative.ID, fixture.invalidZero.ID, fixture.invalidMaximum.ID, fixture.yearOne.ID, fixture.dateOne.ID, fixture.yearMiddle.ID, fixture.dateMiddle.ID, fixture.yearMaximum.ID, fixture.dateMaximum.ID}
	canonicalDescending := []dto.UUID{fixture.dateMaximum.ID, fixture.yearMaximum.ID, fixture.dateMiddle.ID, fixture.yearMiddle.ID, fixture.dateOne.ID, fixture.yearOne.ID, fixture.invalidNegative.ID, fixture.invalidZero.ID, fixture.invalidMaximum.ID}
	legacyAscending := []dto.UUID{fixture.invalidNegative.ID, fixture.invalidZero.ID, fixture.invalidMaximum.ID, fixture.yearOne.ID, fixture.dateOne.ID, fixture.yearMiddle.ID, fixture.dateMiddle.ID, fixture.yearMaximum.ID, fixture.dateMaximum.ID}
	legacyDescending := []dto.UUID{fixture.dateMaximum.ID, fixture.yearMaximum.ID, fixture.dateMiddle.ID, fixture.yearMiddle.ID, fixture.dateOne.ID, fixture.invalidNegative.ID, fixture.invalidZero.ID, fixture.invalidMaximum.ID, fixture.yearOne.ID}

	paths := []struct {
		name  string
		query func(store.SortOrder) store.ItemQuery
		asc   []dto.UUID
		desc  []dto.UUID
	}{
		{
			name: "canonical OrderBy leaves invalid and absent dates null",
			query: func(direction store.SortOrder) store.ItemQuery {
				return store.ItemQuery{OrderBy: []store.ItemOrder{{SortBy: store.ItemSortByPremiereDate, SortOrder: direction}}}
			},
			asc: canonicalAscending, desc: canonicalDescending,
		},
		{
			name: "legacy SortBy maps invalid and absent dates to MinValue",
			query: func(direction store.SortOrder) store.ItemQuery {
				return store.ItemQuery{SortBy: []store.ItemSortBy{store.ItemSortByPremiereDate}, SortOrder: []store.SortOrder{direction}}
			},
			asc: legacyAscending, desc: legacyDescending,
		},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			for _, direction := range []struct {
				name  string
				value store.SortOrder
				want  []dto.UUID
			}{
				{name: "ascending", value: store.SortOrderAscending, want: path.asc},
				{name: "descending", value: store.SortOrderDescending, want: path.desc},
			} {
				t.Run(direction.name, func(t *testing.T) {
					query := path.query(direction.value)
					fixture.assertQuery(t, query, direction.want)

					limit, start := 3, 2
					query.Limit, query.StartIndex = &limit, &start
					fixture.assertQuery(t, query, direction.want[start:start+limit])
				})
			}
		})
	}
}

type premiereDateFallbackFixture struct {
	items *store.ItemStore

	invalidNegative item.Item
	invalidZero     item.Item
	invalidMaximum  item.Item
	yearOne         item.Item
	dateOne         item.Item
	yearMiddle      item.Item
	dateMiddle      item.Item
	yearMaximum     item.Item
	dateMaximum     item.Item
}

func newPremiereDateFallbackFixture(t *testing.T, items *store.ItemStore) premiereDateFallbackFixture {
	t.Helper()
	dateOne := time.Date(1, time.January, 2, 0, 0, 0, 0, time.UTC)
	dateMiddle := time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC)
	dateMaximum := time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
	newItem := func(id, productionYear int, name string, premiereDate *time.Time) item.Item {
		it := scalarOrderStoreItem(t, id, name, 0)
		it.ProductionYear = scalarOrderStoreInt(productionYear)
		it.PremiereDate = premiereDate
		return it
	}
	fixture := premiereDateFallbackFixture{
		items:           items,
		invalidNegative: newItem(401, -1, "a-invalid-negative", nil),
		invalidZero:     newItem(402, 0, "b-invalid-zero", nil),
		invalidMaximum:  newItem(403, 10000, "c-invalid-maximum", nil),
		yearOne:         newItem(404, 1, "d-year-one", nil),
		dateOne:         newItem(405, 2000, "e-date-year-one", &dateOne),
		yearMiddle:      newItem(406, 2020, "f-year-middle", nil),
		dateMiddle:      newItem(407, 2000, "g-date-middle", &dateMiddle),
		yearMaximum:     newItem(408, 9999, "h-year-maximum", nil),
		dateMaximum:     newItem(409, 2000, "i-date-maximum", &dateMaximum),
	}
	for _, it := range []item.Item{fixture.invalidNegative, fixture.invalidZero, fixture.invalidMaximum, fixture.yearOne, fixture.dateOne, fixture.yearMiddle, fixture.dateMiddle, fixture.yearMaximum, fixture.dateMaximum} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting premiere-date fallback fixture item %s: %v", it.ID, err)
		}
	}
	return fixture
}

func (f premiereDateFallbackFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID) {
	t.Helper()
	query.EnableTotalRecordCount = true
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying premiere-date fallback fixture: %v", err)
	}
	if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, want) || got.TotalRecordCount != 9 {
		t.Fatalf("query IDs/count = %v/%d, want %v/9", ids, got.TotalRecordCount, want)
	}
	f.assertScalars(t, got.Items)
}

func (f premiereDateFallbackFixture) assertScalars(t *testing.T, got []item.Item) {
	t.Helper()
	wantByID := map[dto.UUID]item.Item{}
	for _, want := range []item.Item{f.invalidNegative, f.invalidZero, f.invalidMaximum, f.yearOne, f.dateOne, f.yearMiddle, f.dateMiddle, f.yearMaximum, f.dateMaximum} {
		wantByID[want.ID] = want
	}
	for _, actual := range got {
		want, ok := wantByID[actual.ID]
		if !ok {
			t.Fatalf("unexpected item %s", actual.ID)
		}
		if !sameScalarOrderInt(actual.ProductionYear, want.ProductionYear) || !sameScalarOrderTime(actual.PremiereDate, want.PremiereDate) {
			t.Fatalf("item %s premiere/year = %v/%v, want %v/%v", actual.ID, actual.PremiereDate, actual.ProductionYear, want.PremiereDate, want.ProductionYear)
		}
	}
}

func sameScalarOrderInt(got, want *int) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && *got == *want)
}

func sameScalarOrderTime(got, want *time.Time) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && got.Equal(*want))
}

type scalarOrderStoreFixture struct {
	items                        *store.ItemStore
	userID                       dto.UUID
	alpha, bravo, charlie, delta item.Item
}

func newScalarOrderStoreFixture(t *testing.T, items *store.ItemStore) scalarOrderStoreFixture {
	t.Helper()
	userID := scalarOrderStoreUUID(t, 300)
	alpha := scalarOrderStoreItem(t, 10, "alpha", 1)
	bravo := scalarOrderStoreItem(t, 11, "bravo", 2)
	charlie := scalarOrderStoreItem(t, 12, "charlie", 0)
	delta := scalarOrderStoreItem(t, 13, "delta", 3)

	alpha.Album = scalarOrderStoreString("z")
	alpha.Values = item.ItemValues{Artists: []string{"z"}, AlbumArtists: []string{"z"}, Studios: []string{"z"}}
	alpha.Overview = scalarOrderStoreString("overview")
	alpha.OfficialRating = scalarOrderStoreString("PG")
	alpha.PremiereDate = scalarOrderStoreTimePointer(2020, 1)
	alpha.DateLastSaved = scalarOrderStoreTimePointer(2025, 1)

	bravo.Album = scalarOrderStoreString("a")
	bravo.Values = item.ItemValues{Artists: []string{"a"}, AlbumArtists: []string{"a"}, Studios: []string{"a"}}
	bravo.Overview = scalarOrderStoreString("")
	bravo.ProductionYear = scalarOrderStoreInt(2019)
	bravo.DateLastSaved = scalarOrderStoreTimePointer(2025, 2)
	bravo.IsFolder = true

	delta.Album = scalarOrderStoreString("b")
	delta.Values = item.ItemValues{Artists: []string{"b"}, AlbumArtists: []string{"b"}, Studios: []string{"b"}}
	delta.Overview = scalarOrderStoreString("overview")
	delta.PremiereDate = scalarOrderStoreTimePointer(2022, 3)
	delta.IsLocked = true

	for _, it := range []item.Item{alpha, bravo, charlie, delta} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting scalar/order fixture item %s: %v", it.ID, err)
		}
	}
	// User rows must follow public upserts, after their item and canonical key
	// materialization. They give the user-relative sorts both nonzero and absent
	// values so COALESCE and stable ties are observable.
	for _, row := range []struct {
		it       item.Item
		play     int
		favorite bool
		played   bool
	}{
		{alpha, 1, true, true}, {bravo, 2, false, false}, {delta, 2, false, true},
	} {
		lastPlayed := scalarOrderStoreTime(2025, row.play)
		key := scalarOrderStoreUserKey(t, row.it)
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: key, ItemID: row.it.ID, PlayCount: row.play, IsFavorite: row.favorite, Played: row.played, LastPlayed: &lastPlayed}); err != nil {
			t.Fatalf("upserting scalar/order user data for %s: %v", row.it.ID, err)
		}
	}
	return scalarOrderStoreFixture{items: items, userID: userID, alpha: alpha, bravo: bravo, charlie: charlie, delta: delta}
}

func (f scalarOrderStoreFixture) query(t *testing.T, query store.ItemQuery) store.ItemQueryResult {
	t.Helper()
	query.UserID = &f.userID
	if !query.EnableTotalRecordCount {
		query.EnableTotalRecordCount = true
	}
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying scalar/order fixture: %v", err)
	}
	return got
}

func (f scalarOrderStoreFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID) {
	t.Helper()
	if len(query.SortBy) == 0 {
		query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
		query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	}
	got := f.query(t, query)
	if ids := scalarOrderStoreIDs(got.Items); !reflect.DeepEqual(ids, want) || got.TotalRecordCount != len(want) {
		t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, len(want))
	}
}

func scalarOrderStoreItem(t *testing.T, value int, name string, rank int) item.Item {
	t.Helper()
	base := item.Item{ID: scalarOrderStoreUUID(t, value), Kind: item.KindMovie, Name: scalarOrderStoreString(name), SortName: scalarOrderStoreString(name)}
	if rank == 0 {
		return base
	}
	at := scalarOrderStoreTime(2025, rank)
	base.DateCreated, base.StartDate, base.DateLastMediaAdded = &at, &at, &at
	base.SeriesName = scalarOrderStoreString(map[int]string{1: "z", 2: "a", 3: "b"}[rank])
	base.ProductionYear, base.IndexNumber, base.ParentIndexNumber = scalarOrderStoreInt(2020+rank), scalarOrderStoreInt(rank), scalarOrderStoreInt(rank)
	base.Width, base.Height, base.TotalBitrate, base.InheritedParentalRatingValue = scalarOrderStoreInt(rank*100), scalarOrderStoreInt(rank*100), scalarOrderStoreInt(rank*100), scalarOrderStoreInt(rank)
	base.RunTimeTicks, base.CommunityRating, base.CriticRating = scalarOrderStoreInt64(int64(rank*100)), scalarOrderStoreFloat32(float32(rank)), scalarOrderStoreFloat32(float32(rank))
	key := fmt.Sprintf("series-%d", rank)
	base.PresentationUniqueKey, base.SeriesPresentationUniqueKey = scalarOrderStoreString(key), scalarOrderStoreString(key)
	return base
}

func scalarOrderStoreUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func scalarOrderStoreUserKey(t *testing.T, it item.Item) string {
	t.Helper()
	keys := item.UserDataKeys(it, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatalf("item %s has no canonical user-data keys", it.ID)
	}
	return keys[0]
}

func scalarOrderStoreIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}

func sameScalarIDSet(items []item.Item, want []dto.UUID) bool {
	if len(items) != len(want) {
		return false
	}
	seen := make(map[dto.UUID]struct{}, len(items))
	for _, it := range items {
		seen[it.ID] = struct{}{}
	}
	if len(seen) != len(want) {
		return false
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func scalarOrderStoreTime(year, day int) time.Time {
	return time.Date(year, 1, day, 0, 0, 0, 0, time.UTC)
}

func scalarOrderStoreTimePointer(year, day int) *time.Time {
	value := scalarOrderStoreTime(year, day)
	return &value
}
func scalarOrderStoreString(value string) *string    { return &value }
func scalarOrderStoreInt(value int) *int             { return &value }
func scalarOrderStoreInt64(value int64) *int64       { return &value }
func scalarOrderStoreFloat32(value float32) *float32 { return &value }

type multiOrderStoreCase struct {
	name       string
	sorts      []store.ItemSortBy
	entries    []multiOrderStoreEntry
	ascending  []string
	descending []string
}

type multiOrderStoreEntry struct {
	name           string
	sortName       *string
	productionYear *int
	premiereDate   *time.Time
	rating         *int
	indexNumber    *int
}

func multiOrderStoreNames(items []item.Item) []string {
	names := make([]string, len(items))
	for index, it := range items {
		if it.Name == nil {
			names[index] = "<nil>"
			continue
		}
		names[index] = *it.Name
	}
	return names
}
