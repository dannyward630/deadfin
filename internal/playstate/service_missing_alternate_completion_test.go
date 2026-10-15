// SPDX-License-Identifier: GPL-2.0-only

package playstate_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
)

// Completing a linked movie must also create completed user data for an
// alternate whose distinct persistence key has never been seen for this user.
func TestServiceProgressCompletionCreatesMissingLinkedAlternateUserData(t *testing.T) {
	fixture := newPlaystateServiceFixture(t, item.KindMovie)
	alternate := fixture.addLinkedAlternateMovie(t)
	primaryKeys := item.UserDataKeys(fixture.item, item.UserDataInputs{})
	alternateKeys := item.UserDataKeys(alternate, item.UserDataInputs{})
	if len(primaryKeys) == 0 || len(alternateKeys) == 0 {
		t.Fatalf("linked movie versions must both have user-data keys: primary=%v alternate=%v", primaryKeys, alternateKeys)
	}
	if primaryKeys[0] == alternateKeys[0] {
		t.Fatalf("linked movie versions unexpectedly share user-data key %q", primaryKeys[0])
	}
	primaryBefore := fixture.seedUserData(t, fixture.primaryUser)
	fixture.startLivePlayback(t)
	position := ticks(900)

	if err := fixture.application.Progress(t.Context(), fixture.actor, playstate.Report{PlaybackState: session.PlaybackState{
		ItemID: fixture.item.ID, PositionTicks: &position,
	}}); err != nil {
		t.Fatalf("report completing playback progress: %v", err)
	}

	fixture.assertCompletedUserData(t, fixture.userData(t, fixture.primaryUser), primaryBefore)

	alternateData := fixture.userDataForItem(t, fixture.primaryUser, alternate)
	if alternateData.UserID != fixture.primaryUser || alternateData.ItemID != alternate.ID || alternateData.CustomKey != alternateKeys[0] {
		t.Fatalf("created alternate user data identity = %#v, want user %s, item %s, key %q", alternateData, fixture.primaryUser, alternate.ID, alternateKeys[0])
	}
	if !alternateData.Played || alternateData.PositionTicks != 0 {
		t.Fatalf("created alternate user data = %#v, want Played=true and PositionTicks=0", alternateData)
	}
}
