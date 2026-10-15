// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

func stockRawString(t *testing.T, values map[string]json.RawMessage, key string) string {
	value := stockRawStringPtr(values, key)
	if value == nil {
		t.Fatalf("missing fixture scalar %s", key)
	}
	return *value
}

func stockRawStringPtr(values map[string]json.RawMessage, key string) *string {
	raw, ok := values[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockRawBool(values map[string]json.RawMessage, key string) bool {
	raw, ok := values[key]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func stockRawIntPtr(values map[string]json.RawMessage, key string) *int {
	raw, ok := values[key]
	if !ok {
		return nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockRawInt64Ptr(values map[string]json.RawMessage, key string) *int64 {
	raw, ok := values[key]
	if !ok {
		return nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockRawFloat32Ptr(values map[string]json.RawMessage, key string) *float32 {
	raw, ok := values[key]
	if !ok {
		return nil
	}
	var value float32
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockRawTimePtr(values map[string]json.RawMessage, key string) *time.Time {
	value := stockRawStringPtr(values, key)
	if value == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return &parsed
}

func stockFixtureStreams(t *testing.T, source []map[string]json.RawMessage) []playback.MediaStream {
	result := make([]playback.MediaStream, len(source))
	for i, raw := range source {
		result[i] = playback.MediaStream{Index: stockRawInt(raw, "Index"), Codec: stockRawStringValue(raw, "Codec"), Language: stockRawStringValuePtr(raw, "Language"), IsExternal: stockRawBoolValue(raw, "IsExternal"), IsForced: stockRawBoolValue(raw, "IsForced"), BitRate: stockRawIntPtrValue(raw, "BitRate"), Width: stockRawIntPtrValue(raw, "Width"), Height: stockRawIntPtrValue(raw, "Height"), Type: stockStreamType(t, stockRawStringValue(raw, "Type"))}
	}
	return result
}

func stockRawStringValue(values map[string]json.RawMessage, key string) string {
	value := stockRawStringValuePtr(values, key)
	if value == nil {
		return ""
	}
	return *value
}

func stockRawStringValuePtr(values map[string]json.RawMessage, key string) *string {
	raw, ok := values[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockRawBoolValue(values map[string]json.RawMessage, key string) bool {
	raw, ok := values[key]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func stockRawInt(values map[string]json.RawMessage, key string) int {
	value := stockRawIntPtrValue(values, key)
	if value == nil {
		return 0
	}
	return *value
}

func stockRawIntPtrValue(values map[string]json.RawMessage, key string) *int {
	raw, ok := values[key]
	if !ok {
		return nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stockAnyBool(t *testing.T, key string, raw any) bool {
	value, ok := raw.(bool)
	if !ok {
		t.Fatalf("%s = %#v, want bool", key, raw)
	}
	return value
}

func stockAnyBoolPtr(t *testing.T, key string, raw any) *bool {
	value := stockAnyBool(t, key, raw)
	return &value
}

func stockAnyString(t *testing.T, key string, raw any) string {
	value, ok := raw.(string)
	if !ok {
		t.Fatalf("%s = %#v, want string", key, raw)
	}
	return value
}

func stockAnyStrings(t *testing.T, key string, raw any) []string {
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want string array", key, raw)
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = stockAnyString(t, fmt.Sprintf("%s[%d]", key, i), value)
	}
	return result
}

func stockAnyIntPtr(t *testing.T, key string, raw any) *int {
	value := stockAnyInt(t, key, raw)
	return &value
}

func stockAnyInt(t *testing.T, key string, raw any) int {
	value, ok := raw.(float64)
	if !ok || value != float64(int(value)) {
		t.Fatalf("%s = %#v, want integer", key, raw)
	}
	return int(value)
}

func stockAnyInts(t *testing.T, key string, raw any) []int {
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s must be integer array", key)
	}
	result := make([]int, len(values))
	for i := range values {
		result[i] = stockAnyInt(t, key, values[i])
	}
	return result
}

func stockAnyFloat32Ptr(t *testing.T, key string, raw any) *float32 {
	value, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s must be number", key)
	}
	result := float32(value)
	return &result
}

func stockAnyTimePtr(t *testing.T, key string, raw any) *time.Time {
	text := stockAnyString(t, key, raw)
	value, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatalf("%s = %q: %v", key, text, err)
	}
	return &value
}

func stockKinds(t *testing.T, key string, raw any) []item.Kind {
	values := stockAnyStrings(t, key, raw)
	result := make([]item.Kind, len(values))
	for i, value := range values {
		kind, err := item.ParseKind(value)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		result[i] = kind
	}
	return result
}

func stockMediaTypes(t *testing.T, key string, raw any) []item.MediaType {
	values := stockAnyStrings(t, key, raw)
	result := make([]item.MediaType, len(values))
	for i, value := range values {
		result[i] = stockMediaType(t, value)
	}
	return result
}

func stockMediaType(t *testing.T, value string) item.MediaType {
	switch value {
	case "Unknown":
		return item.MediaTypeUnknown
	case "Video":
		return item.MediaTypeVideo
	case "Audio":
		return item.MediaTypeAudio
	case "Photo":
		return item.MediaTypePhoto
	case "Book":
		return item.MediaTypeBook
	default:
		t.Fatalf("unknown media type %q", value)
		return item.MediaTypeUnknown
	}
}

func stockVideoTypes(t *testing.T, key string, raw any) []item.VideoType {
	values := stockAnyStrings(t, key, raw)
	result := make([]item.VideoType, len(values))
	for i, value := range values {
		result[i] = stockVideoType(t, value)
	}
	return result
}

func stockVideoType(t *testing.T, value string) item.VideoType {
	switch value {
	case "VideoFile":
		return item.VideoTypeVideoFile
	case "Iso":
		return item.VideoTypeIso
	case "Dvd":
		return item.VideoTypeDvd
	case "BluRay":
		return item.VideoTypeBluRay
	default:
		t.Fatalf("unknown video type %q", value)
		return item.VideoTypeVideoFile
	}
}

func stockStreamType(t *testing.T, value string) playback.StreamType {
	switch value {
	case "Video":
		return playback.StreamTypeVideo
	case "Audio":
		return playback.StreamTypeAudio
	case "Subtitle":
		return playback.StreamTypeSubtitle
	default:
		t.Fatalf("unknown stream type %q", value)
		return playback.StreamTypeVideo
	}
}

func stockSorts(t *testing.T, raw any) []store.ItemSortBy {
	values := stockAnyStrings(t, "SortBy", raw)
	result := make([]store.ItemSortBy, len(values))
	for i, value := range values {
		sort := store.ItemSortBy(value)
		if !containsItemSortBy(store.ItemSortByValues(), sort) {
			t.Fatalf("unknown sort %q", value)
		}
		result[i] = sort
	}
	return result
}

func stockSortOrders(t *testing.T, raw any) []store.SortOrder {
	values := stockAnyStrings(t, "SortOrder", raw)
	result := make([]store.SortOrder, len(values))
	for i, value := range values {
		if value != string(store.SortOrderAscending) && value != string(store.SortOrderDescending) {
			t.Fatalf("unknown sort order %q", value)
		}
		result[i] = store.SortOrder(value)
	}
	return result
}

func stockPersonKind(t *testing.T, value string) item.PersonKind {
	switch value {
	case "Actor":
		return item.PersonKindActor
	case "Director":
		return item.PersonKindDirector
	case "Artist":
		return item.PersonKindArtist
	case "AlbumArtist":
		return item.PersonKindAlbumArtist
	case "Composer":
		return item.PersonKindComposer
	case "Writer":
		return item.PersonKindWriter
	default:
		t.Fatalf("unknown person type %q", value)
		return item.PersonKindUnknown
	}
}
