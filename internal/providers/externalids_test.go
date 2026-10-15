// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// TestExternalIDDescriptorParity verifies the external-ID descriptor table
// matches the pinned IExternalId classes exactly: Name, Key,
// Type, and the supported item-kind set for every provider Deadfin ports.
func TestExternalIDDescriptorParity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		providerName string
		key          string
		typeStr      *string
		supports     []item.Kind
	}{
		// IMDb
		{"ImdbDefault", "IMDb", "Imdb", nil, []item.Kind{item.KindMovie, item.KindMusicVideo, item.KindSeries, item.KindEpisode, item.KindTrailer}},
		{"ImdbPerson", "IMDb", "Imdb", strPtr("Person"), []item.Kind{item.KindPerson}},
		// TMDb
		{"TmdbMovie", "TheMovieDb", "Tmdb", strPtr("Movie"), []item.Kind{item.KindMovie}},
		{"TmdbSeries", "TheMovieDb", "Tmdb", strPtr("Series"), []item.Kind{item.KindSeries}},
		{"TmdbSeason", "TheMovieDb", "Tmdb", strPtr("Season"), []item.Kind{item.KindSeason}},
		{"TmdbEpisode", "TheMovieDb", "Tmdb", strPtr("Episode"), []item.Kind{item.KindEpisode}},
		{"TmdbPerson", "TheMovieDb", "Tmdb", strPtr("Person"), []item.Kind{item.KindPerson}},
		{"TmdbBoxSet", "TheMovieDb", "TmdbCollection", strPtr("BoxSet"), []item.Kind{item.KindMovie, item.KindMusicVideo, item.KindTrailer}},
		// MusicBrainz
		{"MbArtist", "MusicBrainz", "MusicBrainzArtist", strPtr("Artist"), []item.Kind{item.KindMusicArtist}},
		{"MbOtherArtist", "MusicBrainz", "MusicBrainzArtist", strPtr("OtherArtist"), []item.Kind{item.KindAudio, item.KindMusicAlbum}},
		{"MbReleaseGroup", "MusicBrainz", "MusicBrainzReleaseGroup", strPtr("ReleaseGroup"), []item.Kind{item.KindAudio, item.KindMusicAlbum}},
		{"MbAlbumArtist", "MusicBrainz", "MusicBrainzAlbumArtist", strPtr("AlbumArtist"), []item.Kind{item.KindAudio}},
		{"MbAlbum", "MusicBrainz", "MusicBrainzAlbum", strPtr("Album"), []item.Kind{item.KindAudio, item.KindMusicAlbum}},
		{"MbRecording", "MusicBrainz", "MusicBrainzRecording", strPtr("Recording"), []item.Kind{item.KindAudio}},
		{"MbTrack", "MusicBrainz", "MusicBrainzTrack", strPtr("Track"), []item.Kind{item.KindAudio}},
		// TheAudioDb
		{"AudioDbArtist", "TheAudioDb", "AudioDbArtist", strPtr("Artist"), []item.Kind{item.KindMusicArtist}},
		{"AudioDbOtherArtist", "TheAudioDb", "AudioDbArtist", strPtr("OtherArtist"), []item.Kind{item.KindAudio, item.KindMusicAlbum}},
		{"AudioDbAlbum", "TheAudioDb", "AudioDbAlbum", nil, []item.Kind{item.KindMusicAlbum}},
		{"AudioDbOtherAlbum", "TheAudioDb", "AudioDbAlbum", strPtr("Album"), []item.Kind{item.KindAudio}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc := findDescriptor(t, tc.providerName, tc.key, tc.typeStr)
			for _, kind := range tc.supports {
				if !desc.Supports(kind) {
					t.Errorf("%s: Supports(%v) = false, want true", tc.name, kind)
				}
			}
			// Spot-check an unsupported kind is false.
			for _, kind := range allKinds() {
				if !contains(tc.supports, kind) && desc.Supports(kind) {
					t.Errorf("%s: Supports(%v) = true, want false", tc.name, kind)
				}
			}
		})
	}
}

// TestGetExternalIdInfosFiltersByKind verifies GetExternalIdInfos returns only
// the descriptors whose Supports matches the kind (ProviderManager.cs:1002-1016).
func TestGetExternalIdInfosFiltersByKind(t *testing.T) {
	movieDescs := providers.GetExternalIDInfos(item.KindMovie)
	if len(movieDescs) == 0 {
		t.Fatal("GetExternalIdInfos(Movie) returned no descriptors")
	}
	for _, d := range movieDescs {
		if !d.Supports(item.KindMovie) {
			t.Errorf("GetExternalIdInfos(Movie) returned descriptor %q/%q that does not support Movie", d.ProviderName, d.Key)
		}
	}
	// Movie should include IMDb default, TMDb Movie, and TMDb BoxSet (supports Movie).
	foundImdb := false
	foundTmdbMovie := false
	foundTmdbBoxSet := false
	for _, d := range movieDescs {
		if d.ProviderName == "IMDb" && d.Key == "Imdb" && d.Type == nil {
			foundImdb = true
		}
		if d.ProviderName == "TheMovieDb" && d.Key == "Tmdb" && d.Type != nil && *d.Type == "Movie" {
			foundTmdbMovie = true
		}
		if d.ProviderName == "TheMovieDb" && d.Key == "TmdbCollection" && d.Type != nil && *d.Type == "BoxSet" {
			foundTmdbBoxSet = true
		}
	}
	if !foundImdb {
		t.Error("Movie external IDs missing IMDb default")
	}
	if !foundTmdbMovie {
		t.Error("Movie external IDs missing TMDb Movie")
	}
	if !foundTmdbBoxSet {
		t.Error("Movie external IDs missing TMDb BoxSet (supports Movie)")
	}
}

// TestExternalIDDescriptorCardinality asserts the exact total count of
// descriptors matches the pinned IExternalId classes. The
// table has two IMDb, six TMDb, seven MusicBrainz (Artist, OtherArtist,
// ReleaseGroup, AlbumArtist, Album, Recording, Track), and four AudioDb =
// 19 entries total.
func TestExternalIDDescriptorCardinality(t *testing.T) {
	const want = 19
	if got := len(providers.ExternalIDDescriptors()); got != want {
		t.Fatalf("ExternalIDDescriptors cardinality = %d, want %d (2 IMDb + 6 TMDb + 7 MusicBrainz + 4 AudioDb)", got, want)
	}
}

// TestGetExternalIdInfosAudioIncludesRecordingAndTrack verifies Audio returns
// the MusicBrainz Recording and Track descriptors.
func TestGetExternalIdInfosAudioIncludesRecordingAndTrack(t *testing.T) {
	audioDescs := providers.GetExternalIDInfos(item.KindAudio)
	foundRecording := false
	foundTrack := false
	for _, d := range audioDescs {
		if d.ProviderName == "MusicBrainz" && d.Key == "MusicBrainzRecording" && d.Type != nil && *d.Type == "Recording" {
			foundRecording = true
		}
		if d.ProviderName == "MusicBrainz" && d.Key == "MusicBrainzTrack" && d.Type != nil && *d.Type == "Track" {
			foundTrack = true
		}
	}
	if !foundRecording {
		t.Error("Audio external IDs missing MusicBrainz Recording")
	}
	if !foundTrack {
		t.Error("Audio external IDs missing MusicBrainz Track")
	}
}

func findDescriptor(t *testing.T, providerName, key string, typeStr *string) providers.ExternalIDDescriptor {
	t.Helper()
	for _, d := range providers.ExternalIDDescriptors() {
		if d.ProviderName == providerName && d.Key == key && ptrEqual(d.Type, typeStr) {
			return d
		}
	}
	t.Fatalf("descriptor not found: %s/%s type=%v", providerName, key, typeStr)
	return providers.ExternalIDDescriptor{}
}

func ptrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func contains(kinds []item.Kind, k item.Kind) bool {
	for _, k2 := range kinds {
		if k2 == k {
			return true
		}
	}
	return false
}

func allKinds() []item.Kind {
	return []item.Kind{
		item.KindMovie, item.KindSeries, item.KindSeason, item.KindEpisode,
		item.KindPerson, item.KindBoxSet, item.KindTrailer, item.KindMusicVideo,
		item.KindMusicArtist, item.KindMusicAlbum, item.KindAudio, item.KindBook,
	}
}

func strPtr(s string) *string { return &s }
