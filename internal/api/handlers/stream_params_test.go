// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestDecodeStreamParamsPortsEveryLegacySlot pins the positional layout of
// StreamingHelpers.ParseParams (Jellyfin 10.10.7, lines 397-609). The decoder
// is deliberately pure: routes bind explicit query fields first, then pass
// that result here so a nonblank packed slot replaces the corresponding query
// value exactly as the upstream controller does.
func TestDecodeStreamParamsPortsEveryLegacySlot(t *testing.T) {
	slots := []string{
		"obsolete-device-profile", // 0: DeviceProfileId is obsolete
		"device-D",                // 1: DeviceId
		"source-N",                // 2: MediaSourceId
		"TrUe",                    // 3: Static
		"h264,hevc|av1",           // 4: VideoCodec
		"aac,opus",                // 5: AudioCodec
		"-2",                      // 6: AudioStreamIndex
		"-1",                      // 7: SubtitleStreamIndex
		"-3000000",                // 8: VideoBitRate
		"-192000",                 // 9: AudioBitRate
		"-2",                      // 10: MaxAudioChannels
		"-23.976",                 // 11: MaxFramerate
		"-1920",                   // 12: MaxWidth
		"-1080",                   // 13: MaxHeight
		"-9223372036854775808",    // 14: StartTimeTicks
		"-4.1",                    // 15: Level
		"-16",                     // 16: MaxRefFrames
		"-10",                     // 17: MaxVideoBitDepth
		"high10",                  // 18: Profile
		"obsolete-cabac",          // 19: CABAC is obsolete
		"session-P",               // 20: PlaySessionId
		"legacy-api-key",          // 21: api_key is obsolete
		"live-B",                  // 22: LiveStreamId
		"duplicate-item-id",       // 23: duplicate ItemId is obsolete
		"TRUE",                    // 24: CopyTimestamps
		"Embed",                   // 25: SubtitleMethod
		"-6",                      // 26: TranscodingMaxAudioChannels
		"true",                    // 27: EnableSubtitlesInManifest
		"opaque-tag",              // 28: Tag
		"true",                    // 29: RequireAvc
		"webvtt,ass",              // 30: SubtitleCodec
		"true",                    // 31: RequireNonAnamorphic
		"true",                    // 32: DeInterlace
		"VideoCodecNotSupported",  // 33: TranscodeReasons
	}

	got, err := handlers.DecodeStreamParams(strings.Join(slots, ";"), true, handlers.StreamParams{})
	if err != nil {
		t.Fatalf("DecodeStreamParams full slot table: %v", err)
	}

	if got.DeviceID == nil || *got.DeviceID != "device-D" || got.MediaSourceID == nil || *got.MediaSourceID != "source-N" {
		t.Errorf("identity slots = device:%v source:%v, want device-D/source-N", got.DeviceID, got.MediaSourceID)
	}
	if got.Static == nil || !*got.Static {
		t.Errorf("slot 3 Static = %v, want true from case-insensitive literal", got.Static)
	}
	if got.VideoCodec == nil || *got.VideoCodec != "h264,hevc|av1" || got.AudioCodec == nil || *got.AudioCodec != "aac,opus" || got.SubtitleCodec == nil || *got.SubtitleCodec != "webvtt,ass" {
		t.Errorf("codec CSV slots = video:%v audio:%v subtitle:%v, want exact accepted codec lists", got.VideoCodec, got.AudioCodec, got.SubtitleCodec)
	}
	assertStreamParamsInt(t, "AudioStreamIndex", got.AudioStreamIndex, -2)
	assertStreamParamsInt(t, "SubtitleStreamIndex", got.SubtitleStreamIndex, -1)
	assertStreamParamsInt(t, "VideoBitrate", got.VideoBitrate, -3_000_000)
	assertStreamParamsInt(t, "AudioBitrate", got.AudioBitrate, -192_000)
	assertStreamParamsInt(t, "MaxAudioChannels", got.MaxAudioChannels, -2)
	if got.MaxFramerate == nil || *got.MaxFramerate != -23.976 {
		t.Errorf("MaxFramerate = %v, want -23.976", got.MaxFramerate)
	}
	assertStreamParamsInt(t, "MaxWidth", got.MaxWidth, -1920)
	assertStreamParamsInt(t, "MaxHeight", got.MaxHeight, -1080)
	if got.StartTimeTicks == nil || *got.StartTimeTicks != -9_223_372_036_854_775_808 {
		t.Errorf("StartTimeTicks = %v, want signed int64 minimum", got.StartTimeTicks)
	}
	if got.Level == nil || *got.Level != "-4.1" || got.Profile == nil || *got.Profile != "high10" {
		t.Errorf("level/profile slots = level:%v profile:%v, want -4.1/high10", got.Level, got.Profile)
	}
	assertStreamParamsInt(t, "MaxRefFrames", got.MaxRefFrames, -16)
	assertStreamParamsInt(t, "MaxVideoBitDepth", got.MaxVideoBitDepth, -10)
	if got.PlaySessionID == nil || *got.PlaySessionID != "session-P" || got.LiveStreamID == nil || *got.LiveStreamID != "live-B" || got.Tag == nil || *got.Tag != "opaque-tag" || got.TranscodeReasons == nil || *got.TranscodeReasons != "VideoCodecNotSupported" {
		t.Errorf("string slots = session:%v live:%v tag:%v reasons:%v, want packed values", got.PlaySessionID, got.LiveStreamID, got.Tag, got.TranscodeReasons)
	}
	if got.CopyTimestamps == nil || !*got.CopyTimestamps || got.EnableSubtitlesInManifest == nil || !*got.EnableSubtitlesInManifest || got.RequireAVC == nil || !*got.RequireAVC || got.RequireNonAnamorphic == nil || !*got.RequireNonAnamorphic || got.Deinterlace == nil || !*got.Deinterlace {
		t.Errorf("boolean slots = copy:%v manifest:%v avc:%v nonAnamorphic:%v deinterlace:%v, want all true", got.CopyTimestamps, got.EnableSubtitlesInManifest, got.RequireAVC, got.RequireNonAnamorphic, got.Deinterlace)
	}
	if got.SubtitleMethod == nil || *got.SubtitleMethod != playback.SubtitleEmbed {
		t.Errorf("SubtitleMethod = %v, want Embed", got.SubtitleMethod)
	}
	assertStreamParamsInt(t, "TranscodingMaxAudioChannels", got.TranscodingMaxAudioChannels, -6)
}

// TestDecodeStreamParamsPreservesDelimiterAndQueryPrecedence proves that
// empty, whitespace-only, and trailing packed slots do not erase already-bound
// query data, while a nonblank packed slot wins. This is the ParseParams loop's
// IsNullOrWhiteSpace guard rather than a new precedence rule.
func TestDecodeStreamParamsPreservesDelimiterAndQueryPrecedence(t *testing.T) {
	explicitStatic, explicitCopy, explicitManifest := true, true, true
	existing := handlers.StreamParams{
		DeviceID:                  streamParamsString("query-device"),
		MediaSourceID:             streamParamsString("query-source"),
		Static:                    &explicitStatic,
		AudioCodec:                streamParamsString("query-audio"),
		AudioBitrate:              streamParamsInt(64_000),
		CopyTimestamps:            &explicitCopy,
		EnableSubtitlesInManifest: &explicitManifest,
		PlaySessionID:             streamParamsString("query-session"),
	}
	// Slots 1, 3, 5, 9, 20, 24, and 27 are blank/whitespace and must retain
	// explicit values. Slot 2 deliberately replaces the explicit source. The
	// final two delimiters prove trailing empty slots are not synthetic values.
	packed := "; ;packed-source; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ; ;"
	got, err := handlers.DecodeStreamParams(packed, true, existing)
	if err != nil {
		t.Fatalf("DecodeStreamParams empty/trailing slots: %v", err)
	}
	if got.DeviceID == nil || *got.DeviceID != "query-device" || got.MediaSourceID == nil || *got.MediaSourceID != "packed-source" || got.Static == nil || !*got.Static || got.AudioCodec == nil || *got.AudioCodec != "query-audio" || got.AudioBitrate == nil || *got.AudioBitrate != 64_000 || got.PlaySessionID == nil || *got.PlaySessionID != "query-session" || got.CopyTimestamps == nil || !*got.CopyTimestamps || got.EnableSubtitlesInManifest == nil || !*got.EnableSubtitlesInManifest {
		t.Errorf("empty/trailing slots overwrote explicit values: %#v", got)
	}
}

// TestDecodeStreamParamsUsesUpstreamLiteralBooleansAndVideoApplicability
// guards two non-obvious source behaviors: nonempty text means false unless it
// is exactly 'true' ignoring case, and video-only slots are ignored for an
// audio StreamingRequestDto even when their text is malformed.
func TestDecodeStreamParamsUsesUpstreamLiteralBooleansAndVideoApplicability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slot   int
		packed string
		video  bool
		check  func(t *testing.T, got handlers.StreamParams)
	}{
		{
			name: "static whitespace is false rather than trimmed true",
			slot: 3, packed: " true ",
			check: func(t *testing.T, got handlers.StreamParams) {
				t.Helper()
				if got.Static == nil || *got.Static {
					t.Errorf("Static = %v, want literal false", got.Static)
				}
			},
		},
		{
			name: "copy timestamps false literal replaces true",
			slot: 24, packed: "FALSE", video: true,
			check: func(t *testing.T, got handlers.StreamParams) {
				t.Helper()
				if got.CopyTimestamps == nil || *got.CopyTimestamps {
					t.Errorf("CopyTimestamps = %v, want false", got.CopyTimestamps)
				}
			},
		},
		{
			name: "unknown truthy text is false",
			slot: 29, packed: "1", video: true,
			check: func(t *testing.T, got handlers.StreamParams) {
				t.Helper()
				if got.RequireAVC == nil || *got.RequireAVC {
					t.Errorf("RequireAVC = %v, want false", got.RequireAVC)
				}
			},
		},
		{
			name: "audio request ignores malformed video bitrate slot",
			slot: 8, packed: "not-an-int",
			check: func(t *testing.T, got handlers.StreamParams) {
				t.Helper()
				if got.VideoBitrate != nil {
					t.Errorf("audio request VideoBitrate = %v, want untouched nil", got.VideoBitrate)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slots := make([]string, tc.slot+1)
			slots[tc.slot] = tc.packed
			got, err := handlers.DecodeStreamParams(strings.Join(slots, ";"), tc.video, handlers.StreamParams{})
			if err != nil {
				t.Fatalf("DecodeStreamParams video=%t slot %d: %v", tc.video, tc.slot, err)
			}
			tc.check(t, got)
		})
	}
}

// TestDecodeStreamParamsRejectsMalformedNumbers checks the source's int.Parse,
// long.Parse, and float.Parse boundary. ParseParams throws before an HTTP
// response is selected, and no stock HTTP capture records that exception's
// middleware mapping; these intentionally assert only the pure error until a
// route-status capture exists.
func TestDecodeStreamParamsRejectsMalformedNumbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		slot int
		text string
	}{
		{name: "audio bitrate", slot: 9, text: "192k"},
		{name: "maximum audio channels", slot: 10, text: "two"},
		{name: "maximum framerate", slot: 11, text: "23,976"},
		{name: "start ticks", slot: 14, text: "9223372036854775808"},
		{name: "video width", slot: 12, text: "wide"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slots := make([]string, tc.slot+1)
			slots[tc.slot] = tc.text
			if _, err := handlers.DecodeStreamParams(strings.Join(slots, ";"), true, handlers.StreamParams{}); err == nil {
				t.Errorf("DecodeStreamParams slot %d text %q succeeded, want pure parse error", tc.slot, tc.text)
			}
		})
	}
}

// TestDecodeStreamParamsIgnoresInvalidCodecAndEnumSlots reflects the source's
// conditional assignments: codecs, level, profile, and subtitle methods fail
// validation without replacing an already-bound explicit query value. Extra
// slots past 33 are likewise ignored by the switch rather than rejected.
func TestDecodeStreamParamsIgnoresInvalidCodecAndEnumSlots(t *testing.T) {
	explicitVideo := streamParamsString("query-video")
	explicitAudio := streamParamsString("query-audio")
	explicitLevel := streamParamsString("4.0")
	explicitProfile := streamParamsString("main")
	explicitSubtitleCodec := streamParamsString("srt")
	explicitSubtitleMethod := playback.SubtitleExternal
	existing := handlers.StreamParams{
		VideoCodec:     explicitVideo,
		AudioCodec:     explicitAudio,
		Level:          explicitLevel,
		Profile:        explicitProfile,
		SubtitleCodec:  explicitSubtitleCodec,
		SubtitleMethod: &explicitSubtitleMethod,
	}
	slots := make([]string, 35)
	slots[4] = strings.Repeat("v", 41) // violates upstream codec regex length
	slots[5] = "aac!"                  // violates upstream codec regex alphabet
	slots[15] = "4.1 level"            // violates upstream level regex
	slots[18] = "high profile"         // violates upstream codec regex alphabet
	slots[25] = "embed"                // Enum.TryParse is case-sensitive here
	slots[30] = "ass!"                 // violates upstream codec regex alphabet
	slots[34] = "future-slot-must-ignore"

	got, err := handlers.DecodeStreamParams(strings.Join(slots, ";"), true, existing)
	if err != nil {
		t.Fatalf("DecodeStreamParams ignored validation slots: %v", err)
	}
	if got.VideoCodec == nil || *got.VideoCodec != "query-video" || got.AudioCodec == nil || *got.AudioCodec != "query-audio" || got.Level == nil || *got.Level != "4.0" || got.Profile == nil || *got.Profile != "main" || got.SubtitleCodec == nil || *got.SubtitleCodec != "srt" || got.SubtitleMethod == nil || *got.SubtitleMethod != playback.SubtitleExternal {
		t.Errorf("ignored validation slots replaced explicit values: %#v", got)
	}
}

func assertStreamParamsInt(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %d", name, got, want)
	}
}

func streamParamsInt(value int) *int {
	return &value
}

func streamParamsString(value string) *string {
	return &value
}
