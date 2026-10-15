// SPDX-License-Identifier: GPL-2.0-only

package dto_test

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/attest"
	"github.com/deadfin-org/deadfin/internal/dto"
)

const pinnedBaseItemDTOSource = "upstream/jellyfin/MediaBrowser.Model/Dto/BaseItemDto.cs"

var baseItemDTOAutoProperty = regexp.MustCompile(`^\s*public\s+.+\s+([A-Za-z][A-Za-z0-9_]*)\s+\{\s*get;\s*set;\s*\}`)

// TestBaseItemDtoMatchesPinnedSchema defends the complete API item wire
// surface. It derives the pinned C# properties directly so a Go DTO cannot
// silently omit a client-observable field, change its Pascal wire name, or
func TestBaseItemDtoMatchesPinnedSchema(t *testing.T) {
	jellyport := attest.GateForTest(t)
	want := pinnedBaseItemDTOProperties(t, jellyport)
	if got, wantCount := len(want), 155; got != wantCount {
		t.Fatalf("pinned BaseItemDto property count = %d, want %d", got, wantCount)
	}

	typ := reflect.TypeFor[dto.BaseItemDto]()
	if got, wantCount := typ.NumField(), len(want); got != wantCount {
		t.Fatalf("BaseItemDto field count = %d, want complete pinned schema of %d", got, wantCount)
	}
	for index, wantName := range want {
		field := typ.Field(index)
		if field.PkgPath != "" {
			t.Errorf("BaseItemDto field %d is unexported: %s", index, field.Name)
			continue
		}
		if got, wantTag := field.Tag.Get("json"), baseItemDTOJSONTag(field.Name, wantName); got != wantTag {
			t.Errorf("BaseItemDto field %d = %s with JSON tag %q, want %q", index, field.Name, got, wantTag)
		}
	}
	dateLastMediaAdded, found := typ.FieldByName("DateLastMediaAdded")
	if !found {
		t.Fatal("BaseItemDto missing DateLastMediaAdded")
	}
	if got, want := dateLastMediaAdded.Type, reflect.TypeFor[*dto.Time](); got != want {
		t.Errorf("BaseItemDto DateLastMediaAdded type = %s, want %s", got, want)
	}
}

// TestBaseItemDtoEmitsExplicitZeroDateLastMediaAdded protects the nullable
// folder lifecycle contract: a present DateTime.MinValue must remain on the
// wire, while a nil pointer remains omittable.
func TestBaseItemDtoEmitsExplicitZeroDateLastMediaAdded(t *testing.T) {
	zero := dto.NewTime(time.Time{})
	encoded, err := json.Marshal(dto.BaseItemDto{DateLastMediaAdded: &zero})
	if err != nil {
		t.Fatalf("marshal BaseItemDto with explicit zero DateLastMediaAdded: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode BaseItemDto wire: %v", err)
	}
	if got, want := string(wire["DateLastMediaAdded"]), `"0001-01-01T00:00:00.0000000Z"`; got != want {
		t.Errorf("explicit zero DateLastMediaAdded = %s, want %s", got, want)
	}
}

// BaseItemDto omits every unset optional/reference fact, except ChannelId:
// stock explicitly publishes ChannelId:null. ID and type discriminators are
// value facts, so their zero values must remain observable rather than being
// silently erased by a blanket omitzero tag.
var baseItemDTORequiredJSONFields = map[string]struct{}{
	"ID":        {},
	"ChannelID": {},
	"Type":      {},
	"MediaType": {},
}

func baseItemDTOJSONTag(fieldName, propertyName string) string {
	if fieldName == "DateLastMediaAdded" {
		return propertyName + ",omitempty"
	}
	if _, required := baseItemDTORequiredJSONFields[fieldName]; required {
		return propertyName
	}
	return propertyName + ",omitzero"
}

func pinnedBaseItemDTOProperties(t *testing.T, jellyport string) []string {
	t.Helper()
	contents, err := os.ReadFile(attest.AdjacentPath(jellyport, pinnedBaseItemDTOSource))
	if err != nil {
		t.Fatalf("read pinned BaseItemDto source: %v", err)
	}
	var properties []string
	for _, line := range strings.Split(string(contents), "\n") {
		match := baseItemDTOAutoProperty.FindStringSubmatch(line)
		if match != nil {
			properties = append(properties, match[1])
		}
	}
	return properties
}

// TestMediaProjectionDTOsMatchPinnedSourceFields protects the nested public
// wire types used by BaseItemDto. These are deliberately independent from the
// playback orchestration types, but must retain the stored source facts that
// Jellyfin publishes from its item projections.
func TestMediaProjectionDTOsMatchPinnedSourceFields(t *testing.T) {
	tests := []struct {
		name   string
		typ    reflect.Type
		fields map[string]string
	}{
		{
			name: "media source",
			typ:  reflect.TypeFor[dto.MediaSourceInfo](),
			fields: map[string]string{
				"Protocol": "Protocol", "ID": "Id", "Path": "Path", "Type": "Type", "Container": "Container", "Size": "Size", "Name": "Name", "RunTimeTicks": "RunTimeTicks", "IsRemote": "IsRemote", "ReadAtNativeFramerate": "ReadAtNativeFramerate", "IgnoreDts": "IgnoreDts", "IgnoreIndex": "IgnoreIndex", "GenPtsInput": "GenPtsInput", "SupportsTranscoding": "SupportsTranscoding", "SupportsDirectStream": "SupportsDirectStream", "SupportsDirectPlay": "SupportsDirectPlay", "SupportsProbing": "SupportsProbing", "IsInfiniteStream": "IsInfiniteStream", "UseMostCompatibleTranscodingProfile": "UseMostCompatibleTranscodingProfile", "RequiresOpening": "RequiresOpening", "RequiresClosing": "RequiresClosing", "RequiresLooping": "RequiresLooping", "VideoType": "VideoType", "IsoType": "IsoType", "Video3DFormat": "Video3DFormat", "MediaStreams": "MediaStreams", "MediaAttachments": "MediaAttachments", "Bitrate": "Bitrate", "DefaultAudioStreamIndex": "DefaultAudioStreamIndex", "DefaultSubtitleStreamIndex": "DefaultSubtitleStreamIndex", "HasSegments": "HasSegments",
			},
		},
		{
			name: "media stream",
			typ:  reflect.TypeFor[dto.MediaStreamInfo](),
			fields: map[string]string{
				"Codec": "Codec", "Language": "Language", "BitRate": "BitRate", "Channels": "Channels", "ChannelLayout": "ChannelLayout", "Width": "Width", "Height": "Height", "Type": "Type", "Index": "Index", "IsDefault": "IsDefault", "IsForced": "IsForced", "IsExternal": "IsExternal", "IsInterlaced": "IsInterlaced", "IsHearingImpaired": "IsHearingImpaired", "IsOriginal": "IsOriginal", "SupportsExternalStream": "SupportsExternalStream", "DeliveryMethod": "DeliveryMethod", "DeliveryUrl": "DeliveryUrl", "Path": "Path", "SampleRate": "SampleRate", "Profile": "Profile", "TimeBase": "TimeBase",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for name, jsonName := range tc.fields {
				field, found := tc.typ.FieldByName(name)
				if !found {
					t.Errorf("%s DTO is missing %s", tc.name, name)
					continue
				}
				if got := strings.Split(field.Tag.Get("json"), ",")[0]; got != jsonName {
					t.Errorf("%s DTO %s JSON name = %q, want %q", tc.name, name, got, jsonName)
				}
			}
		})
	}
}
