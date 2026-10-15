// SPDX-License-Identifier: GPL-2.0-only

package dto_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/render"
)

// TestPathPreservesStoredFilesystemBytesAcrossJSONProfiles fixes the wire
// contract for persisted media and metadata paths independently of the item
// route. DTO paths are filesystem values, not URL components.
func TestPathPreservesStoredFilesystemBytesAcrossJSONProfiles(t *testing.T) {
	paths := []struct {
		name, stored, want string
	}{
		{
			name:   "metadata placeholder with spaces",
			stored: "%MetadataPath%/People/G/Goldenrec Generator",
			want:   "%MetadataPath%/People/G/Goldenrec Generator",
		},
		{
			name:   "existing percent sequences remain literal",
			stored: "/metadata/Caf%C3%A9%20A",
			want:   "/metadata/Caf%C3%A9%20A",
		},
		{
			name:   "spaces and unicode remain literal",
			stored: "/metadata/Música/Café déjà-vu.mkv",
			want:   "/metadata/Música/Café déjà-vu.mkv",
		},
		{
			name:   "plus remains literal",
			stored: "/metadata/C++/A+B.mp3",
			want:   "/metadata/C++/A+B.mp3",
		},
		{
			name:   "malformed percent sequences remain literal",
			stored: "/metadata/bad%2G/lone%/truncated%2",
			want:   "/metadata/bad%2G/lone%/truncated%2",
		},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			value := struct {
				Path string `json:"Path"`
			}{Path: path.stored}
			for _, profile := range []struct {
				name, key string
				marshal   func(any) ([]byte, error)
			}{
				{name: "native", key: "Path", marshal: json.Marshal},
				{name: "Camel", key: "path", marshal: render.MarshalCamel},
			} {
				t.Run(profile.name, func(t *testing.T) {
					body, err := profile.marshal(value)
					if err != nil {
						t.Fatalf("marshal %s path: %v", profile.name, err)
					}
					var wire map[string]json.RawMessage
					if err := json.Unmarshal(body, &wire); err != nil {
						t.Fatalf("decode %s path: %v; body=%s", profile.name, err, body)
					}
					want, err := json.Marshal(path.want)
					if err != nil {
						t.Fatalf("encode %s expected path: %v", profile.name, err)
					}
					if got := wire[profile.key]; !bytes.Equal(got, want) {
						t.Errorf("%s path JSON bytes = %s, want %s", profile.name, got, want)
					}
				})
			}
		})
	}
}
