// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// Sources: upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:99-110
// constructs ProviderIds with StringComparer.OrdinalIgnoreCase; :1632-1646 defines base key ordering.
// upstream/jellyfin/MediaBrowser.Controller/Entities/TV/Season.cs:103-119 appends its suffix
// to every series key verbatim. upstream/jellyfin/MediaBrowser.Controller/Entities/Audio/Audio.cs:100-126
// inserts the unnormalized song key.
func TestUserDataKeys(t *testing.T) {
	baseID := keysUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	seriesID := keysUUID(t, "11112222-3333-4444-5555-666677778888")

	tests := []struct {
		name string
		item Item
		in   UserDataInputs
		want []string
	}{
		{
			name: "base uses dashed GUID",
			item: Item{ID: baseID, Kind: KindFolder},
			want: []string{"00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "channel external ID precedes dashed GUID",
			item: Item{ID: baseID, Kind: KindFolder, SourceType: SourceTypeChannel, ExternalID: keysString("channel/42")},
			want: []string{"channel/42", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "empty channel external ID is absent",
			item: Item{ID: baseID, Kind: KindFolder, SourceType: SourceTypeChannel},
			want: []string{"00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "movie provider priority ignores provider-key casing",
			item: Item{ID: baseID, Kind: KindMovie, ProviderIDs: providerIDs(map[string]string{
				"iMdB": "tt-7", "tMdB": "tm-8",
			})},
			want: []string{"tm-8", "tt-7", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "video ignores empty providers but matches keys without case sensitivity",
			item: Item{ID: baseID, Kind: KindVideo, ProviderIDs: providerIDs(map[string]string{
				"iMdB": "", "tMdB": " ",
			})},
			want: []string{" ", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "video extra keys reverse prepend and include runtime ticks",
			item: Item{ID: baseID, Kind: KindMovie, ExtraType: keysExtraType(ExtraTypeBehindTheScenes), RunTimeTicks: keysInt64(9001), ProviderIDs: providerIDs(map[string]string{
				"Imdb": "tt-7", "Tmdb": "tm-8",
			})},
			want: []string{"tt-7-behindthescenes-9001", "tm-8-behindthescenes-9001", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "series provider priority ignores provider-key casing",
			item: Item{ID: seriesID, Kind: KindSeries, ProviderIDs: providerIDs(map[string]string{
				"iMdB": "tt-show", "tVdB": "tv-show", "cUsToM": "custom-show",
			})},
			want: []string{"custom-show", "tv-show", "tt-show", "11112222-3333-4444-5555-666677778888"},
		},
		{
			name: "episode prefixes series provider keys but drops series GUID",
			item: Item{ID: baseID, Kind: KindEpisode, ParentIndexNumber: keysInt(2), IndexNumber: keysInt(3)},
			in: UserDataInputs{Series: &Item{ID: seriesID, Kind: KindSeries, ProviderIDs: providerIDs(map[string]string{
				"Imdb": "tt-show", "Tvdb": "tv-show", "Custom": "custom-show",
			})}},
			want: []string{"custom-show002003", "tv-show002003", "tt-show002003", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "episode leaves only its base key without complete series context",
			item: Item{ID: baseID, Kind: KindEpisode, ParentIndexNumber: keysInt(2)},
			in:   UserDataInputs{Series: &Item{ID: seriesID, Kind: KindSeries}},
			want: []string{"00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "season prefixes every series key including its GUID and zero index",
			item: Item{ID: baseID, Kind: KindSeason},
			in: UserDataInputs{Series: &Item{ID: seriesID, Kind: KindSeries, ProviderIDs: providerIDs(map[string]string{
				"Custom": "custom-show",
			})}},
			want: []string{"custom-show000", "11112222-3333-4444-5555-666677778888000", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "audio key retains punctuation case whitespace and diacritics",
			item: Item{ID: baseID, Kind: KindAudio, Name: keysString("Béla: Part 1"), Album: keysString("Cœur"), Values: ItemValues{AlbumArtists: []string{"Björk"}}, ParentIndexNumber: keysInt(4), IndexNumber: keysInt(7)},
			want: []string{"Björk-Cœur-0004-0007Béla: Part 1", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "audiobook inherits unnormalized audio key construction",
			item: Item{ID: baseID, Kind: KindAudioBook, Name: keysString("Béla Book"), Album: keysString("Séries"), Values: ItemValues{AlbumArtists: []string{"Aùthor"}}, IndexNumber: keysInt(1)},
			want: []string{"Aùthor-Séries-0001Béla Book", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "artist musicbrainz casing and mapped name",
			item: Item{ID: baseID, Kind: KindMusicArtist, Name: keysString("cœur"), ProviderIDs: providerIDs(map[string]string{"MusicBrainzArtist": "mb-artist"})},
			want: []string{"Artist-Musicbrainz-mb-artist", "Artist-coeur", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "album release group wins album and artist name",
			item: Item{ID: baseID, Kind: KindMusicAlbum, Name: keysString("Album"), Values: ItemValues{AlbumArtists: []string{"Artist"}}, ProviderIDs: providerIDs(map[string]string{
				"MusicBrainzAlbum": "album-id", "MusicBrainzReleaseGroup": "release-group-id",
			})},
			want: []string{"MusicAlbum-MusicBrainzReleaseGroup-release-group-id", "MusicAlbum-Musicbrainz-album-id", "Artist-Album", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "person uses runtime type spelling and mapping only",
			item: Item{ID: baseID, Kind: KindPerson, Name: keysString("Béla / Person")},
			want: []string{"Person-Bela / Person", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "studio uses runtime type spelling and mapping only",
			item: Item{ID: baseID, Kind: KindStudio, Name: keysString("Béla / Studio")},
			want: []string{"Studio-Bela / Studio", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "genre uses runtime type spelling and mapping only",
			item: Item{ID: baseID, Kind: KindGenre, Name: keysString("Béla / Genre")},
			want: []string{"Genre-Bela / Genre", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "music genre uses runtime type spelling and mapping only",
			item: Item{ID: baseID, Kind: KindMusicGenre, Name: keysString("Béla / Rock")},
			want: []string{"MusicGenre-Bela / Rock", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "year key does not remove diacritics",
			item: Item{ID: baseID, Kind: KindYear, Name: keysString("20é4")},
			want: []string{"Year-20é4", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "live TV channel prefixes its client type name by default",
			item: Item{ID: baseID, Kind: KindLiveTvChannel, Name: keysString("Channel One")},
			want: []string{"TvChannel-Channel One", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "live TV channel name key can be disabled",
			item: Item{ID: baseID, Kind: KindLiveTvChannel, Name: keysString("Channel One")},
			in:   UserDataInputs{DisableLiveTVChannelUserDataName: true},
			want: []string{"00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "live TV nonseries provider priority",
			item: Item{ID: baseID, Kind: KindLiveTvProgram, ProviderIDs: providerIDs(map[string]string{"Imdb": "tt-program", "Tmdb": "tm-program"})},
			want: []string{"tm-program", "tt-program", "00112233-4455-6677-8899-aabbccddeeff"},
		},
		{
			name: "live TV series program concatenates name and episode title without separator",
			item: Item{ID: baseID, Kind: KindLiveTvProgram, IsSeries: true, Name: keysString("News"), EpisodeTitle: keysString("At Noon")},
			want: []string{"Program-NewsAt Noon", "00112233-4455-6677-8899-aabbccddeeff"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UserDataKeys(tt.item, tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("UserDataKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// Sources: BaseItem.cs:1615-1618; Series.cs:79-110; Season.cs:137-148,
// 237-242; Video.cs:434-442; and MusicArtist.cs:152-155.
func TestPresentationUniqueKey(t *testing.T) {
	baseID := keysUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	primaryID := keysUUID(t, "10213243-5465-7687-98a9-bacbdcedfe0f")
	folderA := keysUUID(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	folderB := keysUUID(t, "11111111-2222-3333-4444-555555555555")

	tests := []struct {
		name string
		item Item
		in   PresentationInputs
		want string
	}{
		{"stored empty wins", Item{ID: baseID, Kind: KindMovie, PresentationUniqueKey: keysString("")}, PresentationInputs{}, ""},
		{"stored value wins", Item{ID: baseID, Kind: KindMovie, PresentationUniqueKey: keysString("stored-key")}, PresentationInputs{}, "stored-key"},
		{"stored nil computes the item N GUID", Item{ID: baseID, Kind: KindMovie, PresentationUniqueKey: nil}, PresentationInputs{}, "00112233445566778899aabbccddeeff"},
		{"video primary version uses N GUID", Item{ID: baseID, Kind: KindMovie, PrimaryVersionID: &primaryID}, PresentationInputs{}, "102132435465768798a9bacbdcedfe0f"},
		{
			name: "series grouping preserves caller folder order",
			item: Item{ID: baseID, Kind: KindSeries, PreferredMetadataLanguage: keysString("fr"), ProviderIDs: providerIDs(map[string]string{"Tvdb": "tv-show"})},
			in:   PresentationInputs{SeriesGrouping: SeriesGroupingInputs{EnableAutomaticGrouping: true, CollectionFolderIDs: []dto.UUID{folderA, folderB}}},
			want: "tv-show-fr-aaaaaaaabbbbccccddddeeeeeeeeeeee-11111111222233334444555555555555",
		},
		{
			name: "season uses resolved series stored nil as empty rather than computing fallback",
			item: Item{ID: baseID, Kind: KindSeason, IndexNumber: keysInt(1)},
			in:   PresentationInputs{Series: &Item{ID: primaryID, Kind: KindSeries, PresentationUniqueKey: nil}},
			want: "-001",
		},
		{
			name: "season uses resolved series stored empty verbatim",
			item: Item{ID: baseID, Kind: KindSeason, IndexNumber: keysInt(2)},
			in:   PresentationInputs{Series: &Item{ID: primaryID, Kind: KindSeries, PresentationUniqueKey: keysString("")}},
			want: "-002",
		},
		{"artist presentation key is name mapping only", Item{ID: baseID, Kind: KindMusicArtist, Name: keysString("cœur"), ProviderIDs: providerIDs(map[string]string{"MusicBrainzArtist": "mb"})}, PresentationInputs{}, "Artist-coeur"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PresentationUniqueKey(tt.item, tt.in); got != tt.want {
				t.Fatalf("PresentationUniqueKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSeriesPresentationUniqueKey(t *testing.T) {
	stored := "series-key"
	empty := ""
	tests := []struct {
		name string
		item Item
		in   PresentationInputs
		want *string
	}{
		{"episode without resolved series is nil", Item{Kind: KindEpisode}, PresentationInputs{}, nil},
		{"episode preserves resolved stored nil", Item{Kind: KindEpisode}, PresentationInputs{Series: &Item{Kind: KindSeries}}, nil},
		{"season preserves resolved stored empty", Item{Kind: KindSeason}, PresentationInputs{Series: &Item{Kind: KindSeries, PresentationUniqueKey: &empty}}, &empty},
		{"episode returns resolved stored value", Item{Kind: KindEpisode}, PresentationInputs{Series: &Item{Kind: KindSeries, PresentationUniqueKey: &stored}}, &stored},
		{"book returns stored series presentation key", Item{Kind: KindBook, SeriesPresentationUniqueKey: &stored}, PresentationInputs{}, &stored},
		{"audiobook returns stored series presentation key", Item{Kind: KindAudioBook, SeriesPresentationUniqueKey: &stored}, PresentationInputs{}, &stored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeriesPresentationUniqueKey(tt.item, tt.in)
			if !keysEqualStringPointer(got, tt.want) {
				t.Fatalf("SeriesPresentationUniqueKey() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// Upstream returns immutable strings: upstream/jellyfin/MediaBrowser.Controller/Entities/TV/Episode.cs:189-191;
// upstream/jellyfin/MediaBrowser.Controller/Entities/TV/Season.cs:237-242; upstream/jellyfin/MediaBrowser.Controller/Entities/Book.cs:45-48;
// and upstream/jellyfin/MediaBrowser.Controller/Entities/AudioBook.cs:40-43. The Go pointer API must therefore return
// independent copies rather than expose caller-owned item state.
func TestSeriesPresentationUniqueKeyReturnsIndependentCopy(t *testing.T) {
	tests := []struct {
		name  string
		build func(*string) (Item, PresentationInputs)
	}{
		{
			name: "episode resolved series key",
			build: func(stored *string) (Item, PresentationInputs) {
				return Item{Kind: KindEpisode}, PresentationInputs{Series: &Item{Kind: KindSeries, PresentationUniqueKey: stored}}
			},
		},
		{
			name: "season resolved series key",
			build: func(stored *string) (Item, PresentationInputs) {
				return Item{Kind: KindSeason}, PresentationInputs{Series: &Item{Kind: KindSeries, PresentationUniqueKey: stored}}
			},
		},
		{
			name: "book stored series key",
			build: func(stored *string) (Item, PresentationInputs) {
				return Item{Kind: KindBook, SeriesPresentationUniqueKey: stored}, PresentationInputs{}
			},
		},
		{
			name: "audiobook stored series key",
			build: func(stored *string) (Item, PresentationInputs) {
				return Item{Kind: KindAudioBook, SeriesPresentationUniqueKey: stored}, PresentationInputs{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := "stored-series-key"
			item, in := tt.build(&stored)
			got := SeriesPresentationUniqueKey(item, in)
			if got == nil || *got != stored {
				t.Fatalf("SeriesPresentationUniqueKey() = %#v, want independent %q", got, stored)
			}

			*got = "mutated-result"
			if stored != "stored-series-key" {
				t.Fatalf("mutating returned key changed item/input state to %q", stored)
			}
		})
	}
}

// Sources: BaseItem.cs:1951-1958; PlaylistsFolder.cs:51-54;
// LiveTvChannel.cs:118-121; LiveTvProgram.cs:205-208.
func TestClientTypeName(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
	}{
		{"playlist folder alias", Item{Kind: KindPlaylistsFolder}, "ManualPlaylistsFolder"},
		{"live TV channel alias", Item{Kind: KindLiveTvChannel}, "TvChannel"},
		{"legacy TV channel alias", Item{Kind: KindTvChannel}, "TvChannel"},
		{"live TV program alias", Item{Kind: KindLiveTvProgram}, "Program"},
		{"legacy TV program alias", Item{Kind: KindTvProgram}, "Program"},
		{"program alias", Item{Kind: KindProgram}, "Program"},
		{"channel sourced folder alias", Item{Kind: KindFolder, SourceType: SourceTypeChannel}, "ChannelFolderItem"},
		{"channel itself is not folder alias", Item{Kind: KindChannel, SourceType: SourceTypeChannel}, "Channel"},
		{"season itself is not folder alias", Item{Kind: KindSeason, SourceType: SourceTypeChannel}, "Season"},
		{"series itself is not folder alias", Item{Kind: KindSeries, SourceType: SourceTypeChannel}, "Series"},
		{"recording remains its concrete client name", Item{Kind: KindRecording}, "Recording"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClientTypeName(tt.item); got != tt.want {
				t.Fatalf("ClientTypeName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func keysUUID(t *testing.T, s string) dto.UUID {
	t.Helper()
	u, err := dto.ParseUUID(s)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", s, err)
	}
	return u
}

func keysString(s string) *string              { return &s }
func keysInt(n int) *int                       { return &n }
func keysInt64(n int64) *int64                 { return &n }
func keysExtraType(value ExtraType) *ExtraType { return &value }

// providerIDs routes legacy map-shaped fixtures through the public mutation
// seam, so Item composites cannot bypass ProviderIDs case normalization.
func providerIDs(entries map[string]string) ProviderIDs {
	var providers ProviderIDs
	for name, value := range entries {
		providers.Set(name, value)
	}
	return providers
}

func keysEqualStringPointer(got, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}
