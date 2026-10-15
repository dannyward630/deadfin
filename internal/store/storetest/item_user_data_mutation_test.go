// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

var errUserDataMutationAborted = errors.New("injected user-data mutation failure")

// TestItemStoreMutateUserDataForItemSQLite defines the durable playback-state
// mutation contract on the production SQLite backend.
func TestItemStoreMutateUserDataForItemSQLite(t *testing.T) {
	runItemStoreMutateUserDataForItem(t, openReviewerItemStore)
}

// TestItemStoreMutateUserDataForItemPostgres exercises the same transaction
// contract on PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreMutateUserDataForItemPostgres(t *testing.T) {
	runItemStoreMutateUserDataForItem(t, openPostgresReviewerItemStore)
}

func runItemStoreMutateUserDataForItem(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	t.Run("reuses the existing shared key while retaining unrelated user data", func(t *testing.T) {
		items, stores := open(t)
		fixture := newUserDataMutationFixture(t, items)
		before := fixture.data
		lastPlayed := time.Date(2026, 7, 27, 13, 14, 15, 0, time.UTC)

		got, err := items.MutateUserDataForItem(t.Context(), fixture.userID, fixture.sibling.ID, func(data *store.UserData) error {
			data.Played = true
			data.PositionTicks = 4_000
			data.PlayCount++
			data.LastPlayed = &lastPlayed
			return nil
		})
		if err != nil {
			t.Fatalf("mutate sibling playback user data: %v", err)
		}

		want := before
		want.ItemID = fixture.sibling.ID
		want.Played = true
		want.PositionTicks = 4_000
		want.PlayCount++
		want.LastPlayed = &lastPlayed
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("committed playback mutation = %#v, want %#v", got, want)
		}
		stored, err := items.GetUserData(t.Context(), fixture.userID, fixture.sharedKey)
		if err != nil {
			t.Fatalf("get shared playback user data after mutation: %v", err)
		}
		if !reflect.DeepEqual(stored, want) {
			t.Fatalf("stored playback mutation = %#v, want %#v", stored, want)
		}
		assertUserDataMutationRowCount(t, stores, fixture.userID, 1)
	})

	t.Run("serializes concurrent play count increments", func(t *testing.T) {
		items, _ := open(t)
		fixture := newUserDataMutationFixture(t, items)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()

		const increments = 16
		operations := make([]func(context.Context) error, 0, increments)
		for range increments {
			operations = append(operations, func(ctx context.Context) error {
				_, err := items.MutateUserDataForItem(ctx, fixture.userID, fixture.sibling.ID, func(data *store.UserData) error {
					data.PlayCount++
					return nil
				})
				return err
			})
		}
		runConcurrentItemStoreOperations(t, ctx, operations)

		stored, err := items.GetUserData(ctx, fixture.userID, fixture.sharedKey)
		if err != nil {
			t.Fatalf("get playback user data after concurrent increments: %v", err)
		}
		if got, want := stored.PlayCount, fixture.data.PlayCount+increments; got != want {
			t.Fatalf("play count after %d concurrent increments = %d, want %d", increments, got, want)
		}
	})

	t.Run("rolls back callback changes when the mutation fails", func(t *testing.T) {
		items, stores := open(t)
		fixture := newUserDataMutationFixture(t, items)

		got, err := items.MutateUserDataForItem(t.Context(), fixture.userID, fixture.sibling.ID, func(data *store.UserData) error {
			data.Played = true
			data.PositionTicks = 9_999
			data.PlayCount++
			return errUserDataMutationAborted
		})
		if !errors.Is(err, errUserDataMutationAborted) {
			t.Fatalf("failed playback mutation error = %v, want %v", err, errUserDataMutationAborted)
		}
		if got != (store.UserData{}) {
			t.Fatalf("failed playback mutation returned %#v, want no committed row", got)
		}
		stored, err := items.GetUserData(t.Context(), fixture.userID, fixture.sharedKey)
		if err != nil {
			t.Fatalf("get playback user data after failed mutation: %v", err)
		}
		if !reflect.DeepEqual(stored, fixture.data) {
			t.Fatalf("failed playback mutation persisted %#v, want %#v", stored, fixture.data)
		}
		assertUserDataMutationRowCount(t, stores, fixture.userID, 1)
	})
}

type userDataMutationFixture struct {
	userID    dto.UUID
	source    item.Item
	sibling   item.Item
	sharedKey string
	data      store.UserData
}

func newUserDataMutationFixture(t *testing.T, items *store.ItemStore) userDataMutationFixture {
	t.Helper()

	fixture := userDataMutationFixture{
		userID:    contractUUID(t, 76_000),
		sharedKey: "shared-playback-mutation-key",
		source: item.Item{
			ID:   contractUUID(t, 76_001),
			Kind: item.KindMovie,
			Name: stringPtr("shared playback source"),
		},
		sibling: item.Item{
			ID:   contractUUID(t, 76_002),
			Kind: item.KindMovie,
			Name: stringPtr("shared playback sibling"),
		},
	}
	fixture.source.ProviderIDs.Set("Tmdb", fixture.sharedKey)
	fixture.sibling.ProviderIDs.Set("Tmdb", fixture.sharedKey)
	for _, value := range []item.Item{fixture.source, fixture.sibling} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert shared playback item %s: %v", value.ID, err)
		}
	}

	rating := float32(8.5)
	likes := 3
	audio := 1
	subtitle := 2
	lastPlayed := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	fixture.data = store.UserData{
		UserID:              fixture.userID,
		CustomKey:           fixture.sharedKey,
		ItemID:              fixture.source.ID,
		PositionTicks:       1_000,
		PlayCount:           7,
		IsFavorite:          true,
		Rating:              &rating,
		Likes:               &likes,
		LastPlayed:          &lastPlayed,
		AudioStreamIndex:    &audio,
		SubtitleStreamIndex: &subtitle,
	}
	if err := items.UpsertUserData(t.Context(), fixture.data); err != nil {
		t.Fatalf("seed shared playback user data: %v", err)
	}
	return fixture
}

func assertUserDataMutationRowCount(t *testing.T, stores store.Stores, userID dto.UUID, want int) {
	t.Helper()
	var got int
	query := "SELECT count(*) FROM user_data WHERE user_id = " + stores.Dialect().Placeholder(1)
	if err := stores.Reader().QueryRowContext(t.Context(), query, userID[:]).Scan(&got); err != nil {
		t.Fatalf("count persisted playback user-data rows: %v", err)
	}
	if got != want {
		t.Fatalf("persisted playback user-data rows = %d, want %d", got, want)
	}
}
