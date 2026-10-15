// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryGroupingAndAccessParitySQLite exercises the public store
// seam. Its PostgreSQL twin uses the same fixture when DEADFIN_TEST_POSTGRES_DSN
// is available, keeping D-03 and transformed-count behavior dialect-neutral.
func TestItemStoreQueryGroupingAndAccessParitySQLite(t *testing.T) {
	runItemStoreQueryGroupingAndAccessParity(t, openItemStore)
}

func TestItemStoreQueryGroupingAndAccessParityPostgres(t *testing.T) {
	runItemStoreQueryGroupingAndAccessParity(t, openPostgresItemStore)
}

func runItemStoreQueryGroupingAndAccessParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	t.Run("presentation and series grouping retain source-selected ids", func(t *testing.T) {
		fixture := newGroupingAccessGroupingFixture(t, open(t))
		user := &store.User{ID: fixture.userID}

		fixture.assertQuery(t, store.ItemQuery{
			User:                         user,
			ItemIDs:                      []dto.UUID{fixture.primary.ID, fixture.alternate.ID, fixture.minimum.ID, fixture.higher.ID},
			IncludeItemTypes:             []item.Kind{item.KindMovie},
			GroupByPresentationUniqueKey: true,
		}, []dto.UUID{fixture.primary.ID, fixture.minimum.ID}, 2)

		limit, start := 1, 1
		fixture.assertQuery(t, store.ItemQuery{
			User:                         user,
			ItemIDs:                      []dto.UUID{fixture.primary.ID, fixture.alternate.ID, fixture.minimum.ID, fixture.higher.ID},
			IncludeItemTypes:             []item.Kind{item.KindMovie},
			GroupByPresentationUniqueKey: true,
			Limit:                        &limit,
			StartIndex:                   &start,
		}, []dto.UUID{fixture.minimum.ID}, 2)

		fixture.assertQuery(t, store.ItemQuery{
			User:                               user,
			ItemIDs:                            []dto.UUID{fixture.seriesLow.ID, fixture.seriesHigh.ID},
			IncludeItemTypes:                   []item.Kind{item.KindMovie},
			GroupBySeriesPresentationUniqueKey: true,
		}, []dto.UUID{fixture.seriesLow.ID}, 1)

		// QueryBuilding.cs:72-85 uses the combined (presentation, series) key
		// only when presentation grouping is enabled; the source gate disables
		// that branch when GroupBySeriesPresentationUniqueKey is set, then groups
		// solely by series key and chooses the minimum ID.
		fixture.assertQuery(t, store.ItemQuery{
			User:                               user,
			ItemIDs:                            []dto.UUID{fixture.seriesLow.ID, fixture.seriesHigh.ID},
			IncludeItemTypes:                   []item.Kind{item.KindMovie},
			GroupByPresentationUniqueKey:       true,
			GroupBySeriesPresentationUniqueKey: true,
		}, []dto.UUID{fixture.seriesLow.ID}, 1)
	})

	t.Run("box set collapse uses manual links and applies name range after replacement", func(t *testing.T) {
		fixture := newGroupingAccessCollapseFixture(t, open(t))

		// QueryBuilding.cs:167-193 replaces a matched manual child with its
		// BoxSet parent even when the BoxSet itself was not in the initial set.
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:             []dto.UUID{fixture.manualChild.ID},
			CollapseBoxSetItems: groupingAccessBool(true),
		}, []dto.UUID{fixture.boxSet.ID}, 1)
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:             []dto.UUID{fixture.nonManualChild.ID},
			CollapseBoxSetItems: groupingAccessBool(true),
		}, []dto.UUID{fixture.nonManualChild.ID}, 1)
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:                 []dto.UUID{fixture.manualChild.ID},
			CollapseBoxSetItems:     groupingAccessBool(true),
			CollapseBoxSetItemTypes: []item.Kind{item.KindEpisode},
		}, []dto.UUID{fixture.manualChild.ID}, 1)

		// The child matches "match", but its replacement BoxSet does not. The
		// name range must therefore be evaluated against the collapsed relation.
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:             []dto.UUID{fixture.manualChild.ID},
			NameStartsWith:      "match",
			CollapseBoxSetItems: groupingAccessBool(true),
		}, []dto.UUID{}, 0)
		fixture.assertQuery(t, store.ItemQuery{
			ItemIDs:        []dto.UUID{fixture.manualChild.ID},
			NameStartsWith: "match",
		}, []dto.UUID{fixture.manualChild.ID}, 1)
	})

	t.Run("collapse keeps a visible child when its manual BoxSet parent is inaccessible", func(t *testing.T) {
		items := open(t)
		userID, visibleRootID, hiddenRootID := groupingAccessUUID(t, 110), groupingAccessUUID(t, 111), groupingAccessUUID(t, 112)
		visibleRoot := groupingAccessItem(t, 113, item.KindFolder, "visible library")
		visibleRoot.ID, visibleRoot.IsFolder = visibleRootID, true
		hiddenRoot := groupingAccessItem(t, 114, item.KindFolder, "hidden library")
		hiddenRoot.ID, hiddenRoot.IsFolder = hiddenRootID, true
		child := groupingAccessItem(t, 115, item.KindMovie, "visible child")
		child.ParentID = &visibleRootID
		boxSet := groupingAccessItem(t, 116, item.KindBoxSet, "hidden collection")
		boxSet.ParentID, boxSet.IsFolder = &hiddenRootID, true
		boxSet.LinkedChildren = []item.LinkedChildRef{{ItemID: child.ID, Type: item.LinkedChildTypeManual}}
		for _, value := range []item.Item{visibleRoot, hiddenRoot, child, boxSet} {
			groupingAccessUpsert(t, items, value)
		}

		collapse := true
		restricted := &store.User{ID: userID, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + visibleRootID.String() + `"]}`)}
		query := store.ItemQuery{User: restricted, ItemIDs: []dto.UUID{child.ID}, CollapseBoxSetItems: &collapse}
		groupingAccessAssertQuery(t, items, query, []dto.UUID{child.ID}, 1)

		administrator := &store.User{ID: userID, Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":[]}`)}
		query.User = administrator
		groupingAccessAssertQuery(t, items, query, []dto.UUID{boxSet.ID}, 1)
		query.User = nil
		groupingAccessAssertQuery(t, items, query, []dto.UUID{boxSet.ID}, 1)
	})

	t.Run("adjacency is exact target plus immediate sort-name neighbors", func(t *testing.T) {
		fixture := newGroupingAccessAdjacentFixture(t, open(t))
		fixture.assertQuery(t, store.ItemQuery{AdjacentTo: &fixture.bravo.ID}, []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID}, 3)
		fixture.assertQuery(t, store.ItemQuery{AdjacentTo: &fixture.alpha.ID}, []dto.UUID{fixture.alpha.ID, fixture.bravo.ID}, 2)
		fixture.assertQuery(t, store.ItemQuery{AdjacentTo: &fixture.delta.ID}, []dto.UUID{fixture.charlie.ID, fixture.delta.ID}, 2)
		missing := groupingAccessUUID(t, 499)
		fixture.assertQuery(t, store.ItemQuery{AdjacentTo: &missing}, []dto.UUID{fixture.alpha.ID, fixture.bravo.ID, fixture.charlie.ID, fixture.delta.ID}, 4)
	})

	t.Run("playlist and collection access use normalized D-03 facts", func(t *testing.T) {
		fixture := newGroupingAccessVisibilityFixture(t, open(t))
		playlistQuery := func(user dto.UUID) store.ItemQuery {
			return store.ItemQuery{User: &store.User{ID: user}, IncludeItemTypes: []item.Kind{item.KindPlaylist}}
		}
		fixture.assertQuery(t, playlistQuery(fixture.owner), []dto.UUID{fixture.ownerPlaylist.ID, fixture.openPlaylist.ID}, 2)
		fixture.assertQuery(t, playlistQuery(fixture.viewer), []dto.UUID{fixture.sharedPlaylist.ID, fixture.openPlaylist.ID}, 2)
		fixture.assertQuery(t, playlistQuery(fixture.stranger), []dto.UUID{fixture.openPlaylist.ID}, 1)

		// boxset_library_folders is the normalized collection visibility relation;
		// a folder scope cannot leak a BoxSet linked to a different library.
		fixture.assertQuery(t, store.ItemQuery{
			IncludeItemTypes:     []item.Kind{item.KindBoxSet},
			BoxSetLibraryFolders: []dto.UUID{fixture.allowedFolder},
		}, []dto.UUID{fixture.allowedCollection.ID}, 1)
	})
}

type groupingAccessGroupingFixture struct {
	items                               *store.ItemStore
	userID                              dto.UUID
	primary, alternate, minimum, higher item.Item
	seriesLow, seriesHigh               item.Item
}

func newGroupingAccessGroupingFixture(t *testing.T, items *store.ItemStore) groupingAccessGroupingFixture {
	t.Helper()
	primary := groupingAccessItem(t, 10, item.KindMovie, "alpha")
	alternate := groupingAccessItem(t, 11, item.KindMovie, "alternate")
	minimum := groupingAccessItem(t, 20, item.KindMovie, "bravo")
	higher := groupingAccessItem(t, 21, item.KindMovie, "higher")
	seriesLow := groupingAccessItem(t, 30, item.KindMovie, "charlie")
	seriesHigh := groupingAccessItem(t, 31, item.KindMovie, "delta")
	primary.PresentationUniqueKey, alternate.PresentationUniqueKey = groupingAccessString("presentation-a"), groupingAccessString("presentation-a")
	alternate.PrimaryVersionID = &primary.ID
	minimum.PresentationUniqueKey, higher.PresentationUniqueKey = groupingAccessString("presentation-b"), groupingAccessString("presentation-b")
	seriesLow.PresentationUniqueKey, seriesHigh.PresentationUniqueKey = groupingAccessString("different-presentation-a"), groupingAccessString("different-presentation-b")
	seriesLow.SeriesPresentationUniqueKey, seriesHigh.SeriesPresentationUniqueKey = groupingAccessString("series-a"), groupingAccessString("series-a")
	for _, value := range []item.Item{primary, alternate, minimum, higher, seriesLow, seriesHigh} {
		groupingAccessUpsert(t, items, value)
	}
	return groupingAccessGroupingFixture{items: items, userID: groupingAccessUUID(t, 1), primary: primary, alternate: alternate, minimum: minimum, higher: higher, seriesLow: seriesLow, seriesHigh: seriesHigh}
}

type groupingAccessCollapseFixture struct {
	items                       *store.ItemStore
	boxSet                      item.Item
	manualChild, nonManualChild item.Item
}

func newGroupingAccessCollapseFixture(t *testing.T, items *store.ItemStore) groupingAccessCollapseFixture {
	t.Helper()
	manualChild := groupingAccessItem(t, 100, item.KindMovie, "match child")
	nonManualChild := groupingAccessItem(t, 101, item.KindMovie, "other child")
	boxSet := groupingAccessItem(t, 102, item.KindBoxSet, "collection name")
	boxSet.LinkedChildren = []item.LinkedChildRef{
		{ItemID: manualChild.ID, Type: item.LinkedChildTypeManual},
		{ItemID: nonManualChild.ID, Type: item.LinkedChildTypeLinkedAlternateVersion},
	}
	groupingAccessUpsert(t, items, manualChild)
	groupingAccessUpsert(t, items, nonManualChild)
	groupingAccessUpsert(t, items, boxSet)
	return groupingAccessCollapseFixture{items: items, boxSet: boxSet, manualChild: manualChild, nonManualChild: nonManualChild}
}

type groupingAccessAdjacentFixture struct {
	items                        *store.ItemStore
	alpha, bravo, charlie, delta item.Item
}

func newGroupingAccessAdjacentFixture(t *testing.T, items *store.ItemStore) groupingAccessAdjacentFixture {
	t.Helper()
	alpha := groupingAccessItem(t, 200, item.KindMovie, "alpha")
	bravo := groupingAccessItem(t, 201, item.KindMovie, "bravo")
	charlie := groupingAccessItem(t, 202, item.KindMovie, "charlie")
	delta := groupingAccessItem(t, 203, item.KindMovie, "delta")
	for _, value := range []item.Item{alpha, bravo, charlie, delta} {
		groupingAccessUpsert(t, items, value)
	}
	return groupingAccessAdjacentFixture{items: items, alpha: alpha, bravo: bravo, charlie: charlie, delta: delta}
}

type groupingAccessVisibilityFixture struct {
	items                                       *store.ItemStore
	owner, viewer, stranger                     dto.UUID
	ownerPlaylist, sharedPlaylist, openPlaylist item.Item
	allowedCollection                           item.Item
	allowedFolder                               dto.UUID
}

func newGroupingAccessVisibilityFixture(t *testing.T, items *store.ItemStore) groupingAccessVisibilityFixture {
	t.Helper()
	owner, viewer, stranger, creator := groupingAccessUUID(t, 300), groupingAccessUUID(t, 301), groupingAccessUUID(t, 302), groupingAccessUUID(t, 303)
	ownerPlaylist := groupingAccessItem(t, 310, item.KindPlaylist, "alpha owner")
	ownerPlaylist.PlaylistOwnerUserID = owner
	sharedPlaylist := groupingAccessItem(t, 311, item.KindPlaylist, "bravo shared")
	sharedPlaylist.PlaylistOwnerUserID = creator
	openPlaylist := groupingAccessItem(t, 312, item.KindPlaylist, "charlie open")
	openPlaylist.PlaylistOwnerUserID, openPlaylist.OpenAccess = creator, true
	deniedPlaylist := groupingAccessItem(t, 313, item.KindPlaylist, "delta denied")
	deniedPlaylist.PlaylistOwnerUserID = creator
	genericOwnerOnly := groupingAccessItem(t, 314, item.KindPlaylist, "echo generic-owner")
	genericOwnerOnly.PlaylistOwnerUserID, genericOwnerOnly.OwnerID = creator, &viewer
	allowedCollection := groupingAccessItem(t, 320, item.KindBoxSet, "allowed collection")
	deniedCollection := groupingAccessItem(t, 321, item.KindBoxSet, "denied collection")
	allowedFolder, deniedFolder := groupingAccessUUID(t, 330), groupingAccessUUID(t, 331)
	for _, value := range []item.Item{ownerPlaylist, sharedPlaylist, openPlaylist, deniedPlaylist, genericOwnerOnly, allowedCollection, deniedCollection} {
		groupingAccessUpsert(t, items, value)
	}
	if err := items.ReplaceShares(t.Context(), sharedPlaylist.ID, []store.ItemShare{{UserID: viewer}}); err != nil {
		t.Fatalf("replacing shared playlist access: %v", err)
	}
	if err := items.ReplaceBoxSetLibraryFolders(t.Context(), allowedCollection.ID, []dto.UUID{allowedFolder}); err != nil {
		t.Fatalf("setting allowed collection library folder: %v", err)
	}
	if err := items.ReplaceBoxSetLibraryFolders(t.Context(), deniedCollection.ID, []dto.UUID{deniedFolder}); err != nil {
		t.Fatalf("setting denied collection library folder: %v", err)
	}
	return groupingAccessVisibilityFixture{items: items, owner: owner, viewer: viewer, stranger: stranger, ownerPlaylist: ownerPlaylist, sharedPlaylist: sharedPlaylist, openPlaylist: openPlaylist, allowedCollection: allowedCollection, allowedFolder: allowedFolder}
}

func (f groupingAccessGroupingFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	groupingAccessAssertQuery(t, f.items, query, want, total)
}

func (f groupingAccessCollapseFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	groupingAccessAssertQuery(t, f.items, query, want, total)
}

func (f groupingAccessAdjacentFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	groupingAccessAssertQuery(t, f.items, query, want, total)
}

func (f groupingAccessVisibilityFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	groupingAccessAssertQuery(t, f.items, query, want, total)
}

func groupingAccessAssertQuery(t *testing.T, items *store.ItemStore, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	query.EnableTotalRecordCount = true
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	got, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("Query(%#v): %v", query, err)
	}
	ids := make([]dto.UUID, len(got.Items))
	for index, value := range got.Items {
		ids[index] = value.ID
	}
	if !reflect.DeepEqual(ids, want) || got.TotalRecordCount != total {
		t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, total)
	}
}

func groupingAccessUpsert(t *testing.T, items *store.ItemStore, value item.Item) {
	t.Helper()
	if err := items.Upsert(t.Context(), value); err != nil {
		t.Fatalf("upserting %s: %v", value.ID, err)
	}
}

func groupingAccessItem(t *testing.T, value int, kind item.Kind, name string) item.Item {
	t.Helper()
	return item.Item{ID: groupingAccessUUID(t, value), Kind: kind, Name: groupingAccessString(name), SortName: groupingAccessString(name)}
}

func groupingAccessUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing grouping/access UUID %d: %v", value, err)
	}
	return id
}

func groupingAccessBool(value bool) *bool       { return &value }
func groupingAccessString(value string) *string { return &value }
