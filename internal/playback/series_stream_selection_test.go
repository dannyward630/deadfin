// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// seriesPreviousEpisode is an anime episode with English and Japanese TrueHD
// audio and English PGS dialogue and signs/songs tracks.
func seriesPreviousEpisode() []playback.MediaStream {
	return []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "truehd", Language: sourceSelectionString("eng"), Title: sourceSelectionString("English"), Channels: sourceSelectionInt(8), IsDefault: true},
		{Index: 2, Type: playback.StreamTypeAudio, Codec: "truehd", Language: sourceSelectionString("jpn"), Title: sourceSelectionString("Japanese"), Channels: sourceSelectionInt(2)},
		{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Signs & Songs"), IsForced: true},
		{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Dialogue")},
	}
}

// seriesNextEpisode orders its streams differently and adds tracks, so the
// previous episode's indexes name different streams here.
func seriesNextEpisode() playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID: "next-episode",
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc"},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "truehd", Language: sourceSelectionString("eng"), Title: sourceSelectionString("English"), Channels: sourceSelectionInt(8), IsDefault: true},
			{Index: 2, Type: playback.StreamTypeAudio, Codec: "ac3", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Commentary"), Channels: sourceSelectionInt(2)},
			{Index: 3, Type: playback.StreamTypeAudio, Codec: "aac", Language: sourceSelectionString("jpn"), Title: sourceSelectionString("Japanese (compat)"), Channels: sourceSelectionInt(2)},
			{Index: 4, Type: playback.StreamTypeAudio, Codec: "truehd", Language: sourceSelectionString("jpn"), Title: sourceSelectionString("Japanese"), Channels: sourceSelectionInt(2)},
			{Index: 5, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Dialogue")},
			{Index: 6, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Signs & Songs"), IsForced: true},
		},
	}
}

// seriesEnglishOnlyEpisode lacks the Japanese audio chosen before.
func seriesEnglishOnlyEpisode() playback.MediaSourceInfo {
	return playback.MediaSourceInfo{
		ID: "english-only",
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc"},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "eac3", Language: sourceSelectionString("eng"), Channels: sourceSelectionInt(6), IsDefault: true},
			{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Signs & Songs"), IsForced: true},
			{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", Language: sourceSelectionString("eng"), Title: sourceSelectionString("Dialogue")},
		},
	}
}

func seriesConfiguration() dto.UserConfiguration {
	return dto.UserConfiguration{
		AudioLanguagePreference:    sourceSelectionString("eng"),
		SubtitleLanguagePreference: sourceSelectionString("eng"),
		SubtitleMode:               dto.SubtitleModeSmart,
		PlayDefaultAudioTrack:      true,
		RememberAudioSelections:    true,
		RememberSubtitleSelections: true,
	}
}

func TestApplyUserSourceSelection_SeriesSelection(t *testing.T) {
	for _, tc := range []struct {
		name          string
		source        playback.MediaSourceInfo
		configure     func(*dto.UserConfiguration)
		audio         *int
		subtitle      *int
		item          *playback.SourceUserData
		wantAudio     *int
		wantAudioFrom playback.AudioIndexSource
		wantSubtitle  *int
	}{
		{
			name:          "stock defaults without a series choice",
			source:        seriesNextEpisode(),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexLanguage | playback.AudioIndexDefault,
			wantSubtitle:  sourceSelectionInt(6),
		},
		{
			name:          "Japanese TrueHD and dialogue carry to moved streams",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(2),
			subtitle:      sourceSelectionInt(4),
			wantAudio:     sourceSelectionInt(4),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(5),
		},
		{
			name:          "signs and songs carry by title and forced flag",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(1),
			subtitle:      sourceSelectionInt(3),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(6),
		},
		{
			name:          "subtitles turned off stay off",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(1),
			subtitle:      sourceSelectionInt(-1),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(-1),
		},
		{
			// Smart mode compares the subtitle preference with the audio that
			// plays: Japanese audio needs full English subtitles, not signs only.
			name:          "carried audio drives the smart subtitle default",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(2),
			wantAudio:     sourceSelectionInt(4),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(5),
		},
		{
			name:          "missing language falls back to the stock audio",
			source:        seriesEnglishOnlyEpisode(),
			audio:         sourceSelectionInt(2),
			subtitle:      sourceSelectionInt(4),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexLanguage | playback.AudioIndexDefault,
			wantSubtitle:  sourceSelectionInt(3),
		},
		{
			name:          "remember audio off keeps the stock audio",
			source:        seriesNextEpisode(),
			configure:     func(c *dto.UserConfiguration) { c.RememberAudioSelections = false },
			audio:         sourceSelectionInt(2),
			subtitle:      sourceSelectionInt(4),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexLanguage | playback.AudioIndexDefault,
			wantSubtitle:  sourceSelectionInt(5),
		},
		{
			name:          "remember subtitles off keeps the stock subtitle",
			source:        seriesNextEpisode(),
			configure:     func(c *dto.UserConfiguration) { c.RememberSubtitleSelections = false },
			audio:         sourceSelectionInt(1),
			subtitle:      sourceSelectionInt(-1),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(6),
		},
		{
			name:          "subtitle mode none ignores the subtitle choice",
			source:        seriesNextEpisode(),
			configure:     func(c *dto.UserConfiguration) { c.SubtitleMode = dto.SubtitleModeNone },
			audio:         sourceSelectionInt(1),
			subtitle:      sourceSelectionInt(4),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexUser,
		},
		{
			name:          "the episode's own saved choice wins",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(2),
			subtitle:      sourceSelectionInt(4),
			item:          &playback.SourceUserData{AudioStreamIndex: sourceSelectionInt(2), SubtitleStreamIndex: sourceSelectionInt(6)},
			wantAudio:     sourceSelectionInt(2),
			wantAudioFrom: playback.AudioIndexUser,
			wantSubtitle:  sourceSelectionInt(6),
		},
		{
			name:          "an index absent from the previous episode is ignored",
			source:        seriesNextEpisode(),
			audio:         sourceSelectionInt(9),
			subtitle:      sourceSelectionInt(9),
			wantAudio:     sourceSelectionInt(1),
			wantAudioFrom: playback.AudioIndexLanguage | playback.AudioIndexDefault,
			wantSubtitle:  sourceSelectionInt(6),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := seriesConfiguration()
			if tc.configure != nil {
				tc.configure(&configuration)
			}
			selection := playback.SourceSelection{
				MediaKind:     playback.SourceMediaVideo,
				Configuration: configuration,
				ItemUserData:  tc.item,
			}
			if tc.audio != nil || tc.subtitle != nil {
				selection.SeriesSelection = &playback.SeriesStreamSelection{
					Streams:             seriesPreviousEpisode(),
					AudioStreamIndex:    tc.audio,
					SubtitleStreamIndex: tc.subtitle,
				}
			}
			got := playback.ApplyUserSourceSelection([]playback.MediaSourceInfo{tc.source}, selection, nil)[0]

			if !equalOptionalInt(got.DefaultAudioStreamIndex, tc.wantAudio) {
				t.Errorf("DefaultAudioStreamIndex = %v, want %v", optionalIntString(got.DefaultAudioStreamIndex), optionalIntString(tc.wantAudio))
			}
			if got.DefaultAudioIndexSource != tc.wantAudioFrom {
				t.Errorf("DefaultAudioIndexSource = %v, want %v", got.DefaultAudioIndexSource, tc.wantAudioFrom)
			}
			if !equalOptionalInt(got.DefaultSubtitleStreamIndex, tc.wantSubtitle) {
				t.Errorf("DefaultSubtitleStreamIndex = %v, want %v", optionalIntString(got.DefaultSubtitleStreamIndex), optionalIntString(tc.wantSubtitle))
			}
		})
	}
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func optionalIntString(value *int) any {
	if value == nil {
		return "nil"
	}
	return *value
}
