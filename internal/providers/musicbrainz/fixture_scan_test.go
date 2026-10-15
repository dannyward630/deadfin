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
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/audiodb"
	"github.com/deadfin-org/deadfin/internal/providers/musicbrainz"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/store"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

type scanRequestLog struct {
	mu      sync.Mutex
	entries []string
}

func (log *scanRequestLog) append(value string) {
	log.mu.Lock()
	log.entries = append(log.entries, value)
	log.mu.Unlock()
}

func (log *scanRequestLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.entries...)
}

type loggedDoer struct {
	inner remote.Doer
	log   *scanRequestLog
}

func (doer loggedDoer) Do(request *http.Request) (*http.Response, error) {
	doer.log.append(request.Method + " " + request.URL.String())
	return doer.inner.Do(request)
}

// TestNFOlessMusicFixtureScan is P8's package-level exit fixture: a real
// scanner persists MusicBrainz identities and AudioDb descriptions/artwork
// without an NFO or live network access.
func TestNFOlessMusicFixtureScan(t *testing.T) {
	musicCassette, err := remotetest.Load(filepath.Join("testdata", "music-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	audioCassette, err := remotetest.Load(filepath.Join("..", "audiodb", "testdata", "supplemental-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	requests := &scanRequestLog{}
	musicConfig := musicbrainz.DefaultConfig()
	audioConfig := audiodb.DefaultConfig()
	audioConfig.APIKey = "test-key"
	audioConfig.ReplaceAlbumName = true
	registry := providers.NewRegistry(
		musicbrainz.NewProvider(musicbrainz.Options{
			HTTP:   remote.NewClient(remote.Config{Doer: loggedDoer{inner: musicCassette, log: requests}}),
			Config: musicConfig,
		}),
		audiodb.NewProvider(audiodb.Options{
			HTTP:   remote.NewClient(remote.Config{Doer: loggedDoer{inner: audioCassette, log: requests}}),
			Config: audioConfig,
		}),
	)

	mediaRoot := filepath.Join(t.TempDir(), "music")
	track := filepath.Join(mediaRoot, "Fixture Artist", "Fixture Album", "01 - Fixture Track.mp3")
	if err := os.MkdirAll(filepath.Dir(track), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(track, []byte("cassette fixture; not probed"), 0o600); err != nil {
		t.Fatal(err)
	}

	stores, err := storesqlite.Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close stores: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	options := library.LibraryOptions{
		PathInfos:                 []library.MediaPathInfo{{Path: mediaRoot}},
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: "fr",
		TypeOptions: []library.TypeOptions{
			{Type: "MusicArtist", MetadataFetchers: []string{"MusicBrainz", "TheAudioDB"}, MetadataFetcherOrder: []string{"MusicBrainz", "TheAudioDB"}, ImageFetchers: []string{"TheAudioDB"}},
			{Type: "MusicAlbum", MetadataFetchers: []string{"MusicBrainz", "TheAudioDB"}, MetadataFetcherOrder: []string{"MusicBrainz", "TheAudioDB"}, ImageFetchers: []string{"TheAudioDB"}},
		},
	}
	if _, err := topology.AddVirtualFolder(t.Context(), "Cassette Music", "music", options); err != nil {
		t.Fatal(err)
	}
	scanner, err := library.NewScanner(topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{
		Concurrency: 1,
		Refresher:   registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})
	result, err := scanner.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("fixture scan errors = %v", result.Errors)
	}

	artists, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMusicArtist}, Recursive: true})
	if err != nil || len(artists.Items) != 1 {
		t.Fatalf("artist query = %#v, %v", artists, err)
	}
	artist := artists.Items[0]
	if artist.Name == nil || *artist.Name != "Fixture Artist" {
		t.Fatalf("artist Name = %v", artist.Name)
	}
	if artist.Overview == nil || *artist.Overview != "Biographie & détails." {
		t.Fatalf("artist Overview = %v", artist.Overview)
	}
	if got, _ := artist.ProviderIDs.Get("MusicBrainzArtist"); got != artistID {
		t.Fatalf("artist MusicBrainz ID = %q", got)
	}
	if got, _ := artist.ProviderIDs.Get("AudioDbArtist"); got != "1001" {
		t.Fatalf("artist AudioDb ID = %q", got)
	}
	if len(artist.ImageInfos) != 6 || !hasFixtureImage(artist.ImageInfos, item.ImageTypePrimary, "https://images.example/artist-thumb.jpg") || !hasFixtureImage(artist.ImageInfos, item.ImageTypeBackdrop, "https://images.example/artist-fanart-3.jpg") {
		t.Fatalf("artist artwork = %#v", artist.ImageInfos)
	}

	albums, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMusicAlbum}, Recursive: true})
	if err != nil || len(albums.Items) != 1 {
		t.Fatalf("album query = %#v, %v", albums, err)
	}
	album := albums.Items[0]
	if album.Name == nil || *album.Name != "Fixture Album" {
		t.Fatalf("album Name = %v", album.Name)
	}
	if album.Album == nil || *album.Album != "Fixture Album (AudioDb)" {
		t.Fatalf("album canonical Album = %v", album.Album)
	}
	if album.Overview == nil || *album.Overview != "Description française & notes." {
		t.Fatalf("album Overview = %v", album.Overview)
	}
	if got, _ := album.ProviderIDs.Get("MusicBrainzAlbum"); got != releaseID {
		t.Fatalf("album release ID = %q", got)
	}
	if got, _ := album.ProviderIDs.Get("MusicBrainzReleaseGroup"); got != groupID {
		t.Fatalf("album release-group ID = %q", got)
	}
	if got, _ := album.ProviderIDs.Get("AudioDbAlbum"); got != "2002" {
		t.Fatalf("album AudioDb ID = %q", got)
	}
	if len(album.ImageInfos) != 2 || !hasFixtureImage(album.ImageInfos, item.ImageTypePrimary, "https://images.example/album-thumb.jpg") || !hasFixtureImage(album.ImageInfos, item.ImageTypeDisc, "https://images.example/album-disc.png") {
		t.Fatalf("album artwork = %#v", album.ImageInfos)
	}

	if got, want := requests.snapshot(), []string{
		`GET https://musicbrainz.org/ws/2/artist/?fmt=json&query=%22Fixture+Artist%22`,
		"GET https://www.theaudiodb.com/api/v1/json/test-key/artist-mb.php?i=" + artistID,
		`GET https://musicbrainz.org/ws/2/release/?fmt=json&query=%22Fixture+Album%22+AND+artist%3A%22Fixture+Artist%22`,
		"GET https://www.theaudiodb.com/api/v1/json/test-key/album-mb.php?i=" + groupID,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fixture provider order = %#v, want %#v", got, want)
	}
}

func hasFixtureImage(images []item.ImageInfo, imageType item.ImageType, path string) bool {
	for _, image := range images {
		if image.Type == imageType && image.Path == path {
			return true
		}
	}
	return false
}
