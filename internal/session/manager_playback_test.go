// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
)

// Playback events are addressed by the authenticated Client+DeviceID pair. At
// this seam the HTTP adapter has already discarded a body SessionId, and a
// PlaySessionId is only opaque playback correlation: even a value that happens
// to equal another active session's ID must not redirect the event.
func TestManagerPlaybackLifecycleMutatesAuthenticatedSessionAndPreservesIdentity(t *testing.T) {
	manager, fake := newManager(t)
	primary := logActivity(t, manager, "Jellyfin Web", "living-room", sessionTestUserA)
	other := logActivity(t, manager, "Jellyfin Android", "phone", sessionTestUserA)

	capabilities := session.Capabilities{
		PlayableMediaTypes:   []string{"Video"},
		SupportedCommands:    []string{"Play", "Stop"},
		SupportsMediaControl: true,
	}
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{CurrentID: primary.ID, Capabilities: capabilities}); err != nil {
		t.Fatalf("seed primary capabilities: %v", err)
	}

	itemID := dto.UUID{0x7a, 0x3f, 0x20, 0xc9, 0x9d, 0x50, 0x49, 0xea, 0x98, 0x1c, 0x48, 0xa5, 0xa9, 0xc5, 0xb5, 0xe1}
	startPosition := int64(12_345)
	start := session.PlaybackState{
		ItemID:        itemID,
		MediaSourceID: "source-main",
		PositionTicks: &startPosition,
		// This deliberately collides with another session's identity. It is
		// playback correlation, not a lookup key.
		PlaySessionID: other.ID,
		CanSeek:       true,
		PlayMethod:    "DirectPlay",
		RepeatMode:    "RepeatNone",
		PlaybackOrder: "Default",
	}
	if err := manager.StartPlayback(t.Context(), primary.Target, start); err != nil {
		t.Fatalf("start playback: %v", err)
	}
	startPosition = -1 // The manager must not retain caller-owned pointer state.

	started := sessionByID(t, manager.Sessions(session.Query{}), primary.ID)
	if started.NowPlaying == nil {
		t.Fatal("started session has no now-playing playback state")
	}
	if got := started.NowPlaying; got.ItemID != itemID || got.MediaSourceID != "source-main" || got.PositionTicks == nil || *got.PositionTicks != 12_345 || got.PlaySessionID != other.ID || !got.CanSeek || got.PlayMethod != "DirectPlay" {
		t.Fatalf("started now-playing state = %#v, want item, source, position, opaque play session, and playback controls", got)
	}
	if !started.LastPlaybackCheckIn.Equal(fake.Now()) {
		t.Fatalf("start playback check-in = %s, want fake-clock time %s", started.LastPlaybackCheckIn, fake.Now())
	}
	if redirected := sessionByID(t, manager.Sessions(session.Query{}), other.ID); redirected.NowPlaying != nil {
		t.Fatalf("PlaySessionId redirected playback to another session: %#v", redirected.NowPlaying)
	}

	fake.Advance(17 * time.Second)
	pausedPosition := int64(98_765)
	if err := manager.ProgressPlayback(t.Context(), primary.Target, session.PlaybackState{
		ItemID:        itemID,
		MediaSourceID: "source-main",
		PositionTicks: &pausedPosition,
		PlaySessionID: other.ID,
		CanSeek:       true,
		IsPaused:      true,
		PlayMethod:    "DirectPlay",
		RepeatMode:    "RepeatNone",
		PlaybackOrder: "Default",
	}); err != nil {
		t.Fatalf("report playback progress: %v", err)
	}
	pausedPosition = -1 // The manager must own incoming state after the call.

	progressed := sessionByID(t, manager.Sessions(session.Query{}), primary.ID)
	if got := progressed.NowPlaying; got == nil || got.PositionTicks == nil || *got.PositionTicks != 98_765 || !got.IsPaused {
		t.Fatalf("progressed now-playing state = %#v, want paused position 98765", got)
	}
	if !progressed.LastPlaybackCheckIn.Equal(fake.Now()) {
		t.Fatalf("progress check-in = %s, want fake-clock time %s", progressed.LastPlaybackCheckIn, fake.Now())
	}
	if progressed.LastPausedDate == nil || !progressed.LastPausedDate.Equal(fake.Now()) {
		t.Fatalf("progress pause date = %v, want fake-clock time %s", progressed.LastPausedDate, fake.Now())
	}

	// A snapshot is a value callers may freely mutate; its nested state and
	// timestamp pointers must not alias registry state.
	*progressed.NowPlaying.PositionTicks = 1
	progressed.NowPlaying.MediaSourceID = "mutated-source"
	*progressed.LastPausedDate = fake.Now().Add(time.Hour)
	afterMutation := sessionByID(t, manager.Sessions(session.Query{}), primary.ID)
	if got := afterMutation.NowPlaying; got == nil || got.PositionTicks == nil || *got.PositionTicks != 98_765 || got.MediaSourceID != "source-main" {
		t.Fatalf("mutating returned playback snapshot changed manager state: %#v", got)
	}
	if afterMutation.LastPausedDate == nil || !afterMutation.LastPausedDate.Equal(fake.Now()) {
		t.Fatalf("mutating returned pause timestamp changed manager state: %v, want %s", afterMutation.LastPausedDate, fake.Now())
	}

	if err := manager.StopPlayback(t.Context(), primary.Target, session.PlaybackState{}); err != nil {
		t.Fatalf("stop playback: %v", err)
	}
	stopped := sessionByID(t, manager.Sessions(session.Query{}), primary.ID)
	if stopped.NowPlaying != nil {
		t.Fatalf("stopped session retained now-playing state: %#v", stopped.NowPlaying)
	}
	if stopped.ID != primary.ID || stopped.Client != primary.Client || stopped.DeviceID != primary.DeviceID || stopped.UserID != primary.UserID {
		t.Fatalf("stop playback changed session identity: %#v, want identity from %#v", stopped, primary)
	}
	if !sameStrings(stopped.Capabilities.PlayableMediaTypes, capabilities.PlayableMediaTypes) || !sameStrings(stopped.Capabilities.SupportedCommands, capabilities.SupportedCommands) || !stopped.Capabilities.SupportsMediaControl {
		t.Fatalf("stop playback discarded session capabilities: %#v", stopped.Capabilities)
	}
}

func TestManagerPlaybackMissingSessionErrorsOnStartAndStopButProgressIsNoOp(t *testing.T) {
	manager, _ := newManager(t)
	position := int64(1)
	state := session.PlaybackState{ItemID: dto.UUID{0x1}, PositionTicks: &position, PlaySessionID: "opaque-playback-correlation"}
	missing := session.Target{ID: "missing-target", Generation: 1}

	if err := manager.StartPlayback(t.Context(), missing, state); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("start playback for a missing session error = %v, want ErrNotFound", err)
	}
	if err := manager.StopPlayback(t.Context(), missing, state); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("stop playback for a missing session error = %v, want ErrNotFound", err)
	}
	if err := manager.ProgressPlayback(t.Context(), missing, state); err != nil {
		t.Fatalf("progress playback for a missing session error = %v, want nil no-op", err)
	}
	if sessions := manager.Sessions(session.Query{}); len(sessions) != 0 {
		t.Fatalf("missing-session playback events created sessions: %#v", sessions)
	}
}
