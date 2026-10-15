// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
)

// A live session is the public source for /Sessions. Start and progress must
// retain every client-reported player-state field, while stopped playback keeps
// its terminal timing and queue history after the active item is removed.
func TestManagerProjectsCompleteActiveAndTerminalPlaybackState(t *testing.T) {
	manager, fake := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "projection-device", sessionTestUserA)
	itemID := dto.UUID{0xa5, 0x00, 0x02}
	startPosition, progressPosition := int64(12_345), int64(98_765)
	startTime, progressStartTime := int64(1_000), int64(2_000)
	startAudio, progressAudio := 1, 3
	startSubtitle, progressSubtitle := 2, 4
	startVolume, progressVolume := 20, 80
	startBrightness, progressBrightness := 30, 90

	if err := manager.StartPlayback(t.Context(), created.Target, session.PlaybackState{
		CanSeek:                true,
		ItemID:                 itemID,
		MediaSourceID:          "source-start",
		AudioStreamIndex:       &startAudio,
		SubtitleStreamIndex:    &startSubtitle,
		IsMuted:                true,
		PositionTicks:          &startPosition,
		PlaybackStartTimeTicks: &startTime,
		VolumeLevel:            &startVolume,
		Brightness:             &startBrightness,
		AspectRatio:            "16:9",
		PlayMethod:             "DirectPlay",
		LiveStreamID:           "live-start",
		PlaySessionID:          "play-session",
		RepeatMode:             "RepeatAll",
		PlaybackOrder:          "Shuffle",
		PlaylistItemID:         "playlist-start",
	}); err != nil {
		t.Fatalf("start playback: %v", err)
	}

	started := sessionByID(t, manager.Sessions(session.Query{}), created.ID)
	assertCompletePlaybackState(t, started.NowPlaying, session.PlaybackState{
		CanSeek:                true,
		ItemID:                 itemID,
		MediaSourceID:          "source-start",
		AudioStreamIndex:       &startAudio,
		SubtitleStreamIndex:    &startSubtitle,
		IsMuted:                true,
		PositionTicks:          &startPosition,
		PlaybackStartTimeTicks: &startTime,
		VolumeLevel:            &startVolume,
		Brightness:             &startBrightness,
		AspectRatio:            "16:9",
		PlayMethod:             "DirectPlay",
		LiveStreamID:           "live-start",
		PlaySessionID:          "play-session",
		RepeatMode:             "RepeatAll",
		PlaybackOrder:          "Shuffle",
		PlaylistItemID:         "playlist-start",
	})
	if !started.LastPlaybackCheckIn.Equal(fake.Now()) || started.LastPausedDate != nil {
		t.Fatalf("started playback timestamps = check-in %s, paused %v; want check-in %s and no pause date", started.LastPlaybackCheckIn, started.LastPausedDate, fake.Now())
	}

	fake.Advance(17 * time.Second)
	if err := manager.ProgressPlayback(t.Context(), created.Target, session.PlaybackState{
		CanSeek:                true,
		ItemID:                 itemID,
		MediaSourceID:          "source-progress",
		AudioStreamIndex:       &progressAudio,
		SubtitleStreamIndex:    &progressSubtitle,
		IsPaused:               true,
		IsMuted:                true,
		PositionTicks:          &progressPosition,
		PlaybackStartTimeTicks: &progressStartTime,
		VolumeLevel:            &progressVolume,
		Brightness:             &progressBrightness,
		AspectRatio:            "4:3",
		PlayMethod:             "DirectStream",
		LiveStreamID:           "live-progress",
		PlaySessionID:          "play-session",
		RepeatMode:             "RepeatOne",
		PlaybackOrder:          "Shuffle",
		PlaylistItemID:         "playlist-progress",
	}); err != nil {
		t.Fatalf("report playback progress: %v", err)
	}

	progressed := sessionByID(t, manager.Sessions(session.Query{}), created.ID)
	assertCompletePlaybackState(t, progressed.NowPlaying, session.PlaybackState{
		CanSeek:                true,
		ItemID:                 itemID,
		MediaSourceID:          "source-progress",
		AudioStreamIndex:       &progressAudio,
		SubtitleStreamIndex:    &progressSubtitle,
		IsPaused:               true,
		IsMuted:                true,
		PositionTicks:          &progressPosition,
		PlaybackStartTimeTicks: &progressStartTime,
		VolumeLevel:            &progressVolume,
		Brightness:             &progressBrightness,
		AspectRatio:            "4:3",
		PlayMethod:             "DirectStream",
		LiveStreamID:           "live-progress",
		PlaySessionID:          "play-session",
		RepeatMode:             "RepeatOne",
		PlaybackOrder:          "Shuffle",
		PlaylistItemID:         "playlist-progress",
	})
	if !progressed.LastPlaybackCheckIn.Equal(fake.Now()) || progressed.LastPausedDate == nil || !progressed.LastPausedDate.Equal(fake.Now()) {
		t.Fatalf("progress playback timestamps = check-in %s, paused %v; want both %s", progressed.LastPlaybackCheckIn, progressed.LastPausedDate, fake.Now())
	}

	terminalQueue := []dto.QueueItem{
		{ID: itemID, PlaylistItemID: stringPointer("queue-current")},
		{ID: dto.UUID{0xa5, 0x00, 0x03}, PlaylistItemID: stringPointer("queue-next")},
	}
	fake.Advance(5 * time.Second)
	if err := manager.StopPlayback(t.Context(), created.Target, session.PlaybackState{
		NowPlayingQueue: &terminalQueue,
		PlaylistItemID:  "queue-current",
	}); err != nil {
		t.Fatalf("stop playback: %v", err)
	}

	stopped := sessionByID(t, manager.Sessions(session.Query{}), created.ID)
	if stopped.NowPlaying != nil {
		t.Fatalf("stopped session retains active playback: %#v", stopped.NowPlaying)
	}
	if !stopped.LastPlaybackCheckIn.Equal(progressed.LastPlaybackCheckIn) || stopped.LastPausedDate == nil || !stopped.LastPausedDate.Equal(*progressed.LastPausedDate) {
		t.Fatalf("stop changed terminal playback timestamps to check-in %s, paused %v; want progress timestamps check-in %s, paused %s", stopped.LastPlaybackCheckIn, stopped.LastPausedDate, progressed.LastPlaybackCheckIn, progressed.LastPausedDate)
	}
	if stopped.PlaylistItemID != "queue-current" || len(stopped.NowPlayingQueue) != len(terminalQueue) || stopped.NowPlayingQueue[0].ID != itemID || stopped.NowPlayingQueue[0].PlaylistItemID == nil || *stopped.NowPlayingQueue[0].PlaylistItemID != "queue-current" || stopped.NowPlayingQueue[1].ID != terminalQueue[1].ID || stopped.NowPlayingQueue[1].PlaylistItemID == nil || *stopped.NowPlayingQueue[1].PlaylistItemID != "queue-next" {
		t.Fatalf("stopped session terminal queue = %#v with playlist item %q, want %#v with playlist item %q", stopped.NowPlayingQueue, stopped.PlaylistItemID, terminalQueue, "queue-current")
	}
}

func assertCompletePlaybackState(t *testing.T, got *session.PlaybackState, want session.PlaybackState) {
	t.Helper()
	if got == nil || got.CanSeek != want.CanSeek || got.ItemID != want.ItemID || got.MediaSourceID != want.MediaSourceID || got.AudioStreamIndex == nil || *got.AudioStreamIndex != *want.AudioStreamIndex || got.SubtitleStreamIndex == nil || *got.SubtitleStreamIndex != *want.SubtitleStreamIndex || got.IsPaused != want.IsPaused || got.IsMuted != want.IsMuted || got.PositionTicks == nil || *got.PositionTicks != *want.PositionTicks || got.PlaybackStartTimeTicks == nil || *got.PlaybackStartTimeTicks != *want.PlaybackStartTimeTicks || got.VolumeLevel == nil || *got.VolumeLevel != *want.VolumeLevel || got.Brightness == nil || *got.Brightness != *want.Brightness || got.AspectRatio != want.AspectRatio || got.PlayMethod != want.PlayMethod || got.LiveStreamID != want.LiveStreamID || got.PlaySessionID != want.PlaySessionID || got.RepeatMode != want.RepeatMode || got.PlaybackOrder != want.PlaybackOrder || got.PlaylistItemID != want.PlaylistItemID {
		t.Fatalf("active playback state = %#v, want complete state %#v", got, want)
	}
}

func stringPointer(value string) *string { return &value }
