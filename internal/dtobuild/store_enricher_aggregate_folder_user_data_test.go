// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestStoreEnricherProjectsAggregateFolderUserData pins Folder.FillUserDataDtoValues
// (upstream/jellyfin/MediaBrowser.Controller/Entities/Folder.cs:1930-1987):
// aggregates use visible recursive (or BoxSet-linked) leaves, not their own
// user-data row. Zero is a populated count/percentage where upstream sets it;
// an empty folder alone omits PlayedPercentage.
func TestStoreEnricherProjectsAggregateFolderUserData(t *testing.T) {
	items := openBuilderItemStore(t)
	primaryUser, secondUser, restrictedUser := builderUUID(t, 700), builderUUID(t, 701), builderUUID(t, 702)
	rootA, rootB := aggregateFolderItem(t, 710, item.KindFolder, "visible library", nil), aggregateFolderItem(t, 711, item.KindFolder, "hidden library", nil)
	rootA.IsFolder, rootB.IsFolder = true, true

	allUnplayed := aggregateFolderItem(t, 720, item.KindBoxSet, "all unplayed", &rootA.ID)
	allUnplayed.IsFolder = true
	allUnplayedLeaves := []item.Item{
		aggregateFolderItem(t, 721, item.KindMovie, "all unplayed one", nil),
		aggregateFolderItem(t, 722, item.KindMovie, "all unplayed two", nil),
		aggregateFolderItem(t, 723, item.KindMovie, "all unplayed three", nil),
	}
	for _, leaf := range allUnplayedLeaves {
		allUnplayed.LinkedChildren = append(allUnplayed.LinkedChildren, item.LinkedChildRef{ItemID: leaf.ID, Type: item.LinkedChildTypeManual})
	}

	mixed := aggregateFolderItem(t, 730, item.KindFolder, "mixed", &rootA.ID)
	mixed.IsFolder = true
	mixedNested := aggregateFolderItem(t, 731, item.KindFolder, "mixed nested", &mixed.ID)
	mixedNested.IsFolder = true
	mixedPlayed := aggregateFolderItem(t, 732, item.KindMovie, "mixed played", &mixedNested.ID)
	mixedUnplayed := aggregateFolderItem(t, 733, item.KindMovie, "mixed unplayed", &mixedNested.ID)

	fullyPlayed := aggregateFolderItem(t, 740, item.KindFolder, "fully played", &rootA.ID)
	fullyPlayed.IsFolder = true
	fullyPlayedFirst := aggregateFolderItem(t, 741, item.KindMovie, "fully played one", &fullyPlayed.ID)
	fullyPlayedSecond := aggregateFolderItem(t, 742, item.KindMovie, "fully played two", &fullyPlayed.ID)

	resumeOnly := aggregateFolderItem(t, 750, item.KindFolder, "resume only", &rootA.ID)
	resumeOnly.IsFolder = true
	resumeLeaf := aggregateFolderItem(t, 751, item.KindMovie, "resume leaf", &resumeOnly.ID)

	empty := aggregateFolderItem(t, 760, item.KindFolder, "empty", &rootA.ID)
	empty.IsFolder = true

	visibilityBoxSet := aggregateFolderItem(t, 770, item.KindBoxSet, "visible links only", &rootA.ID)
	visibilityBoxSet.IsFolder = true
	visibleLinked := aggregateFolderItem(t, 771, item.KindMovie, "visible linked", &rootA.ID)
	hiddenLinked := aggregateFolderItem(t, 772, item.KindMovie, "hidden linked", &rootB.ID)
	visibilityBoxSet.LinkedChildren = []item.LinkedChildRef{{ItemID: visibleLinked.ID, Type: item.LinkedChildTypeManual}, {ItemID: hiddenLinked.ID, Type: item.LinkedChildTypeManual}}

	for _, value := range append([]item.Item{
		rootA, rootB, allUnplayed, mixed, mixedNested, mixedPlayed, mixedUnplayed,
		fullyPlayed, fullyPlayedFirst, fullyPlayedSecond, resumeOnly, resumeLeaf,
		empty, visibilityBoxSet, visibleLinked, hiddenLinked,
	}, allUnplayedLeaves...) {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert aggregate fixture %q: %v", *value.Name, err)
		}
	}
	aggregateUpsertUserData(t, items, primaryUser, mixedPlayed, true, 0)
	aggregateUpsertUserData(t, items, primaryUser, fullyPlayedFirst, true, 0)
	aggregateUpsertUserData(t, items, primaryUser, fullyPlayedSecond, true, 0)
	aggregateUpsertUserData(t, items, primaryUser, resumeLeaf, false, 99)
	for _, leaf := range allUnplayedLeaves {
		aggregateUpsertUserData(t, items, secondUser, leaf, true, 0)
	}

	users := aggregateUserReader{users: map[dto.UUID]store.User{
		primaryUser:    {ID: primaryUser, Policy: []byte(`{"EnableAllFolders":true}`)},
		secondUser:     {ID: secondUser, Policy: []byte(`{"EnableAllFolders":true}`)},
		restrictedUser: {ID: restrictedUser, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + rootA.ID.String() + `"]}`)},
	}}
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, users, nil))
	page := []item.Item{allUnplayed, mixed, fullyPlayed, resumeOnly, empty, visibilityBoxSet, allUnplayedLeaves[0]}

	primary := aggregateBuildPage(t, builder, page, primaryUser)
	aggregateAssertUserData(t, primary[allUnplayed.ID], false, 3, float64Pointer(0))
	aggregateAssertUserData(t, primary[mixed.ID], false, 1, float64Pointer(50))
	aggregateAssertUserData(t, primary[fullyPlayed.ID], true, 0, float64Pointer(100))
	aggregateAssertUserData(t, primary[resumeOnly.ID], false, 1, float64Pointer(0))
	aggregateAssertUserData(t, primary[empty.ID], true, 0, nil)
	// The unrestricted primary user sees both BoxSet links.
	aggregateAssertUserData(t, primary[visibilityBoxSet.ID], false, 2, float64Pointer(0))
	aggregateAssertLeafOmitsAggregateFields(t, primary[allUnplayedLeaves[0].ID])

	second := aggregateBuildPage(t, builder, []item.Item{allUnplayed}, secondUser)
	aggregateAssertUserData(t, second[allUnplayed.ID], true, 0, float64Pointer(100))

	restricted := aggregateBuildPage(t, builder, []item.Item{visibilityBoxSet}, restrictedUser)
	// BoxSet.FilterLinkedChildrenPerUser is true upstream: inaccessible links are
	// excluded before its played/total aggregation.
	aggregateAssertUserData(t, restricted[visibilityBoxSet.ID], false, 1, float64Pointer(0))
}

type aggregateUserReader struct{ users map[dto.UUID]store.User }

func (r aggregateUserReader) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return r.users[id], nil
}

func aggregateFolderItem(t *testing.T, value int, kind item.Kind, name string, parent *dto.UUID) item.Item {
	t.Helper()
	return item.Item{ID: builderUUID(t, value), Kind: kind, Name: builderString(name), ParentID: parent}
}

func aggregateUpsertUserData(t *testing.T, items *store.ItemStore, userID dto.UUID, value item.Item, played bool, position int64) {
	t.Helper()
	keys := item.UserDataKeys(value, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatalf("no user-data key for %s", value.ID)
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: keys[0], ItemID: value.ID, Played: played, PositionTicks: position}); err != nil {
		t.Fatalf("upsert user data for %q: %v", *value.Name, err)
	}
}

func aggregateBuildPage(t *testing.T, builder *dtobuild.Builder, values []item.Item, userID dto.UUID) map[dto.UUID]map[string]json.RawMessage {
	t.Helper()
	result, err := builder.Build(t.Context(), values, store.ItemQueryDTOOptions{EnableUserData: true}, &userID)
	if err != nil {
		t.Fatalf("build aggregate DTO page: %v", err)
	}
	got := make(map[dto.UUID]map[string]json.RawMessage, len(result.Items))
	for _, value := range result.Items {
		wire := builderWireObject(t, value)
		got[value.ID] = aggregateUserDataWire(t, wire)
	}
	return got
}

func aggregateUserDataWire(t *testing.T, itemWire map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var data map[string]json.RawMessage
	if err := json.Unmarshal(itemWire["UserData"], &data); err != nil {
		t.Fatalf("decode UserData %s: %v", itemWire["UserData"], err)
	}
	return data
}

func aggregateAssertUserData(t *testing.T, data map[string]json.RawMessage, played bool, unplayed int, percentage *float64) {
	t.Helper()
	if got, want := string(data["Played"]), aggregateJSON(played); got != want {
		t.Errorf("Played = %s, want %s", got, want)
	}
	if got, want := string(data["UnplayedItemCount"]), aggregateJSON(unplayed); got != want {
		t.Errorf("UnplayedItemCount = %s, want %s", got, want)
	}
	if percentage == nil {
		if _, found := data["PlayedPercentage"]; found {
			t.Errorf("PlayedPercentage = %s, want omitted", data["PlayedPercentage"])
		}
		return
	}
	if got, want := string(data["PlayedPercentage"]), aggregateJSON(*percentage); got != want {
		t.Errorf("PlayedPercentage = %s, want %s", got, want)
	}
}

func aggregateAssertLeafOmitsAggregateFields(t *testing.T, data map[string]json.RawMessage) {
	t.Helper()
	for _, field := range []string{"PlayedPercentage", "UnplayedItemCount"} {
		if value, found := data[field]; found {
			t.Errorf("leaf UserData unexpectedly emitted %s=%s", field, value)
		}
	}
}

func aggregateJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func float64Pointer(value float64) *float64 { return &value }

// TestStoreEnricherProjectsLeafPlayedPercentage pins BaseItem.FillUserDataDtoValues
// (upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:2644-2666):
// a leaf with a runtime publishes PlayedPercentage from its playback position,
// while a leaf without a runtime or position omits the field. A folder never
// uses the leaf path: its percentage comes from child aggregation only.
func TestStoreEnricherProjectsLeafPlayedPercentage(t *testing.T) {
	items := openBuilderItemStore(t)
	userID := builderUUID(t, 700)
	runtime := int64(10_000_000)

	leafWithPosition := aggregateFolderItem(t, 710, item.KindMovie, "in-progress movie", nil)
	leafWithPosition.RunTimeTicks = &runtime
	leafWithoutRuntime := aggregateFolderItem(t, 711, item.KindMovie, "runtimeless movie", nil)
	leafWithoutPosition := aggregateFolderItem(t, 712, item.KindMovie, "unstarted movie", nil)
	leafWithoutPosition.RunTimeTicks = &runtime
	folder := aggregateFolderItem(t, 720, item.KindFolder, "folder", nil)
	folder.IsFolder = true

	for _, value := range []item.Item{leafWithPosition, leafWithoutRuntime, leafWithoutPosition, folder} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert leaf percentage fixture %s: %v", value.ID, err)
		}
	}
	aggregateUpsertUserData(t, items, userID, leafWithPosition, false, 4_000_000)
	aggregateUpsertUserData(t, items, userID, leafWithoutPosition, false, 0)

	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, aggregateUserReader{users: map[dto.UUID]store.User{
		userID: {ID: userID, Policy: []byte(`{"EnableAllFolders":true}`)},
	}}, nil))
	page := aggregateBuildPage(t, builder, []item.Item{leafWithPosition, leafWithoutRuntime, leafWithoutPosition, folder}, userID)

	if got, want := string(page[leafWithPosition.ID]["PlayedPercentage"]), aggregateJSON(40.0); got != want {
		t.Fatalf("in-progress leaf PlayedPercentage = %s, want %s", got, want)
	}
	if _, found := page[leafWithoutRuntime.ID]["PlayedPercentage"]; found {
		t.Errorf("runtimeless leaf PlayedPercentage = %s, want omitted", page[leafWithoutRuntime.ID]["PlayedPercentage"])
	}
	if _, found := page[leafWithoutPosition.ID]["PlayedPercentage"]; found {
		t.Errorf("unstarted leaf PlayedPercentage = %s, want omitted", page[leafWithoutPosition.ID]["PlayedPercentage"])
	}
	if _, found := page[folder.ID]["PlayedPercentage"]; found {
		t.Errorf("childless folder PlayedPercentage = %s, want omitted (no child aggregation)", page[folder.ID]["PlayedPercentage"])
	}
}
