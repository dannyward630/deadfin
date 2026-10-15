// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestNormalizerItemUserDataPlaybackTimestampsOnlyNormalizesValidItemUserDataDates(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_user_data_playback_timestamps"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal case item DTO UserData retains sibling and lookalike timestamps",
			body: `{
				"Id":"pascal-item-id",
				"Type":"Movie",
				"LastPlayedDate":"2026-07-26T10:20:30Z",
				"UserData":{"LastPlayedDate":"2026-07-26T10:20:30.1234567Z","PlaybackPositionTicks":5000000,"Played":true,"Nested":{"LastPlayedDate":"2026-07-26T10:20:30Z"}},
				"Metadata":{"LastPlayedDate":"2026-07-26T10:20:30Z","UserData":{"LastPlayedDate":"2026-07-26T10:20:30Z"}},
				"Items":[{"Id":"nested-item-id","Type":"Episode","UserData":{"LastPlayedDate":"2026-07-26T10:20:30+02:00","PlayCount":2}}]
			}`,
			want: map[string]any{
				"Id":             "pascal-item-id",
				"Type":           "Movie",
				"LastPlayedDate": "2026-07-26T10:20:30Z",
				"UserData": map[string]any{
					"LastPlayedDate":        normalizedItemUserDataPlaybackTimestamp("<item-user-data-playback-timestamp:2026-07-26T10:20:30.1234567Z>"),
					"PlaybackPositionTicks": json.Number("5000000"),
					"Played":                true,
					"Nested":                map[string]any{"LastPlayedDate": "2026-07-26T10:20:30Z"},
				},
				"Metadata": map[string]any{
					"LastPlayedDate": "2026-07-26T10:20:30Z",
					"UserData":       map[string]any{"LastPlayedDate": "2026-07-26T10:20:30Z"},
				},
				"Items": []any{map[string]any{
					"Id":   "nested-item-id",
					"Type": "Episode",
					"UserData": map[string]any{
						"LastPlayedDate": normalizedItemUserDataPlaybackTimestamp("<item-user-data-playback-timestamp:2026-07-26T10:20:30+02:00>"),
						"PlayCount":      json.Number("2"),
					},
				}},
			},
		},
		{
			name: "lower camel case item DTO UserData retains sibling and lookalike timestamps",
			body: `{
				"id":"camel-item-id",
				"type":"Episode",
				"lastPlayedDate":"2026-07-26T10:20:30Z",
				"userData":{"lastPlayedDate":"2026-07-26T10:20:30.1234567Z","playbackPositionTicks":600000,"played":false,"nested":{"lastPlayedDate":"2026-07-26T10:20:30Z"}},
				"metadata":{"lastPlayedDate":"2026-07-26T10:20:30Z","userData":{"lastPlayedDate":"2026-07-26T10:20:30Z"}},
				"items":[{"id":"nested-item-id","type":"Movie","userData":{"lastPlayedDate":"2026-07-26T10:20:30-03:00","playCount":3}}]
			}`,
			want: map[string]any{
				"id":             "camel-item-id",
				"type":           "Episode",
				"lastPlayedDate": "2026-07-26T10:20:30Z",
				"userData": map[string]any{
					"lastPlayedDate":        normalizedItemUserDataPlaybackTimestamp("<item-user-data-playback-timestamp:2026-07-26T10:20:30.1234567Z>"),
					"playbackPositionTicks": json.Number("600000"),
					"played":                false,
					"nested":                map[string]any{"lastPlayedDate": "2026-07-26T10:20:30Z"},
				},
				"metadata": map[string]any{
					"lastPlayedDate": "2026-07-26T10:20:30Z",
					"userData":       map[string]any{"lastPlayedDate": "2026-07-26T10:20:30Z"},
				},
				"items": []any{map[string]any{
					"id":   "nested-item-id",
					"type": "Movie",
					"userData": map[string]any{
						"lastPlayedDate": normalizedItemUserDataPlaybackTimestamp("<item-user-data-playback-timestamp:2026-07-26T10:20:30-03:00>"),
						"playCount":      json.Number("3"),
					},
				}},
			},
		},
		{
			name: "missing null non-string and invalid dates remain exact",
			body: `{
				"Items":[
					{"Id":"null-item-id","Type":"Movie","UserData":{"LastPlayedDate":null,"Played":false}},
					{"id":"boolean-item-id","type":"Movie","userData":{"lastPlayedDate":true,"played":false}},
					{"Id":"number-item-id","Type":"Movie","UserData":{"LastPlayedDate":42,"Played":false}},
					{"id":"invalid-item-id","type":"Movie","userData":{"lastPlayedDate":"not-a-timestamp","played":false}},
					{"Id":"missing-item-id","Type":"Movie","UserData":{"Played":false}}
				]
			}`,
			want: map[string]any{
				"Items": []any{
					map[string]any{"Id": "null-item-id", "Type": "Movie", "UserData": map[string]any{"LastPlayedDate": nil, "Played": false}},
					map[string]any{"id": "boolean-item-id", "type": "Movie", "userData": map[string]any{"lastPlayedDate": true, "played": false}},
					map[string]any{"Id": "number-item-id", "Type": "Movie", "UserData": map[string]any{"LastPlayedDate": json.Number("42"), "Played": false}},
					map[string]any{"id": "invalid-item-id", "type": "Movie", "userData": map[string]any{"lastPlayedDate": "not-a-timestamp", "played": false}},
					map[string]any{"Id": "missing-item-id", "Type": "Movie", "UserData": map[string]any{"Played": false}},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize item DTO: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}
