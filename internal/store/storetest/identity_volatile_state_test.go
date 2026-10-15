// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// ClearUserVolatileState is the persistence boundary used by replay seeding.
// A failed reset must leave every state family intact: deleting a device before
// preferences, for example, must not strand a partially reset account.
func TestIdentityClearUserVolatileStateIsAtomicAndIdempotent(t *testing.T) {
	ctx := t.Context()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite stores: %v", err)
		}
	})
	identity := store.NewIdentity(stores)
	userID := mustStoreUUID(t, "abababab-abab-abab-abab-abababababab")
	itemID := mustStoreUUID(t, "cdcdcdcd-cdcd-cdcd-cdcd-cdcdcdcdcdcd")
	sortBy := "DateCreated"
	if _, err := identity.CreateUser(ctx, store.User{ID: userID, Name: "replay", NormalizedName: "REPLAY"}); err != nil {
		t.Fatalf("create replay user: %v", err)
	}
	device := createStoreDevice(t, identity, userID, "volatile-token", "volatile-device")
	if err := identity.PutDeviceOptions(ctx, store.DeviceOptions{DeviceID: device.DeviceID}); err != nil {
		t.Fatalf("create volatile device options: %v", err)
	}
	if err := identity.PutDisplayPreference(ctx, store.DisplayPreference{UserID: userID, ItemID: itemID, Client: "volatile-client", Data: []byte(`{"ShowSidebar":false}`)}); err != nil {
		t.Fatalf("create volatile display preference: %v", err)
	}
	if err := identity.PutCustomItemDisplayPreference(ctx, store.CustomItemDisplayPreference{UserID: userID, ItemID: itemID, Client: "volatile-client", Key: "SortBy", Value: &sortBy}); err != nil {
		t.Fatalf("create volatile custom preference: %v", err)
	}
	if err := identity.ReplaceAccessSchedules(ctx, userID, []store.AccessSchedule{{UserID: userID, DayOfWeek: 1, StartHour: 8, EndHour: 17}}); err != nil {
		t.Fatalf("create volatile access schedule: %v", err)
	}

	if _, err := stores.Writer().ExecContext(ctx, `CREATE TRIGGER fail_clear_display_preferences BEFORE DELETE ON display_preferences BEGIN SELECT RAISE(ABORT, 'injected clear failure'); END`); err != nil {
		t.Fatalf("install rollback trigger: %v", err)
	}
	if err := identity.ClearUserVolatileState(ctx, userID); err == nil {
		t.Fatal("ClearUserVolatileState with failing display-preference delete succeeded")
	}
	assertVolatileStatePresent(t, identity, stores, userID, itemID, device)

	if _, err := stores.Writer().ExecContext(ctx, `DROP TRIGGER fail_clear_display_preferences`); err != nil {
		t.Fatalf("remove rollback trigger: %v", err)
	}
	if err := identity.ClearUserVolatileState(ctx, userID); err != nil {
		t.Fatalf("clear volatile state: %v", err)
	}
	assertVolatileStateAbsent(t, identity, stores, userID, itemID, device)
	if err := identity.ClearUserVolatileState(ctx, userID); err != nil {
		t.Fatalf("repeat clear volatile state: %v", err)
	}
}

func assertVolatileStatePresent(t *testing.T, identity *store.Identity, stores store.Stores, userID, itemID storeUUID, device store.Device) {
	t.Helper()
	if _, err := identity.DeviceByToken(t.Context(), device.AccessToken); err != nil {
		t.Fatalf("device lost after failed clear: %v", err)
	}
	if _, err := identity.GetDeviceOptions(t.Context(), device.DeviceID); err != nil {
		t.Fatalf("device options lost after failed clear: %v", err)
	}
	if _, err := identity.GetDisplayPreference(t.Context(), userID, itemID, "volatile-client"); err != nil {
		t.Fatalf("display preference lost after failed clear: %v", err)
	}
	assertVolatileCustomPreferenceCount(t, stores, userID, 1)
	schedules, err := identity.AccessSchedules(t.Context(), userID)
	if err != nil {
		t.Fatalf("read access schedules after failed clear: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("access schedules after failed clear = %#v, want one", schedules)
	}
}

func assertVolatileStateAbsent(t *testing.T, identity *store.Identity, stores store.Stores, userID, itemID storeUUID, device store.Device) {
	t.Helper()
	if _, err := identity.DeviceByToken(t.Context(), device.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device after clear = %v, want ErrNotFound", err)
	}
	if _, err := identity.GetDeviceOptions(t.Context(), device.DeviceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device options after clear = %v, want ErrNotFound", err)
	}
	if _, err := identity.GetDisplayPreference(t.Context(), userID, itemID, "volatile-client"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("display preference after clear = %v, want ErrNotFound", err)
	}
	assertVolatileCustomPreferenceCount(t, stores, userID, 0)
	schedules, err := identity.AccessSchedules(t.Context(), userID)
	if err != nil {
		t.Fatalf("read access schedules after clear: %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("access schedules after clear = %#v, want none", schedules)
	}
}

type storeUUID = [16]byte

func assertVolatileCustomPreferenceCount(t *testing.T, stores store.Stores, userID storeUUID, want int) {
	t.Helper()
	var got int
	if err := stores.Reader().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM custom_item_display_preferences WHERE user_id = ?`, userID[:]).Scan(&got); err != nil {
		t.Fatalf("count custom item display preferences: %v", err)
	}
	if got != want {
		t.Fatalf("custom item display preference count = %d, want %d", got, want)
	}
}
