// SPDX-License-Identifier: GPL-2.0-only

package dto_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestBaseItemDtoPreservesDictionaryKeysAcrossCasingProfiles defends the API
// casing boundary: struct properties switch Pascal/Camel profiles, while
// provider and image dictionary keys remain upstream identifiers verbatim.
func TestBaseItemDtoPreservesDictionaryKeysAcrossCasingProfiles(t *testing.T) {
	value := dto.BaseItemDto{
		ProviderIDs:     map[string]string{"Tmdb": "42"},
		ImageTags:       map[string]string{"Primary": "poster"},
		ImageBlurHashes: map[string]map[string]string{"Primary": {"poster": "blur"}},
	}
	pascal, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal Pascal DTO: %v", err)
	}
	camel, err := render.MarshalCamel(value)
	if err != nil {
		t.Fatalf("marshal Camel DTO: %v", err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		keys []string
	}{
		{name: "Pascal", body: pascal, keys: []string{"ProviderIds", "ImageTags", "ImageBlurHashes"}},
		{name: "Camel", body: camel, keys: []string{"providerIds", "imageTags", "imageBlurHashes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(tc.body, &wire); err != nil {
				t.Fatalf("decode %s DTO: %v", tc.name, err)
			}
			for _, key := range tc.keys {
				if _, ok := wire[key]; !ok {
					t.Errorf("%s DTO missing property key %q", tc.name, key)
				}
			}
			var imageTags map[string]string
			if err := json.Unmarshal(wire[tc.keys[1]], &imageTags); err != nil {
				t.Fatalf("decode %s ImageTags: %v", tc.name, err)
			}
			if got, want := imageTags["Primary"], "poster"; got != want {
				t.Errorf("%s ImageTags map key Primary = %q, want %q", tc.name, got, want)
			}
		})
	}
}

// TestMediaProjectionNestedDTOsPreservePascalAndCamelWireShapes protects the
// public nested values rather than merely BaseItemDto's collection property.
// The source payload is a pinned representative stock shape: nullable facts
// absent from it must stay absent, while persisted technical facts survive both
// renderer profiles.
func TestMediaProjectionNestedDTOsPreservePascalAndCamelWireShapes(t *testing.T) {
	const source = `{"Protocol":"File","Id":"source-42","Path":"/library/feature.mkv","Type":"Default","Container":"mkv","Size":1000000,"Name":"Feature","RunTimeTicks":90000000,"IsRemote":false,"SupportsTranscoding":true,"SupportsDirectStream":true,"SupportsDirectPlay":true,"SupportsProbing":true,"MediaAttachments":[{"Codec":"mjpeg","CodecTag":"MJPG","Comment":"cover","Index":3,"FileName":"cover.jpg","MimeType":"image/jpeg"}],"MediaStreams":[{"Codec":"h264","Language":"eng","BitRate":850000,"Channels":6,"ChannelLayout":"5.1","Width":1920,"Height":1080,"Type":"Video","Index":0,"IsDefault":true,"IsForced":false,"IsExternal":false,"IsInterlaced":false,"IsHearingImpaired":false,"IsOriginal":false,"SupportsExternalStream":false,"TimeBase":"1/90000"},{"Codec":"eac3","Language":"eng","BitRate":640000,"Channels":6,"Type":"Audio","Index":1,"IsDefault":true,"IsForced":false,"IsExternal":false,"IsInterlaced":false,"IsHearingImpaired":false,"IsOriginal":false,"SupportsExternalStream":false},{"Codec":"subrip","Language":"spa","Type":"Subtitle","Index":2,"IsDefault":false,"IsForced":true,"IsExternal":true,"IsInterlaced":false,"IsHearingImpaired":false,"IsOriginal":false,"SupportsExternalStream":false,"DeliveryUrl":"/Items/source-42/ExternalSubtitles/2"}]}`

	var value dto.MediaSourceInfo
	if err := json.Unmarshal([]byte(source), &value); err != nil {
		t.Fatalf("decode pinned media source: %v", err)
	}
	pascal, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal Pascal media source: %v", err)
	}
	camel, err := render.MarshalCamel(value)
	if err != nil {
		t.Fatalf("marshal Camel media source: %v", err)
	}
	assertMediaProjectionJSON(t, "Pascal", pascal, []byte(source), false)
	assertMediaProjectionJSON(t, "Camel", camel, []byte(source), true)
}

func assertMediaProjectionJSON(t *testing.T, profile string, got, pinned []byte, camel bool) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatalf("decode %s media output: %v", profile, err)
	}
	if err := json.Unmarshal(pinned, &expected); err != nil {
		t.Fatalf("decode pinned media fixture: %v", err)
	}
	if camel {
		expected = mediaProjectionCamelKeys(expected)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("%s media projection = %s, want pinned source shape %v", profile, got, expected)
	}
}

func mediaProjectionCamelKeys(value any) any {
	switch node := value.(type) {
	case []any:
		for index := range node {
			node[index] = mediaProjectionCamelKeys(node[index])
		}
		return node
	case map[string]any:
		result := make(map[string]any, len(node))
		for key, child := range node {
			result[strings.ToLower(key[:1])+key[1:]] = mediaProjectionCamelKeys(child)
		}
		return result
	default:
		return value
	}
}
