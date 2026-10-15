// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/library"
)

// TestApplicationCanonicalFixtureInventoryThroughAuthenticatedItems protects the
// public recursive Items inventory after the production application has bootstrapped
// its system topology and scanned the complete generated fixture tree. Artist and
// photo materialization must be public, while metadata Person rows, system roots,
// and the 720p alternate must remain absent from the stock-default inventory.
func TestApplicationCanonicalFixtureInventoryThroughAuthenticatedItems(t *testing.T) {
	fixtures := applicationProbeFixtureRoot(t)
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	for _, libraryFixture := range []struct {
		name, collection, path string
	}{
		{name: "Canonical Movies", collection: "movies", path: filepath.Join(fixtures, "movies")},
		{name: "Canonical Music", collection: "music", path: filepath.Join(fixtures, "music")},
		{name: "Canonical Television", collection: "tvshows", path: filepath.Join(fixtures, "tv")},
		{name: "Canonical Photos", collection: "photos", path: filepath.Join(fixtures, "photos")},
	} {
		if _, err := app.topology.AddVirtualFolder(t.Context(), libraryFixture.name, libraryFixture.collection, library.LibraryOptions{
			PathInfos: []library.MediaPathInfo{{Path: libraryFixture.path}},
		}); err != nil {
			t.Fatalf("add %s library: %v", libraryFixture.name, err)
		}
	}

	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan complete canonical fixture tree: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan complete canonical fixture tree errors = %#v", result.Errors)
	}

	response := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Items?Recursive=true&Fields=Path", nil, token)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /Items Recursive=true Fields=Path status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	actual := canonicalInventoryIdentities(t, response.Body.Bytes(), fixtures)
	if len(actual) != 52 {
		t.Fatalf("authenticated canonical inventory count = %d, want 52 with the 720p alternate hidden", len(actual))
	}
	want := canonicalFixtureInventoryIdentities()
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("authenticated canonical inventory identities differ\nwant: %#v\ngot:  %#v", want, actual)
	}

	primaryID := canonicalInventoryItemID(t, response.Body.Bytes(), fixtures, "Movie:/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv")
	playback := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Items/"+primaryID+"/PlaybackInfo", nil, token)
	if playback.Code != http.StatusOK {
		t.Fatalf("GET primary synthetic feature PlaybackInfo status = %d, want %d; body=%s", playback.Code, http.StatusOK, playback.Body.String())
	}
	assertCanonicalAlternatePlaybackSources(t, playback.Body.Bytes(), fixtures)
}

type canonicalInventoryWireItem struct {
	ID   string `json:"Id"`
	Path string `json:"Path"`
	Type string `json:"Type"`
}

func canonicalInventoryItems(t *testing.T, body []byte) []canonicalInventoryWireItem {
	t.Helper()

	var response struct {
		Items []canonicalInventoryWireItem `json:"Items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode authenticated Items inventory: %v; body=%s", err, body)
	}
	return response.Items
}

func canonicalInventoryIdentities(t *testing.T, body []byte, root string) []string {
	t.Helper()

	items := canonicalInventoryItems(t, body)
	identities := make([]string, 0, len(items))
	for _, value := range items {
		identities = append(identities, value.Type+":"+canonicalInventoryPath(t, root, value.Path))
	}
	sort.Strings(identities)
	return identities
}

func canonicalInventoryItemID(t *testing.T, body []byte, root, want string) string {
	t.Helper()

	for _, value := range canonicalInventoryItems(t, body) {
		if value.Type+":"+canonicalInventoryPath(t, root, value.Path) == want {
			if value.ID == "" {
				t.Fatalf("primary canonical inventory item %q omitted Id", want)
			}
			return value.ID
		}
	}
	t.Fatalf("primary canonical inventory item %q missing from Items response", want)
	return ""
}

func canonicalInventoryPath(t *testing.T, root, raw string) string {
	t.Helper()

	relative, err := filepath.Rel(root, raw)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "<outside-canonical-fixture>:" + filepath.ToSlash(raw)
	}
	return "/media/" + filepath.ToSlash(relative)
}

func assertCanonicalAlternatePlaybackSources(t *testing.T, body []byte, root string) {
	t.Helper()

	var response struct {
		MediaSources []struct {
			Path string `json:"Path"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode synthetic feature PlaybackInfo: %v; body=%s", err, body)
	}
	paths := make([]string, 0, len(response.MediaSources))
	for _, source := range response.MediaSources {
		paths = append(paths, canonicalInventoryPath(t, root, source.Path))
	}
	sort.Strings(paths)
	want := []string{
		"/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv",
		"/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 720p.mkv",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("primary synthetic feature PlaybackInfo source paths = %#v, want primary and alternate %#v", paths, want)
	}
}

func canonicalFixtureInventoryIdentities() []string {
	return []string{
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/10 - Synthetic Track 10.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/11 - Synthetic Track 11.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/2 - Synthetic Track 2.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/3 - Synthetic Track 3.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/4 - Synthetic Track 4.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/5 - Synthetic Track 5.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/6 - Synthetic Track 6.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/7 - Synthetic Track 7.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/8 - Synthetic Track 8.mp3",
		"Audio:/media/music/Goldenrec Generator/Synthetic Album/9 - Synthetic Track 9.mp3",
		"BoxSet:/media/movies/Synthetic Collection [boxset]",
		"Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E01 - First Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E02 - Second Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E03 - Third Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E01 - Return Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E02 - Wide Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Season 02/Synthetic Show S02E03 - Final Signal.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Specials/Synthetic Show S00E01 - Pilot Extra.mkv",
		"Episode:/media/tv/Synthetic Show (2024)/Specials/Synthetic Show S00E02 - Behind The Bars.mkv",
		"Folder:/media/movies",
		"Folder:/media/movies/HLG Sample (2024)",
		"Folder:/media/movies/Odd Names",
		"Folder:/media/movies/Subtitle Sample (2024)",
		"Folder:/media/music",
		"Folder:/media/photos",
		"Folder:/media/tv",
		"Movie:/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv",
		"Movie:/media/movies/Odd Names/Café déjà-vu (2020).mkv",
		"Movie:/media/movies/Odd Names/Film.Name.2022.1080p.BluRay.mkv",
		"Movie:/media/movies/Odd Names/The [Odd] Film (2021) {edition-Director's Cut}.mkv",
		"Movie:/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv",
		"Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv",
		"Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
		"Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
		"Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
		"Movie:/media/movies/Synthetic Disc.bluray.disc",
		"Movie:/media/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv",
		"MusicAlbum:/media/music/Goldenrec Generator/Synthetic Album",
		"MusicArtist:/media/music/Goldenrec Generator",
		"Photo:/media/photos/orientation-1.jpg",
		"Photo:/media/photos/orientation-2.jpg",
		"Photo:/media/photos/orientation-3.jpg",
		"Photo:/media/photos/orientation-4.jpg",
		"Photo:/media/photos/orientation-5.jpg",
		"Photo:/media/photos/orientation-6.jpg",
		"Photo:/media/photos/orientation-7.jpg",
		"Photo:/media/photos/orientation-8.jpg",
		"Season:/media/tv/Synthetic Show (2024)/Season 01",
		"Season:/media/tv/Synthetic Show (2024)/Season 02",
		"Season:/media/tv/Synthetic Show (2024)/Specials",
		"Series:/media/tv/Synthetic Show (2024)",
	}
}
