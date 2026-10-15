// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

var outerItemJoin = regexp.MustCompile(`(?i)\bFROM\s+items(?:\s+AS\s+\w+)?\s+JOIN\b`)

// TestTranslateItemQueryFilterFamiliesUsesPortableExistsAndParameters defends
// the executable SQL boundary: every relation-backed predicate narrows items
// with a correlated EXISTS, while caller values remain arguments for both
// dialects and the count observes precisely the unpaged filtered set.
func TestTranslateItemQueryFilterFamiliesUsesPortableExistsAndParameters(t *testing.T) {
	ancestor := filterTranslateUUID(t, 1)
	topParent := filterTranslateUUID(t, 2)
	excluded := filterTranslateUUID(t, 3)
	person := filterTranslateUUID(t, 4)
	minPremiere := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	maxPremiere := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	minRating, maxRating := float32(6.5), float32(9.0)
	minRuntime, maxRuntime := int64(1_000), int64(9_000)
	isFolder, isVirtual := false, true
	unairedReference := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// The quote-bearing genre and wildcard-bearing search term make accidental
	// SQL interpolation observable without coupling this test to SQL layout.
	genre := "Drama' OR 1=1 --"
	query := store.ItemQuery{
		IncludeItemTypes:  []item.Kind{item.KindMovie},
		ExcludeItemTypes:  []item.Kind{item.KindEpisode},
		MediaTypes:        []item.MediaType{item.MediaTypeVideo},
		AncestorIDs:       []dto.UUID{ancestor},
		TopParentIDs:      []dto.UUID{topParent},
		ExcludeItemIDs:    []dto.UUID{excluded},
		Genres:            []string{genre},
		Studios:           []string{"North Star"},
		Tags:              []string{"Featured"},
		ExcludeTags:       []string{"Blocked"},
		HasAnyProviderID:  map[string]string{"Tmdb": "603"},
		HasAnyProviderIDs: map[string][]string{"Imdb": {"tt0084787", "*"}},
		ExcludeProviderIDs: map[string]string{
			"Tvdb": "81189",
		},
		PersonIDs:              []dto.UUID{person},
		Person:                 "Ava",
		PersonTypes:            []string{"Actor"},
		Years:                  []int{2024},
		MinPremiereDate:        &minPremiere,
		MaxPremiereDate:        &maxPremiere,
		MinCommunityRating:     &minRating,
		MaxCommunityRating:     &maxRating,
		MinRunTimeTicks:        &minRuntime,
		MaxRunTimeTicks:        &maxRuntime,
		SearchTerm:             "cafe_%",
		IsFolder:               &isFolder,
		IsVirtualItem:          &isVirtual,
		IsMissing:              &isVirtual,
		IsUnaired:              &isFolder,
		ReferenceTime:          &unairedReference,
		EnableTotalRecordCount: true,
		StartIndex:             filterTranslateInt(7),
		Limit:                  filterTranslateInt(11),
	}

	cases := []struct {
		name        string
		dialect     store.Dialect
		placeholder string
		forbidden   string
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}, placeholder: "?", forbidden: "$1"},
		{name: "PostgreSQL", dialect: postgres.Dialect{}, placeholder: "$1", forbidden: "?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("translating filter-family query: %v", err)
			}
			if !strings.Contains(translated.SQL, tc.placeholder) || !strings.Contains(translated.CountSQL, tc.placeholder) {
				t.Fatalf("%s SQL must use %q placeholders:\npage:  %s\ncount: %s", tc.name, tc.placeholder, translated.SQL, translated.CountSQL)
			}
			if strings.Contains(translated.SQL, tc.forbidden) || strings.Contains(translated.CountSQL, tc.forbidden) {
				t.Fatalf("%s SQL leaked the other dialect's placeholder syntax:\npage:  %s\ncount: %s", tc.name, translated.SQL, translated.CountSQL)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				if strings.Contains(sql, genre) || strings.Contains(sql, "cafe_%") || strings.Contains(sql, "tt0084787") {
					t.Fatalf("translated SQL interpolated a filter value: %s", sql)
				}
				if outerItemJoin.MatchString(sql) {
					t.Fatalf("translated SQL must retain one outer items row and use correlated EXISTS for satellite filters: %s", sql)
				}
				if !strings.Contains(strings.ToUpper(sql), "EXISTS (SELECT 1 FROM") {
					t.Fatalf("translated SQL must use EXISTS for relation-backed filters: %s", sql)
				}
			}
			if strings.Contains(strings.ToUpper(translated.CountSQL), " ORDER BY ") || strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Fatalf("count SQL must count the complete filtered set before ordering or pagination: %s", translated.CountSQL)
			}
			if !reflect.DeepEqual(translated.Args[:len(translated.CountArgs)], translated.CountArgs) {
				t.Fatalf("page filter arguments = %#v, want count filter arguments %#v as their exact prefix", translated.Args[:len(translated.CountArgs)], translated.CountArgs)
			}
			if len(translated.Args) < len(translated.CountArgs)+2 {
				t.Fatalf("page arguments = %#v, want filter arguments plus limit and start index", translated.Args)
			}
			if got, want := translated.Args[len(translated.Args)-2:], []any{11, 7}; !reflect.DeepEqual(got, want) {
				t.Fatalf("final pagination arguments = %#v, want %#v", got, want)
			}
		})
	}
}

// TestTranslateItemQueryUserRelativeFiltersRequireUserID defends the request
// validation boundary: user-relative predicates never silently query another
// user's data or all users when the caller supplies no user identity.
func TestTranslateItemQueryUserRelativeFiltersRequireUserID(t *testing.T) {
	trueValue := true
	cases := []struct {
		name  string
		query store.ItemQuery
	}{
		{name: "played", query: store.ItemQuery{IsPlayed: &trueValue}},
		{name: "resumable", query: store.ItemQuery{IsResumable: &trueValue}},
		{name: "favorite", query: store.ItemQuery{IsFavorite: &trueValue}},
		{name: "favorite or liked", query: store.ItemQuery{IsFavoriteOrLiked: &trueValue}},
		{name: "liked", query: store.ItemQuery{IsLiked: &trueValue}},
		{name: "last saved for user", query: store.ItemQuery{MinDateLastSavedForUser: filterTranslateTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Translate(tc.query, sqlite.Dialect{}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "user") {
				t.Fatalf("Translate(%s without UserID) error = %v, want a user identity error", tc.name, err)
			}
		})
	}
}

// TestTranslateItemQueryUnairedUsesExplicitReferenceTime defends deterministic
// unaired classification: callers supply the instant, and both predicates bind
// it as a dialect value rather than consulting the process wall clock.
func TestTranslateItemQueryUnairedUsesExplicitReferenceTime(t *testing.T) {
	reference := time.Date(2025, 6, 1, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name     string
		unaired  bool
		operator string
	}{
		{name: "unaired includes premiere at reference", unaired: true, operator: ">="},
		{name: "aired excludes premiere at reference", unaired: false, operator: "<"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(store.ItemQuery{IsUnaired: &tc.unaired, ReferenceTime: &reference}, sqlite.Dialect{})
			if err != nil {
				t.Fatalf("translate deterministic unaired query: %v", err)
			}
			if !strings.Contains(translated.SQL, "premiere_date "+tc.operator+" ?") {
				t.Fatalf("unaired SQL = %q, want bound premiere predicate using %q", translated.SQL, tc.operator)
			}
			if got, want := translated.Args, []any{sqlite.Dialect{}.TimeValue(reference)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("unaired args = %#v, want %#v", got, want)
			}
		})
	}
}

// TestTranslateItemQueryUnairedRequiresReferenceTime forbids a wall-clock
// fallback: zero and absent instants are ambiguous request inputs, not a clock
// choice the persistence layer may silently make.
func TestTranslateItemQueryUnairedRequiresReferenceTime(t *testing.T) {
	trueValue := true
	zero := time.Time{}
	cases := []struct {
		name      string
		reference *time.Time
	}{
		{name: "absent reference"},
		{name: "zero reference", reference: &zero},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Translate(store.ItemQuery{IsUnaired: &trueValue, ReferenceTime: tc.reference}, sqlite.Dialect{})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reference") {
				t.Fatalf("Translate unaired query with %s error = %v, want clear reference-time error", tc.name, err)
			}
		})
	}
}

func filterTranslateUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func filterTranslateInt(value int) *int { return &value }

func filterTranslateTime(value time.Time) *time.Time { return &value }
