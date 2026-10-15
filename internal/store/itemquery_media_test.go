// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

var mediaOuterItemJoin = regexp.MustCompile(`(?i)\bFROM\s+items(?:\s+AS\s+\w+)?\s+JOIN\b`)

// TestTranslateItemQueryMediaFiltersBindsCallerValuesAndKeepsCountFilters
// defends the portable translation contract for the media family: a media
// query has one outer item row, carries every user value as a parameter, and
// gives the count query precisely the same semantic filters before paging.
// ItemStore parity tests execute these predicates on both backends.
func TestTranslateItemQueryMediaFiltersBindsCallerValuesAndKeepsCountFilters(t *testing.T) {
	trueValue := true
	internalLanguage := "internal' OR 1=1 --"
	externalLanguage := "external' OR 1=1 --"
	query := store.ItemQuery{
		VideoTypes:                             []item.VideoType{item.VideoTypeDvd},
		Is3D:                                   &trueValue,
		HasNoInternalSubtitleTrackWithLanguage: internalLanguage,
		HasNoExternalSubtitleTrackWithLanguage: externalLanguage,
		SubtitleLanguages:                      []string{"und"},
		HasChapterImages:                       &trueValue,
		HasTrailer:                             &trueValue,
		HasThemeSong:                           &trueValue,
		EnableTotalRecordCount:                 true,
		Limit:                                  mediaTranslateInt(1),
		StartIndex:                             mediaTranslateInt(2),
	}

	cases := []struct {
		name      string
		dialect   store.Dialect
		countArgs []any
		fragments []string
	}{
		{
			name:    "SQLite",
			dialect: sqlite.Dialect{},
			countArgs: []any{
				"Subtitle", internalLanguage, 0,
				"Subtitle", externalLanguage, 1,
				"Subtitle", "und", "2", "0", "2", "8",
			},
			fragments: []string{
				"media_stream.is_external = ?", "media_stream.language IS NULL OR media_stream.language = ''",
			},
		},
		{
			name:    "PostgreSQL",
			dialect: postgres.Dialect{},
			countArgs: []any{
				"Subtitle", internalLanguage, false,
				"Subtitle", externalLanguage, true,
				"Subtitle", "und", "2", "0", "2", "8",
			},
			fragments: []string{
				"media_stream.is_external = $3", "media_stream.is_external = $6", "media_stream.language IS NULL OR media_stream.language = ''",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("translating media query: %v", err)
			}
			if got, want := translated.CountArgs, tc.countArgs; !reflect.DeepEqual(got, want) {
				t.Fatalf("count arguments = %#v, want %#v", got, want)
			}
			if got, want := len(translated.CountArgs), 12; got != want {
				t.Fatalf("count argument count = %d, want %d", got, want)
			}
			if got, want := translated.Args, append(append([]any(nil), tc.countArgs...), 1, 2); !reflect.DeepEqual(got, want) {
				t.Fatalf("page arguments = %#v, want count arguments followed only by limit and offset %#v", got, want)
			}

			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				for _, value := range []string{internalLanguage, externalLanguage} {
					if strings.Contains(sql, value) {
						t.Fatalf("translated SQL interpolated language %q: %s", value, sql)
					}
				}
				if mediaOuterItemJoin.MatchString(sql) {
					t.Fatalf("media predicates must retain one outer item row rather than join satellites: %s", sql)
				}
				for _, fragment := range append([]string{
					"media_stream.stream_type", "media_stream.language IS NULL OR media_stream.language = ''", "chapter.image_path IS NOT NULL",
					"media_item.video_type IN", "media_item.iso_type IN", "media_item.video_3d_format IS NOT NULL",
					"media_extra.owner_id = media_item.id", "media_ancestor.ancestor_id = items.id", "media_linked.parent_item_id = items.id",
				}, tc.fragments...) {
					if !strings.Contains(sql, fragment) {
						t.Fatalf("translated SQL lacks media branch %q: %s", fragment, sql)
					}
				}
			}
			if !strings.Contains(strings.ToUpper(translated.CountSQL), "SELECT COUNT(") || strings.Contains(strings.ToUpper(translated.CountSQL), " ORDER BY ") || strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Fatalf("count SQL must count the complete media-filtered set before ordering or paging: %s", translated.CountSQL)
			}
			if tc.name == "SQLite" {
				for _, sql := range []string{translated.SQL, translated.CountSQL} {
					if strings.Contains(sql, "$") || !strings.Contains(sql, "?") {
						t.Fatalf("SQLite SQL must use only positional question-mark placeholders: %s", sql)
					}
				}
			} else {
				for _, sql := range []string{translated.SQL, translated.CountSQL} {
					if strings.Contains(sql, "?") {
						t.Fatalf("PostgreSQL SQL must not contain SQLite placeholders: %s", sql)
					}
				}
				for index := 1; index <= 14; index++ {
					if !strings.Contains(translated.SQL, fmt.Sprintf("$%d", index)) {
						t.Fatalf("PostgreSQL page SQL must use sequential placeholder $%d: %s", index, translated.SQL)
					}
				}
				for index := 1; index <= 12; index++ {
					if !strings.Contains(translated.CountSQL, fmt.Sprintf("$%d", index)) {
						t.Fatalf("PostgreSQL count SQL must use sequential placeholder $%d: %s", index, translated.CountSQL)
					}
				}
			}
		})
	}
}

// TestTranslateItemQuerySpecialFeatureBindsExcludedExtraTypes pins the
// source-defined special-feature exclusion list independently of the trailer
// and theme predicates above, so a new extra type cannot silently become a
// special feature by omitting one of the four bound exclusions.
func TestTranslateItemQuerySpecialFeatureBindsExcludedExtraTypes(t *testing.T) {
	trueValue := true
	translated, err := store.Translate(store.ItemQuery{HasSpecialFeature: &trueValue}, sqlite.Dialect{})
	if err != nil {
		t.Fatalf("translating special-feature query: %v", err)
	}
	if got, want := translated.CountArgs, []any{"0", "2", "8", "9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("special-feature count arguments = %#v, want %#v", got, want)
	}
	for _, fragment := range []string{"media_extra.owner_id = media_item.id", "media_extra.extra_type IS NOT NULL", "media_extra.extra_type <> ?"} {
		if !strings.Contains(translated.CountSQL, fragment) {
			t.Fatalf("special-feature SQL lacks source exclusion branch %q: %s", fragment, translated.CountSQL)
		}
	}
}

func mediaTranslateInt(value int) *int { return &value }
