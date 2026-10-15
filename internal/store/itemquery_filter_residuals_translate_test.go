// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryResidualBoundaries defends boundary behavior that is
// otherwise obscured by an implicit database clock or by a zero-value UUID.
func TestTranslateItemQueryResidualBoundaries(t *testing.T) {
	t.Run("unaired includes the exact request reference instant", func(t *testing.T) {
		trueValue, falseValue := true, false
		reference := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		cases := []struct {
			name  string
			value *bool
			want  string
		}{
			{name: "unaired", value: &trueValue, want: "premiere_date >= ?"},
			{name: "aired", value: &falseValue, want: "premiere_date < ?"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				translated, err := store.Translate(store.ItemQuery{IsUnaired: tc.value, ReferenceTime: &reference}, sqlite.Dialect{})
				if err != nil {
					t.Fatalf("translating %s boundary: %v", tc.name, err)
				}
				if !strings.Contains(translated.SQL, tc.want) || !strings.Contains(translated.CountSQL, tc.want) {
					t.Fatalf("%s boundary SQL page=%q count=%q, want %q", tc.name, translated.SQL, translated.CountSQL, tc.want)
				}
				if len(translated.Args) != 1 || translated.Args[0] != (sqlite.Dialect{}).TimeValue(reference) {
					t.Fatalf("%s reference args = %#v, want exactly the request reference", tc.name, translated.Args)
				}
			})
		}
	})
	t.Run("zero user UUID is rejected for user relative filters", func(t *testing.T) {
		trueValue := true
		zeroUser := dto.UUID{}
		if _, err := store.Translate(store.ItemQuery{UserID: &zeroUser, IsPlayed: &trueValue}, sqlite.Dialect{}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "user") {
			t.Fatalf("Translate(IsPlayed with zero UserID) error = %v, want user identity error", err)
		}
	})
}
