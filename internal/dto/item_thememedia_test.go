// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"encoding/json"
	"testing"
)

// TestThemeMediaResultJSONShape verifies that ThemeMediaResult publishes
// Items, TotalRecordCount, StartIndex, and OwnerId in the wire format.
func TestThemeMediaResultJSONShape(t *testing.T) {
	result := ThemeMediaResult{
		QueryResult: NewQueryResult[BaseItemDto](),
		OwnerID:     UUID{0x01},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal ThemeMediaResult: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal ThemeMediaResult: %v", err)
	}
	for _, key := range []string{"Items", "TotalRecordCount", "StartIndex", "OwnerId"} {
		if _, found := raw[key]; !found {
			t.Fatalf("ThemeMediaResult JSON missing key %q: %v", key, raw)
		}
	}
	if items, ok := raw["Items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("ThemeMediaResult Items = %#v, want empty array", raw["Items"])
	}
}

// TestAllThemeMediaResultJSONShape verifies that AllThemeMediaResult publishes
// ThemeVideosResult, ThemeSongsResult, and SoundtrackSongsResult.
func TestAllThemeMediaResultJSONShape(t *testing.T) {
	result := AllThemeMediaResult{
		ThemeVideosResult:     ThemeMediaResult{QueryResult: NewQueryResult[BaseItemDto]()},
		ThemeSongsResult:      ThemeMediaResult{QueryResult: NewQueryResult[BaseItemDto]()},
		SoundtrackSongsResult: ThemeMediaResult{QueryResult: NewQueryResult[BaseItemDto]()},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal AllThemeMediaResult: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal AllThemeMediaResult: %v", err)
	}
	for _, key := range []string{"ThemeVideosResult", "ThemeSongsResult", "SoundtrackSongsResult"} {
		if _, found := raw[key]; !found {
			t.Fatalf("AllThemeMediaResult JSON missing key %q: %v", key, raw)
		}
	}
}
