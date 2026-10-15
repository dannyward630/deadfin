// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestStoreEnricherAggregatesFolderUserDataPostgres exercises the identical
// builder/store contract when DEADFIN_TEST_POSTGRES_DSN enables the opt-in
// PostgreSQL integration suite.
func TestStoreEnricherAggregatesFolderUserDataPostgres(t *testing.T) {
	runStoreEnricherAggregatesFolderUserData(t, func(t *testing.T) *store.ItemStore {
		return store.NewItemStore(storetest.OpenPostgres(t))
	})
}

func runStoreEnricherAggregatesFolderUserData(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	fixture := seedFolderUserDataFixture(t, items)
	page, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: fixture.pageIDs})
	if err != nil {
		t.Fatalf("load aggregate UserData page: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), page.Items, store.ItemQueryDTOOptions{EnableUserData: true}, &fixture.userID)
	if err != nil {
		t.Fatalf("build aggregate UserData page: %v", err)
	}
	byID := make(map[dto.UUID]dto.BaseItemDto, len(result.Items))
	keys := make(map[dto.UUID]string, len(page.Items))
	for _, value := range result.Items {
		byID[value.ID] = value
	}
	for _, value := range page.Items {
		candidates := item.UserDataKeys(value, item.UserDataInputs{})
		if len(candidates) != 0 {
			keys[value.ID] = candidates[0]
		}
	}
	for _, tc := range []struct {
		name       string
		id         dto.UUID
		unplayed   int
		percentage *float64
		played     bool
	}{
		{name: "fresh user recursive folder", id: fixture.freshFolder.ID, unplayed: 2, percentage: folderPercentage(0), played: false},
		{name: "mixed recursive folder", id: fixture.mixedFolder.ID, unplayed: 1, percentage: folderPercentage(50), played: false},
		{name: "completed recursive folder", id: fixture.completedFolder.ID, unplayed: 0, percentage: folderPercentage(100), played: true},
		{name: "empty recursive folder", id: fixture.emptyFolder.ID, unplayed: 0, percentage: nil, played: true},
		{name: "mixed linked box set", id: fixture.boxSet.ID, unplayed: 1, percentage: folderPercentage(50), played: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found := byID[tc.id]
			if !found || got.UserData == nil {
				t.Fatalf("folder %s UserData = %#v, want aggregate UserData", tc.id, got.UserData)
			}
			assertAggregateFolderUserData(t, got.UserData, tc.id, keys[tc.id], tc.unplayed, tc.percentage, tc.played)
		})
	}

	leaf := byID[fixture.mixedUnplayed.ID].UserData
	if leaf == nil {
		t.Fatal("mixed leaf UserData = nil, want direct leaf state")
	}
	if leaf.Played || leaf.UnplayedItemCount != nil || leaf.PlayedPercentage != nil {
		t.Fatalf("leaf UserData = %#v, want unchanged direct state without aggregate-only fields", leaf)
	}
	if leaf.Key != keys[fixture.mixedUnplayed.ID] || leaf.ItemID != fixture.mixedUnplayed.ID {
		t.Fatalf("leaf identity = (%q, %s), want (%q, %s)", leaf.Key, leaf.ItemID, keys[fixture.mixedUnplayed.ID], fixture.mixedUnplayed.ID)
	}
}

type folderUserDataFixture struct {
	userID          dto.UUID
	pageIDs         []dto.UUID
	freshFolder     item.Item
	mixedFolder     item.Item
	completedFolder item.Item
	emptyFolder     item.Item
	boxSet          item.Item
	mixedUnplayed   item.Item
}

func seedFolderUserDataFixture(t *testing.T, items *store.ItemStore) folderUserDataFixture {
	t.Helper()
	userID, foreignUserID := builderUUID(t, 700), builderUUID(t, 701)
	freshFolder := folderUserDataItem(t, 710, item.KindFolder, nil, "fresh folder")
	freshNested := folderUserDataItem(t, 711, item.KindFolder, &freshFolder.ID, "fresh nested folder")
	freshFirst := folderUserDataItem(t, 712, item.KindMovie, &freshNested.ID, "fresh first")
	freshSecond := folderUserDataItem(t, 713, item.KindMovie, &freshNested.ID, "fresh second")

	mixedFolder := folderUserDataItem(t, 720, item.KindFolder, nil, "mixed folder")
	mixedPlayed := folderUserDataItem(t, 721, item.KindMovie, &mixedFolder.ID, "mixed played")
	mixedUnplayed := folderUserDataItem(t, 722, item.KindMovie, &mixedFolder.ID, "mixed unplayed")

	completedFolder := folderUserDataItem(t, 730, item.KindFolder, nil, "completed folder")
	completedLeaf := folderUserDataItem(t, 731, item.KindMovie, &completedFolder.ID, "completed leaf")
	emptyFolder := folderUserDataItem(t, 740, item.KindFolder, nil, "empty folder")

	boxFirst := folderUserDataItem(t, 751, item.KindMovie, nil, "box first")
	boxSecond := folderUserDataItem(t, 752, item.KindMovie, nil, "box second")
	boxSet := folderUserDataItem(t, 750, item.KindBoxSet, nil, "mixed box set")
	boxSet.LinkedChildren = []item.LinkedChildRef{{ItemID: boxFirst.ID, Type: item.LinkedChildTypeManual}, {ItemID: boxSecond.ID, Type: item.LinkedChildTypeManual}}

	for _, value := range []item.Item{
		freshFolder, freshNested, freshFirst, freshSecond,
		mixedFolder, mixedPlayed, mixedUnplayed,
		completedFolder, completedLeaf, emptyFolder,
		boxFirst, boxSecond, boxSet,
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert aggregate UserData fixture %s: %v", value.ID, err)
		}
	}
	for _, data := range []store.UserData{
		{UserID: foreignUserID, CustomKey: freshFirst.ID.String(), ItemID: freshFirst.ID, Played: true},
		{UserID: userID, CustomKey: mixedPlayed.ID.String(), ItemID: mixedPlayed.ID, Played: true},
		{UserID: userID, CustomKey: completedLeaf.ID.String(), ItemID: completedLeaf.ID, Played: true},
		{UserID: userID, CustomKey: boxFirst.ID.String(), ItemID: boxFirst.ID, Played: true},
	} {
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert aggregate UserData %q: %v", data.CustomKey, err)
		}
	}
	return folderUserDataFixture{
		userID:      userID,
		pageIDs:     []dto.UUID{freshFolder.ID, mixedFolder.ID, completedFolder.ID, emptyFolder.ID, boxSet.ID, mixedUnplayed.ID},
		freshFolder: freshFolder, mixedFolder: mixedFolder, completedFolder: completedFolder, emptyFolder: emptyFolder, boxSet: boxSet, mixedUnplayed: mixedUnplayed,
	}
}

func folderUserDataItem(t *testing.T, value int, kind item.Kind, parentID *dto.UUID, name string) item.Item {
	t.Helper()
	return item.Item{ID: builderUUID(t, value), Kind: kind, ParentID: parentID, Name: &name, IsFolder: item.IsFolder(item.Item{Kind: kind})}
}

func folderPercentage(value float64) *float64 { return &value }

func assertAggregateFolderUserData(t *testing.T, got *dto.UserItemDataDto, id dto.UUID, key string, unplayed int, percentage *float64, played bool) {
	t.Helper()
	if got.ItemID != id || got.Key != key || got.Played != played {
		t.Fatalf("aggregate identity/state = %#v, want item=%s key=%q played=%t", got, id, key, played)
	}
	if got.UnplayedItemCount == nil || *got.UnplayedItemCount != unplayed {
		t.Fatalf("aggregate UnplayedItemCount = %v, want %d", got.UnplayedItemCount, unplayed)
	}
	if percentage == nil {
		if got.PlayedPercentage != nil {
			t.Fatalf("aggregate PlayedPercentage = %v, want omission for an empty folder", *got.PlayedPercentage)
		}
		return
	}
	if got.PlayedPercentage == nil || *got.PlayedPercentage != *percentage {
		t.Fatalf("aggregate PlayedPercentage = %v, want %v", got.PlayedPercentage, percentage)
	}
}

// TestStoreEnricherSkipsLibraryViewAggregate follows Folder.
// SupportsUserDataFromChildren: Jellyfin computes no played counts for a
// CollectionFolder or UserView, so a UserViews page publishes direct UserData
// only, while a plain folder under the library keeps its aggregate.
func TestStoreEnricherSkipsLibraryViewAggregate(t *testing.T) {
	items := openBuilderItemStore(t)
	userID := builderUUID(t, 760)
	library := folderUserDataItem(t, 761, item.KindCollectionFolder, nil, "library")
	folder := folderUserDataItem(t, 762, item.KindFolder, &library.ID, "folder")
	leaf := folderUserDataItem(t, 763, item.KindMovie, &folder.ID, "played leaf")
	for _, value := range []item.Item{library, folder, leaf} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert library view fixture %s: %v", value.ID, err)
		}
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: leaf.ID.String(), ItemID: leaf.ID, Played: true}); err != nil {
		t.Fatalf("upsert played leaf: %v", err)
	}
	page, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{library.ID, folder.ID}})
	if err != nil {
		t.Fatalf("load library view page: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), page.Items, store.ItemQueryDTOOptions{EnableUserData: true}, &userID)
	if err != nil {
		t.Fatalf("build library view page: %v", err)
	}
	byID := make(map[dto.UUID]*dto.UserItemDataDto, len(result.Items))
	for _, value := range result.Items {
		byID[value.ID] = value.UserData
	}
	view := byID[library.ID]
	if view == nil || view.Played || view.UnplayedItemCount != nil || view.PlayedPercentage != nil {
		t.Fatalf("library view UserData = %#v, want direct state without aggregate fields", view)
	}
	assertAggregateFolderUserData(t, byID[folder.ID], folder.ID, item.UserDataKeys(folder, item.UserDataInputs{})[0], 0, folderPercentage(100), true)
}
