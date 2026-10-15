// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreQuerySQLiteBackfillsLegacyUserDataKeysOnOpen defends upgrade
// continuity: a 00004 database's pre-existing shared user-data row must be
// queryable as soon as sqlite.Open has migrated it, without touching an item.
func TestItemStoreQuerySQLiteBackfillsLegacyUserDataKeysOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-user-data.db")
	legacy, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open legacy SQLite seed store: %v", err)
	}
	seedLegacySharedUserData(t, legacy)
	if err := migrate.DownTo(t.Context(), legacy.Writer(), migrate.SQLite, 4); err != nil {
		t.Fatalf("downgrade SQLite seed to 00004: %v", err)
	}
	var mappingCount int
	if err := legacy.Writer().QueryRowContext(t.Context(), "SELECT count(*) FROM item_user_data_keys").Scan(&mappingCount); err == nil {
		t.Fatal("SQLite seed retained item_user_data_keys after downgrade to 00004")
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close 00004 SQLite seed store: %v", err)
	}

	upgraded, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen and migrate SQLite legacy store: %v", err)
	}
	t.Cleanup(func() {
		if err := upgraded.Close(); err != nil {
			t.Errorf("close upgraded SQLite store: %v", err)
		}
	})
	assertLegacySharedUserDataMatchesSibling(t, upgraded)
}

// TestItemStoreQueryPostgresBackfillsLegacyUserDataKeys verifies the same
// 00004-to-current data contract for the opt-in PostgreSQL backend. The shared
// storetest fixture skips this test when no live PostgreSQL DSN is configured.
func TestItemStoreQueryPostgresBackfillsLegacyUserDataKeys(t *testing.T) {
	stores := storetestpkg.OpenPostgres(t)
	seedLegacySharedUserData(t, stores)
	if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.Postgres, 4); err != nil {
		t.Fatalf("downgrade PostgreSQL seed to 00004: %v", err)
	}
	if err := migrate.Up(t.Context(), stores.Writer(), migrate.Postgres); err != nil {
		t.Fatalf("upgrade PostgreSQL legacy seed: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		t.Fatalf("backfill PostgreSQL legacy user-data keys: %v", err)
	}
	assertLegacySharedUserDataMatchesSibling(t, stores)
}

func seedLegacySharedUserData(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	userID := legacyUserKeyUUID(t, 900)
	source := item.Item{ID: legacyUserKeyUUID(t, 901), Kind: item.KindMovie, Name: legacyUserKeyString("legacy source")}
	candidate := item.Item{ID: legacyUserKeyUUID(t, 902), Kind: item.KindMovie, Name: legacyUserKeyString("legacy sibling")}
	source.ProviderIDs.Set("Tmdb", "legacy-shared")
	candidate.ProviderIDs.Set("Tmdb", "legacy-shared")
	for _, it := range []item.Item{source, candidate} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert legacy item %s: %v", it.ID, err)
		}
	}
	sharedKey := legacySharedUserKey(t, source, candidate)
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: sharedKey, ItemID: source.ID, Played: true}); err != nil {
		t.Fatalf("upsert legacy shared user data: %v", err)
	}
}

func assertLegacySharedUserDataMatchesSibling(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	userID := legacyUserKeyUUID(t, 900)
	candidateID := legacyUserKeyUUID(t, 902)
	played := true
	result, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{candidateID}, UserID: &userID, IsPlayed: &played, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query upgraded legacy sibling: %v", err)
	}
	if got, want := legacyUserKeyIDs(result.Items), []dto.UUID{candidateID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upgraded legacy played sibling IDs = %v, want %v", got, want)
	}
	if result.TotalRecordCount != 1 {
		t.Fatalf("upgraded legacy played sibling count = %d, want 1", result.TotalRecordCount)
	}
}

func legacySharedUserKey(t *testing.T, source, candidate item.Item) string {
	t.Helper()
	candidateKeys := make(map[string]struct{})
	for _, key := range item.UserDataKeys(candidate, item.UserDataInputs{}) {
		candidateKeys[key] = struct{}{}
	}
	for _, key := range item.UserDataKeys(source, item.UserDataInputs{}) {
		if _, ok := candidateKeys[key]; ok {
			return key
		}
	}
	t.Fatalf("legacy source keys %q and candidate keys %q have no shared identity", item.UserDataKeys(source, item.UserDataInputs{}), item.UserDataKeys(candidate, item.UserDataInputs{}))
	return ""
}

func legacyUserKeyUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse legacy fixture UUID %d: %v", value, err)
	}
	return id
}

func legacyUserKeyString(value string) *string { return &value }

func legacyUserKeyIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
