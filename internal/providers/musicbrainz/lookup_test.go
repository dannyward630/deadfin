// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/musicbrainz"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
)

func newCassetteLookup(t *testing.T, cassettePath string) *musicbrainz.LookupProvider {
	t.Helper()
	cassette, err := remotetest.Load(cassettePath)
	if err != nil {
		t.Fatalf("load cassette: %v", err)
	}
	return musicbrainz.NewLookupProvider(musicbrainz.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: cassette, CacheEntries: 32}),
		Config: musicbrainz.DefaultConfig(),
	})
}

// TestItemLookupSongInfoFallbacksAndResultProjection verifies MusicBrainz
// album search issues the release-group lookup before generic name search
// when a MusicBrainzReleaseGroup id is carried in the Lookup or SongInfos,
// and that the returned result projects AlbumArtist and Artists with the
// pinned MusicBrainzArtist provider IDs.
func TestItemLookupSongInfoFallbacksAndResultProjection(t *testing.T) {
	// --- Release-group fallback from Lookup.ProviderIDs ---
	provider := newCassetteLookup(t, "testdata/lookup-release-group.json")
	if !provider.SearchSupports(item.KindMusicAlbum) {
		t.Fatal("SearchSupports(MusicAlbum) = false, want true")
	}
	results, err := provider.Search(context.Background(), providers.Lookup{
		Kind:        item.KindMusicAlbum,
		Name:        "RG Album",
		ProviderIDs: map[string]string{"MusicBrainzReleaseGroup": "test-rg-id"},
	})
	if err != nil {
		t.Fatalf("Search (release-group): %v", err)
	}
	if len(results) == 0 {
		t.Fatal("release-group fallback returned no results")
	}
	r := results[0]
	if r.ProviderIDs["MusicBrainzAlbum"] != "release-1" {
		t.Errorf("ProviderIDs[MusicBrainzAlbum] = %q, want release-1", r.ProviderIDs["MusicBrainzAlbum"])
	}
	if r.ProviderIDs["MusicBrainzReleaseGroup"] != "test-rg-id" {
		t.Errorf("ProviderIDs[MusicBrainzReleaseGroup] = %q, want test-rg-id", r.ProviderIDs["MusicBrainzReleaseGroup"])
	}
	// Artists must carry MusicBrainzArtist IDs.
	if len(r.Artists) == 0 {
		t.Fatal("Artists empty, want at least one artist with MusicBrainzArtist ID")
	}
	if r.Artists[0].ProviderIDs["MusicBrainzArtist"] != "rg-artist-id" {
		t.Errorf("Artists[0].ProviderIDs[MusicBrainzArtist] = %q, want rg-artist-id", r.Artists[0].ProviderIDs["MusicBrainzArtist"])
	}
	if r.AlbumArtist == nil || r.AlbumArtist.ProviderIDs["MusicBrainzArtist"] != "rg-artist-id" {
		t.Errorf("AlbumArtist provider ID = %v, want rg-artist-id", r.AlbumArtist)
	}

	// --- Release-group fallback from SongInfos ---
	provider2 := newCassetteLookup(t, "testdata/lookup-release-group.json")
	results2, err := provider2.Search(context.Background(), providers.Lookup{
		Kind: item.KindMusicAlbum,
		Name: "RG Album",
		SongInfos: []providers.SongInfo{
			{ProviderIDs: map[string]string{"MusicBrainzReleaseGroup": "test-rg-id"}},
		},
	})
	if err != nil {
		t.Fatalf("Search (SongInfo release-group): %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("SongInfo release-group fallback returned no results")
	}
	if results2[0].ProviderIDs["MusicBrainzReleaseGroup"] != "test-rg-id" {
		t.Errorf("SongInfo fallback: MusicBrainzReleaseGroup = %q, want test-rg-id", results2[0].ProviderIDs["MusicBrainzReleaseGroup"])
	}

	// --- Direct release lookup with full Artists projection ---
	provider3 := newCassetteLookup(t, "testdata/lookup-release.json")
	results3, err := provider3.Search(context.Background(), providers.Lookup{
		Kind:        item.KindMusicAlbum,
		Name:        "Test Album",
		ProviderIDs: map[string]string{"MusicBrainzAlbum": "test-release-id"},
	})
	if err != nil {
		t.Fatalf("Search (release lookup): %v", err)
	}
	if len(results3) == 0 {
		t.Fatal("release lookup returned no results")
	}
	r3 := results3[0]
	if r3.ProviderIDs["MusicBrainzAlbum"] != "test-release-id" {
		t.Errorf("ProviderIDs[MusicBrainzAlbum] = %q, want test-release-id", r3.ProviderIDs["MusicBrainzAlbum"])
	}
	if r3.ProviderIDs["MusicBrainzReleaseGroup"] != "test-release-group-id" {
		t.Errorf("ProviderIDs[MusicBrainzReleaseGroup] = %q, want test-release-group-id", r3.ProviderIDs["MusicBrainzReleaseGroup"])
	}
	// Two artists in the credit; both must carry MusicBrainzArtist IDs.
	if len(r3.Artists) != 2 {
		t.Fatalf("Artists count = %d, want 2", len(r3.Artists))
	}
	if r3.Artists[0].ProviderIDs["MusicBrainzArtist"] != "test-artist-id" {
		t.Errorf("Artists[0].ProviderIDs[MusicBrainzArtist] = %q, want test-artist-id", r3.Artists[0].ProviderIDs["MusicBrainzArtist"])
	}
	if r3.Artists[1].ProviderIDs["MusicBrainzArtist"] != "featured-artist-id" {
		t.Errorf("Artists[1].ProviderIDs[MusicBrainzArtist] = %q, want featured-artist-id", r3.Artists[1].ProviderIDs["MusicBrainzArtist"])
	}
	if r3.AlbumArtist == nil || r3.AlbumArtist.ProviderIDs["MusicBrainzArtist"] != "test-artist-id" {
		t.Errorf("AlbumArtist provider ID = %v, want test-artist-id", r3.AlbumArtist)
	}
}

// TestItemLookupProviderIDsWithoutName proves album, release-group, and artist
// ID-only cassette requests return pinned results without a Name, mirroring
// upstream's ID-first lookup ordering (MusicBrainzArtistProvider.cs:39-44,
// MusicBrainzAlbumProvider.cs:39-50). When a provider ID is set, the lookup
// by ID runs before the name search; an empty Name does not short-circuit.
func TestItemLookupProviderIDsWithoutName(t *testing.T) {
	t.Run("artist", func(t *testing.T) {
		provider := newCassetteLookup(t, "testdata/lookup-artist.json")
		results, err := provider.Search(context.Background(), providers.Lookup{
			Kind:        item.KindMusicArtist,
			ProviderIDs: map[string]string{"MusicBrainzArtist": "test-artist-id"},
		})
		if err != nil {
			t.Fatalf("Search (artist ID-only): %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("artist ID-only returned %d results, want 1", len(results))
		}
		r := results[0]
		if r.ProviderIDs["MusicBrainzArtist"] != "test-artist-id" {
			t.Errorf("ProviderIDs[MusicBrainzArtist] = %q, want test-artist-id", r.ProviderIDs["MusicBrainzArtist"])
		}
		if r.Name == nil || *r.Name != "Test Artist" {
			t.Errorf("Name = %v, want \"Test Artist\" (from the looked-up artist, not synthesized)", r.Name)
		}
		if r.ProductionYear == nil || *r.ProductionYear != 2000 {
			t.Errorf("ProductionYear = %v, want 2000 (from life-span begin)", r.ProductionYear)
		}
		if r.PremiereDate == nil {
			t.Error("PremiereDate = nil, want a date from life-span begin")
		}
	})

	t.Run("album", func(t *testing.T) {
		provider := newCassetteLookup(t, "testdata/lookup-release.json")
		results, err := provider.Search(context.Background(), providers.Lookup{
			Kind:        item.KindMusicAlbum,
			ProviderIDs: map[string]string{"MusicBrainzAlbum": "test-release-id"},
		})
		if err != nil {
			t.Fatalf("Search (album ID-only): %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("album ID-only returned %d results, want 1", len(results))
		}
		if results[0].ProviderIDs["MusicBrainzAlbum"] != "test-release-id" {
			t.Errorf("ProviderIDs[MusicBrainzAlbum] = %q, want test-release-id", results[0].ProviderIDs["MusicBrainzAlbum"])
		}
	})

	t.Run("release-group", func(t *testing.T) {
		provider := newCassetteLookup(t, "testdata/lookup-release-group.json")
		results, err := provider.Search(context.Background(), providers.Lookup{
			Kind:        item.KindMusicAlbum,
			ProviderIDs: map[string]string{"MusicBrainzReleaseGroup": "test-rg-id"},
		})
		if err != nil {
			t.Fatalf("Search (release-group ID-only): %v", err)
		}
		if len(results) == 0 {
			t.Fatal("release-group ID-only returned no results")
		}
		if results[0].ProviderIDs["MusicBrainzReleaseGroup"] != "test-rg-id" {
			t.Errorf("ProviderIDs[MusicBrainzReleaseGroup] = %q, want test-rg-id", results[0].ProviderIDs["MusicBrainzReleaseGroup"])
		}
	})
}
