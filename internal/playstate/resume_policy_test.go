// SPDX-License-Identifier: GPL-2.0-only

package playstate_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/playstate"
)

const resumeTicksPerSecond int64 = 10_000_000

// Source: the pinned Phase 5 resume contract. ApplyResumePolicy is deliberately
// pure: the application service supplies media facts and explicit settings, then
// persists this result without duplicating threshold rules.
func TestApplyResumePolicy_OrdinaryMediaThresholdBoundaries(t *testing.T) {
	settings := playstate.ResumeSettings{
		MinResumePct:             12,
		MaxResumePct:             83,
		MinResumeDurationSeconds: 401,
		MinAudiobookResume:       6,
		MaxAudiobookResume:       7,
	}

	for _, tc := range []struct {
		name     string
		settings playstate.ResumeSettings
		input    playstate.ResumeInput
		want     playstate.ResumeResult
	}{
		{
			name:     "positive position below minimum percentage clears without marking played",
			settings: settings,
			input:    ordinaryResumeInput(119, 1_000),
			want:     playstate.ResumeResult{PositionTicks: 0, Played: false, PlayedToCompletion: false},
		},
		{
			name:     "position exactly at minimum percentage remains resumable",
			settings: settings,
			input:    ordinaryResumeInput(120, 1_000),
			want:     playstate.ResumeResult{PositionTicks: ticks(120), Played: false, PlayedToCompletion: false},
		},
		{
			name:     "position above minimum percentage remains resumable",
			settings: settings,
			input:    ordinaryResumeInput(121, 1_000),
			want:     playstate.ResumeResult{PositionTicks: ticks(121), Played: false, PlayedToCompletion: false},
		},
		{
			name:     "position below maximum percentage remains resumable",
			settings: settings,
			input:    ordinaryResumeInput(829, 1_000),
			want:     playstate.ResumeResult{PositionTicks: ticks(829), Played: false, PlayedToCompletion: false},
		},
		{
			name:     "position exactly at maximum percentage remains resumable",
			settings: settings,
			input:    ordinaryResumeInput(830, 1_000),
			want:     playstate.ResumeResult{PositionTicks: ticks(830), Played: false, PlayedToCompletion: false},
		},
		{
			name:     "position above maximum percentage completes playback",
			settings: settings,
			input:    ordinaryResumeInput(831, 1_000),
			want:     playstate.ResumeResult{PositionTicks: 0, Played: true, PlayedToCompletion: true},
		},
		{
			name:     "runtime below minimum duration completes playback",
			settings: settings,
			input:    ordinaryResumeInput(200, 400),
			want:     playstate.ResumeResult{PositionTicks: 0, Played: true, PlayedToCompletion: true},
		},
		{
			name:     "runtime exactly at minimum duration remains resumable",
			settings: settings,
			input:    ordinaryResumeInput(200, 401),
			want:     playstate.ResumeResult{PositionTicks: ticks(200), Played: false, PlayedToCompletion: false},
		},
		{
			name: "one second from end completes even when percentage maximum permits it",
			settings: playstate.ResumeSettings{
				MinResumePct:             12,
				MaxResumePct:             100,
				MinResumeDurationSeconds: 401,
				MinAudiobookResume:       6,
				MaxAudiobookResume:       7,
			},
			input: ordinaryResumeInput(999, 1_000),
			want:  playstate.ResumeResult{PositionTicks: 0, Played: true, PlayedToCompletion: true},
		},
		{
			name:     "zero position stays unplayed",
			settings: settings,
			input:    ordinaryResumeInput(0, 1_000),
			want:     playstate.ResumeResult{PositionTicks: 0, Played: false, PlayedToCompletion: false},
		},
		{
			name:     "negative reported progress bypasses known-runtime threshold policy",
			settings: settings,
			input:    ordinaryResumeInput(-1, 1_000),
			want:     playstate.ResumeResult{PositionTicks: ticks(-1), Played: false, PlayedToCompletion: false},
		},
		{
			name:     "unknown runtime completes positive position",
			settings: settings,
			input:    ordinaryResumeInput(999, 0),
			want:     playstate.ResumeResult{PositionTicks: 0, Played: true, PlayedToCompletion: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := playstate.ApplyResumePolicy(tc.settings, tc.input, false)
			if got != tc.want {
				t.Errorf("ApplyResumePolicy() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Source: upstream/jellyfin/Emby.Server.Implementations/Library/UserDataManager.cs:448-508.
// Books bypass ordinary percentage and duration thresholds; audiobooks use
// elapsed and remaining minute thresholds instead. Capability gates apply after
// either media-specific policy.
func TestApplyResumePolicy_BookAndAudiobookRulesAndCapabilityGates(t *testing.T) {
	settings := playstate.ResumeSettings{
		MinResumePct:             12,
		MaxResumePct:             83,
		MinResumeDurationSeconds: 401,
		MinAudiobookResume:       6,
		MaxAudiobookResume:       7,
	}

	for _, tc := range []struct {
		name  string
		input playstate.ResumeInput
		want  playstate.ResumeResult
	}{
		{
			name:  "book retains early position despite ordinary percentage and duration limits",
			input: bookResumeInput(1, 400, true, true),
			want:  playstate.ResumeResult{PositionTicks: ticks(1), Played: false, PlayedToCompletion: false},
		},
		{
			name:  "book without played status support clears position and remains unplayed",
			input: bookResumeInput(1, 400, false, true),
			want:  playstate.ResumeResult{PositionTicks: 0, Played: false, PlayedToCompletion: false},
		},
		{
			name:  "book without position resume support clears position",
			input: bookResumeInput(1, 400, true, false),
			want:  playstate.ResumeResult{PositionTicks: 0, Played: false, PlayedToCompletion: false},
		},
		{
			name:  "audiobook elapsed just below minimum clears without marking played",
			input: audiobookResumeInput(359, 1_200),
			want:  playstate.ResumeResult{PositionTicks: 0, Played: false, PlayedToCompletion: false},
		},
		{
			name:  "audiobook elapsed exactly at minimum remains resumable",
			input: audiobookResumeInput(360, 1_200),
			want:  playstate.ResumeResult{PositionTicks: ticks(360), Played: false, PlayedToCompletion: false},
		},
		{
			name:  "audiobook remaining exactly at maximum remains resumable",
			input: audiobookResumeInput(780, 1_200),
			want:  playstate.ResumeResult{PositionTicks: ticks(780), Played: false, PlayedToCompletion: false},
		},
		{
			name:  "audiobook remaining just below maximum completes playback",
			input: audiobookResumeInput(781, 1_200),
			want:  playstate.ResumeResult{PositionTicks: 0, Played: true, PlayedToCompletion: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := playstate.ApplyResumePolicy(settings, tc.input, false)
			if got != tc.want {
				t.Errorf("ApplyResumePolicy() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func ordinaryResumeInput(positionSeconds, runtimeSeconds int64) playstate.ResumeInput {
	input := playstate.ResumeInput{
		PositionTicks:               int64Pointer(ticks(positionSeconds)),
		IsAudiobook:                 false,
		SupportsPlayedStatus:        true,
		SupportsPositionTicksResume: true,
	}
	if runtimeSeconds > 0 {
		input.RunTimeTicks = int64Pointer(ticks(runtimeSeconds))
	}
	return input
}

func bookResumeInput(positionSeconds, runtimeSeconds int64, supportsPlayedStatus, supportsPositionTicksResume bool) playstate.ResumeInput {
	input := ordinaryResumeInput(positionSeconds, runtimeSeconds)
	input.IsBook = true
	input.SupportsPlayedStatus = supportsPlayedStatus
	input.SupportsPositionTicksResume = supportsPositionTicksResume
	return input
}

func audiobookResumeInput(positionSeconds, runtimeSeconds int64) playstate.ResumeInput {
	input := ordinaryResumeInput(positionSeconds, runtimeSeconds)
	input.IsAudiobook = true
	return input
}

func ticks(seconds int64) int64 {
	return seconds * resumeTicksPerSecond
}

func int64Pointer(value int64) *int64 {
	return &value
}
