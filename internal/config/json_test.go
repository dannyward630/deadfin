// SPDX-License-Identifier: GPL-2.0-only

package config_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
)

// System.Text.Json builds a fresh array from the document. encoding/json
// would decode into the default elements and keep their omitted members.
func TestServerConfigurationJSONReplacesArraysInsteadOfMerging(t *testing.T) {
	var got config.ServerConfiguration
	if err := json.Unmarshal([]byte(`{"MetadataOptions":[{"ItemType":"Movie"}],"CorsHosts":[],"ActivityLogRetentionDays":7}`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := []config.MetadataOptions{{ItemType: "Movie"}}; !reflect.DeepEqual(got.MetadataOptions, want) {
		t.Fatalf("MetadataOptions = %#v, want %#v", got.MetadataOptions, want)
	}
	if got.CorsHosts == nil || len(got.CorsHosts) != 0 {
		t.Fatalf("CorsHosts = %#v, want empty", got.CorsHosts)
	}
	if got.ActivityLogRetentionDays == nil || *got.ActivityLogRetentionDays != 7 {
		t.Fatalf("ActivityLogRetentionDays = %v, want 7", got.ActivityLogRetentionDays)
	}
	if fresh := config.DefaultServerConfiguration(); *fresh.ActivityLogRetentionDays != 30 {
		t.Fatalf("decode wrote through the default pointer: %d", *fresh.ActivityLogRetentionDays)
	}
}

func TestConfigurationJSONRejectsNonObjects(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"text"`, `{"H264Crf":true}`} {
		var options config.EncodingOptions
		if err := json.Unmarshal([]byte(body), &options); !errors.Is(err, config.ErrInvalidConfiguration) {
			t.Errorf("Unmarshal(%s) error = %v, want ErrInvalidConfiguration", body, err)
		}
	}
}
