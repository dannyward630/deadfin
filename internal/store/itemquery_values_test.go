// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestTranslateItemQueryValueRelationsUseBoundCorrelatedPredicates defends the
// public SQL boundary for the pinned value and relation filters. Jellyfin
// translates referenced names and artist variants at
// BaseItemRepository.TranslateQuery.cs:588-659, and its remaining relation
// predicates at :1046-1122. The query must retain exactly one outer item row:
// satellite multiplicity belongs inside correlated predicates, not in page or
// count rows.
func TestTranslateItemQueryValueRelationsUseBoundCorrelatedPredicates(t *testing.T) {
	ids := make([]dto.UUID, 12)
	for index := range ids {
		ids[index] = valuesTranslateUUID(t, index+1)
	}
	hasRating := true
	query := store.ItemQuery{
		GenreIDs:                          []dto.UUID{ids[0]},
		StudioIDs:                         []dto.UUID{ids[1]},
		OfficialRatings:                   []string{"TV-MA' OR 1=1 --"},
		HasOfficialRating:                 &hasRating,
		ArtistIDs:                         []dto.UUID{ids[2]},
		AlbumArtistIDs:                    []dto.UUID{ids[3]},
		ContributingArtistIDs:             []dto.UUID{ids[4]},
		ExcludeArtistIDs:                  []dto.UUID{ids[5]},
		AlbumIDs:                          []dto.UUID{ids[6]},
		TrailerTypes:                      []item.TrailerType{item.TrailerTypeLocalTrailer},
		ImageTypes:                        []item.ImageType{item.ImageTypePrimary},
		ExternalSeriesID:                  "series' OR 1=1 --",
		SeriesStatuses:                    []item.SeriesStatus{item.SeriesStatusEnded},
		LinkedChildAncestorIDs:            []dto.UUID{ids[7]},
		PresentationUniqueKey:             "presentation' OR 1=1 --",
		AncestorWithPresentationUniqueKey: "ancestor' OR 1=1 --",
		SeriesPresentationUniqueKey:       "series-presentation' OR 1=1 --",
		IncludeInheritedTags:              []string{"FÉATURED / Prime"},
		ExcludeInheritedTags:              []string{"BLOCKED / Prime"},
		BoxSetLibraryFolders:              []dto.UUID{ids[8], ids[9]},
		Limit:                             valuesTranslateInt(3),
		StartIndex:                        valuesTranslateInt(2),
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
				t.Fatalf("translating relation-value query: %v", err)
			}
			for _, sql := range []string{translated.SQL, translated.CountSQL} {
				if !strings.Contains(sql, tc.placeholder) || strings.Contains(sql, tc.forbidden) {
					t.Fatalf("%s placeholders in SQL = %q, want %q and no %q", tc.name, sql, tc.placeholder, tc.forbidden)
				}
				for _, literal := range []string{"TV-MA' OR 1=1 --", "series' OR 1=1 --", "presentation' OR 1=1 --", "ancestor' OR 1=1 --", "series-presentation' OR 1=1 --"} {
					if strings.Contains(sql, literal) {
						t.Fatalf("translated SQL interpolated caller value %q: %s", literal, sql)
					}
				}
				if outerItemJoin.MatchString(sql) {
					t.Fatalf("relation predicates must not multiply outer items rows: %s", sql)
				}
				for _, relation := range []string{"item_value_map", "trailer_types", "item_images", "linked_children", "ancestors", "boxset_library_folders"} {
					if !strings.Contains(sql, relation) {
						t.Fatalf("translated SQL omits %s relation predicate: %s", relation, sql)
					}
				}
			}
			if strings.Contains(strings.ToUpper(translated.CountSQL), " ORDER BY ") || strings.Contains(strings.ToUpper(translated.CountSQL), " LIMIT ") || strings.Contains(strings.ToUpper(translated.CountSQL), " OFFSET ") {
				t.Fatalf("count SQL must observe the complete relation-filtered set before paging: %s", translated.CountSQL)
			}
			if !reflect.DeepEqual(translated.Args[:len(translated.CountArgs)], translated.CountArgs) {
				t.Fatalf("page filter args = %#v, want count args %#v as exact prefix", translated.Args[:len(translated.CountArgs)], translated.CountArgs)
			}
			if got, want := translated.Args[len(translated.CountArgs):], []any{3, 2}; !reflect.DeepEqual(got, want) {
				t.Fatalf("page-only args = %#v, want %#v", got, want)
			}
		})
	}
}

// TestTranslateItemQueryValueRelationEmptyListsAreUnconstrained defends list
// semantics from InternalItemsQuery: an absent or empty relation filter is not
// an empty-set query and must not generate invalid IN () SQL.
func TestTranslateItemQueryValueRelationEmptyListsAreUnconstrained(t *testing.T) {
	query := store.ItemQuery{
		GenreIDs:               []dto.UUID{},
		StudioIDs:              []dto.UUID{},
		ArtistIDs:              []dto.UUID{},
		AlbumArtistIDs:         []dto.UUID{},
		ContributingArtistIDs:  []dto.UUID{},
		ExcludeArtistIDs:       []dto.UUID{},
		AlbumIDs:               []dto.UUID{},
		TrailerTypes:           []item.TrailerType{},
		ImageTypes:             []item.ImageType{},
		SeriesStatuses:         []item.SeriesStatus{},
		LinkedChildAncestorIDs: []dto.UUID{},
		IncludeInheritedTags:   []string{},
		ExcludeInheritedTags:   []string{},
		BoxSetLibraryFolders:   []dto.UUID{},
	}
	for _, tc := range []struct {
		name    string
		dialect store.Dialect
	}{
		{name: "SQLite", dialect: sqlite.Dialect{}},
		{name: "PostgreSQL", dialect: postgres.Dialect{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translated, err := store.Translate(query, tc.dialect)
			if err != nil {
				t.Fatalf("translating empty relation lists: %v", err)
			}
			baseline, err := store.Translate(store.ItemQuery{}, tc.dialect)
			if err != nil {
				t.Fatalf("translating baseline relation query: %v", err)
			}
			if !reflect.DeepEqual(translated.CountArgs, baseline.CountArgs) || translated.CountSQL != baseline.CountSQL {
				t.Fatalf("empty relation lists changed filtered count query\n got: %s %#v\nwant: %s %#v", translated.CountSQL, translated.CountArgs, baseline.CountSQL, baseline.CountArgs)
			}
		})
	}
}

func valuesTranslateUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID("00000000-0000-0000-0000-" + valuesTranslateHex(value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func valuesTranslateHex(value int) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 12)
	for index := len(out) - 1; index >= 0; index-- {
		out[index] = digits[value&0xf]
		value >>= 4
	}
	return string(out)
}

func valuesTranslateInt(value int) *int { return &value }
