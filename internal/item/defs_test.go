// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

type definitionCase struct {
	kind Kind
	def  KindDef
	caps Capabilities
}

func canonicalDefinitionCases() []definitionCase {
	// Sources for inherited defaults: upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:182-183,453-454,768-788;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Folder.cs:83-94; upstream/jellyfin/MediaBrowser.Controller/Entities/Video.cs:53-57,252-253;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/Audio.cs:42-68.
	// Per-kind exceptions: upstream/jellyfin/MediaBrowser.Controller/Entities/AggregateFolder.cs:49-50; upstream/jellyfin/MediaBrowser.Controller/Entities/CollectionFolder.cs:73-74;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/UserRootFolder.cs:38-39; upstream/jellyfin/MediaBrowser.Controller/Entities/UserView.cs:65-70;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/PhotoAlbum.cs:7-18 (inherits Folder without a MediaType override, so BaseItem.cs:449-454 remains Unknown); upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/MusicAlbum.cs:50-60;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Movies/BoxSet.cs:35-36; upstream/jellyfin/MediaBrowser.Controller/Entities/TV/Season.cs:36-37;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/TV/Series.cs:50-51; upstream/jellyfin/MediaBrowser.Controller/Entities/Genre.cs:28-35;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/MusicGenre.cs:23-38; upstream/jellyfin/MediaBrowser.Controller/Entities/Person.cs:35-39;
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Studio.cs:27-34; upstream/jellyfin/MediaBrowser.Controller/Entities/Year.cs:19-23.
	// Display-only is Deadfin policy at docs/01-architecture.md:168-170. Alias/overlay authority: upstream/jellyfin/Emby.Server.Implementations/Data/ItemTypeLookup.cs:25-64;
	// upstream/jellyfin/Emby.Server.Implementations/Playlists/PlaylistsFolder.cs:15-16,51-54; upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:118-121;
	// upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvProgram.cs:205-208; upstream/jellyfin/Emby.Server.Implementations/Dto/DtoService.cs:403-417.
	rows := []struct {
		kind                                           Kind
		folder, people, ancestors, played, displayOnly bool
		media                                          MediaType
		client                                         string
	}{
		{KindAggregateFolder, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindAudio, false, true, true, true, false, MediaTypeAudio, ""},
		{KindAudioBook, false, true, true, true, false, MediaTypeAudio, ""},
		{KindBasePluginFolder, true, false, true, true, false, MediaTypeUnknown, ""},
		{KindBook, false, true, true, true, false, MediaTypeBook, ""},
		{KindBoxSet, true, true, true, true, false, MediaTypeUnknown, ""},
		{KindChannel, true, false, true, true, false, MediaTypeUnknown, ""},
		{KindChannelFolderItem, true, false, true, true, false, MediaTypeUnknown, ""},
		{KindCollectionFolder, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindEpisode, false, true, true, true, false, MediaTypeVideo, ""},
		{KindFolder, true, false, true, true, false, MediaTypeUnknown, ""},
		{KindGenre, false, false, false, false, true, MediaTypeUnknown, ""},
		{KindManualPlaylistsFolder, true, false, true, true, false, MediaTypeUnknown, ""},
		{KindMovie, false, true, true, true, false, MediaTypeVideo, ""},
		{KindLiveTvChannel, false, false, true, false, false, MediaTypeVideo, "TvChannel"},
		{KindLiveTvProgram, false, false, false, false, false, MediaTypeUnknown, "Program"},
		{KindMusicAlbum, true, true, true, false, false, MediaTypeUnknown, ""},
		{KindMusicArtist, false, false, true, false, false, MediaTypeUnknown, ""},
		{KindMusicGenre, false, false, false, false, true, MediaTypeUnknown, ""},
		{KindMusicVideo, false, true, true, true, false, MediaTypeVideo, ""},
		{KindPerson, false, false, false, false, true, MediaTypeUnknown, ""},
		{KindPhoto, false, false, true, false, false, MediaTypePhoto, ""},
		{KindPhotoAlbum, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindPlaylist, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindPlaylistsFolder, true, false, true, true, false, MediaTypeUnknown, "ManualPlaylistsFolder"},
		{KindProgram, false, false, false, false, false, MediaTypeUnknown, ""},
		{KindRecording, false, true, true, true, false, MediaTypeVideo, ""},
		{KindSeason, true, true, true, true, false, MediaTypeUnknown, ""},
		{KindSeries, true, true, true, true, false, MediaTypeUnknown, ""},
		{KindStudio, false, false, false, false, true, MediaTypeUnknown, ""},
		{KindTrailer, false, true, true, true, false, MediaTypeVideo, ""},
		{KindTvChannel, false, false, true, false, false, MediaTypeVideo, ""},
		{KindTvProgram, false, false, false, false, false, MediaTypeUnknown, "Program"},
		{KindUserRootFolder, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindUserView, true, false, true, false, false, MediaTypeUnknown, ""},
		{KindVideo, false, true, true, true, false, MediaTypeVideo, ""},
		{KindYear, false, false, false, false, true, MediaTypeUnknown, ""},
	}

	cases := make([]definitionCase, 0, len(rows))
	for _, row := range rows {
		name := row.kind.String()
		def := KindDef{
			Kind:                        row.kind,
			Name:                        name,
			DefaultIsFolder:             row.folder,
			DefaultMediaType:            row.media,
			SupportsPeople:              row.people,
			SupportsAncestors:           row.ancestors,
			SupportsUserData:            true,
			DefaultSupportsPlayedStatus: row.played,
			IsDisplayOnly:               row.displayOnly,
			ClientTypeName:              row.client,
		}
		caps := Capabilities{
			IsFolder:             row.folder,
			MediaType:            row.media,
			SupportsPeople:       row.people,
			SupportsAncestors:    row.ancestors,
			SupportsUserData:     true,
			SupportsPlayedStatus: row.played,
			IsDisplayOnly:        row.displayOnly,
		}
		cases = append(cases, definitionCase{kind: row.kind, def: def, caps: caps})
	}
	return cases
}

func TestDefinitionIsExhaustiveAndExact(t *testing.T) {
	cases := canonicalDefinitionCases()
	if len(cases) != 37 {
		t.Fatalf("definition table has %d kinds, want 37", len(cases))
	}
	seen := make(map[Kind]bool, len(cases))
	for _, tc := range cases {
		if seen[tc.kind] {
			t.Errorf("duplicate definition test row for %s", tc.kind)
		}
		seen[tc.kind] = true
		got, ok := Definition(tc.kind)
		if !ok {
			t.Errorf("Definition(%s) reported unknown", tc.kind)
			continue
		}
		if got != tc.def {
			t.Errorf("Definition(%s) = %#v, want %#v", tc.kind, got, tc.def)
		}
	}

	for _, invalid := range []Kind{Kind(37), Kind(255)} {
		if got, ok := Definition(invalid); ok || got != (KindDef{}) {
			t.Errorf("Definition(%d) = (%#v, %v), want (zero, false)", invalid, got, ok)
		}
	}
}

func TestDefaultCapabilitiesAndNarrowFunctionsAgree(t *testing.T) {
	// Definition defaults are zero-value item defaults. Dynamic departures are
	// separately specified by upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/MusicArtist.cs:27-48,72-73;
	// upstream/jellyfin/MediaBrowser.Controller/Playlists/Playlist.cs:72-87; upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:42-48.
	// Base user-data keys are unconditional at upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:1632-1645, so every known kind supports user data.
	for _, tc := range canonicalDefinitionCases() {
		t.Run(tc.kind.String(), func(t *testing.T) {
			got, ok := DefaultCapabilities(tc.kind)
			if !ok {
				t.Fatalf("DefaultCapabilities(%s) reported unknown", tc.kind)
			}
			if got != tc.caps {
				t.Errorf("DefaultCapabilities(%s) = %#v, want %#v", tc.kind, got, tc.caps)
			}

			itemCaps := CapabilitiesOf(Item{Kind: tc.kind})
			if itemCaps != tc.caps {
				t.Errorf("CapabilitiesOf(zero %s) = %#v, want %#v", tc.kind, itemCaps, tc.caps)
			}
			if got := IsFolder(Item{Kind: tc.kind}); got != tc.caps.IsFolder {
				t.Errorf("IsFolder(zero %s) = %v, want %v", tc.kind, got, tc.caps.IsFolder)
			}
			if got := MediaTypeOf(Item{Kind: tc.kind}); got != tc.caps.MediaType {
				t.Errorf("MediaTypeOf(zero %s) = %v, want %v", tc.kind, got, tc.caps.MediaType)
			}
			if got := SupportsPlayedStatus(Item{Kind: tc.kind}); got != tc.caps.SupportsPlayedStatus {
				t.Errorf("SupportsPlayedStatus(zero %s) = %v, want %v", tc.kind, got, tc.caps.SupportsPlayedStatus)
			}
			if got := SupportsPeople(Item{Kind: tc.kind}); got != tc.caps.SupportsPeople {
				t.Errorf("SupportsPeople(zero %s) = %v, want %v", tc.kind, got, tc.caps.SupportsPeople)
			}
			if got := SupportsAncestors(Item{Kind: tc.kind}); got != tc.caps.SupportsAncestors {
				t.Errorf("SupportsAncestors(zero %s) = %v, want %v", tc.kind, got, tc.caps.SupportsAncestors)
			}
			if !SupportsUserData(Item{Kind: tc.kind}) {
				t.Errorf("SupportsUserData(%s) = false, want true for every known kind", tc.kind)
			}
			if got := IsDisplayOnly(tc.kind); got != tc.caps.IsDisplayOnly {
				t.Errorf("IsDisplayOnly(%s) = %v, want %v", tc.kind, got, tc.caps.IsDisplayOnly)
			}
		})
	}

	for _, invalid := range []Kind{Kind(37), Kind(255)} {
		if got, ok := DefaultCapabilities(invalid); ok || got != (Capabilities{}) {
			t.Errorf("DefaultCapabilities(%d) = (%#v, %v), want (zero, false)", invalid, got, ok)
		}
		if got := CapabilitiesOf(Item{Kind: invalid}); got != (Capabilities{}) {
			t.Errorf("CapabilitiesOf(invalid %d) = %#v, want zero", invalid, got)
		}
		if IsFolder(Item{Kind: invalid}) || MediaTypeOf(Item{Kind: invalid}) != MediaTypeUnknown ||
			SupportsPlayedStatus(Item{Kind: invalid}) || SupportsPeople(Item{Kind: invalid}) ||
			SupportsAncestors(Item{Kind: invalid}) || SupportsUserData(Item{Kind: invalid}) || IsDisplayOnly(invalid) {
			t.Errorf("invalid kind %d exposed a capability", invalid)
		}
	}
}

func TestCapabilitiesOfHandlesItemDependentKinds(t *testing.T) {
	// upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/MusicArtist.cs:27-33 derives folderness from ParentId.Empty;
	// its explicit played/people exceptions are :47-48,72-73.
	artistWithoutParent := Item{Kind: KindMusicArtist}
	artistWithParent := Item{Kind: KindMusicArtist, ParentID: &dto.UUID{1}}
	if IsFolder(artistWithoutParent) || CapabilitiesOf(artistWithoutParent).IsFolder {
		t.Error("MusicArtist without parent must not be a folder")
	}
	if !IsFolder(artistWithParent) || !CapabilitiesOf(artistWithParent).IsFolder {
		t.Error("MusicArtist with parent must be a folder")
	}
	for _, artist := range []Item{artistWithoutParent, artistWithParent} {
		caps := CapabilitiesOf(artist)
		if caps.MediaType != MediaTypeUnknown || caps.SupportsPeople || !caps.SupportsAncestors ||
			!caps.SupportsUserData || caps.SupportsPlayedStatus || caps.IsDisplayOnly {
			t.Errorf("MusicArtist capabilities = %#v, want only folderness to vary", caps)
		}
	}

	// Source: upstream/jellyfin/MediaBrowser.Controller/Playlists/Playlist.cs:72-87.
	// Playlist media is its sole nullable Item.MediaType fact; a missing value is
	// Unknown, and only Video supports played status.
	for _, tc := range []struct {
		name   string
		media  *MediaType
		want   MediaType
		played bool
	}{
		{name: "nil media type remains unknown", want: MediaTypeUnknown},
		{name: "video supports played status", media: defsMediaType(MediaTypeVideo), want: MediaTypeVideo, played: true},
		{name: "audio does not support played status", media: defsMediaType(MediaTypeAudio), want: MediaTypeAudio},
		{name: "book does not support played status", media: defsMediaType(MediaTypeBook), want: MediaTypeBook},
	} {
		it := Item{Kind: KindPlaylist, MediaType: tc.media}
		caps := CapabilitiesOf(it)
		if caps.MediaType != tc.want || MediaTypeOf(it) != tc.want {
			t.Errorf("Playlist(%s) media = %s/%s, want %s", tc.name, caps.MediaType, MediaTypeOf(it), tc.want)
		}
		if caps.SupportsPlayedStatus != tc.played || SupportsPlayedStatus(it) != tc.played {
			t.Errorf("Playlist(%s) played = %v/%v, want %v", tc.name, caps.SupportsPlayedStatus, SupportsPlayedStatus(it), tc.played)
		}
		if !caps.IsFolder || caps.SupportsPeople || !caps.SupportsAncestors || !caps.SupportsUserData || caps.IsDisplayOnly {
			t.Errorf("Playlist(%s) changed a non-dynamic capability: %#v", tc.name, caps)
		}
	}

	// upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:42-48 makes radio audio and all other channel types video.
	// TvChannel maps to the same concrete upstream class (upstream/jellyfin/Emby.Server.Implementations/Data/ItemTypeLookup.cs:57),
	// so the alias obeys the same item-aware rule.
	for _, kind := range []Kind{KindLiveTvChannel, KindTvChannel} {
		for _, tc := range []struct {
			channel ChannelType
			media   MediaType
		}{
			{ChannelTypeTV, MediaTypeVideo},
			{ChannelTypeRadio, MediaTypeAudio},
		} {
			it := Item{Kind: kind, Extra: ExtraFields{LiveTV: &LiveTVExtra{ChannelType: tc.channel}}}
			caps := CapabilitiesOf(it)
			if caps.MediaType != tc.media || MediaTypeOf(it) != tc.media {
				t.Errorf("%s(%d) media = %s/%s, want %s", kind, tc.channel, caps.MediaType, MediaTypeOf(it), tc.media)
			}
			if caps.IsFolder || caps.SupportsPeople || !caps.SupportsAncestors || !caps.SupportsUserData || caps.SupportsPlayedStatus || caps.IsDisplayOnly {
				t.Errorf("%s(%d) changed a non-dynamic capability: %#v", kind, tc.channel, caps)
			}
		}
	}
}

func TestDisplayOnlyPolicyIsExact(t *testing.T) {
	// Deadfin's display-only policy is exactly Person, Genre, Studio, MusicGenre,
	// and Year (docs/01-architecture.md:168-170); it is not an upstream entity capability.
	displayOnly := map[Kind]bool{
		KindPerson:     true,
		KindGenre:      true,
		KindStudio:     true,
		KindMusicGenre: true,
		KindYear:       true,
	}
	for _, tc := range canonicalDefinitionCases() {
		if got, want := IsDisplayOnly(tc.kind), displayOnly[tc.kind]; got != want {
			t.Errorf("IsDisplayOnly(%s) = %v, want %v", tc.kind, got, want)
		}
	}
	if IsDisplayOnly(Kind(255)) {
		t.Error("invalid Kind must not be display-only")
	}
}

func TestDefinitionAndCapabilitiesReturnIndependentValues(t *testing.T) {
	// The API returns value copies: callers must not be able to alter later
	// definitions or capability results through a returned value.
	wantDef := canonicalDefinitionCases()[0].def
	firstDef, ok := Definition(KindAggregateFolder)
	if !ok {
		t.Fatal("Definition(AggregateFolder) reported unknown")
	}
	firstDef.Kind = KindYear
	firstDef.Name = "corrupt"
	firstDef.DefaultIsFolder = false
	firstDef.SupportsUserData = false
	secondDef, ok := Definition(KindAggregateFolder)
	if !ok || secondDef != wantDef {
		t.Errorf("Definition return was not independent: got (%#v, %v), want (%#v, true)", secondDef, ok, wantDef)
	}

	firstCaps, ok := DefaultCapabilities(KindVideo)
	if !ok {
		t.Fatal("DefaultCapabilities(Video) reported unknown")
	}
	firstCaps.MediaType = MediaTypeBook
	firstCaps.IsFolder = true
	firstCaps.SupportsUserData = false
	secondCaps, ok := DefaultCapabilities(KindVideo)
	wantCaps := Capabilities{MediaType: MediaTypeVideo, SupportsPeople: true, SupportsAncestors: true, SupportsUserData: true, SupportsPlayedStatus: true}
	if !ok || secondCaps != wantCaps {
		t.Errorf("DefaultCapabilities return was not independent: got (%#v, %v), want (%#v, true)", secondCaps, ok, wantCaps)
	}
}

func defsMediaType(value MediaType) *MediaType { return &value }
