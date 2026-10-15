// SPDX-License-Identifier: GPL-2.0-only

package providers

import (
	"github.com/deadfin-org/deadfin/internal/item"
)

// ExternalIDDescriptor is one entry in the static external-id table, ported from
// the pinned IExternalId classes. Type is nil for the provider-independent
// default entry (for example, IMDb's movie id); non-nil for kind-specific ids
// (for example, TmdbSeriesExternalId with Type "Series"). Supports filters by
// item kind, mirroring ProviderManager.GetSupportedExternalIds
// (ProviderManager.cs:1002-1016).
type ExternalIDDescriptor struct {
	ProviderName string
	Key          string
	Type         *string
	Supports     func(item.Kind) bool
}

// ExternalIDDescriptors is the static table of external-id descriptors ported
// verbatim from the pinned IExternalId classes for providers Deadfin ports,
// plus the provider-independent IMDb entries. The order mirrors upstream
// registration so GetExternalIdInfos output is deterministic.
//
// IMDb (provider-independent):
//   - ImdbExternalId: Name "IMDb", Key "Imdb", Type null, supports
//     Movie/MusicVideo/Series/Episode/Trailer (ImdbExternalId.cs:Supports).
//   - ImdbPersonExternalId: Name "IMDb", Key "Imdb", Type "Person", supports Person.
//
// TMDb (Name "TheMovieDb", TmdbUtils.ProviderName):
//   - TmdbMovieExternalId: Key "Tmdb", Type "Movie", supports Movie.
//   - TmdbSeriesExternalId: Key "Tmdb", Type "Series", supports Series.
//   - TmdbSeasonExternalId: Key "Tmdb", Type "Season", supports Season.
//   - TmdbEpisodeExternalId: Key "Tmdb", Type "Episode", supports Episode.
//   - TmdbPersonExternalId: Key "Tmdb", Type "Person", supports Person.
//   - TmdbBoxSetExternalId: Key "TmdbCollection", Type "BoxSet", supports
//     Movie/MusicVideo/Trailer (TmdbBoxSetExternalId.cs:Supports — it records the
//     collection a movie/music-video/trailer belongs to, not BoxSet items).
//
// MusicBrainz (Name "MusicBrainz"):
//   - MusicBrainzArtistExternalId: Key "MusicBrainzArtist", Type "Artist", supports MusicArtist.
//   - MusicBrainzOtherArtistExternalId: Key "MusicBrainzArtist", Type "OtherArtist",
//     supports Audio/MusicAlbum.
//   - MusicBrainzReleaseGroupExternalId: Key "MusicBrainzReleaseGroup", Type "ReleaseGroup",
//     supports Audio/MusicAlbum.
//   - MusicBrainzAlbumArtistExternalId: Key "MusicBrainzAlbumArtist", Type "AlbumArtist",
//     supports Audio.
//   - MusicBrainzAlbumExternalId: Key "MusicBrainzAlbum", Type "Album", supports Audio/MusicAlbum.
//   - MusicBrainzRecordingId: Key "MusicBrainzRecording", Type "Recording", supports Audio.
//   - MusicBrainzTrackId: Key "MusicBrainzTrack", Type "Track", supports Audio.
//
// TheAudioDb (Name "TheAudioDb"):
//   - AudioDbArtistExternalId: Key "AudioDbArtist", Type "Artist", supports MusicArtist.
//   - AudioDbOtherArtistExternalId: Key "AudioDbArtist", Type "OtherArtist", supports Audio/MusicAlbum.
//   - AudioDbAlbumExternalId: Key "AudioDbAlbum", Type null, supports MusicAlbum.
//   - AudioDbOtherAlbumExternalId: Key "AudioDbAlbum", Type "Album", supports Audio.
//
// Season/Episode are included (TMDb) since the pinned tree declares them;
// Audio (KindAudio) maps upstream's Audio entity. Recording/Track are included
// from MusicBrainzRecordingId/MusicBrainzTrackId (both support Audio).
//
// It is a function, not a package-level var, so no caller can mutate the
// shared table (CONVENTIONS 1.1).
func ExternalIDDescriptors() []ExternalIDDescriptor {
	return []ExternalIDDescriptor{
		// IMDb (provider-independent default)
		{ProviderName: "IMDb", Key: "Imdb", Type: nil, Supports: func(k item.Kind) bool {
			switch k {
			case item.KindMovie, item.KindMusicVideo, item.KindSeries, item.KindEpisode, item.KindTrailer:
				return true
			}
			return false
		}},
		{ProviderName: "IMDb", Key: "Imdb", Type: stringPtr("Person"), Supports: func(k item.Kind) bool {
			return k == item.KindPerson
		}},

		// TMDb (Name "TheMovieDb")
		{ProviderName: "TheMovieDb", Key: "Tmdb", Type: stringPtr("Movie"), Supports: func(k item.Kind) bool {
			return k == item.KindMovie
		}},
		{ProviderName: "TheMovieDb", Key: "Tmdb", Type: stringPtr("Series"), Supports: func(k item.Kind) bool {
			return k == item.KindSeries
		}},
		{ProviderName: "TheMovieDb", Key: "Tmdb", Type: stringPtr("Season"), Supports: func(k item.Kind) bool {
			return k == item.KindSeason
		}},
		{ProviderName: "TheMovieDb", Key: "Tmdb", Type: stringPtr("Episode"), Supports: func(k item.Kind) bool {
			return k == item.KindEpisode
		}},
		{ProviderName: "TheMovieDb", Key: "Tmdb", Type: stringPtr("Person"), Supports: func(k item.Kind) bool {
			return k == item.KindPerson
		}},
		{ProviderName: "TheMovieDb", Key: "TmdbCollection", Type: stringPtr("BoxSet"), Supports: func(k item.Kind) bool {
			switch k {
			case item.KindMovie, item.KindMusicVideo, item.KindTrailer:
				return true
			}
			return false
		}},

		// MusicBrainz (Name "MusicBrainz")
		{ProviderName: "MusicBrainz", Key: "MusicBrainzArtist", Type: stringPtr("Artist"), Supports: func(k item.Kind) bool {
			return k == item.KindMusicArtist
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzArtist", Type: stringPtr("OtherArtist"), Supports: func(k item.Kind) bool {
			switch k {
			case item.KindAudio, item.KindMusicAlbum:
				return true
			}
			return false
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzReleaseGroup", Type: stringPtr("ReleaseGroup"), Supports: func(k item.Kind) bool {
			switch k {
			case item.KindAudio, item.KindMusicAlbum:
				return true
			}
			return false
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzAlbumArtist", Type: stringPtr("AlbumArtist"), Supports: func(k item.Kind) bool {
			return k == item.KindAudio
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzAlbum", Type: stringPtr("Album"), Supports: func(k item.Kind) bool {
			switch k {
			case item.KindAudio, item.KindMusicAlbum:
				return true
			}
			return false
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzRecording", Type: stringPtr("Recording"), Supports: func(k item.Kind) bool {
			return k == item.KindAudio
		}},
		{ProviderName: "MusicBrainz", Key: "MusicBrainzTrack", Type: stringPtr("Track"), Supports: func(k item.Kind) bool {
			return k == item.KindAudio
		}},

		// TheAudioDb (Name "TheAudioDb")
		{ProviderName: "TheAudioDb", Key: "AudioDbArtist", Type: stringPtr("Artist"), Supports: func(k item.Kind) bool {
			return k == item.KindMusicArtist
		}},
		{ProviderName: "TheAudioDb", Key: "AudioDbArtist", Type: stringPtr("OtherArtist"), Supports: func(k item.Kind) bool {
			switch k {
			case item.KindAudio, item.KindMusicAlbum:
				return true
			}
			return false
		}},
		{ProviderName: "TheAudioDb", Key: "AudioDbAlbum", Type: nil, Supports: func(k item.Kind) bool {
			return k == item.KindMusicAlbum
		}},
		{ProviderName: "TheAudioDb", Key: "AudioDbAlbum", Type: stringPtr("Album"), Supports: func(k item.Kind) bool {
			return k == item.KindAudio
		}},
	}
}

// GetExternalIDInfos filters the static descriptor table by Supports(kind)
// (ProviderManager.cs:1002-1016). The wire shape is {Name, Key, Type} only — no
// URL format string.
func GetExternalIDInfos(kind item.Kind) []ExternalIDDescriptor {
	descriptors := ExternalIDDescriptors()
	result := make([]ExternalIDDescriptor, 0, len(descriptors))
	for _, desc := range descriptors {
		if desc.Supports != nil && desc.Supports(kind) {
			result = append(result, desc)
		}
	}
	return result
}

func stringPtr(s string) *string { return &s }
