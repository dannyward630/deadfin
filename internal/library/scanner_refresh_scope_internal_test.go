// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestMergeRefreshOptionsTargets proves a queued scoped refresh keeps the
// union of the scopes, and an unscoped intent widens the merge to every
// library.
func TestMergeRefreshOptionsTargets(t *testing.T) {
	alpha := RefreshTarget{ItemID: dto.UUID{1}}
	bravo := RefreshTarget{ItemID: dto.UUID{2}, Path: "/media/bravo"}
	for _, tc := range []struct {
		name               string
		current, requested []RefreshTarget
		want               []RefreshTarget
	}{
		{name: "union", current: []RefreshTarget{alpha}, requested: []RefreshTarget{bravo}, want: []RefreshTarget{alpha, bravo}},
		{name: "unscoped current", requested: []RefreshTarget{bravo}},
		{name: "unscoped request", current: []RefreshTarget{alpha}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := RefreshOptions{Targets: tc.current}
			got := mergeRefreshOptions(current, RefreshOptions{Targets: tc.requested}).Targets
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("merged targets = %#v, want %#v", got, tc.want)
			}
			if len(tc.current) > 0 && !reflect.DeepEqual(current.Targets, tc.current) {
				t.Fatalf("merge mutated current targets = %#v", current.Targets)
			}
		})
	}
}

func TestPathWithin(t *testing.T) {
	for _, tc := range []struct {
		path, base string
		want       bool
	}{
		{"/media/animu", "/media/animu", true},
		{"/media/animu/Show/Season 1", "/media/animu", true},
		{"/media/animu/", "/media/animu", true},
		{"/media/animu2/Show", "/media/animu", false},
		{"/media/tv/Game.Of.Thrones", "/media/animu", false},
		{"/media/tv", "/", true},
		{"/media/tv", "", false},
	} {
		if got := pathWithin(tc.path, tc.base); got != tc.want {
			t.Errorf("pathWithin(%q, %q) = %t, want %t", tc.path, tc.base, got, tc.want)
		}
	}
}
