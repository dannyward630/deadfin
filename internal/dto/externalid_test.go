// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"encoding/json"
	"testing"
)

// TestExternalIdInfoOmitsNullType verifies that when Type is nil, the JSON
// output omits the Type field entirely (matching stock's
// DefaultIgnoreCondition = WhenWritingNull), not rendering it as null.
func TestExternalIdInfoOmitsNullType(t *testing.T) {
	info := ExternalIdInfo{
		Name: "IMDb",
		Key:  "Imdb",
		Type: nil,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, hasType := raw["Type"]; hasType {
		t.Errorf("Type field present in JSON (%s), want omitted when nil", string(raw["Type"]))
	}
}

// TestExternalIdInfoIncludesNonNullType verifies that when Type is non-nil,
// the JSON output includes the Type field.
func TestExternalIdInfoIncludesNonNullType(t *testing.T) {
	mediaType := "Movie"
	info := ExternalIdInfo{
		Name: "TheMovieDb",
		Key:  "Tmdb",
		Type: &mediaType,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	typeRaw, hasType := raw["Type"]
	if !hasType {
		t.Fatal("Type field missing in JSON, want present when non-nil")
	}
	var got string
	if err := json.Unmarshal(typeRaw, &got); err != nil {
		t.Fatalf("unmarshal Type: %v", err)
	}
	if got != "Movie" {
		t.Errorf("Type = %q, want Movie", got)
	}
}
