// SPDX-License-Identifier: GPL-2.0-only

package item_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// Source: upstream/jellyfin MediaBrowser.Controller/Entities/BaseItem.cs:185-186,
// AudioBook.cs:15-16, Book.cs:20-22, and Video.cs:62-84. Resume ticks are
// available only to books, audiobooks, and video-derived items that are not
// sample, theme-video, or trailer extras.
func TestSupportsPositionTicksResume(t *testing.T) {
	cases := []struct {
		name string
		item item.Item
		want bool
	}{
		{name: "base item", item: item.Item{}, want: false},
		{name: "audiobook", item: item.Item{Kind: item.KindAudioBook}, want: true},
		{name: "book", item: item.Item{Kind: item.KindBook}, want: true},
	}

	for _, kind := range []struct {
		name string
		kind item.Kind
	}{
		{name: "aggregate folder", kind: item.KindAggregateFolder},
		{name: "audio", kind: item.KindAudio},
		{name: "base plugin folder", kind: item.KindBasePluginFolder},
		{name: "box set", kind: item.KindBoxSet},
		{name: "channel", kind: item.KindChannel},
		{name: "channel folder item", kind: item.KindChannelFolderItem},
		{name: "collection folder", kind: item.KindCollectionFolder},
		{name: "folder", kind: item.KindFolder},
		{name: "genre", kind: item.KindGenre},
		{name: "manual playlists folder", kind: item.KindManualPlaylistsFolder},
		{name: "live TV channel", kind: item.KindLiveTvChannel},
		{name: "live TV program", kind: item.KindLiveTvProgram},
		{name: "music album", kind: item.KindMusicAlbum},
		{name: "music artist", kind: item.KindMusicArtist},
		{name: "music genre", kind: item.KindMusicGenre},
		{name: "person", kind: item.KindPerson},
		{name: "photo", kind: item.KindPhoto},
		{name: "photo album", kind: item.KindPhotoAlbum},
		{name: "playlist", kind: item.KindPlaylist},
		{name: "playlists folder", kind: item.KindPlaylistsFolder},
		{name: "program", kind: item.KindProgram},
		{name: "season", kind: item.KindSeason},
		{name: "series", kind: item.KindSeries},
		{name: "studio", kind: item.KindStudio},
		{name: "TV channel", kind: item.KindTvChannel},
		{name: "TV program", kind: item.KindTvProgram},
		{name: "user root folder", kind: item.KindUserRootFolder},
		{name: "user view", kind: item.KindUserView},
		{name: "year", kind: item.KindYear},
	} {
		cases = append(cases, struct {
			name string
			item item.Item
			want bool
		}{name: kind.name, item: item.Item{Kind: kind.kind}, want: false})
	}

	zeroExtra := item.ExtraTypeUnknown
	sample := item.ExtraTypeSample
	themeVideo := item.ExtraTypeThemeVideo
	trailer := item.ExtraTypeTrailer
	for _, kind := range []struct {
		name string
		kind item.Kind
	}{
		{name: "movie", kind: item.KindMovie},
		{name: "episode", kind: item.KindEpisode},
		{name: "video", kind: item.KindVideo},
		{name: "recording", kind: item.KindRecording},
		{name: "music video", kind: item.KindMusicVideo},
		{name: "trailer kind", kind: item.KindTrailer},
	} {
		for _, extra := range []struct {
			name  string
			value *item.ExtraType
			want  bool
		}{
			{name: "without extra", value: nil, want: true},
			{name: "with zero extra", value: &zeroExtra, want: true},
			{name: "sample extra", value: &sample, want: false},
			{name: "theme video extra", value: &themeVideo, want: false},
			{name: "trailer extra", value: &trailer, want: false},
		} {
			cases = append(cases, struct {
				name string
				item item.Item
				want bool
			}{
				name: kind.name + " " + extra.name,
				item: item.Item{Kind: kind.kind, ExtraType: extra.value},
				want: extra.want,
			})
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := item.SupportsPositionTicksResume(tc.item); got != tc.want {
				t.Errorf("SupportsPositionTicksResume(%+v) = %t, want %t", tc.item, got, tc.want)
			}
		})
	}
}
