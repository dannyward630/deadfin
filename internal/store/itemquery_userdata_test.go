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
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

var itemQueryUserDataOuterJoin = regexp.MustCompile(`(?i)\bFROM\s+items(?:\s+AS\s+\w+)?\s+JOIN\b`)

// TestTranslateItemQueryUserDataFiltersUseBoundCorrelatedPredicates defends the
// executable query boundary: user-relative facts remain item-set predicates
// over one outer item row (no join that could duplicate it), bind the caller's values for both dialects, and are
// identically applied to the unpaged count.
func TestTranslateItemQueryUserDataFiltersUseBoundCorrelatedPredicates(t *testing.T) {
	userID := itemQueryUserDataUUID(t, 1)
	minSaved := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	played, resumable, favorite, favoriteOrLiked, liked := true, false, true, false, false
	query := store.ItemQuery{
		UserID:                  &userID,
		IsPlayed:                &played,
		IsResumable:             &resumable,
		IsFavorite:              &favorite,
		IsFavoriteOrLiked:       &favoriteOrLiked,
		IsLiked:                 &liked,
		MinDateLastSavedForUser: &minSaved,
	}

	for _, tc := range []struct {
		name        string
		dialect     store.Dialect
		placeholder string
		forbidden   string
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}, placeholder: "?", forbidden: "$1"},
		{name: "PostgreSQL", dialect: postgres.Dialect{}, placeholder: "$1", forbidden: "?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("translating user-data query: %v", err)
			}
			if !strings.Contains(translated.SQL, tc.placeholder) || !strings.Contains(translated.CountSQL, tc.placeholder) {
				t.Fatalf("%s translated query did not bind values:\npage: %s\ncount: %s", tc.name, translated.SQL, translated.CountSQL)
			}
			if strings.Contains(translated.SQL, tc.forbidden) || strings.Contains(translated.CountSQL, tc.forbidden) {
				t.Fatalf("%s translated query used the other dialect's placeholders:\npage: %s\ncount: %s", tc.name, translated.SQL, translated.CountSQL)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				if itemQueryUserDataOuterJoin.MatchString(sql) {
					t.Fatalf("%s translated query must retain one outer item row: %s", tc.name, sql)
				}
				if got := strings.Count(strings.ToUpper(sql), ".ID IN (SELECT USER_DATA.ITEM_ID FROM USER_DATA"); got != 5 {
					t.Fatalf("%s user-data predicates = %d, want one item-set predicate for each state filter in %s", tc.name, got, sql)
				}
				if strings.Contains(sql, userID.String()) || strings.Contains(sql, minSaved.Format(time.RFC3339Nano)) {
					t.Fatalf("%s translated query interpolated caller data: %s", tc.name, sql)
				}
			}
			if !reflect.DeepEqual(translated.Args, translated.CountArgs) {
				t.Fatalf("%s page filter args = %#v, want identical count filter args %#v", tc.name, translated.Args, translated.CountArgs)
			}
			// Each state filter binds the user and its value once per half of the
			// item set (stored item id, carried custom key).
			if got, want := len(translated.CountArgs), 19; got != want {
				t.Fatalf("%s bound user-data args (%d) = %#v, want %d arguments", tc.name, got, translated.CountArgs, want)
			}
		})
	}
}

// TestTranslateItemQueryUserDataFiltersRequireUsableUserID defends the
// authorization boundary: a compound user-data query must reject both absent
// and zero-valued identity rather than silently reading a global or another
// user's state.
func TestTranslateItemQueryUserDataFiltersRequireUsableUserID(t *testing.T) {
	played := true
	minSaved := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	zeroUser := dto.UUID{}
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
	}{
		{name: "absent identity", query: store.ItemQuery{IsPlayed: &played, MinDateLastSavedForUser: &minSaved}},
		{name: "zero identity", query: store.ItemQuery{UserID: &zeroUser, IsPlayed: &played, MinDateLastSavedForUser: &minSaved}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Translate(tc.query, sqlite.Dialect{}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "user") {
				t.Fatalf("Translate user-data query with %s error = %v, want a user identity error", tc.name, err)
			}
		})
	}
}

func itemQueryUserDataUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing user-data fixture UUID %d: %v", value, err)
	}
	return id
}
