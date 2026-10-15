// SPDX-License-Identifier: GPL-2.0-only

package playstate

import (
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
)

const userDataChangeDebounce = 500 * time.Millisecond

// SubscribeUserDataChanges returns one user's trailing 500ms update batch.
// UserDataChangeNotifier.cs groups independently per user and de-duplicates
// item ids before constructing the outbound UserDataChanged payload.
func SubscribeUserDataChanges(bus *events.Bus[UserDataChanged], userID dto.UUID, buffer int) *events.Subscription[UserDataChanged] {
	return bus.SubscribeWith(
		events.SubscriptionOptions{Coalesce: userDataChangeDebounce, Buffer: buffer},
		func(change UserDataChanged) bool { return change.UserID == userID },
		mergeUserDataChanges,
	)
}

func mergeUserDataChanges(current, next UserDataChanged) UserDataChanged {
	result := UserDataChanged{UserID: next.UserID}
	seen := make(map[dto.UUID]struct{}, len(current.ItemIDs)+len(next.ItemIDs)+2)
	appendID := func(id dto.UUID) {
		if id.IsNil() {
			return
		}
		if _, found := seen[id]; found {
			return
		}
		seen[id] = struct{}{}
		result.ItemIDs = append(result.ItemIDs, id)
	}
	for _, value := range []UserDataChanged{current, next} {
		appendID(value.ItemID)
		for _, id := range value.ItemIDs {
			appendID(id)
		}
	}
	return result
}
