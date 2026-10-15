// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryUserDataOrderPathsSQLite pins each way a user row reaches
// an item for user-relative sorts: by stored item id only, by a custom key the
// item carries, and (DatePlayed only) by a row of an alternate version.
func TestItemStoreQueryUserDataOrderPathsSQLite(t *testing.T) {
	runItemStoreQueryUserDataOrderPaths(t, openItemStore)
}

func TestItemStoreQueryUserDataOrderPathsPostgres(t *testing.T) {
	runItemStoreQueryUserDataOrderPaths(t, openPostgresItemStore)
}

func runItemStoreQueryUserDataOrderPaths(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	userID := scalarOrderStoreUUID(t, 900)
	direct := scalarOrderStoreItem(t, 901, "d-direct", 0)
	carried := scalarOrderStoreItem(t, 902, "k-carried", 0)
	carried.ProviderIDs.Set("Tmdb", "777")
	sharer := scalarOrderStoreItem(t, 903, "s-sharer", 0)
	sharer.ProviderIDs.Set("Tmdb", "777")
	primary := scalarOrderStoreItem(t, 904, "p-primary", 0)
	alternate := scalarOrderStoreItem(t, 905, "a-alternate", 0)
	alternate.PrimaryVersionID = &primary.ID
	none := scalarOrderStoreItem(t, 906, "n-none", 0)
	for _, it := range []item.Item{direct, carried, sharer, primary, alternate, none} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting %s: %v", it.ID, err)
		}
	}
	for _, row := range []struct {
		key  string
		item dto.UUID
		day  int
	}{
		// The key belongs to no item, so only the stored item id reaches direct.
		{"orphan-key", direct.ID, 5},
		// The row is stored for sharer; carried reaches it by the shared key.
		{"777", sharer.ID, 3},
		{scalarOrderStoreUserKey(t, alternate), alternate.ID, 4},
	} {
		lastPlayed := scalarOrderStoreTime(2025, row.day)
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: row.key, ItemID: row.item, PlayCount: row.day, LastPlayed: &lastPlayed}); err != nil {
			t.Fatalf("upserting user data %s: %v", row.key, err)
		}
	}
	query := func(sortBy store.ItemSortBy) []dto.UUID {
		t.Helper()
		got, err := items.Query(t.Context(), store.ItemQuery{
			ItemIDs:   []dto.UUID{direct.ID, carried.ID, primary.ID, none.ID},
			UserID:    &userID,
			SortBy:    []store.ItemSortBy{sortBy},
			SortOrder: []store.SortOrder{store.SortOrderDescending},
		})
		if err != nil {
			t.Fatalf("query %s: %v", sortBy, err)
		}
		return scalarOrderStoreIDs(got.Items)
	}
	if got, want := query(store.ItemSortByDatePlayed), []dto.UUID{direct.ID, primary.ID, carried.ID, none.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DatePlayed order = %v, want %v", got, want)
	}
	if got, want := query(store.ItemSortByPlayCount), []dto.UUID{direct.ID, carried.ID, none.ID, primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PlayCount order = %v, want %v", got, want)
	}
}
