// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestKindCanonicalValuesAndString(t *testing.T) {
	// Canonical order and spelling are upstream/jellyfin/Jellyfin.Data/Enums/BaseItemKind.cs:9-204.
	// In particular, Recording is deliberately present at :141-147 and TvChannel/TvProgram
	// are deliberate externally overridden names at :169-183.
	cases := []struct {
		kind Kind
		name string
	}{
		{KindAggregateFolder, "AggregateFolder"},
		{KindAudio, "Audio"},
		{KindAudioBook, "AudioBook"},
		{KindBasePluginFolder, "BasePluginFolder"},
		{KindBook, "Book"},
		{KindBoxSet, "BoxSet"},
		{KindChannel, "Channel"},
		{KindChannelFolderItem, "ChannelFolderItem"},
		{KindCollectionFolder, "CollectionFolder"},
		{KindEpisode, "Episode"},
		{KindFolder, "Folder"},
		{KindGenre, "Genre"},
		{KindManualPlaylistsFolder, "ManualPlaylistsFolder"},
		{KindMovie, "Movie"},
		{KindLiveTvChannel, "LiveTvChannel"},
		{KindLiveTvProgram, "LiveTvProgram"},
		{KindMusicAlbum, "MusicAlbum"},
		{KindMusicArtist, "MusicArtist"},
		{KindMusicGenre, "MusicGenre"},
		{KindMusicVideo, "MusicVideo"},
		{KindPerson, "Person"},
		{KindPhoto, "Photo"},
		{KindPhotoAlbum, "PhotoAlbum"},
		{KindPlaylist, "Playlist"},
		{KindPlaylistsFolder, "PlaylistsFolder"},
		{KindProgram, "Program"},
		{KindRecording, "Recording"},
		{KindSeason, "Season"},
		{KindSeries, "Series"},
		{KindStudio, "Studio"},
		{KindTrailer, "Trailer"},
		{KindTvChannel, "TvChannel"},
		{KindTvProgram, "TvProgram"},
		{KindUserRootFolder, "UserRootFolder"},
		{KindUserView, "UserView"},
		{KindVideo, "Video"},
		{KindYear, "Year"},
	}

	if len(cases) != 37 {
		t.Fatalf("canonical kind table has %d values, want 37", len(cases))
	}
	for i, tc := range cases {
		if got, want := uint8(tc.kind), uint8(i); got != want {
			t.Errorf("%s numeric value = %d, want %d (upstream enum ordinal)", tc.name, got, want)
		}
		if got := tc.kind.String(); got != tc.name {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.name)
		}
	}

	for _, invalid := range []Kind{Kind(37), Kind(255)} {
		if got := invalid.String(); got != "" {
			t.Errorf("invalid Kind(%d).String() = %q, want empty", invalid, got)
		}
	}
}

func TestKindTextAndJSONRoundTrip(t *testing.T) {
	// upstream/jellyfin/Jellyfin.Data/Enums/BaseItemKind.cs:9-204 supplies the canonical case.
	// Jellyfin's public item-type binder is case-insensitive; Deadfin retains that input
	// seam while emitting the canonical enum name.
	cases := []struct {
		kind Kind
		name string
	}{
		{KindAggregateFolder, "AggregateFolder"},
		{KindAudio, "Audio"},
		{KindAudioBook, "AudioBook"},
		{KindBasePluginFolder, "BasePluginFolder"},
		{KindBook, "Book"},
		{KindBoxSet, "BoxSet"},
		{KindChannel, "Channel"},
		{KindChannelFolderItem, "ChannelFolderItem"},
		{KindCollectionFolder, "CollectionFolder"},
		{KindEpisode, "Episode"},
		{KindFolder, "Folder"},
		{KindGenre, "Genre"},
		{KindManualPlaylistsFolder, "ManualPlaylistsFolder"},
		{KindMovie, "Movie"},
		{KindLiveTvChannel, "LiveTvChannel"},
		{KindLiveTvProgram, "LiveTvProgram"},
		{KindMusicAlbum, "MusicAlbum"},
		{KindMusicArtist, "MusicArtist"},
		{KindMusicGenre, "MusicGenre"},
		{KindMusicVideo, "MusicVideo"},
		{KindPerson, "Person"},
		{KindPhoto, "Photo"},
		{KindPhotoAlbum, "PhotoAlbum"},
		{KindPlaylist, "Playlist"},
		{KindPlaylistsFolder, "PlaylistsFolder"},
		{KindProgram, "Program"},
		{KindRecording, "Recording"},
		{KindSeason, "Season"},
		{KindSeries, "Series"},
		{KindStudio, "Studio"},
		{KindTrailer, "Trailer"},
		{KindTvChannel, "TvChannel"},
		{KindTvProgram, "TvProgram"},
		{KindUserRootFolder, "UserRootFolder"},
		{KindUserView, "UserView"},
		{KindVideo, "Video"},
		{KindYear, "Year"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseKind(strings.ToUpper(tc.name))
			if err != nil {
				t.Fatalf("ParseKind(upper %q): %v", tc.name, err)
			}
			if parsed != tc.kind {
				t.Fatalf("ParseKind(upper %q) = %v, want %v", tc.name, parsed, tc.kind)
			}

			text, err := tc.kind.MarshalText()
			if err != nil {
				t.Fatalf("%s.MarshalText(): %v", tc.name, err)
			}
			if got := string(text); got != tc.name {
				t.Errorf("%s.MarshalText() = %q, want %q", tc.name, got, tc.name)
			}
			var fromText Kind
			if err := fromText.UnmarshalText([]byte(strings.ToLower(tc.name))); err != nil {
				t.Fatalf("%s.UnmarshalText(lower): %v", tc.name, err)
			}
			if fromText != tc.kind {
				t.Errorf("UnmarshalText(lower %q) = %v, want %v", tc.name, fromText, tc.kind)
			}

			jsonValue, err := json.Marshal(tc.kind)
			if err != nil {
				t.Fatalf("json.Marshal(%s): %v", tc.name, err)
			}
			if got, want := string(jsonValue), `"`+tc.name+`"`; got != want {
				t.Errorf("json.Marshal(%s) = %s, want %s", tc.name, got, want)
			}
			var fromJSON Kind
			if err := json.Unmarshal([]byte(`"`+strings.ToLower(tc.name)+`"`), &fromJSON); err != nil {
				t.Fatalf("json.Unmarshal(lower %q): %v", tc.name, err)
			}
			if fromJSON != tc.kind {
				t.Errorf("json.Unmarshal(lower %q) = %v, want %v", tc.name, fromJSON, tc.kind)
			}
		})
	}
}

// upstream/jellyfin/Jellyfin.Data/Enums/BaseItemKind.cs:9-204 declares the contiguous
// 0..36 wire ordinals. The parameterless JsonStringEnumConverter registered by
// upstream/jellyfin/src/Jellyfin.Extensions/Json/JsonDefaults.cs:35-44 accepts those
// integers while serializing their canonical enum names.
func TestKindJSONAcceptsUpstreamNumericOrdinalsAndEmitsCanonicalNames(t *testing.T) {
	names := []string{
		"AggregateFolder", "Audio", "AudioBook", "BasePluginFolder", "Book", "BoxSet", "Channel", "ChannelFolderItem", "CollectionFolder", "Episode",
		"Folder", "Genre", "ManualPlaylistsFolder", "Movie", "LiveTvChannel", "LiveTvProgram", "MusicAlbum", "MusicArtist", "MusicGenre", "MusicVideo",
		"Person", "Photo", "PhotoAlbum", "Playlist", "PlaylistsFolder", "Program", "Recording", "Season", "Series", "Studio", "Trailer", "TvChannel",
		"TvProgram", "UserRootFolder", "UserView", "Video", "Year",
	}

	for ordinal, name := range names {
		t.Run(name, func(t *testing.T) {
			var kind Kind
			if err := json.Unmarshal([]byte(strconv.Itoa(ordinal)), &kind); err != nil {
				t.Fatalf("json.Unmarshal(%d): %v", ordinal, err)
			}
			if want := Kind(ordinal); kind != want {
				t.Fatalf("json.Unmarshal(%d) = Kind(%d), want exact upstream ordinal Kind(%d)", ordinal, kind, ordinal)
			}
			got, err := json.Marshal(kind)
			if err != nil {
				t.Fatalf("json.Marshal(kind decoded from %d): %v", ordinal, err)
			}
			if want := `"` + name + `"`; string(got) != want {
				t.Fatalf("json.Marshal(kind decoded from %d) = %s, want %s", ordinal, got, want)
			}
		})
	}

	for _, input := range []string{"-1", "37", "255"} {
		t.Run("rejects_"+input, func(t *testing.T) {
			kind := KindMovie
			if err := json.Unmarshal([]byte(input), &kind); err == nil {
				t.Fatalf("json.Unmarshal(%s) succeeded, want error", input)
			}
			if kind != KindMovie {
				t.Fatalf("json.Unmarshal(%s) changed receiver to %v on error, want Movie", input, kind)
			}
		})
	}
}

func TestKindRejectsInvalidTextAndJSON(t *testing.T) {
	// upstream/jellyfin/Jellyfin.Data/Enums/BaseItemKind.cs:9-204 is exhaustive:
	// no empty, numeric, or unknown string is a valid public item type.
	for _, input := range []string{"", "not-a-kind", "38", "0"} {
		if _, err := ParseKind(input); err == nil {
			t.Errorf("ParseKind(%q) succeeded, want error", input)
		}
		current := KindMovie
		if err := current.UnmarshalText([]byte(input)); err == nil {
			t.Errorf("Kind.UnmarshalText(%q) succeeded, want error", input)
		} else if current != KindMovie {
			t.Errorf("Kind.UnmarshalText(%q) changed receiver to %v on error", input, current)
		}
	}

	for _, input := range []string{`null`, `42`, `{}`, `[]`, `"not-a-kind"`} {
		current := KindMovie
		if err := json.Unmarshal([]byte(input), &current); err == nil {
			t.Errorf("json.Unmarshal(%s) succeeded, want error", input)
		} else if current != KindMovie {
			t.Errorf("json.Unmarshal(%s) changed receiver to %v on error", input, current)
		}
	}
	for _, invalid := range []Kind{Kind(37), Kind(255)} {
		if _, err := invalid.MarshalText(); err == nil {
			t.Errorf("invalid Kind(%d).MarshalText() succeeded, want error", invalid)
		}
		if _, err := json.Marshal(invalid); err == nil {
			t.Errorf("json.Marshal(invalid Kind(%d)) succeeded, want error", invalid)
		}
	}
}

func TestSupportEnums(t *testing.T) {
	// upstream/jellyfin/Jellyfin.Data/Enums/MediaType.cs:6-32 fixes these wire names.
	mediaCases := []struct {
		value MediaType
		name  string
	}{
		{MediaTypeUnknown, "Unknown"},
		{MediaTypeVideo, "Video"},
		{MediaTypeAudio, "Audio"},
		{MediaTypePhoto, "Photo"},
		{MediaTypeBook, "Book"},
	}
	for i, tc := range mediaCases {
		if got, want := uint8(tc.value), uint8(i); got != want {
			t.Errorf("MediaType %s = %d, want %d", tc.name, got, want)
		}
		if got := tc.value.String(); got != tc.name {
			t.Errorf("MediaType(%d).String() = %q, want %q", tc.value, got, tc.name)
		}
	}
	if got := MediaType(255).String(); got != "" {
		t.Errorf("unknown MediaType.String() = %q, want empty", got)
	}
}
