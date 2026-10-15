// SPDX-License-Identifier: GPL-2.0-only

package audiodb_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/audiodb"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
)

const (
	artistID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	groupID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

type recordingDoer struct {
	inner remote.Doer

	mu       sync.Mutex
	requests []string
}

type errorDoer struct {
	err error
}

func (doer errorDoer) Do(*http.Request) (*http.Response, error) {
	return nil, doer.err
}

type upstreamCredentialError string

func (err upstreamCredentialError) Error() string { return string(err) }

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

func TestProviderMapsLocalizedArtistAlbumAndArtworkFromCassette(t *testing.T) {
	cassette, err := remotetest.Load(filepath.Join("testdata", "supplemental-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	doer := &recordingDoer{inner: cassette}
	config := audiodb.DefaultConfig()
	config.APIKey = "test-key"
	config.ReplaceAlbumName = true
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: doer}),
		Config: config,
	})

	artist := item.Item{Kind: item.KindMusicArtist, Name: stringPointer("Fixture Artist")}
	artist.ProviderIDs.Set("MusicBrainzArtist", artistID)
	handled, err := provider.Refresh(t.Context(), refreshContext("fr"), &artist)
	if err != nil || !handled {
		t.Fatalf("refresh artist = handled %t error %v", handled, err)
	}
	if artist.Overview == nil || *artist.Overview != "Biographie & détails." {
		t.Fatalf("artist Overview = %v", artist.Overview)
	}
	if got, want := artist.Values.Genres, []string{"Art Rock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artist Genres = %#v, want %#v", got, want)
	}
	if got, ok := artist.ProviderIDs.Get("AudioDbArtist"); !ok || got != "1001" {
		t.Fatalf("artist AudioDb ID = %q, %t", got, ok)
	}
	wantArtistImages := []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: "https://images.example/artist-thumb.jpg"},
		{Type: item.ImageTypeLogo, Path: "https://images.example/artist-logo.png"},
		{Type: item.ImageTypeBanner, Path: "https://images.example/artist-banner.jpg"},
		{Type: item.ImageTypeBackdrop, Path: "https://images.example/artist-fanart-1.jpg", Index: 0},
		{Type: item.ImageTypeBackdrop, Path: "https://images.example/artist-fanart-2.jpg", Index: 1},
		{Type: item.ImageTypeBackdrop, Path: "https://images.example/artist-fanart-3.jpg", Index: 2},
	}
	if !reflect.DeepEqual(artist.ImageInfos, wantArtistImages) {
		t.Fatalf("artist images = %#v, want pinned order %#v", artist.ImageInfos, wantArtistImages)
	}

	album := item.Item{Kind: item.KindMusicAlbum, Name: stringPointer("Directory Album")}
	album.ProviderIDs.Set("MusicBrainzReleaseGroup", groupID)
	handled, err = provider.Refresh(t.Context(), refreshContext("fr"), &album)
	if err != nil || !handled {
		t.Fatalf("refresh album = handled %t error %v", handled, err)
	}
	if album.Name == nil || *album.Name != "Directory Album" {
		t.Fatalf("album Name = %v, pinned provider changes Album but not Name", album.Name)
	}
	if album.Album == nil || *album.Album != "Fixture Album (AudioDb)" {
		t.Fatalf("album Album = %v", album.Album)
	}
	if album.Overview == nil || *album.Overview != "Description française & notes." {
		t.Fatalf("album Overview = %v", album.Overview)
	}
	if album.ProductionYear == nil || *album.ProductionYear != 2024 {
		t.Fatalf("album ProductionYear = %v", album.ProductionYear)
	}
	if got, want := album.Values.AlbumArtists, []string{"Fixture Artist"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("album artists = %#v, want %#v", got, want)
	}
	if got, want := album.Values.Genres, []string{"Progressive Rock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("album genres = %#v, want %#v", got, want)
	}
	for name, want := range map[string]string{
		"AudioDbArtist":           "1001",
		"AudioDbAlbum":            "2002",
		"MusicBrainzAlbumArtist":  artistID,
		"MusicBrainzReleaseGroup": groupID,
	} {
		if got, ok := album.ProviderIDs.Get(name); !ok || got != want {
			t.Errorf("album %s = %q, %t; want %q", name, got, ok, want)
		}
	}
	wantAlbumImages := []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: "https://images.example/album-thumb.jpg"},
		{Type: item.ImageTypeDisc, Path: "https://images.example/album-disc.png"},
	}
	if !reflect.DeepEqual(album.ImageInfos, wantAlbumImages) {
		t.Fatalf("album images = %#v, want %#v", album.ImageInfos, wantAlbumImages)
	}
	if got, want := doer.snapshot(), []string{
		"GET https://www.theaudiodb.com/api/v1/json/test-key/artist-mb.php?i=" + artistID,
		"GET https://www.theaudiodb.com/api/v1/json/test-key/album-mb.php?i=" + groupID,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AudioDb request order = %#v, want %#v", got, want)
	}
}

func TestProviderPreservesExistingImagesUnlessFullReplacementRequested(t *testing.T) {
	cassette, err := remotetest.Load(filepath.Join("testdata", "supplemental-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := audiodb.DefaultConfig()
	config.APIKey = "test-key"
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: cassette}),
		Config: config,
	})
	existing := item.ImageInfo{Type: item.ImageTypePrimary, Path: "/local/artist.jpg"}
	target := item.Item{Kind: item.KindMusicArtist, ImageInfos: []item.ImageInfo{existing}}
	target.ProviderIDs.Set("MusicBrainzArtist", artistID)
	if handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target); err != nil || !handled {
		t.Fatalf("ordinary refresh = handled %t error %v", handled, err)
	}
	if target.ImageInfos[0] != existing {
		t.Fatalf("ordinary refresh replaced local primary: %#v", target.ImageInfos)
	}

	refresh := refreshContext("en")
	refresh.Image = library.RefreshModeFullRefresh
	refresh.ReplaceAllImages = true
	if handled, err := provider.Refresh(t.Context(), refresh, &target); err != nil || !handled {
		t.Fatalf("replace refresh = handled %t error %v", handled, err)
	}
	if target.ImageInfos[0].Path != "https://images.example/artist-thumb.jpg" {
		t.Fatalf("replace refresh images = %#v", target.ImageInfos)
	}
}

func TestProviderPinsNoMatchAndFailureSemantics(t *testing.T) {
	artistURL := "https://www.theaudiodb.com/api/v1/json/test-key/artist-mb.php?i=" + artistID
	for _, tc := range []struct {
		name        string
		interaction remotetest.Interaction
		wantError   bool
	}{
		{name: "null artists", interaction: remotetest.Interaction{Method: http.MethodGet, URL: artistURL, Status: http.StatusOK, Body: []byte(`{"artists":null}`)}},
		{name: "artist status is fatal", interaction: remotetest.Interaction{Method: http.MethodGet, URL: artistURL, Status: http.StatusServiceUnavailable, Body: []byte(`{"artists":[]}`)}, wantError: true},
		{name: "malformed artist", interaction: remotetest.Interaction{Method: http.MethodGet, URL: artistURL, Status: http.StatusOK, Body: []byte(`{`)}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := audiodb.DefaultConfig()
			config.APIKey = "test-key"
			provider := audiodb.NewProvider(audiodb.Options{
				HTTP:   remote.NewClient(remote.Config{Doer: remotetest.NewCassette(tc.interaction)}),
				Config: config,
			})
			target := item.Item{Kind: item.KindMusicArtist}
			target.ProviderIDs.Set("MusicBrainzArtist", artistID)
			handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target)
			if handled || (err != nil) != tc.wantError {
				t.Fatalf("refresh = handled %t error %v, want false/error %t", handled, err, tc.wantError)
			}
		})
	}

	// AudioDbAlbumProvider does not call EnsureSuccessStatusCode before
	// deserializing its cached response. A valid body on a non-2xx response is
	// therefore still metadata in pinned stock.
	albumURL := "https://www.theaudiodb.com/api/v1/json/test-key/album-mb.php?i=" + groupID
	cassette := remotetest.NewCassette(remotetest.Interaction{
		Method: http.MethodGet,
		URL:    albumURL,
		Status: http.StatusServiceUnavailable,
		Body:   []byte(`{"album":[{"idAlbum":"still-mapped","strMusicBrainzID":"` + groupID + `"}]}`),
	})
	config := audiodb.DefaultConfig()
	config.APIKey = "test-key"
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: cassette}),
		Config: config,
	})
	target := item.Item{Kind: item.KindMusicAlbum}
	target.ProviderIDs.Set("MusicBrainzReleaseGroup", groupID)
	if handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target); err != nil || !handled {
		t.Fatalf("album non-2xx valid body = handled %t error %v, want true/nil", handled, err)
	}
	if got, _ := target.ProviderIDs.Get("AudioDbAlbum"); got != "still-mapped" {
		t.Fatalf("album non-2xx body ID = %q", got)
	}
}

func TestProviderRedactsAPIKeyFromUpstreamErrors(t *testing.T) {
	const secret = "super-secret/key"
	upstreamError := upstreamCredentialError("upstream unavailable api_key=" + secret)
	config := audiodb.DefaultConfig()
	config.APIKey = secret
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: errorDoer{err: upstreamError}}),
		Config: config,
	})
	target := item.Item{Kind: item.KindMusicArtist}
	target.ProviderIDs.Set("MusicBrainzArtist", artistID)

	handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target)
	if handled || err == nil {
		t.Fatalf("refresh = handled %t error %v, want false/error", handled, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("upstream error leaked API key: %v", err)
	}
	if !errors.Is(err, remote.ErrRequestFailed) {
		t.Fatalf("errors.Is(err, remote.ErrRequestFailed) = false, want true: %T %v", err, err)
	}
	if errors.Is(err, upstreamError) {
		t.Fatalf("error retained raw upstream identity: %T %v", err, err)
	}
	var raw upstreamCredentialError
	if errors.As(err, &raw) {
		t.Fatalf("errors.As exposed raw upstream error %q", raw)
	}
	if unwrapped := errors.Unwrap(err); errors.Is(unwrapped, upstreamError) {
		t.Fatalf("errors.Unwrap exposed raw upstream identity: %T %v", unwrapped, unwrapped)
	}
}

func TestProviderNilRemoteLoggerDoesNotLogAPIKey(t *testing.T) {
	const secret = "super-secret/key"

	previousDefault := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousDefault) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	config := audiodb.DefaultConfig()
	config.APIKey = secret
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: errorDoer{err: errors.New("upstream unavailable")}}),
		Config: config,
	})
	target := item.Item{Kind: item.KindMusicArtist}
	target.ProviderIDs.Set("MusicBrainzArtist", artistID)

	handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target)
	if handled || err == nil {
		t.Fatalf("refresh = handled %t error %v, want false/error", handled, err)
	}
	for _, output := range []string{err.Error(), logs.String()} {
		for _, exposed := range []string{secret, url.PathEscape(secret)} {
			if strings.Contains(output, exposed) {
				t.Errorf("nil remote logger leaked API key %q in observable output: %q", exposed, output)
			}
		}
	}
}

func TestProviderSkipsMalformedOptionalAlbumYear(t *testing.T) {
	config := audiodb.DefaultConfig()
	config.APIKey = "test-key"
	albumURL := "https://www.theaudiodb.com/api/v1/json/test-key/album-mb.php?i=" + groupID
	cassette := remotetest.NewCassette(remotetest.Interaction{
		Method: http.MethodGet,
		URL:    albumURL,
		Status: http.StatusOK,
		Body:   []byte(`{"album":[{"idAlbum":"mapped","intYearReleased":"unknown","strGenre":"Rock"}]}`),
	})
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: cassette}),
		Config: config,
	})
	target := item.Item{Kind: item.KindMusicAlbum}
	target.ProviderIDs.Set("MusicBrainzReleaseGroup", groupID)

	handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target)
	if !handled || err != nil {
		t.Fatalf("refresh = handled %t error %v, want true/nil", handled, err)
	}
	if target.ProductionYear != nil {
		t.Fatalf("malformed optional year produced %v", target.ProductionYear)
	}
	if got, _ := target.ProviderIDs.Get("AudioDbAlbum"); got != "mapped" {
		t.Fatalf("metadata after malformed year = %q, want mapped ID", got)
	}
}

func TestConfigLoadsXMLAndExternalAPIKey(t *testing.T) {
	t.Setenv("AUDIODB_API_KEY", "")
	defaults := audiodb.LoadConfig(filepath.Join(t.TempDir(), "missing.xml"))
	if defaults.Server != "https://www.theaudiodb.com/api/v1/json" || defaults.APIKey != "" || defaults.ReplaceAlbumName {
		t.Fatalf("default config = %#v", defaults)
	}

	path := filepath.Join(t.TempDir(), "Jellyfin.Plugin.AudioDb.xml")
	if err := os.WriteFile(path, []byte(`<PluginConfiguration><Server>https://audio.example/api/</Server><ApiKey>file-key</ApiKey><ReplaceAlbumName>true</ReplaceAlbumName></PluginConfiguration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := audiodb.LoadConfig(path)
	if config.Server != "https://audio.example/api" || config.APIKey != "file-key" || !config.ReplaceAlbumName {
		t.Fatalf("loaded config = %#v", config)
	}

	t.Setenv("AUDIODB_API_KEY", " env-key ")
	config = audiodb.LoadConfig(path)
	if config.APIKey != "env-key" {
		t.Fatalf("environment API key = %q, want env-key", config.APIKey)
	}
}

func TestProviderContractAndKeylessInertBehavior(t *testing.T) {
	provider := audiodb.NewProvider(audiodb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: remotetest.NewCassette()}),
		Config: audiodb.DefaultConfig(),
	})
	if provider.Name() != "TheAudioDB" || provider.Stage() != providers.ProviderStageRemoteMetadata {
		t.Fatalf("provider contract = %q/%v", provider.Name(), provider.Stage())
	}
	target := item.Item{Kind: item.KindMusicArtist}
	target.ProviderIDs.Set("MusicBrainzArtist", artistID)
	if handled, err := provider.Refresh(t.Context(), refreshContext("en"), &target); handled || err != nil {
		t.Fatalf("keyless provider = handled %t error %v", handled, err)
	}
	if _, err := provider.Refresh(t.Context(), refreshContext("en"), nil); err == nil {
		t.Fatal("nil target did not fail")
	}
}

func refreshContext(language string) library.RefreshContext {
	return library.RefreshContext{
		Options: library.LibraryOptions{
			EnableInternetProviders:   true,
			PreferredMetadataLanguage: language,
		},
	}
}

func stringPointer(value string) *string { return &value }
