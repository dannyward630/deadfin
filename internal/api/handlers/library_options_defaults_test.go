// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestLibraryOptionsOmittedMembersKeepUpstreamDefaults drives the real
// library handlers over a real topology and reads back the persisted
// options.xml. Upstream deserializes LibraryOptions after its
// constructor runs, so every member the client omits keeps the constructor
// default on create and on update. Members the client sends, including
// explicit false, "", 0, and [], replace the default.
func TestLibraryOptionsOmittedMembersKeepUpstreamDefaults(t *testing.T) {
	rt, topology, dataRoot := libraryOptionsHarness(t)

	t.Run("create with partial options keeps every omitted default", func(t *testing.T) {
		media := libraryOptionsMediaPath(t)
		body := `{"LibraryOptions":{"PathInfos":[{"Path":` + jsonString(t, media) + `}]}}`
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Movies&collectionType=movies", body, "application/json")
		assertLibraryStatus(t, response, http.StatusNoContent)

		want := library.DefaultLibraryOptions()
		want.PathInfos = []library.MediaPathInfo{{Path: media}}
		got := assertPersistedOptions(t, dataRoot, "Movies", want)
		// The fields that a deployed server once persisted as zero values.
		if !got.Enabled || !got.EnablePhotos || !got.EnableAutomaticSeriesGrouping ||
			got.SeasonZeroDisplayName != "Specials" || !got.SkipSubtitlesIfAudioTrackMatches ||
			!got.RequirePerfectSubtitleMatch || !got.SaveSubtitlesWithMedia ||
			got.AllowEmbeddedSubtitles != library.AllowEmbeddedSubtitlesAll ||
			!slices.Equal(got.CustomTagDelimiters, []string{"/", "|", ";", `\`}) {
			t.Fatalf("persisted options = %#v, want upstream constructor defaults", got)
		}
	})

	t.Run("create honors explicit values and case-insensitive names", func(t *testing.T) {
		media := libraryOptionsMediaPath(t)
		body := `{"libraryoptions":{"enableinternetproviders":true,"Enabled":false,"SeasonZeroDisplayName":"",` +
			`"AutomaticRefreshIntervalDays":0,"CustomTagDelimiters":[],"AllowEmbeddedSubtitles":"AllowText",` +
			`"PreferredMetadataLanguage":"ja","PathInfos":[{"Path":` + jsonString(t, media) + `}]}}`
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Shows&collectionType=tvshows", body, "application/json")
		assertLibraryStatus(t, response, http.StatusNoContent)

		want := library.DefaultLibraryOptions()
		want.EnableInternetProviders = true
		want.Enabled = false
		want.SeasonZeroDisplayName = ""
		want.CustomTagDelimiters = []string{}
		want.AllowEmbeddedSubtitles = library.AllowEmbeddedSubtitlesText
		want.PreferredMetadataLanguage = "ja"
		want.PathInfos = []library.MediaPathInfo{{Path: media}}
		assertPersistedOptions(t, dataRoot, "Shows", want)
	})

	t.Run("create without a body persists the defaults with query paths", func(t *testing.T) {
		media := libraryOptionsMediaPath(t)
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Music&collectionType=music&paths="+url.QueryEscape(media), "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)

		want := library.DefaultLibraryOptions()
		want.PathInfos = []library.MediaPathInfo{{Path: media}}
		assertPersistedOptions(t, dataRoot, "Music", want)
	})

	t.Run("create with null options persists the defaults", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Books", `{"LibraryOptions":null}`, "application/json")
		assertLibraryStatus(t, response, http.StatusNoContent)
		assertPersistedOptions(t, dataRoot, "Books", library.DefaultLibraryOptions())
	})

	t.Run("update replaces the document and defaults omitted members", func(t *testing.T) {
		folders, err := topology.VirtualFolders(t.Context())
		if err != nil {
			t.Fatalf("list virtual folders: %v", err)
		}
		var shows library.VirtualFolder
		for _, folder := range folders {
			if folder.Name == "Shows" {
				shows = folder
			}
		}
		if shows.Name == "" {
			t.Fatalf("virtual folders = %#v, want Shows", folders)
		}
		// Shows was created with Enabled=false and an empty
		// SeasonZeroDisplayName. Upstream's update writes the request value
		// wholesale (CollectionFolder.UpdateLibraryOptions), so omitted
		// members return to the constructor defaults.
		body := `{"Id":"` + shows.CollectionFolderID.Dashed() + `","LibraryOptions":{"EnableInternetProviders":true,` +
			`"SaveLocalMetadata":false,"PathInfos":[{"Path":` + jsonString(t, shows.Options.PathInfos[0].Path) + `}]}}`
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders/LibraryOptions", body, "application/json")
		assertLibraryStatus(t, response, http.StatusNoContent)

		want := library.DefaultLibraryOptions()
		want.EnableInternetProviders = true
		want.PathInfos = shows.Options.PathInfos
		assertPersistedOptions(t, dataRoot, "Shows", want)
	})
}

func libraryOptionsHarness(t *testing.T) (*router.Router, *library.Topology, string) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite stores: %v", err)
		}
	})
	dataRoot := t.TempDir()
	topology, err := library.NewTopology(dataRoot, store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	rt := router.New()
	handlers.RegisterLibrary(rt, &fakeLibraryRefreshTrigger{}, topology)
	return rt, topology, dataRoot
}

func libraryOptionsMediaPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create media path: %v", err)
	}
	return path
}

// assertPersistedOptions requires options.xml to be byte-identical to the
// XML encoding of want, then returns the decoded document.
func assertPersistedOptions(t *testing.T, dataRoot, name string, want library.LibraryOptions) library.LibraryOptions {
	t.Helper()
	persisted, err := os.ReadFile(filepath.Join(dataRoot, "root", "default", name, "options.xml"))
	if err != nil {
		t.Fatalf("read %s options.xml: %v", name, err)
	}
	wantXML, err := xml.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want options: %v", err)
	}
	if !bytes.Equal(persisted, wantXML) {
		t.Fatalf("%s options.xml =\n%s\nwant\n%s", name, persisted, wantXML)
	}
	var got library.LibraryOptions
	if err := xml.Unmarshal(persisted, &got); err != nil {
		t.Fatalf("decode %s options.xml: %v", name, err)
	}
	return got
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode JSON string: %v", err)
	}
	return string(encoded)
}
