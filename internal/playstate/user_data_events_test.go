// SPDX-License-Identifier: GPL-2.0-only

package playstate_test

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/playstate"
)

func TestSubscribeUserDataChangesBatchesPerUserAfterFiveHundredMilliseconds(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	bus := events.NewBus[playstate.UserDataChanged](fake)
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	userID, otherUserID := userDataEventUUID(1), userDataEventUUID(2)
	firstItem, secondItem := userDataEventUUID(3), userDataEventUUID(4)
	subscription := playstate.SubscribeUserDataChanges(bus, userID, 1)

	bus.Publish(playstate.UserDataChanged{UserID: userID, ItemID: firstItem})
	bus.Publish(playstate.UserDataChanged{UserID: otherUserID, ItemID: userDataEventUUID(5)})
	fake.Advance(499 * time.Millisecond)
	assertNoUserDataBatch(t, subscription.Events())

	bus.Publish(playstate.UserDataChanged{UserID: userID, ItemID: secondItem})
	bus.Publish(playstate.UserDataChanged{UserID: userID, ItemID: firstItem})
	fake.Advance(499 * time.Millisecond)
	assertNoUserDataBatch(t, subscription.Events())
	fake.Advance(time.Millisecond)

	got := receiveUserDataBatch(t, subscription.Events())
	if got.UserID != userID || got.ItemID != (dto.UUID{}) ||
		len(got.ItemIDs) != 2 || got.ItemIDs[0] != firstItem || got.ItemIDs[1] != secondItem {
		t.Fatalf("user-data batch = %+v, want user %s with de-duplicated item ids [%s %s]", got, userID, firstItem, secondItem)
	}
}

func receiveUserDataBatch(t *testing.T, values <-chan playstate.UserDataChanged) playstate.UserDataChanged {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatal("user-data subscription closed")
		}
		return value
	case <-timer.C:
		t.Fatal("user-data batch was not delivered")
		return playstate.UserDataChanged{}
	}
}

func assertNoUserDataBatch(t *testing.T, values <-chan playstate.UserDataChanged) {
	t.Helper()
	select {
	case value := <-values:
		t.Fatalf("user-data batch arrived before trailing debounce: %+v", value)
	default:
	}
}

func userDataEventUUID(value byte) dto.UUID {
	var id dto.UUID
	id[15] = value
	return id
}
