// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/library"
)

// hostCountingDoer answers every remote request with an empty JSON search
// result and counts the requests per host.
type hostCountingDoer struct {
	mu    sync.Mutex
	hosts map[string]int
}

func (d *hostCountingDoer) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	if d.hosts == nil {
		d.hosts = make(map[string]int)
	}
	d.hosts[request.URL.Host]++
	d.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"artists":[],"release-groups":[],"releases":[],"count":0}`)),
		Request:    request,
	}, nil
}

func (d *hostCountingDoer) count(host string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hosts[host]
}

// TestLibraryAvailableOptionsDriveRemoteFetchers follows the jellyfin-web
// library dialog: it reads GET /Libraries/AvailableOptions, saves the
// fetchers the tester left checked through POST
// /Library/VirtualFolders/LibraryOptions, and scans. A fetcher the tester
// turns off makes no request; turning it back on makes the next scan use it.
func TestLibraryAvailableOptionsDriveRemoteFetchers(t *testing.T) {
	doer := &hostCountingDoer{}
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"), withProviderRemoteDoer(doer))
	source := t.TempDir()
	writeTrack := func(album string) {
		t.Helper()
		path := filepath.Join(source, "Artist", album, "01 - Track.mp3")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create album %q: %v", album, err)
		}
		if err := os.WriteFile(path, []byte("track"), 0o600); err != nil {
			t.Fatalf("write track of %q: %v", album, err)
		}
	}
	writeTrack("First Album")
	_, err := app.topology.AddVirtualFolder(t.Context(), "Music", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}})
	if err != nil {
		t.Fatalf("add music library: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	folders := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Library/VirtualFolders", nil, token)
	var virtualFolders []struct {
		ItemID string `json:"ItemId"`
	}
	if err := json.Unmarshal(folders.Body.Bytes(), &virtualFolders); err != nil || len(virtualFolders) != 1 || virtualFolders[0].ItemID == "" {
		t.Fatalf("GET /Library/VirtualFolders = %d %s (%v), want one library with an ItemId", folders.Code, folders.Body.String(), err)
	}

	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Libraries/AvailableOptions?LibraryContentType=music&IsNewLibrary=true", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Libraries/AvailableOptions status = %d: %s", rec.Code, rec.Body.String())
	}
	var available struct {
		TypeOptions []struct {
			Type             string
			MetadataFetchers []struct {
				Name           string
				DefaultEnabled bool
			}
			ImageFetchers []struct {
				Name           string
				DefaultEnabled bool
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &available); err != nil {
		t.Fatalf("decode available options: %v", err)
	}

	// The composed catalog lists the dynamic image providers.
	movies := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Libraries/AvailableOptions?LibraryContentType=movies", nil, token)
	if body := movies.Body.String(); movies.Code != http.StatusOK || !strings.Contains(body, `"Name":"Screen Grabber"`) || !strings.Contains(body, `"Name":"Embedded Image Extractor"`) {
		t.Fatalf("movies available options = %d %s, want the dynamic image fetchers", movies.Code, body)
	}

	// save builds TypeOptions as libraryoptionseditor getLibraryOptions does:
	// the checked fetchers, and every listed fetcher as the order.
	save := func(musicBrainz bool) {
		t.Helper()
		typeOptions := []library.TypeOptions{}
		listed := false
		for _, entry := range available.TypeOptions {
			if len(entry.MetadataFetchers) == 0 && len(entry.ImageFetchers) == 0 {
				continue
			}
			options := library.TypeOptions{Type: entry.Type, MetadataFetchers: []string{}, MetadataFetcherOrder: []string{}, ImageFetchers: []string{}, ImageFetcherOrder: []string{}}
			for _, fetcher := range entry.MetadataFetchers {
				options.MetadataFetcherOrder = append(options.MetadataFetcherOrder, fetcher.Name)
				enabled := fetcher.DefaultEnabled
				if fetcher.Name == "MusicBrainz" {
					listed = true
					enabled = musicBrainz
				}
				if enabled {
					options.MetadataFetchers = append(options.MetadataFetchers, fetcher.Name)
				}
			}
			for _, fetcher := range entry.ImageFetchers {
				options.ImageFetcherOrder = append(options.ImageFetcherOrder, fetcher.Name)
				if fetcher.DefaultEnabled {
					options.ImageFetchers = append(options.ImageFetchers, fetcher.Name)
				}
			}
			typeOptions = append(typeOptions, options)
		}
		if !listed {
			t.Fatalf("available options list no MusicBrainz fetcher: %s", rec.Body.String())
		}
		options := library.DefaultLibraryOptions()
		options.PathInfos = []library.MediaPathInfo{{Path: source}}
		options.TypeOptions = typeOptions
		body, err := json.Marshal(map[string]any{"Id": virtualFolders[0].ItemID, "LibraryOptions": options})
		if err != nil {
			t.Fatalf("encode library options: %v", err)
		}
		update := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Library/VirtualFolders/LibraryOptions", body, token)
		if update.Code != http.StatusNoContent {
			t.Fatalf("POST /Library/VirtualFolders/LibraryOptions status = %d: %s", update.Code, update.Body.String())
		}
	}
	scan := func() {
		t.Helper()
		result, err := app.scanner.Scan(t.Context())
		if err != nil {
			t.Fatalf("scan music library: %v", err)
		}
		if len(result.Errors) != 0 {
			t.Fatalf("scan errors = %#v", result.Errors)
		}
	}

	save(false)
	scan()
	if got := doer.count("musicbrainz.org"); got != 0 {
		t.Fatalf("MusicBrainz requests with the fetcher off = %d, want 0", got)
	}

	save(true)
	writeTrack("Second Album")
	scan()
	if got := doer.count("musicbrainz.org"); got == 0 {
		t.Fatal("MusicBrainz requests with the fetcher on = 0, want the new album looked up")
	}
}
