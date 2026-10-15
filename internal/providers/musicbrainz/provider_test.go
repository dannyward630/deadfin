// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz_test

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/musicbrainz"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
)

const (
	artistID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	releaseID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	groupID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

type recordingDoer struct {
	inner remote.Doer

	mu       sync.Mutex
	requests []string
}

func (doer *recordingDoer) Do(request *http.Request) (*http.Response, error) {
	doer.mu.Lock()
	doer.requests = append(doer.requests, request.Method+" "+request.URL.String())
	doer.mu.Unlock()
	return doer.inner.Do(request)
}

func (doer *recordingDoer) snapshot() []string {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	return append([]string(nil), doer.requests...)
}

func TestProviderMapsFirstArtistAndAlbumResultsFromCassette(t *testing.T) {
	cassette, err := remotetest.Load(filepath.Join("testdata", "music-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	doer := &recordingDoer{inner: cassette}
	config := musicbrainz.DefaultConfig()
	config.ReplaceArtistName = true
	provider := musicbrainz.NewProvider(musicbrainz.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: doer}),
		Config: config,
	})

	artist := item.Item{Kind: item.KindMusicArtist, Name: stringPointer("Fixture Artist")}
	handled, err := provider.Refresh(t.Context(), remoteRefresh(), &artist)
	if err != nil || !handled {
		t.Fatalf("refresh artist = handled %t error %v", handled, err)
	}
	if artist.Name == nil || *artist.Name != "Fixture Artist (canonical)" {
		t.Fatalf("artist Name = %v, want first result's canonical name", artist.Name)
	}
	if got, ok := artist.ProviderIDs.Get("MusicBrainzArtist"); !ok || got != artistID {
		t.Fatalf("artist MusicBrainz ID = %q, %t", got, ok)
	}
	if artist.ProductionYear != nil || artist.PremiereDate != nil {
		t.Fatalf("artist metadata contains search-only dates: year=%v date=%v", artist.ProductionYear, artist.PremiereDate)
	}

	albumPath := filepath.Join("/music", "Fixture Artist", "Fixture Album")
	album := item.Item{Kind: item.KindMusicAlbum, Name: stringPointer("Fixture Album"), Path: &albumPath}
	handled, err = provider.Refresh(t.Context(), remoteRefresh(), &album)
	if err != nil || !handled {
		t.Fatalf("refresh album = handled %t error %v", handled, err)
	}
	if got, ok := album.ProviderIDs.Get("MusicBrainzAlbum"); !ok || got != releaseID {
		t.Fatalf("album release ID = %q, %t", got, ok)
	}
	if got, ok := album.ProviderIDs.Get("MusicBrainzReleaseGroup"); !ok || got != groupID {
		t.Fatalf("album release-group ID = %q, %t", got, ok)
	}
	if album.ProductionYear == nil || *album.ProductionYear != 2024 {
		t.Fatalf("album ProductionYear = %v, want 2024", album.ProductionYear)
	}
	if album.PremiereDate != nil {
		t.Fatalf("album PremiereDate = %v, pinned metadata provider sets only ProductionYear", album.PremiereDate)
	}
	if album.Overview == nil || *album.Overview != "MusicBrainz album annotation." {
		t.Fatalf("album Overview = %v", album.Overview)
	}
	if got, want := doer.snapshot(), []string{
		`GET https://musicbrainz.org/ws/2/artist/?fmt=json&query=%22Fixture+Artist%22`,
		`GET https://musicbrainz.org/ws/2/release/?fmt=json&query=%22Fixture+Album%22+AND+artist%3A%22Fixture+Artist%22`,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MusicBrainz request order = %#v, want %#v", got, want)
	}
}

func TestProviderPinsLookupRequestShapes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		target      item.Item
		interaction remotetest.Interaction
		wantID      string
		wantGroup   string
	}{
		{
			name: "release to release group",
			target: func() item.Item {
				value := item.Item{Kind: item.KindMusicAlbum, Name: stringPointer("Album")}
				value.ProviderIDs.Set("MusicBrainzAlbum", releaseID)
				return value
			}(),
			interaction: remotetest.Interaction{
				Method: http.MethodGet,
				URL:    "https://musicbrainz.org/ws/2/release/" + releaseID + "?fmt=json&inc=release-groups",
				Status: http.StatusOK,
				Body:   []byte(`{"id":"` + releaseID + `","release-group":{"id":"` + groupID + `"}}`),
			},
			wantID: releaseID, wantGroup: groupID,
		},
		{
			name: "release group to first release",
			target: func() item.Item {
				value := item.Item{Kind: item.KindMusicAlbum, Name: stringPointer("Album")}
				value.ProviderIDs.Set("MusicBrainzReleaseGroup", groupID)
				return value
			}(),
			interaction: remotetest.Interaction{
				Method: http.MethodGet,
				URL:    "https://musicbrainz.org/ws/2/release-group/" + groupID + "?fmt=json&inc=releases",
				Status: http.StatusOK,
				Body:   []byte(`{"id":"` + groupID + `","releases":[{"id":"` + releaseID + `"},{"id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}]}`),
			},
			wantID: releaseID, wantGroup: groupID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cassette := remotetest.NewCassette(tc.interaction)
			provider := musicbrainz.NewProvider(musicbrainz.Options{
				HTTP:   remote.NewClient(remote.Config{Doer: cassette}),
				Config: musicbrainz.DefaultConfig(),
			})
			handled, err := provider.Refresh(t.Context(), remoteRefresh(), &tc.target)
			if err != nil || !handled {
				t.Fatalf("refresh = handled %t error %v", handled, err)
			}
			if got, _ := tc.target.ProviderIDs.Get("MusicBrainzAlbum"); got != tc.wantID {
				t.Fatalf("release ID = %q, want %q", got, tc.wantID)
			}
			if got, _ := tc.target.ProviderIDs.Get("MusicBrainzReleaseGroup"); got != tc.wantGroup {
				t.Fatalf("release-group ID = %q, want %q", got, tc.wantGroup)
			}
		})
	}
}

func TestProviderRetriesDiacriticArtistSearchInPinnedOrder(t *testing.T) {
	firstURL := `https://musicbrainz.org/ws/2/artist/?fmt=json&query=%22C%C5%93ur%22`
	secondURL := `https://musicbrainz.org/ws/2/artist/?fmt=json&query=artistaccent%3A%22C%C5%93ur%22`
	cassette := remotetest.NewCassette(
		remotetest.Interaction{Method: http.MethodGet, URL: firstURL, Status: http.StatusOK, Body: []byte(`{"artists":[]}`)},
		remotetest.Interaction{Method: http.MethodGet, URL: secondURL, Status: http.StatusOK, Body: []byte(`{"artists":[{"id":"` + artistID + `","name":"Cœur"}]}`)},
	)
	doer := &recordingDoer{inner: cassette}
	provider := musicbrainz.NewProvider(musicbrainz.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: doer}),
		Config: musicbrainz.DefaultConfig(),
	})
	target := item.Item{Kind: item.KindMusicArtist, Name: stringPointer("Cœur")}
	handled, err := provider.Refresh(t.Context(), remoteRefresh(), &target)
	if err != nil || !handled {
		t.Fatalf("refresh = handled %t error %v", handled, err)
	}
	if got, want := doer.snapshot(), []string{"GET " + firstURL, "GET " + secondURL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diacritic request order = %#v, want %#v", got, want)
	}
}

func TestProviderFailureAndNoMatchSemantics(t *testing.T) {
	searchURL := `https://musicbrainz.org/ws/2/artist/?fmt=json&query=%22Missing%22`
	for _, tc := range []struct {
		name        string
		interaction remotetest.Interaction
		wantHandled bool
		wantError   bool
	}{
		{name: "no match", interaction: remotetest.Interaction{Method: http.MethodGet, URL: searchURL, Status: http.StatusOK, Body: []byte(`{"artists":[]}`)}},
		{name: "upstream status", interaction: remotetest.Interaction{Method: http.MethodGet, URL: searchURL, Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"down"}`)}, wantError: true},
		{name: "malformed response", interaction: remotetest.Interaction{Method: http.MethodGet, URL: searchURL, Status: http.StatusOK, Body: []byte(`{`)}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := musicbrainz.NewProvider(musicbrainz.Options{
				HTTP:   remote.NewClient(remote.Config{Doer: remotetest.NewCassette(tc.interaction)}),
				Config: musicbrainz.DefaultConfig(),
			})
			target := item.Item{Kind: item.KindMusicArtist, Name: stringPointer("Missing")}
			handled, err := provider.Refresh(t.Context(), remoteRefresh(), &target)
			if handled != tc.wantHandled || (err != nil) != tc.wantError {
				t.Fatalf("refresh = handled %t error %v, want handled %t error %t", handled, err, tc.wantHandled, tc.wantError)
			}
		})
	}

	target := item.Item{Kind: item.KindMusicArtist, Name: stringPointer("Existing")}
	target.ProviderIDs.Set("MusicBrainzArtist", "not-a-guid")
	provider := musicbrainz.NewProvider(musicbrainz.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: remotetest.NewCassette()}),
		Config: musicbrainz.DefaultConfig(),
	})
	if handled, err := provider.Refresh(t.Context(), remoteRefresh(), &target); !handled || err != nil {
		t.Fatalf("existing opaque ID = handled %t error %v, want true/nil", handled, err)
	}
}

func TestProviderEscapesAlbumSearchFactsAndIgnoresInvalidLookupIDs(t *testing.T) {
	albumPath := filepath.Join("/music", "AC_DC + Guests && Friends", `An "Album": Deluxe`)
	target := item.Item{
		Kind: item.KindMusicAlbum,
		Name: stringPointer(`An "Album": Deluxe`),
		Path: &albumPath,
	}
	target.ProviderIDs.Set("MusicBrainzAlbum", "not-a-release-guid")
	target.ProviderIDs.Set("MusicBrainzReleaseGroup", "not-a-group-guid")
	target.ProviderIDs.Set("MusicBrainzArtist", "not-an-artist-guid")

	searchURL := `https://musicbrainz.org/ws/2/release/?fmt=json&query=%22An+%5C%22Album%5C%22%5C%3A+Deluxe%22+AND+artist%3A%22AC_DC+%5C%2B+Guests+%5C%26%26+Friends%22`
	cassette := remotetest.NewCassette(remotetest.Interaction{
		Method: http.MethodGet,
		URL:    searchURL,
		Status: http.StatusOK,
		Body:   []byte(`{"releases":[]}`),
	})
	provider := musicbrainz.NewProvider(musicbrainz.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: cassette}),
		Config: musicbrainz.DefaultConfig(),
	})

	handled, err := provider.Refresh(t.Context(), remoteRefresh(), &target)
	if handled || err != nil {
		t.Fatalf("refresh = handled %t error %v, want false/nil", handled, err)
	}
}

func TestConfigLoadsPinnedXMLDefaultsAndValidation(t *testing.T) {
	defaults := musicbrainz.LoadConfig(filepath.Join(t.TempDir(), "missing.xml"))
	if defaults.Server != "https://musicbrainz.org" || defaults.RateLimit != 1 || defaults.ReplaceArtistName {
		t.Fatalf("default config = %#v", defaults)
	}

	path := filepath.Join(t.TempDir(), "Jellyfin.Plugin.MusicBrainz.xml")
	if err := os.WriteFile(path, []byte(`<PluginConfiguration><Server>https://mirror.example/ws/</Server><RateLimit>0.25</RateLimit><ReplaceArtistName>true</ReplaceArtistName></PluginConfiguration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := musicbrainz.LoadConfig(path)
	if config.Server != "https://mirror.example/ws" || config.RateLimit != 0.25 || !config.ReplaceArtistName {
		t.Fatalf("loaded config = %#v", config)
	}

	if err := os.WriteFile(path, []byte(`<PluginConfiguration><Server>invalid</Server><RateLimit>-1</RateLimit></PluginConfiguration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	config = musicbrainz.LoadConfig(path)
	if config.Server != "https://musicbrainz.org" || config.RateLimit != 1 {
		t.Fatalf("normalized invalid config = %#v", config)
	}

	if err := os.WriteFile(path, []byte(`<PluginConfiguration><Server>https://mirror.example</Server><RateLimit>-1</RateLimit></PluginConfiguration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	config = musicbrainz.LoadConfig(path)
	if config.Server != "https://mirror.example" || config.RateLimit != -1 {
		t.Fatalf("custom-server rate limit = %#v, want pinned unconstrained custom value", config)
	}
}

func TestProviderContractAndInertKinds(t *testing.T) {
	provider := musicbrainz.NewProvider(musicbrainz.Options{})
	if provider.Name() != "MusicBrainz" || provider.Stage() != providers.ProviderStageRemoteMetadata {
		t.Fatalf("provider contract = %q/%v", provider.Name(), provider.Stage())
	}
	if handled, err := provider.Refresh(t.Context(), remoteRefresh(), &item.Item{Kind: item.KindMovie}); handled || err != nil {
		t.Fatalf("unsupported kind = handled %t error %v", handled, err)
	}
	if _, err := provider.Refresh(t.Context(), remoteRefresh(), nil); err == nil {
		t.Fatalf("nil target error = %v", err)
	}
}

func remoteRefresh() library.RefreshContext {
	return library.RefreshContext{Options: library.LibraryOptions{EnableInternetProviders: true}}
}

func stringPointer(value string) *string { return &value }
