// SPDX-License-Identifier: GPL-2.0-only

package main

import "testing"

func TestNormalizedComparatorRejectsCrossStepPlaybackTimestampInversion(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	comparator := newNormalizedComparator()

	if equal, difference := comparator.Compare(
		normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody("2026-07-27T12:00:00Z")),
		normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody("2026-07-27T12:00:01Z")),
		playbackTimestampReplayTolerance,
	); !equal {
		t.Fatalf("compare first sequential playback observation: %s", difference)
	}
	if equal, difference := comparator.Compare(
		normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody("2026-07-27T12:00:01Z")),
		normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody("2026-07-27T12:00:00Z")),
		playbackTimestampReplayTolerance,
	); equal {
		t.Fatalf("second comparison accepted a reversed one-second playback chronology: %s", difference)
	}
}

func TestNormalizedComparatorAllowsUniformCrossStepPlaybackTimestampSkew(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	comparator := newNormalizedComparator()
	tests := []struct {
		name      string
		leftDate  string
		rightDate string
	}{
		{
			name:      "first observation remains within host clock skew",
			leftDate:  "2026-07-27T12:00:00Z",
			rightDate: "2026-07-27T12:00:01Z",
		},
		{
			name:      "later observation preserves the same host clock skew",
			leftDate:  "2026-07-27T12:00:01Z",
			rightDate: "2026-07-27T12:00:02Z",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if equal, difference := comparator.Compare(
				normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody(test.leftDate)),
				normalizeTimestampProvenanceResponse(t, normalizer, playbackTimestampItemBody(test.rightDate)),
				playbackTimestampReplayTolerance,
			); !equal {
				t.Fatalf("compare %q and %q: %s", test.leftDate, test.rightDate, difference)
			}
		})
	}
}

func playbackTimestampItemBody(lastPlayedDate string) string {
	return `{"Id":"replay-item","Type":"Movie","UserData":{"LastPlayedDate":"` + lastPlayedDate + `"}}`
}
