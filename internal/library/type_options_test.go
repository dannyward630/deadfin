// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
)

func TestLibraryOptionsImageLimit(t *testing.T) {
	options := LibraryOptions{TypeOptions: []TypeOptions{{Type: "movie", ImageOptions: []ImageOption{{Type: "Backdrop", Limit: 0}, {Type: "Art", Limit: 2}}}}}
	tests := []struct {
		kind      item.Kind
		imageType item.ImageType
		want      int
	}{
		{item.KindMovie, item.ImageTypePrimary, 1},
		{item.KindMovie, item.ImageTypeBackdrop, 0},
		{item.KindMovie, item.ImageTypeArt, 2},
		{item.KindMovie, item.ImageTypeDisc, 0},
		{item.KindEpisode, item.ImageTypePrimary, 1},
		{item.KindEpisode, item.ImageTypeBackdrop, 0},
		{item.KindSeason, item.ImageTypeThumb, 0},
		{item.KindVideo, item.ImageTypeBackdrop, 1},
	}
	for _, test := range tests {
		if got := options.ImageLimit(test.kind, test.imageType); got != test.want {
			t.Errorf("ImageLimit(%s, %s) = %d, want %d", test.kind, test.imageType, got, test.want)
		}
	}
}

func TestRunsDynamicImages(t *testing.T) {
	stamp := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	refreshed := item.Item{DateLastRefreshed: &stamp}
	tests := []struct {
		name    string
		refresh RefreshOptions
		stored  *item.Item
		want    bool
	}{
		{"new item", RefreshOptions{Image: RefreshModeDefault}, nil, true},
		{"validation only", RefreshOptions{Image: RefreshModeValidationOnly}, nil, false},
		{"never refreshed", RefreshOptions{Image: RefreshModeDefault}, &item.Item{}, true},
		{"refreshed item on a rescan", RefreshOptions{Image: RefreshModeDefault}, &refreshed, false},
		{"full refresh", RefreshOptions{Image: RefreshModeFullRefresh}, &refreshed, true},
	}
	for _, test := range tests {
		if got := runsDynamicImages(test.refresh, test.stored); got != test.want {
			t.Errorf("%s: runsDynamicImages = %v, want %v", test.name, got, test.want)
		}
	}
}
