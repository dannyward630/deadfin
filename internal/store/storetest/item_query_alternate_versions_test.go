// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryAlternateVersionsSQLite protects source-compatible normal
// library visibility without removing an explicitly requested playback source.
func TestItemStoreQueryAlternateVersionsSQLite(t *testing.T) {
	runItemStoreQueryAlternateVersions(t, openItemStore)
}

// TestItemStoreQueryAlternateVersionsPostgres exercises the same public
// ItemStore contract against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is set.
func TestItemStoreQueryAlternateVersionsPostgres(t *testing.T) {
	runItemStoreQueryAlternateVersions(t, openPostgresItemStore)
}

func runItemStoreQueryAlternateVersions(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newAlternateVersionQueryFixture(t, open(t))

	t.Run("ordinary recursive movie query removes alternate before count and paging", func(t *testing.T) {
		zero, one := 0, 1
		cases := []struct {
			name  string
			query store.ItemQuery
			want  []dto.UUID
		}{
			{
				name:  "unpaged library result has only primary",
				query: fixture.libraryMovieQuery(nil, nil),
				want:  []dto.UUID{fixture.primary.ID},
			},
			{
				name:  "first one-item page has primary even though alternate sorts first",
				query: fixture.libraryMovieQuery(&zero, &one),
				want:  []dto.UUID{fixture.primary.ID},
			},
			{
				name:  "page after primary is empty with unchanged total",
				query: fixture.libraryMovieQuery(&one, &one),
				want:  []dto.UUID{},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fixture.assertQuery(t, tc.query, tc.want, 1)
			})
		}
	})

	t.Run("explicit alternate ID remains hidden from ordinary item queries", func(t *testing.T) {
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:                []dto.UUID{fixture.alternate.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
			SortOrder:              []store.SortOrder{store.SortOrderAscending},
			EnableTotalRecordCount: true,
		}, []dto.UUID{}, 0)
	})

	t.Run("canonical playback graph retains both source records", func(t *testing.T) {
		records, err := fixture.items.MediaSourceRecords(t.Context(), fixture.primary.ID)
		if err != nil {
			t.Fatalf("loading canonical source graph: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("alternate source graph has %d records, want primary and 720p alternate", len(records))
		}
		byID := make(map[dto.UUID]playback.MediaSourceRecord, len(records))
		for _, record := range records {
			byID[record.ID] = record
		}
		if _, ok := byID[fixture.primary.ID]; !ok {
			t.Fatalf("alternate source graph IDs = %v, missing canonical primary %s", alternateVersionRecordIDs(records), fixture.primary.ID)
		}
		alternate, ok := byID[fixture.alternate.ID]
		if !ok {
			t.Fatalf("alternate source graph IDs = %v, missing 720p alternate %s", alternateVersionRecordIDs(records), fixture.alternate.ID)
		}
		if len(alternate.MediaStreams) != 1 || alternate.MediaStreams[0].Width == nil || alternate.MediaStreams[0].Height == nil || *alternate.MediaStreams[0].Width != 1280 || *alternate.MediaStreams[0].Height != 720 {
			t.Fatalf("720p alternate stream = %#v, want one 1280x720 video stream", alternate.MediaStreams)
		}
	})
}

type alternateVersionQueryFixture struct {
	items                    *store.ItemStore
	root, primary, alternate item.Item
}

func newAlternateVersionQueryFixture(t *testing.T, items *store.ItemStore) alternateVersionQueryFixture {
	t.Helper()
	root := alternateVersionQueryItem(t, 1, item.KindFolder, "movie library")
	root.IsFolder = true
	primary := alternateVersionQueryItem(t, 2, item.KindMovie, "bravo canonical")
	primary.ParentID = &root.ID
	alternate := alternateVersionQueryItem(t, 3, item.KindMovie, "alpha 720p alternate")
	alternate.ParentID = &root.ID
	primary.LinkedChildren = []item.LinkedChildRef{{ItemID: alternate.ID, Type: item.LinkedChildTypeLinkedAlternateVersion}}
	for _, value := range []item.Item{root, alternate, primary} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting alternate-version fixture item %s: %v", value.ID, err)
		}
	}
	primaryWidth, primaryHeight := 1920, 1080
	alternateWidth, alternateHeight := 1280, 720
	if err := items.ReplaceStreams(t.Context(), primary.ID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Width: &primaryWidth, Height: &primaryHeight}}); err != nil {
		t.Fatalf("replacing canonical source stream: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), alternate.ID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Width: &alternateWidth, Height: &alternateHeight}}); err != nil {
		t.Fatalf("replacing 720p alternate source stream: %v", err)
	}
	return alternateVersionQueryFixture{items: items, root: root, primary: primary, alternate: alternate}
}

func (f alternateVersionQueryFixture) libraryMovieQuery(start, limit *int) store.ItemQuery {
	return store.ItemQuery{
		Recursive:              true,
		ParentID:               &f.root.ID,
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		StartIndex:             start,
		Limit:                  limit,
		EnableTotalRecordCount: true,
	}
}

func (f alternateVersionQueryFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying alternate-version fixture: %v", err)
	}
	if ids := alternateVersionQueryIDs(got.Items); !reflect.DeepEqual(ids, want) || got.TotalRecordCount != total {
		t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, total)
	}
}

func alternateVersionQueryItem(t *testing.T, value int, kind item.Kind, name string) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing alternate-version fixture UUID %d: %v", value, err)
	}
	return item.Item{ID: id, Kind: kind, Name: &name, SortName: &name}
}

func alternateVersionQueryIDs(items []item.Item) []dto.UUID {
	result := make([]dto.UUID, len(items))
	for index, value := range items {
		result[index] = value.ID
	}
	return result
}

func alternateVersionRecordIDs(records []playback.MediaSourceRecord) []dto.UUID {
	result := make([]dto.UUID, len(records))
	for index, value := range records {
		result[index] = value.ID
	}
	return result
}
