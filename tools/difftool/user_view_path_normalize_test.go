// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"reflect"
	"testing"
)

// TestNormalizerEquatesOnlyDirectConfiguredUserViewPaths proves that the
// explicit legacy view rule treats the configured storage root as environmental,
// without broadening path normalization beyond a direct UserViews row.
func TestNormalizerEquatesOnlyDirectConfiguredUserViewPaths(t *testing.T) {
	tests := []struct {
		name      string
		leftBody  string
		rightBody string
		pathKey   string
	}{
		{
			name:      "Pascal direct UserViews row",
			leftBody:  `{"Items":[{"Id":"stock-view","Name":"Golden Movies","CollectionType":"movies","Path":"/config/data/root/default/Golden%20Movies"}]}`,
			rightBody: `{"Items":[{"Id":"deadfin-view","Name":"Golden Movies","CollectionType":"movies","Path":"/state/root/default/Golden%20Movies"}]}`,
			pathKey:   "Path",
		},
		{
			name:      "Camel direct UserViews row",
			leftBody:  `{"items":[{"id":"stock-view","name":"Golden Movies","collectionType":"movies","path":"/config/data/root/default/Golden%20Movies"}]}`,
			rightBody: `{"items":[{"id":"deadfin-view","name":"Golden Movies","collectionType":"movies","path":"/state/root/default/Golden%20Movies"}]}`,
			pathKey:   "path",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizer := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}})
			left, err := normalizer.normalize(Response{Status: 200, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize stock UserViews response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: 200, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize Deadfin UserViews response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); !equal {
				t.Fatalf("direct configured UserView root paths must compare equal: %s", reason)
			}

			body := left.Body.(map[string]any)
			items, _ := uniqueCaseInsensitiveField(body, "Items")
			view := items.([]any)[0].(map[string]any)
			if got, want := view[test.pathKey], "<user-view-id:golden movies|movies>"; got != want {
				t.Fatalf("normalized direct UserView %s = %#v, want %q", test.pathKey, got, want)
			}
		})
	}
}

// TestNormalizerPreservesNonDirectUserViewPaths prevents the narrow legacy
// rule from treating arbitrary data, nested DTOs, media source paths, or a
// mismatched basename as configured UserViews state.
func TestNormalizerPreservesNonDirectUserViewPaths(t *testing.T) {
	body := `{
		"Items":[{
			"Id":"view-id","Name":"Golden Movies","CollectionType":"movies",
			"Path":"/arbitrary/library/Golden%20Movies",
			"Nested":{"Path":"/config/data/root/default/Golden%20Movies"},
			"MediaSources":[{"Path":"/state/root/default/Golden%20Movies"}]
		},{
			"Id":"mismatch-id","Name":"Golden Movies","CollectionType":"movies",
			"Path":"/config/data/root/default/Another%20Directory"
		}]}`

	normalized, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: 200, Body: []byte(body)}, nil)
	if err != nil {
		t.Fatalf("normalize UserViews response: %v", err)
	}

	want := map[string]any{
		"Items": []any{
			map[string]any{
				"Id": "<user-view-id:golden movies|movies>", "Name": "Golden Movies", "CollectionType": "movies",
				"Path":         "/arbitrary/library/Golden%20Movies",
				"Nested":       map[string]any{"Path": "/config/data/root/default/Golden%20Movies"},
				"MediaSources": []any{map[string]any{"Path": "/state/root/default/Golden%20Movies"}},
			},
			map[string]any{
				"Id": "<user-view-id:golden movies|movies>", "Name": "Golden Movies", "CollectionType": "movies",
				"Path": "/config/data/root/default/Another%20Directory",
			},
		},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("non-direct UserView paths changed: %#v, want %#v", normalized.Body, want)
	}
}

// TestNormalizerRejectsOneSidedUserViewPaths prevents a configured path from
// hiding a presence mismatch when the corresponding direct view has no Path.
func TestNormalizerRejectsOneSidedUserViewPaths(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}})
	withPath, err := normalizer.normalize(Response{Status: 200, Body: []byte(`{"Items":[{"Id":"stock-view","Name":"Golden Movies","CollectionType":"movies","Path":"/config/data/root/default/Golden%20Movies"}]}`)}, nil)
	if err != nil {
		t.Fatalf("normalize UserViews response with Path: %v", err)
	}
	withoutPath, err := normalizer.normalize(Response{Status: 200, Body: []byte(`{"Items":[{"Id":"deadfin-view","Name":"Golden Movies","CollectionType":"movies"}]}`)}, nil)
	if err != nil {
		t.Fatalf("normalize UserViews response without Path: %v", err)
	}
	if equal, reason := equalNormalized(withPath, withoutPath, 0); equal || reason != "JSON differs at $.Items[0]" {
		t.Fatalf("one-sided UserView Path comparison = (%t, %q), want (false, %q)", equal, reason, "JSON differs at $.Items[0]")
	}
}
