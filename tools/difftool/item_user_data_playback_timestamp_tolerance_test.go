// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"testing"
	"time"
)

// playbackTimestampReplayTolerance permits only the scheduling skew introduced
// when difftool samples the same read from its left and right hosts serially.
// Two seconds covers a clock-boundary crossing without masking the distinct
// playback observations that the replay sequence compares.
const playbackTimestampReplayTolerance = 2 * time.Second

func TestNormalizerItemUserDataPlaybackTimestampsBoundsHostClockSkew(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	tests := []struct {
		name      string
		leftDate  string
		rightDate string
		wantEqual bool
	}{
		{
			name:      "same replay observation crossing the permitted host-clock boundary matches",
			leftDate:  "2026-07-27T11:59:58Z",
			rightDate: "2026-07-27T12:00:00Z",
			wantEqual: true,
		},
		{
			name:      "stale playback timestamp one second beyond the host-clock allowance remains observable",
			leftDate:  "2026-07-27T11:59:57Z",
			rightDate: "2026-07-27T12:00:00Z",
			wantEqual: false,
		},
		{
			name:      "future playback timestamp one second beyond the host-clock allowance remains observable",
			leftDate:  "2026-07-27T12:00:00Z",
			rightDate: "2026-07-27T12:00:03Z",
			wantEqual: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left := normalizePlaybackTimestampItem(t, normalizer, test.leftDate)
			right := normalizePlaybackTimestampItem(t, normalizer, test.rightDate)

			equal, difference := equalNormalized(left, right, playbackTimestampReplayTolerance)
			if equal != test.wantEqual {
				t.Fatalf("timestamps %q and %q compare equal = %t (%s), want %t", test.leftDate, test.rightDate, equal, difference, test.wantEqual)
			}
		})
	}
}

func TestNormalizerItemUserDataPlaybackTimestampsRetainsRepeatedObservationOrder(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	left, err := normalizer.normalize(Response{Status: 200, Body: []byte(`{
		"Items":[
			{"Id":"first-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T11:59:54Z"}},
			{"Id":"second-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T12:00:00Z"}}
		]
	}`)}, nil)
	if err != nil {
		t.Fatalf("normalize chronological playback observations: %v", err)
	}
	right, err := normalizer.normalize(Response{Status: 200, Body: []byte(`{
		"Items":[
			{"Id":"first-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T12:00:00Z"}},
			{"Id":"second-observation","Type":"Movie","UserData":{"LastPlayedDate":"2026-07-27T11:59:54Z"}}
		]
	}`)}, nil)
	if err != nil {
		t.Fatalf("normalize reordered playback observations: %v", err)
	}

	if equal, difference := equalNormalized(left, right, playbackTimestampReplayTolerance); equal {
		t.Fatalf("reversed playback chronology compared equal with %s tolerance; timestamp order must remain observable", playbackTimestampReplayTolerance)
	} else if difference == "" {
		t.Fatal("reversed playback chronology reported no difference")
	}
}

func normalizePlaybackTimestampItem(t *testing.T, normalizer normalizer, lastPlayedDate string) normalizedResponse {
	t.Helper()
	normalized, err := normalizer.normalize(Response{Status: 200, Body: []byte(`{
		"Items":[{"Id":"replay-item","Type":"Movie","UserData":{"LastPlayedDate":"` + lastPlayedDate + `"}}]
	}`)}, nil)
	if err != nil {
		t.Fatalf("normalize playback timestamp %q: %v", lastPlayedDate, err)
	}
	return normalized
}
