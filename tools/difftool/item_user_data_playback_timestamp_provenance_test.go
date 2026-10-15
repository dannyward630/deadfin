// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizerItemUserDataPlaybackTimestampsKeepsSyntheticTagShapedNamesExact(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	left := normalizeTimestampProvenanceResponse(t, normalizer, `{"Name":"<item-user-data-playback-timestamp:2026-07-27T12:00:00Z>"}`)
	right := normalizeTimestampProvenanceResponse(t, normalizer, `{"Name":"<item-user-data-playback-timestamp:2026-07-27T12:00:01Z>"}`)

	for side, normalized := range map[string]normalizedResponse{"left": left, "right": right} {
		name, ok := normalized.Body.(map[string]any)["Name"].(string)
		if !ok || !strings.HasPrefix(name, itemUserDataPlaybackTimestampTagPrefix) || !strings.HasSuffix(name, itemUserDataPlaybackTimestampTagSuffix) {
			t.Fatalf("%s synthetic-tag-shaped Name = %#v, want unchanged ordinary string", side, normalized.Body.(map[string]any)["Name"])
		}
	}

	if equal, difference := equalNormalized(left, right, playbackTimestampReplayTolerance); equal {
		t.Fatalf("distinct ordinary synthetic-tag-shaped names compared equal with %s tolerance: %s", playbackTimestampReplayTolerance, difference)
	}
}

func TestNormalizerItemUserDataPlaybackTimestampsRetainsProvenanceBeyondReadableTag(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	const readableTag = "<item-user-data-playback-timestamp:2026-07-27T12:00:00Z>"

	normalizedTimestamp := normalizeTimestampProvenanceResponse(t, normalizer, `{
		"Id":"item","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T12:00:00Z"}
	}`)
	ordinaryTag := normalizeTimestampProvenanceResponse(t, normalizer, `{
		"Id":"item","Type":"Movie","UserData":{"LastPlayedDate":"<item-user-data-playback-timestamp:2026-07-27T12:00:00Z>"}
	}`)

	for side, normalized := range map[string]normalizedResponse{"normalized timestamp": normalizedTimestamp, "ordinary tag": ordinaryTag} {
		encoded, err := json.Marshal(normalized.Body)
		if err != nil {
			t.Fatalf("serialize %s: %v", side, err)
		}
		var serialized map[string]any
		if err := json.Unmarshal(encoded, &serialized); err != nil {
			t.Fatalf("decode serialized %s: %v", side, err)
		}
		lastPlayedDate, ok := serialized["UserData"].(map[string]any)["LastPlayedDate"].(string)
		if !ok || lastPlayedDate != readableTag {
			t.Fatalf("serialized %s LastPlayedDate = %#v, want readable tag %q", side, serialized["UserData"].(map[string]any)["LastPlayedDate"], readableTag)
		}
	}

	if equal, difference := equalNormalized(normalizedTimestamp, ordinaryTag, playbackTimestampReplayTolerance); equal {
		t.Fatalf("normalized timestamp and ordinary tag compared equal despite distinct provenance: %s", difference)
	}
}

func TestNormalizerItemUserDataPlaybackTimestampsRejectsOneSecondReversedObservations(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	left := normalizeTimestampProvenanceResponse(t, normalizer, `{
		"Items":[
			{"Id":"first-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T11:59:59Z"}},
			{"Id":"second-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T12:00:00Z"}}
		]
	}`)
	right := normalizeTimestampProvenanceResponse(t, normalizer, `{
		"Items":[
			{"Id":"first-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T12:00:00Z"}},
			{"Id":"second-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T11:59:59Z"}}
		]
	}`)

	if equal, difference := equalNormalized(left, right, playbackTimestampReplayTolerance); equal {
		t.Fatalf("one-second reversed playback observations compared equal with %s tolerance: %s", playbackTimestampReplayTolerance, difference)
	}
}

func normalizeTimestampProvenanceResponse(t *testing.T, normalizer normalizer, body string) normalizedResponse {
	t.Helper()
	normalized, err := normalizer.normalize(Response{Status: 200, Body: []byte(body)}, nil)
	if err != nil {
		t.Fatalf("normalize timestamp provenance fixture: %v", err)
	}
	return normalized
}
