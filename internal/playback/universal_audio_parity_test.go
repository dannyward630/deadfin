// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// UniversalAudioController.GetDeviceProfile @ b04614e: a pipe-delimited
// container entry identifies its container first, then every remaining part as
// an accepted audio codec. The resulting profile must negotiate a matching
// source directly.
func TestUniversalAudioCombinedCodecEntryBuildsDirectProfileAndPermitsMP3(t *testing.T) {
	request := playback.UniversalAudioRequest{Containers: []string{"mp3|aac|mp3"}}
	profile := playback.NewUniversalAudioDeviceProfile(request)

	if len(profile.DirectPlayProfiles) != 1 {
		t.Fatalf("DirectPlayProfiles = %#v, want one combined-codec profile", profile.DirectPlayProfiles)
	}
	gotProfile := profile.DirectPlayProfiles[0]
	if gotProfile.Type != playback.DlnaAudio || gotProfile.Container != "mp3" || gotProfile.AudioCodec != "aac,mp3" {
		t.Errorf("DirectPlayProfile = %#v, want audio container mp3 with codecs aac,mp3", gotProfile)
	}

	decision, err := playback.DecideUniversalAudio(request, universalAudioSource("mp3", "mp3"), "item", nil)
	if err != nil {
		t.Fatalf("DecideUniversalAudio() error = %v", err)
	}
	if !decision.Static {
		t.Error("DecideUniversalAudio().Static = false, want direct MP3 source permitted by combined codec entry")
	}
}

// UniversalAudioController.GetDeviceProfile keeps any supplied limit, including
// zero. Zero is a real upper bound, rather than an omitted capability, so a
// source with positive values cannot be delivered directly.
func TestUniversalAudioZeroLimitsRemainConditionsAndRejectPositiveAudioFacts(t *testing.T) {
	zero := 0
	request := playback.UniversalAudioRequest{
		Containers:         []string{"mp3"},
		MaxAudioChannels:   &zero,
		MaxAudioSampleRate: &zero,
		MaxAudioBitDepth:   &zero,
	}

	t.Run("profile retains every zero limit", func(t *testing.T) {
		profile := playback.NewUniversalAudioDeviceProfile(request)
		if len(profile.CodecProfiles) != 1 {
			t.Fatalf("CodecProfiles = %#v, want one aggregate audio-limit profile", profile.CodecProfiles)
		}
		conditions := profile.CodecProfiles[0].Conditions
		want := []struct {
			property playback.ConditionProperty
		}{
			{playback.PropAudioSampleRate},
			{playback.PropAudioBitDepth},
			{playback.PropAudioChannels},
		}
		if len(conditions) != len(want) {
			t.Fatalf("audio limit conditions = %#v, want sample rate, bit depth, and channels", conditions)
		}
		for index, expected := range want {
			condition := conditions[index]
			if condition.Condition != playback.ConditionLessThanEqual || condition.Property != expected.property || condition.Value != "0" {
				t.Errorf("condition %d = %#v, want %s %s 0", index, condition, playback.ConditionLessThanEqual, expected.property)
			}
		}
	})

	t.Run("positive source values are not directly eligible", func(t *testing.T) {
		decision, err := playback.DecideUniversalAudio(request, universalAudioSource("mp3", "mp3", universalAudioPositiveFacts()), "item", nil)
		if err != nil {
			t.Fatalf("DecideUniversalAudio() error = %v", err)
		}
		if decision.Static {
			t.Error("DecideUniversalAudio().Static = true, want a positive-valued source rejected by zero limits")
		}
	})
}

// A missing audio stream is invalid input to the audio stream builder. Universal
// audio must preserve that established error rather than inventing a matching
// stream from the source container.
func TestUniversalAudioMissingAudioStreamReturnsBuilderError(t *testing.T) {
	_, err := playback.DecideUniversalAudio(
		playback.UniversalAudioRequest{Containers: []string{"mp3"}},
		playback.MediaSourceInfo{ID: "no-audio", Container: "mp3", SupportsDirectPlay: true},
		"item", nil,
	)
	if err == nil {
		t.Fatal("DecideUniversalAudio() error = nil, want audio stream is required")
	}
	if err.Error() != "audio stream is required" {
		t.Errorf("DecideUniversalAudio() error = %q, want audio stream is required", err)
	}
}

// Codec limits are a single aggregate profile in the stock request shape. Its
// container scope retains the submitted entries verbatim, including pipe codec
// suffixes, while an empty Codec applies the aggregate limits to all codecs.
func TestUniversalAudioCodecConditionsRetainAggregateContainerScope(t *testing.T) {
	limit := 2
	profile := playback.NewUniversalAudioDeviceProfile(playback.UniversalAudioRequest{
		Containers:       []string{"mp3|aac|mp3", "m4a|aac"},
		MaxAudioChannels: &limit,
	})

	if len(profile.CodecProfiles) != 1 {
		t.Fatalf("CodecProfiles = %#v, want exactly one aggregate profile", profile.CodecProfiles)
	}
	got := profile.CodecProfiles[0]
	if got.Type != playback.CodecAudio {
		t.Errorf("CodecProfile.Type = %v, want %v", got.Type, playback.CodecAudio)
	}
	if got.Codec != "" {
		t.Errorf("CodecProfile.Codec = %q, want empty aggregate codec scope", got.Codec)
	}
	if got.Container != "mp3|aac|mp3,m4a|aac" {
		t.Errorf("CodecProfile.Container = %q, want original entries joined by comma", got.Container)
	}
}

func universalAudioSource(container, codec string, facts ...playback.MediaStream) playback.MediaSourceInfo {
	streams := facts
	if len(streams) == 0 {
		streams = []playback.MediaStream{{
			Index: 0, Type: playback.StreamTypeAudio, Codec: codec, IsDefault: true,
		}}
	}
	// A source without a bitrate counts as 40 Mbps, above the 8 Mbps
	// profile default (StreamBuilder.IsBitrateLimitExceeded).
	bitrate := 320000
	return playback.MediaSourceInfo{
		ID:                   "universal-audio-source",
		Container:            container,
		Bitrate:              &bitrate,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams:         streams,
	}
}

func universalAudioPositiveFacts() playback.MediaStream {
	one := 1
	return playback.MediaStream{
		Index:      0,
		Type:       playback.StreamTypeAudio,
		Codec:      "mp3",
		IsDefault:  true,
		Channels:   &one,
		SampleRate: &one,
		BitDepth:   &one,
	}
}
