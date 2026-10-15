// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
)

// A playback report is bound to the session generation observed at
// authentication, not merely to a reusable client/device identity. Refreshing
// that identity must revoke the old target before an in-flight report can
// mutate the newly authenticated session.
func TestManagerPlaybackTargetsRejectStaleSessionGenerations(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "generation-client", "generation-device", sessionTestUserA)
	staleTarget := created.Target

	refreshed, err := manager.LogActivity(t.Context(), session.Activity{
		Client: "GENERATION-CLIENT", Version: "10.11.0", DeviceID: "GENERATION-DEVICE", DeviceName: "Refreshed device", UserID: sessionTestUserB,
	})
	if err != nil {
		t.Fatalf("refresh session activity: %v", err)
	}
	if refreshed.ID != created.ID {
		t.Fatalf("refreshed session ID = %q, want existing ID %q", refreshed.ID, created.ID)
	}
	currentTarget := refreshed.Target
	if currentTarget.Generation == staleTarget.Generation {
		t.Fatalf("refreshed target generation = %v, want a generation distinct from stale target %v", currentTarget.Generation, staleTarget.Generation)
	}

	started := session.PlaybackState{ItemID: dto.UUID{0x71}, PlaySessionID: "current-generation"}
	if err := manager.StartPlayback(t.Context(), staleTarget, started); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("start with stale target error = %v, want ErrNotFound", err)
	}
	if err := manager.StartPlayback(t.Context(), currentTarget, started); err != nil {
		t.Fatalf("start with current target: %v", err)
	}

	progressPosition := int64(600)
	progressed := session.PlaybackState{ItemID: dto.UUID{0x71}, PositionTicks: &progressPosition, PlaySessionID: "stale-progress"}
	if err := manager.StopPlayback(t.Context(), staleTarget, session.PlaybackState{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("stop with stale target error = %v, want ErrNotFound", err)
	}
	if err := manager.ProgressPlayback(t.Context(), staleTarget, progressed); err != nil {
		t.Fatalf("progress with stale target error = %v, want nil no-op", err)
	}

	stillCurrent, found := manager.Session("generation-client", "generation-device")
	if !found || stillCurrent.NowPlaying == nil || stillCurrent.NowPlaying.PlaySessionID != started.PlaySessionID || stillCurrent.NowPlaying.PositionTicks != nil {
		t.Fatalf("stale playback changed refreshed session = %#v, found=%t; want current start state", stillCurrent, found)
	}
	if err := manager.ProgressPlayback(t.Context(), currentTarget, progressed); err != nil {
		t.Fatalf("progress with current target: %v", err)
	}
	if current, found := manager.Session("generation-client", "generation-device"); !found || current.NowPlaying == nil || current.NowPlaying.PositionTicks == nil || *current.NowPlaying.PositionTicks != progressPosition {
		t.Fatalf("current target progress session = %#v, found=%t; want position %d", current, found, progressPosition)
	}
	if err := manager.StopPlayback(t.Context(), currentTarget, session.PlaybackState{}); err != nil {
		t.Fatalf("stop with current target: %v", err)
	}
	if current, found := manager.Session("generation-client", "generation-device"); !found || current.NowPlaying != nil {
		t.Fatalf("current target stop session = %#v, found=%t; want cleared now-playing state", current, found)
	}
}

// Reauthentication changes the session owner as well as its target generation.
// Playback state reported by the prior owner must not become visible to the
// replacement user merely because the client/device identity was reused.
func TestManagerReauthenticationClearsPriorGenerationPlaybackProjection(t *testing.T) {
	manager, fake := newManager(t)
	original := logActivity(t, manager, "review-client", "review-device", sessionTestUserA)
	queue := []dto.QueueItem{
		{ID: dto.UUID{0x81}, PlaylistItemID: stringPointer("queue-current")},
		{ID: dto.UUID{0x82}, PlaylistItemID: stringPointer("queue-next")},
	}
	if err := manager.StartPlayback(t.Context(), original.Target, session.PlaybackState{
		ItemID: dto.UUID{0x81}, PlaySessionID: "prior-generation", NowPlayingQueue: &queue, PlaylistItemID: "queue-current",
	}); err != nil {
		t.Fatalf("start prior-generation playback: %v", err)
	}
	fake.Advance(time.Second)
	position := int64(600)
	if err := manager.ProgressPlayback(t.Context(), original.Target, session.PlaybackState{
		ItemID: dto.UUID{0x81}, PositionTicks: &position, IsPaused: true, PlaySessionID: "prior-generation", NowPlayingQueue: &queue, PlaylistItemID: "queue-current",
	}); err != nil {
		t.Fatalf("progress prior-generation playback: %v", err)
	}
	prior, found := manager.Session("review-client", "review-device")
	if !found || prior.NowPlaying == nil || len(prior.NowPlayingQueue) != len(queue) || prior.PlaylistItemID != "queue-current" || prior.LastPlaybackCheckIn.IsZero() || prior.LastPausedDate == nil {
		t.Fatalf("prior generation playback setup = %#v, found=%t; want active queue and paused playback projection", prior, found)
	}

	reauthenticated, err := manager.LogActivity(t.Context(), session.Activity{
		Client: "REVIEW-CLIENT", Version: "10.11.0", DeviceID: "REVIEW-DEVICE", DeviceName: "Replacement device", UserID: sessionTestUserB,
	})
	if err != nil {
		t.Fatalf("reauthenticate same client/device: %v", err)
	}
	if reauthenticated.UserID != sessionTestUserB || reauthenticated.Target.Generation == original.Target.Generation {
		t.Fatalf("reauthenticated snapshot = %#v, want replacement user and generation", reauthenticated)
	}

	replacement, found := manager.Session("review-client", "review-device")
	if !found || replacement.UserID != sessionTestUserB || replacement.NowPlaying != nil || len(replacement.NowPlayingQueue) != 0 || replacement.PlaylistItemID != "" || !replacement.LastPlaybackCheckIn.IsZero() || replacement.LastPausedDate != nil {
		t.Fatalf("replacement session retained prior user playback projection = %#v, found=%t", replacement, found)
	}
}
