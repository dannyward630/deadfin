// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"reflect"
	"testing"
)

// Sources: upstream/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// MediaBrowser.Controller/Entities/SourceType.cs:6-22 and
// MediaBrowser.Model/LiveTv/ChannelType.cs:6-17. These ordinal enums persist
// as wire values, so their spelling and backing type are public contracts.
func TestSourceAndChannelTypePinnedNamesAndOrdinals(t *testing.T) {
	sourceCases := []struct {
		name  string
		value SourceType
	}{
		{name: "Library", value: SourceTypeLibrary},
		{name: "Channel", value: SourceTypeChannel},
		{name: "LiveTV", value: SourceTypeLiveTV},
	}
	if got := reflect.TypeOf(SourceType(0)).Kind(); got != reflect.Uint8 {
		t.Fatalf("SourceType backing kind = %s, want uint8", got)
	}
	for ordinal, tc := range sourceCases {
		if got := uint8(tc.value); got != uint8(ordinal) {
			t.Errorf("SourceType%s = %d, want %d", tc.name, got, ordinal)
		}
		if got := tc.value.String(); got != tc.name {
			t.Errorf("SourceType(%d).String() = %q, want %q", tc.value, got, tc.name)
		}
	}
	if got := SourceType(255).String(); got != "" {
		t.Errorf("invalid SourceType.String() = %q, want empty", got)
	}

	channelCases := []struct {
		name  string
		value ChannelType
	}{
		{name: "TV", value: ChannelTypeTV},
		{name: "Radio", value: ChannelTypeRadio},
	}
	if got := reflect.TypeOf(ChannelType(0)).Kind(); got != reflect.Uint8 {
		t.Fatalf("ChannelType backing kind = %s, want uint8", got)
	}
	for ordinal, tc := range channelCases {
		if got := uint8(tc.value); got != uint8(ordinal) {
			t.Errorf("ChannelType%s = %d, want %d", tc.name, got, ordinal)
		}
		if got := tc.value.String(); got != tc.name {
			t.Errorf("ChannelType(%d).String() = %q, want %q", tc.value, got, tc.name)
		}
	}
	if got := ChannelType(255).String(); got != "" {
		t.Errorf("invalid ChannelType.String() = %q, want empty", got)
	}
}
