// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreDTOProjectionsFolderUserDataCTESQLite verifies that one VALUES
// relation can aggregate more than one requested folder without changing SQLite
// behavior. Its PostgreSQL twin is the live-DSN regression for typed UUID CTE
// placeholders.
func TestItemStoreDTOProjectionsFolderUserDataCTESQLite(t *testing.T) {
	runItemStoreDTOProjectionsFolderUserDataCTE(t, openItemStore)
}

func TestItemStoreDTOProjectionsFolderUserDataCTEPostgres(t *testing.T) {
	runItemStoreDTOProjectionsFolderUserDataCTE(t, openPostgresItemStore)
}

func runItemStoreDTOProjectionsFolderUserDataCTE(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	userID := folderUserDataProjectionUUID(t, 9800)
	first := item.Item{ID: folderUserDataProjectionUUID(t, 9801), Kind: item.KindFolder, Name: stringPtr("played folder"), IsFolder: true}
	second := item.Item{ID: folderUserDataProjectionUUID(t, 9802), Kind: item.KindFolder, Name: stringPtr("empty folder"), IsFolder: true}
	leaf := item.Item{ID: folderUserDataProjectionUUID(t, 9803), Kind: item.KindMovie, Name: stringPtr("played leaf"), ParentID: &first.ID}
	for _, value := range []item.Item{first, second, leaf} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting folder aggregate fixture %s: %v", value.ID, err)
		}
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: leaf.ID.String(), ItemID: leaf.ID, Played: true}); err != nil {
		t.Fatalf("upserting played folder aggregate state: %v", err)
	}

	projection, err := items.DTOProjections(t.Context(), []dto.UUID{first.ID, second.ID}, &userID, false, true, false, false, []dto.UUID{first.ID, second.ID})
	if err != nil {
		t.Fatalf("projecting folder UserData counts through VALUES CTE: %v", err)
	}
	if got := projection.FolderUserDataCounts[first.ID]; got != (store.FolderUserDataCounts{Played: 1, Total: 1}) {
		t.Fatalf("played folder aggregate = %#v, want one played leaf", got)
	}
	if got, found := projection.FolderUserDataCounts[second.ID]; found || got != (store.FolderUserDataCounts{}) {
		t.Fatalf("empty folder aggregate = %#v (found=%t), want no aggregate row", got, found)
	}

	// A played row also counts through any custom key the leaf carries, as
	// imported Jellyfin rows do when their stored item id is another item's.
	var providers item.ProviderIDs
	providers.Set("Imdb", "tt9800001")
	keyed := item.Item{ID: folderUserDataProjectionUUID(t, 9804), Kind: item.KindMovie, Name: stringPtr("played by key"), ParentID: &first.ID, ProviderIDs: providers}
	unplayed := item.Item{ID: folderUserDataProjectionUUID(t, 9805), Kind: item.KindMovie, Name: stringPtr("unplayed leaf"), ParentID: &first.ID}
	for _, value := range []item.Item{keyed, unplayed} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting folder aggregate fixture %s: %v", value.ID, err)
		}
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "tt9800001", ItemID: folderUserDataProjectionUUID(t, 9806), Played: true}); err != nil {
		t.Fatalf("upserting keyed played state: %v", err)
	}
	projection, err = items.DTOProjections(t.Context(), []dto.UUID{first.ID}, &userID, false, true, false, false, []dto.UUID{first.ID})
	if err != nil {
		t.Fatalf("projecting keyed folder UserData counts: %v", err)
	}
	if got := projection.FolderUserDataCounts[first.ID]; got != (store.FolderUserDataCounts{Played: 2, Total: 3}) {
		t.Fatalf("keyed folder aggregate = %#v, want two played of three leaves", got)
	}
}

func folderUserDataProjectionUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	return contractUUID(t, value)
}
