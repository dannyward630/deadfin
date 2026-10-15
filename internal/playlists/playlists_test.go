// SPDX-License-Identifier: GPL-2.0-only

package playlists

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestAdjustedIndex ports PlaylistManagerTests.DetermineAdjustedIndex
// (tests/Jellyfin.Server.Implementations.Tests/Playlists).
func TestAdjustedIndex(t *testing.T) {
	for _, tc := range []struct{ prior, newIndex, want int }{
		{0, 0, 0}, // DetermineAdjustedIndexMoveToFirstPositionNoPriorInAllList
		{2, 0, 1}, // DetermineAdjustedIndexPriorInMiddleOfAllList
		{2, 1, 3}, // DetermineAdjustedIndexMoveMiddleOfPlaylist
		{0, 1, 1},
	} {
		if got := adjustedIndex(tc.prior, tc.newIndex); got != tc.want {
			t.Errorf("adjustedIndex(%d, %d) = %d, want %d", tc.prior, tc.newIndex, got, tc.want)
		}
	}
}

// TestInsertAt ports AddToPlaylistInternal's position handling: absent or
// past-the-end appends, zero or negative prepends, otherwise inserts.
func TestInsertAt(t *testing.T) {
	a, b, c, x := dto.UUID{1}, dto.UUID{2}, dto.UUID{3}, dto.UUID{9}
	base := []dto.UUID{a, b, c}
	at := func(v int) *int { return &v }
	for _, tc := range []struct {
		position *int
		want     []dto.UUID
	}{
		{nil, []dto.UUID{a, b, c, x}},
		{at(-1), []dto.UUID{x, a, b, c}},
		{at(0), []dto.UUID{x, a, b, c}},
		{at(2), []dto.UUID{a, b, x, c}},
		{at(3), []dto.UUID{a, b, c, x}},
		{at(10), []dto.UUID{a, b, c, x}},
	} {
		if got := insertAt(base, []dto.UUID{x}, tc.position); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("insertAt(%v) = %v, want %v", tc.position, got, tc.want)
		}
	}
	if !reflect.DeepEqual(base, []dto.UUID{a, b, c}) {
		t.Fatalf("insertAt mutated its input: %v", base)
	}
}

// TestContainsSubPath pins Playlist.IsSharedItem's data-path containment.
func TestContainsSubPath(t *testing.T) {
	for _, tc := range []struct {
		candidate string
		want      bool
	}{
		{"/srv/data/playlists/Mix", true},
		{"/srv/data", false},
		{"/srv/database/x", false},
		{"/media/music/mix.m3u", false},
	} {
		if got := containsSubPath("/srv/data", tc.candidate); got != tc.want {
			t.Errorf("containsSubPath(%q) = %v, want %v", tc.candidate, got, tc.want)
		}
	}
}
