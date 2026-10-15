// SPDX-License-Identifier: GPL-2.0-only

// Extra-rule error propagation through aggregate VideoListResolver APIs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoAggregateResolversSurfaceInvalidExtraRuleErrors(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`}}

	for _, tt := range []struct {
		name    string
		resolve func() ([]naming.VideoInfo, error)
	}{
		{"list", func() ([]naming.VideoInfo, error) { return naming.ResolveVideoList([]string{"Movie.mkv"}, opts) }},
		{"collection", func() ([]naming.VideoInfo, error) {
			return naming.ResolveVideoListForCollection([]string{"Movie.mkv"}, opts, "movies")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resolve()
			if err == nil || got != nil {
				t.Fatalf("aggregate invalid reached extra rule = (%#v, %v), want (nil, error)", got, err)
			}
		})
	}

	got, err := naming.ResolveVideoInfoList([]naming.VideoFileInfo{{Path: "Movie.mkv", Name: "Movie"}}, opts, "")
	if err != nil || len(got) != 1 || got[0].Name != "Movie" {
		t.Fatalf("metadata list bypasses extra resolution = (%#v, %v), want one Movie without error", got, err)
	}
}

func TestUpstreamVideoStackAggregatesSurfaceConfiguredRuleErrors(t *testing.T) {
	invalidStack := naming.DefaultOptions()
	invalidStack.VideoFileStackingRules = []naming.FileStackRule{{Token: `[`}}
	for _, tt := range []struct {
		name    string
		resolve func() ([]naming.FileStack, error)
	}{
		{"paths-stack", func() ([]naming.FileStack, error) {
			return naming.ResolveStacks([]string{"Movie part1.mkv", "Movie part2.mkv"}, invalidStack)
		}},
		{"metadata-stack", func() ([]naming.FileStack, error) {
			return naming.ResolveVideoInfoStacks([]naming.VideoFileInfo{{Path: "Movie part1.mkv"}, {Path: "Movie part2.mkv"}}, invalidStack)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resolve()
			if err == nil || got != nil {
				t.Fatalf("stack aggregate invalid stack rule = (%#v, %v), want (nil, error)", got, err)
			}
		})
	}
}
