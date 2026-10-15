// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreMutateUserDataForItemCanonicalKeyOrderSQLite protects the
// production SQLite path: mutation must use the first canonical UserData key,
// rather than the lexically first materialized key.
func TestItemStoreMutateUserDataForItemCanonicalKeyOrderSQLite(t *testing.T) {
	runItemStoreMutateUserDataForItemCanonicalKeyOrder(t, openReviewerItemStore)
}

// TestItemStoreMutateUserDataForItemCanonicalKeyOrderPostgres runs the same
// canonical-key contract against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is
// configured.
func TestItemStoreMutateUserDataForItemCanonicalKeyOrderPostgres(t *testing.T) {
	runItemStoreMutateUserDataForItemCanonicalKeyOrder(t, openPostgresReviewerItemStore)
}

func runItemStoreMutateUserDataForItemCanonicalKeyOrder(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	t.Run("creates the first key and shares it with the sibling movie", func(t *testing.T) {
		items, _ := open(t)
		fixture := newCanonicalUserDataKeyOrderFixture(t, items)

		got, err := items.MutateUserDataForItem(t.Context(), fixture.userID, fixture.source.ID, func(data *store.UserData) error {
			data.Played = true
			data.PositionTicks = 1_234
			return nil
		})
		if err != nil {
			t.Fatalf("mutate movie user data without a stored row: %v", err)
		}
		want := store.UserData{
			UserID:        fixture.userID,
			CustomKey:     fixture.sharedKey,
			ItemID:        fixture.source.ID,
			Played:        true,
			PositionTicks: 1_234,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("created movie user data = %#v, want canonical shared-key row %#v", got, want)
		}
		stored, err := items.GetUserData(t.Context(), fixture.userID, fixture.sharedKey)
		if err != nil {
			t.Fatalf("get canonical shared-key row: %v", err)
		}
		if !reflect.DeepEqual(stored, want) {
			t.Fatalf("stored canonical shared-key row = %#v, want %#v", stored, want)
		}

		played := true
		page, err := items.Query(t.Context(), store.ItemQuery{
			UserID:                 &fixture.userID,
			ItemIDs:                []dto.UUID{fixture.sibling.ID},
			IsPlayed:               &played,
			EnableTotalRecordCount: true,
		})
		if err != nil {
			t.Fatalf("query sibling movie shared playback state: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != fixture.sibling.ID || page.TotalRecordCount != 1 {
			t.Fatalf("played sibling query = %#v, want sibling %s with one result", page, fixture.sibling.ID)
		}
	})

	t.Run("updates the canonical row while retaining the UUID row", func(t *testing.T) {
		items, _ := open(t)
		fixture := newCanonicalUserDataKeyOrderFixture(t, items)
		canonical := store.UserData{
			UserID:        fixture.userID,
			CustomKey:     fixture.sharedKey,
			ItemID:        fixture.source.ID,
			PositionTicks: 10,
			PlayCount:     1,
		}
		uuidRow := store.UserData{
			UserID:        fixture.userID,
			CustomKey:     fixture.uuidKey,
			ItemID:        fixture.source.ID,
			Played:        true,
			PositionTicks: 70,
			PlayCount:     7,
		}
		for _, row := range []store.UserData{canonical, uuidRow} {
			if err := items.UpsertUserData(t.Context(), row); err != nil {
				t.Fatalf("seed user data row %q: %v", row.CustomKey, err)
			}
		}

		got, err := items.MutateUserDataForItem(t.Context(), fixture.userID, fixture.source.ID, func(data *store.UserData) error {
			data.Played = true
			data.PositionTicks += 5
			data.PlayCount++
			return nil
		})
		if err != nil {
			t.Fatalf("mutate canonical movie user data: %v", err)
		}
		canonical.Played = true
		canonical.PositionTicks = 15
		canonical.PlayCount = 2
		if !reflect.DeepEqual(got, canonical) {
			t.Fatalf("mutated movie user data = %#v, want canonical row %#v", got, canonical)
		}
		storedCanonical, err := items.GetUserData(t.Context(), fixture.userID, fixture.sharedKey)
		if err != nil {
			t.Fatalf("get mutated canonical row: %v", err)
		}
		if !reflect.DeepEqual(storedCanonical, canonical) {
			t.Fatalf("stored canonical row = %#v, want %#v", storedCanonical, canonical)
		}
		storedUUID, err := items.GetUserData(t.Context(), fixture.userID, fixture.uuidKey)
		if err != nil {
			t.Fatalf("get retained UUID row: %v", err)
		}
		if !reflect.DeepEqual(storedUUID, uuidRow) {
			t.Fatalf("stored UUID row = %#v, want unchanged %#v", storedUUID, uuidRow)
		}
	})

	t.Run("moves legacy UUID state to the new canonical key after metadata gains Tmdb", func(t *testing.T) {
		items, _ := open(t)
		userID := canonicalUserDataKeyOrderUUID(t, 98_010)
		movie := item.Item{
			ID:   canonicalUserDataKeyOrderUUID(t, 98_011),
			Kind: item.KindMovie,
			Name: canonicalUserDataKeyOrderString("metadata-added canonical key"),
		}
		movie.ProviderIDs.Set("Imdb", "tt-metadata-added")
		if err := items.Upsert(t.Context(), movie); err != nil {
			t.Fatalf("upsert movie before Tmdb metadata: %v", err)
		}

		rating := float32(8.5)
		likes := 3
		audio := 1
		subtitle := 2
		lastPlayed := time.Date(2026, 7, 27, 14, 15, 16, 0, time.UTC)
		legacy := store.UserData{
			UserID:              userID,
			CustomKey:           movie.ID.Dashed(),
			ItemID:              movie.ID,
			Played:              true,
			PositionTicks:       1_000,
			PlayCount:           7,
			IsFavorite:          true,
			Rating:              &rating,
			Likes:               &likes,
			LastPlayed:          &lastPlayed,
			AudioStreamIndex:    &audio,
			SubtitleStreamIndex: &subtitle,
		}
		if err := items.UpsertUserData(t.Context(), legacy); err != nil {
			t.Fatalf("seed legacy UUID user data: %v", err)
		}

		const sharedKey = "metadata-added-shared-key"
		movie.ProviderIDs.Set("Tmdb", sharedKey)
		if err := items.Upsert(t.Context(), movie); err != nil {
			t.Fatalf("upsert movie after Tmdb metadata: %v", err)
		}

		callbackInput := store.UserData{}
		got, err := items.MutateUserDataForItem(t.Context(), userID, movie.ID, func(data *store.UserData) error {
			callbackInput = *data
			data.PositionTicks += 5
			data.PlayCount++
			return nil
		})
		if err != nil {
			t.Fatalf("mutate user data after adding Tmdb metadata: %v", err)
		}
		wantCallbackInput := legacy
		wantCallbackInput.CustomKey = sharedKey
		if !reflect.DeepEqual(callbackInput, wantCallbackInput) {
			t.Fatalf("callback input after adding Tmdb metadata = %#v, want legacy state under canonical key %#v", callbackInput, wantCallbackInput)
		}
		want := wantCallbackInput
		want.PositionTicks = 1_005
		want.PlayCount = 8
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mutated user data after adding Tmdb metadata = %#v, want retained canonical state %#v", got, want)
		}
		stored, err := items.GetUserData(t.Context(), userID, sharedKey)
		if err != nil {
			t.Fatalf("get canonical user data after adding Tmdb metadata: %v", err)
		}
		if !reflect.DeepEqual(stored, want) {
			t.Fatalf("stored canonical user data after adding Tmdb metadata = %#v, want %#v", stored, want)
		}
	})
}

type canonicalUserDataKeyOrderFixture struct {
	userID    dto.UUID
	source    item.Item
	sibling   item.Item
	sharedKey string
	uuidKey   string
}

func newCanonicalUserDataKeyOrderFixture(t *testing.T, items *store.ItemStore) canonicalUserDataKeyOrderFixture {
	t.Helper()

	fixture := canonicalUserDataKeyOrderFixture{
		userID:    canonicalUserDataKeyOrderUUID(t, 98_000),
		sharedKey: "shared-key",
		source: item.Item{
			ID:   canonicalUserDataKeyOrderUUID(t, 98_001),
			Kind: item.KindMovie,
			Name: canonicalUserDataKeyOrderString("canonical source"),
		},
		sibling: item.Item{
			ID:   canonicalUserDataKeyOrderUUID(t, 98_002),
			Kind: item.KindMovie,
			Name: canonicalUserDataKeyOrderString("canonical sibling"),
		},
	}
	fixture.source.ProviderIDs.Set("Tmdb", fixture.sharedKey)
	fixture.source.ProviderIDs.Set("Imdb", "tt-canonical-source")
	fixture.sibling.ProviderIDs.Set("Tmdb", fixture.sharedKey)
	fixture.sibling.ProviderIDs.Set("Imdb", "tt-canonical-sibling")
	fixture.uuidKey = fixture.source.ID.Dashed()
	for _, value := range []item.Item{fixture.source, fixture.sibling} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert canonical user-data-key movie %s: %v", value.ID, err)
		}
	}
	return fixture
}

func canonicalUserDataKeyOrderUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse canonical user-data-key UUID %d: %v", value, err)
	}
	return id
}

func canonicalUserDataKeyOrderString(value string) *string { return &value }
