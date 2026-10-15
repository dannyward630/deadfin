// SPDX-License-Identifier: GPL-2.0-only

package hls_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/hls"
)

const (
	masterPlaylistEntryLimit = 10_000
	masterPlaylistByteLimit  = 8 << 20
)

func TestBuildMasterPlaylistBoundsRenderedEntryCount(t *testing.T) {
	atLimit := masterLimitVariants(masterPlaylistEntryLimit)
	playlist, err := hls.BuildMasterPlaylist(hls.MasterRequest{Variants: atLimit})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist(%d entries): %v", masterPlaylistEntryLimit, err)
	}
	if playlist == "" {
		t.Fatalf("BuildMasterPlaylist(%d entries) returned empty output", masterPlaylistEntryLimit)
	}

	playlist, err = hls.BuildMasterPlaylist(hls.MasterRequest{Variants: masterLimitVariants(masterPlaylistEntryLimit + 1)})
	if err == nil {
		t.Fatalf("BuildMasterPlaylist(%d entries) error = nil, want limit rejection", masterPlaylistEntryLimit+1)
	}
	if playlist != "" {
		t.Fatalf("BuildMasterPlaylist(%d entries) produced %d bytes, want empty rejection", masterPlaylistEntryLimit+1, len(playlist))
	}
}

func TestBuildMasterPlaylistBoundsRenderedBytes(t *testing.T) {
	baseline, err := hls.BuildMasterPlaylist(hls.MasterRequest{Variants: []hls.MasterVariant{masterLimitVariant("u")}})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist baseline: %v", err)
	}
	fixedBytes := len(baseline) - len("u")

	atLimitURI := strings.Repeat("u", masterPlaylistByteLimit-fixedBytes)
	playlist, err := hls.BuildMasterPlaylist(hls.MasterRequest{Variants: []hls.MasterVariant{masterLimitVariant(atLimitURI)}})
	if err != nil {
		t.Fatalf("BuildMasterPlaylist(%d output bytes): %v", masterPlaylistByteLimit, err)
	}
	if len(playlist) != masterPlaylistByteLimit {
		t.Fatalf("BuildMasterPlaylist() output = %d bytes, want exactly %d", len(playlist), masterPlaylistByteLimit)
	}

	overLimitURI := strings.Repeat("u", masterPlaylistByteLimit-fixedBytes+1)
	playlist, err = hls.BuildMasterPlaylist(hls.MasterRequest{Variants: []hls.MasterVariant{masterLimitVariant(overLimitURI)}})
	if err == nil {
		t.Fatalf("BuildMasterPlaylist(%d output bytes) error = nil, want limit rejection", masterPlaylistByteLimit+1)
	}
	if playlist != "" {
		t.Fatalf("BuildMasterPlaylist(%d output bytes) produced %d bytes, want empty rejection", masterPlaylistByteLimit+1, len(playlist))
	}
}

func TestBuildMasterPlaylistRejectsControlCharactersInSubtitleAndRenditionMetadata(t *testing.T) {
	controls := []struct {
		name  string
		value string
	}{
		{name: "NUL", value: "\x00"},
		{name: "TAB", value: "\t"},
		{name: "DEL", value: "\x7f"},
		{name: "C1 next-line", value: "\u0085"},
	}
	metadata := []struct {
		name  string
		build func(string) hls.MasterRequest
	}{
		{
			name: "subtitle name",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Subtitles: []hls.SubtitleRendition{{GroupID: "subs", Name: "English" + value, URI: "subtitles.m3u8", Language: "eng"}}}
			},
		},
		{
			name: "subtitle group ID",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Subtitles: []hls.SubtitleRendition{{GroupID: "subs" + value, Name: "English", URI: "subtitles.m3u8", Language: "eng"}}}
			},
		},
		{
			name: "subtitle URI",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Subtitles: []hls.SubtitleRendition{{GroupID: "subs", Name: "English", URI: "subtitles" + value + ".m3u8", Language: "eng"}}}
			},
		},
		{
			name: "subtitle language",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Subtitles: []hls.SubtitleRendition{{GroupID: "subs", Name: "English", URI: "subtitles.m3u8", Language: "eng" + value}}}
			},
		},
		{
			name: "variant URI",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Variants: []hls.MasterVariant{masterLimitVariant("main" + value + ".m3u8")}}
			},
		},
		{
			name: "variant subtitle group",
			build: func(value string) hls.MasterRequest {
				variant := masterLimitVariant("main.m3u8")
				variant.SubtitleGroup = "subs" + value
				return hls.MasterRequest{Variants: []hls.MasterVariant{variant}}
			},
		},
		{
			name: "trickplay URI",
			build: func(value string) hls.MasterRequest {
				return hls.MasterRequest{Trickplay: []hls.TrickplayRendition{{URI: "tiles" + value + ".m3u8", Bandwidth: 1, Width: 1, Height: 1}}}
			},
		},
	}

	for _, field := range metadata {
		for _, control := range controls {
			t.Run(field.name+" with "+control.name, func(t *testing.T) {
				playlist, err := hls.BuildMasterPlaylist(field.build(control.value))
				if err == nil {
					t.Fatalf("BuildMasterPlaylist accepted %s control in %s", control.name, field.name)
				}
				if playlist != "" {
					t.Fatalf("BuildMasterPlaylist accepted %s control in %s and emitted %q", control.name, field.name, playlist)
				}
			})
		}
	}
}

func TestBuildMasterPlaylistAllowsUTF8Metadata(t *testing.T) {
	request := hls.MasterRequest{
		Subtitles: []hls.SubtitleRendition{{
			GroupID:  "subtítulos",
			Name:     "日本語 — café",
			URI:      "subtitles-日本語.m3u8",
			Language: "日本語",
		}},
		Variants: []hls.MasterVariant{{
			URI:           "main-日本語.m3u8",
			Width:         1920,
			Height:        1080,
			SubtitleGroup: "subtítulos",
		}},
		Trickplay: []hls.TrickplayRendition{{
			URI: "tiles-✓.m3u8", Bandwidth: 1, Width: 1, Height: 1,
		}},
	}
	playlist, err := hls.BuildMasterPlaylist(request)
	if err != nil {
		t.Fatalf("BuildMasterPlaylist UTF-8 metadata: %v", err)
	}
	for _, want := range []string{"日本語 — café", "subtítulos", "main-日本語.m3u8", "tiles-✓.m3u8"} {
		if !strings.Contains(playlist, want) {
			t.Errorf("BuildMasterPlaylist UTF-8 output does not contain %q: %q", want, playlist)
		}
	}
}

func masterLimitVariants(count int) []hls.MasterVariant {
	variants := make([]hls.MasterVariant, count)
	for i := range variants {
		variants[i] = masterLimitVariant("main.m3u8")
	}
	return variants
}

func masterLimitVariant(uri string) hls.MasterVariant {
	return hls.MasterVariant{URI: uri, Width: 1, Height: 1}
}
