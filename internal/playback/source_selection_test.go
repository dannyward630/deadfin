// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// A saved, still-valid audio choice is an explicit user choice even when the
// source arrived with the assembler's default-track provenance.
func TestApplyUserSourceSelection_RememberedAudioMarksUserProvenance(t *testing.T) {
	remembered := 2
	got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{{
		ID:                      "remembered-audio",
		DefaultAudioStreamIndex: sourceSelectionInt(1),
		DefaultAudioIndexSource: playback.AudioIndexDefault,
		MediaStreams: []playback.MediaStream{
			{Index: 1, Type: playback.StreamTypeAudio, IsDefault: true},
			{Index: 2, Type: playback.StreamTypeAudio},
		},
	}}, playback.SourceSelection{
		MediaKind: playback.SourceMediaVideo,
		Configuration: dto.UserConfiguration{
			RememberAudioSelections: true,
			SubtitleMode:            dto.SubtitleModeNone,
		},
		VersionUserData: map[string]playback.SourceUserData{
			"remembered-audio": {AudioStreamIndex: &remembered},
		},
	}, nil)

	if got[0].DefaultAudioStreamIndex == nil || *got[0].DefaultAudioStreamIndex != remembered {
		t.Fatalf("remembered DefaultAudioStreamIndex = %v, want %d", got[0].DefaultAudioStreamIndex, remembered)
	}
	if got[0].DefaultAudioIndexSource != playback.AudioIndexUser {
		t.Errorf("remembered DefaultAudioIndexSource = %v, want AudioIndexUser", got[0].DefaultAudioIndexSource)
	}
}

// Audio fallback provenance identifies whether selection followed language
// preference alone or was intentionally constrained to a source default.
func TestApplyUserSourceSelection_FallbackAudioSetsCurrentProvenance(t *testing.T) {
	english := "eng"
	for _, tc := range []struct {
		name                   string
		preferDefaultTrack     bool
		wantIndex              int
		wantSource             playback.AudioIndexSource
		languageTrackIsDefault bool
	}{
		{
			name:               "language selects non-default track and clears stale default provenance",
			preferDefaultTrack: false,
			wantIndex:          2,
			wantSource:         playback.AudioIndexLanguage,
		},
		{
			name:                   "language selects an IsDefault stream without default-track policy",
			preferDefaultTrack:     false,
			languageTrackIsDefault: true,
			wantIndex:              2,
			wantSource:             playback.AudioIndexLanguage,
		},
		{
			name:               "default fallback retains language and adds default provenance",
			preferDefaultTrack: true,
			wantIndex:          1,
			wantSource:         playback.AudioIndexLanguage | playback.AudioIndexDefault,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{{
				ID:                      "fallback-audio",
				DefaultAudioStreamIndex: sourceSelectionInt(1),
				DefaultAudioIndexSource: playback.AudioIndexDefault,
				MediaStreams: []playback.MediaStream{
					{Index: 1, Type: playback.StreamTypeAudio, Language: sourceSelectionString("fra"), IsDefault: true},
					{Index: 2, Type: playback.StreamTypeAudio, Language: sourceSelectionString("eng"), IsDefault: tc.languageTrackIsDefault},
				},
			}}, playback.SourceSelection{
				MediaKind: playback.SourceMediaVideo,
				Configuration: dto.UserConfiguration{
					AudioLanguagePreference: &english,
					PlayDefaultAudioTrack:   tc.preferDefaultTrack,
					SubtitleMode:            dto.SubtitleModeNone,
				},
			}, nil)

			if got[0].DefaultAudioStreamIndex == nil || *got[0].DefaultAudioStreamIndex != tc.wantIndex {
				t.Fatalf("DefaultAudioStreamIndex = %v, want %d", got[0].DefaultAudioStreamIndex, tc.wantIndex)
			}
			if got[0].DefaultAudioIndexSource != tc.wantSource {
				t.Errorf("DefaultAudioIndexSource = %v, want %v", got[0].DefaultAudioIndexSource, tc.wantSource)
			}
		})
	}
}

// A source whose audio streams all lack IsDefault must not set default-only
// provenance when PlayDefaultAudioTrack is active. Without this guard the
// candidate set empties and the profile skips the audio-codec decision, which
// offers direct play for a codec the client cannot decode.
func TestApplyUserSourceSelection_NoDefaultAudioDoesNotSetDefaultProvenance(t *testing.T) {
	english := "eng"
	for _, tc := range []struct {
		name               string
		preferDefaultTrack bool
		wantSource         playback.AudioIndexSource
		wantIndex          int
	}{
		{
			name:               "no default stream with default-track policy keeps language provenance only",
			preferDefaultTrack: true,
			wantSource:         playback.AudioIndexLanguage,
			wantIndex:          2,
		},
		{
			name:               "no default stream without default-track policy keeps language provenance only",
			preferDefaultTrack: false,
			wantSource:         playback.AudioIndexLanguage,
			wantIndex:          2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{{
				ID:                      "no-default-audio",
				DefaultAudioStreamIndex: sourceSelectionInt(1),
				DefaultAudioIndexSource: playback.AudioIndexDefault,
				MediaStreams: []playback.MediaStream{
					{Index: 1, Type: playback.StreamTypeAudio, Language: sourceSelectionString("fra"), IsDefault: false},
					{Index: 2, Type: playback.StreamTypeAudio, Language: sourceSelectionString("eng"), IsDefault: false},
				},
			}}, playback.SourceSelection{
				MediaKind: playback.SourceMediaVideo,
				Configuration: dto.UserConfiguration{
					AudioLanguagePreference: &english,
					PlayDefaultAudioTrack:   tc.preferDefaultTrack,
					SubtitleMode:            dto.SubtitleModeNone,
				},
			}, nil)

			if got[0].DefaultAudioStreamIndex == nil || *got[0].DefaultAudioStreamIndex != tc.wantIndex {
				t.Fatalf("DefaultAudioStreamIndex = %v, want %d", got[0].DefaultAudioStreamIndex, tc.wantIndex)
			}
			if got[0].DefaultAudioIndexSource != tc.wantSource {
				t.Errorf("DefaultAudioIndexSource = %v, want %v", got[0].DefaultAudioIndexSource, tc.wantSource)
			}
		})
	}
}

// A source whose selected audio stream carries IsDefault must keep default
// provenance, so the candidate set stays constrained to default streams.
func TestApplyUserSourceSelection_ActualDefaultAudioKeepsDefaultProvenance(t *testing.T) {
	got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{{
		ID:                      "actual-default-audio",
		DefaultAudioStreamIndex: sourceSelectionInt(1),
		DefaultAudioIndexSource: playback.AudioIndexNone,
		MediaStreams: []playback.MediaStream{
			{Index: 1, Type: playback.StreamTypeAudio, IsDefault: true},
			{Index: 2, Type: playback.StreamTypeAudio, IsDefault: false},
		},
	}}, playback.SourceSelection{
		MediaKind: playback.SourceMediaVideo,
		Configuration: dto.UserConfiguration{
			PlayDefaultAudioTrack: true,
			SubtitleMode:          dto.SubtitleModeNone,
		},
	}, nil)

	if got[0].DefaultAudioStreamIndex == nil || *got[0].DefaultAudioStreamIndex != 1 {
		t.Fatalf("DefaultAudioStreamIndex = %v, want 1", got[0].DefaultAudioStreamIndex)
	}
	if got[0].DefaultAudioIndexSource != playback.AudioIndexDefault {
		t.Errorf("DefaultAudioIndexSource = %v, want AudioIndexDefault", got[0].DefaultAudioIndexSource)
	}
}

// Each user's subtitle fallback must refresh the scores supplied to playback
// capability selection. These cases preserve unrelated static scores while
// setting the exact user-ranked score for the mode's eligible subtitle.
func TestApplyUserSourceSelection_UserSubtitleFallbackRefreshesScores(t *testing.T) {
	english, french := "eng", "fra"
	for _, tc := range []struct {
		name              string
		preference        *string
		mode              dto.SubtitleMode
		wantSubtitleIndex int
		wantScores        map[int]*int
	}{
		{
			name:              "English always scores every matching non-forced subtitle for the current user",
			preference:        &english,
			mode:              dto.SubtitleModeAlways,
			wantSubtitleIndex: 11,
			wantScores: map[int]*int{
				10: sourceSelectionInt(112221),
				11: sourceSelectionInt(10111221),
				12: sourceSelectionInt(121221),
			},
		},
		{
			name:              "French smart scores the preferred subtitle for a different user",
			preference:        &french,
			mode:              dto.SubtitleModeSmart,
			wantSubtitleIndex: 10,
			wantScores: map[int]*int{
				10: sourceSelectionInt(10112221),
				11: nil,
				12: sourceSelectionInt(121221),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{sourceSelectionScoredSubtitleSource()}, playback.SourceSelection{
				MediaKind: playback.SourceMediaVideo,
				Configuration: dto.UserConfiguration{
					SubtitleLanguagePreference: tc.preference,
					SubtitleMode:               tc.mode,
				},
			}, nil)

			if got[0].DefaultSubtitleStreamIndex == nil || *got[0].DefaultSubtitleStreamIndex != tc.wantSubtitleIndex {
				t.Fatalf("DefaultSubtitleStreamIndex = %v, want %d", got[0].DefaultSubtitleStreamIndex, tc.wantSubtitleIndex)
			}
			for _, stream := range got[0].MediaStreams {
				if stream.Type != playback.StreamTypeSubtitle {
					continue
				}
				want, found := tc.wantScores[stream.Index]
				if !found {
					t.Errorf("subtitle stream index %d has an unexpected score %v", stream.Index, stream.Score)
					continue
				}
				if want == nil {
					if stream.Score != nil {
						t.Errorf("subtitle stream index %d Score = %d, want omitted", stream.Index, *stream.Score)
					}
					continue
				}
				if stream.Score == nil || *stream.Score != *want {
					t.Errorf("subtitle stream index %d Score = %v, want %d", stream.Index, stream.Score, *want)
				}
			}
		})
	}
}

func sourceSelectionScoredSubtitleSource() playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID: "scored-subtitles",
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeAudio, Language: sourceSelectionString("eng"), IsDefault: true},
			{Index: 10, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: sourceSelectionString("fra"), IsDefault: true, SupportsExternalStream: true, Score: sourceSelectionInt(112221)},
			{Index: 11, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: sourceSelectionString("eng"), SupportsExternalStream: true},
			{Index: 12, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: sourceSelectionString("eng"), IsForced: true, SupportsExternalStream: true, Score: sourceSelectionInt(121221)},
		},
	}
}

func sourceSelectionInt(value int) *int          { return &value }
func sourceSelectionString(value string) *string { return &value }
