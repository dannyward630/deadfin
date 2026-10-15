// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/audiodb"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestAudioDbRemoteConfigPacesFreeTier(t *testing.T) {
	source := clock.NewFake(time.Unix(1_700_000_000, 0))
	config := audioDbRemoteConfig(source)

	if config.Clock != source {
		t.Fatal("AudioDb remote config did not retain the application clock")
	}
	if config.MinInterval != audiodb.DefaultRequestInterval || config.Burst != 1 {
		t.Fatalf("AudioDb pacing = %s/burst %d, want %s/burst 1", config.MinInterval, config.Burst, audiodb.DefaultRequestInterval)
	}
}

func TestApplicationScannerComposesLocalImageAndNFORefreshers(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	source := t.TempDir()
	movie := filepath.Join(source, "movie.mkv")
	poster := filepath.Join(source, "poster.jpg")
	nfo := filepath.Join(source, "movie.nfo")
	for path, contents := range map[string]string{
		movie:  "movie",
		poster: "poster",
		nfo:    `<movie><title>Composed NFO Movie</title><year>2024</year><uniqueid type="tmdb">42</uniqueid></movie>`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write scanner fixture %q: %v", path, err)
		}
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add composed scanner source: %v", err)
	}
	if err := app.scanner.Trigger(); err != nil {
		t.Fatalf("trigger composed scan: %v", err)
	}
	waitForComposedScan(t, app)

	result, err := store.NewItemStore(app.stores).Query(t.Context(), store.ItemQuery{Path: movie})
	if err != nil {
		t.Fatalf("query composed scanned movie: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("composed scanned movie count = %d, want 1", len(result.Items))
	}
	got := result.Items[0]
	if got.Name == nil || *got.Name != "Composed NFO Movie" || got.ProductionYear == nil || *got.ProductionYear != 2024 {
		t.Fatalf("composed NFO fields = %#v, want title and year", got)
	}
	if providerID, ok := got.ProviderIDs.Get("tmdb"); !ok || providerID != "42" {
		t.Fatalf("composed NFO provider IDs = %#v, want tmdb 42", got.ProviderIDs.Snapshot())
	}
	for _, image := range got.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == poster {
			return
		}
	}
	t.Fatalf("composed local artwork = %#v, want primary %q", got.ImageInfos, poster)
}

func waitForComposedScan(t *testing.T, app *application) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		result, err, complete := app.scanner.LastResult()
		if complete {
			if err != nil {
				t.Fatalf("composed scan failed: %v", err)
			}
			if len(result.Errors) != 0 {
				t.Fatalf("composed scan errors = %#v", result.Errors)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("composed scanner did not finish")
		case <-time.After(time.Millisecond):
		}
	}
}

// offlineProviderDoer refuses every remote metadata request, so a library
// scan in a composition test never contacts a live provider API.
type offlineProviderDoer struct{}

func (offlineProviderDoer) Do(request *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("composition test: no live provider access for %s", request.URL.Host)
}

func TestApplicationScannerMaterializesNFOTaxonomyForAuthenticatedRoutes(t *testing.T) {
	// The music library runs MusicBrainz for its albums and artist; refuse
	// those requests instead of reaching musicbrainz.org.
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"), withProviderRemoteDoer(offlineProviderDoer{}))
	movieSource, musicSource := filepath.Join(t.TempDir(), "movies"), filepath.Join(t.TempDir(), "music")
	fixtures := map[string]string{
		filepath.Join(movieSource, "Alpha Movie", "Alpha Movie.mkv"):          "alpha movie",
		filepath.Join(movieSource, "Alpha Movie", "movie.nfo"):                `<movie><title>Alpha Movie</title><genre>Drama</genre><genre>Neo Noir</genre><studio>Beta Works</studio><studio>Zeta Pictures</studio></movie>`,
		filepath.Join(movieSource, "Beta Movie", "Beta Movie.mkv"):            "beta movie",
		filepath.Join(movieSource, "Beta Movie", "movie.nfo"):                 `<movie><title>Beta Movie</title><genre>drama</genre><studio>zeta pictures</studio></movie>`,
		filepath.Join(musicSource, "Artist", "Alpha Album", "01 - Alpha.mp3"): "alpha track",
		filepath.Join(musicSource, "Artist", "Alpha Album", "album.nfo"):      `<album><title>Alpha Album</title><genre>Synthwave</genre><studio>Indie House</studio></album>`,
		filepath.Join(musicSource, "Artist", "Beta Album", "01 - Beta.mp3"):   "beta track",
		filepath.Join(musicSource, "Artist", "Beta Album", "album.nfo"):       `<album><title>Beta Album</title><genre>synthwave</genre><studio>indie house</studio></album>`,
	}
	for path, contents := range fixtures {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create NFO fixture directory for %q: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write NFO fixture %q: %v", path, err)
		}
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: movieSource}}}); err != nil {
		t.Fatalf("add movie NFO library: %v", err)
	}
	musicLibrary, err := app.topology.AddVirtualFolder(t.Context(), "Music", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: musicSource}}})
	if err != nil {
		t.Fatalf("add music NFO library: %v", err)
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan NFO taxonomy fixtures: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan NFO taxonomy fixture errors = %#v", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	baseline := applicationNFOTaxonomyItems(t, items)
	assertApplicationNFOTaxonomyItems(t, baseline, []applicationTaxonomyExpectation{
		{kind: item.KindGenre, id: "090eac6e9de4fe1fbc194e5b96691277", name: "Drama"},
		{kind: item.KindGenre, id: "91b1e77cc68aa274431aeec541a8188c", name: "Neo Noir"},
		{kind: item.KindMusicGenre, id: "4ad613d972d9eb295f87e0410ea4d028", name: "Synthwave"},
		{kind: item.KindStudio, id: "bb8227f303e4970b35d1db2db05715eb", name: "Beta Works"},
		{kind: item.KindStudio, id: "473daa6355fd3679b72f9c6e2c9fa912", name: "Indie House"},
		{kind: item.KindStudio, id: "5f426929d54b49daf7373a4348a9eef6", name: "Zeta Pictures"},
	})

	musicParent := applicationScannedItem(t, items, musicLibrary.Path, item.KindCollectionFolder)
	if musicParent.Extra.CollectionFolder == nil || musicParent.Extra.CollectionFolder.CollectionType == nil || *musicParent.Extra.CollectionFolder.CollectionType != item.CollectionTypeMusic {
		t.Fatalf("scanned music route parent = %#v, want CollectionFolder with Music collection type", musicParent)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	for _, tc := range []struct {
		name string
		path string
		want []applicationTaxonomyExpectation
	}{
		{name: "movie genres", path: "/Genres?EnableTotalRecordCount=true", want: []applicationTaxonomyExpectation{{kind: item.KindGenre, id: "090eac6e9de4fe1fbc194e5b96691277", name: "Drama"}, {kind: item.KindGenre, id: "91b1e77cc68aa274431aeec541a8188c", name: "Neo Noir"}}},
		{name: "music parent genres", path: "/Genres?EnableTotalRecordCount=true&ParentId=" + musicParent.ID.Dashed(), want: []applicationTaxonomyExpectation{{kind: item.KindMusicGenre, id: "4ad613d972d9eb295f87e0410ea4d028", name: "Synthwave"}}},
		{name: "all studios", path: "/Studios?EnableTotalRecordCount=true", want: []applicationTaxonomyExpectation{{kind: item.KindStudio, id: "bb8227f303e4970b35d1db2db05715eb", name: "Beta Works"}, {kind: item.KindStudio, id: "473daa6355fd3679b72f9c6e2c9fa912", name: "Indie House"}, {kind: item.KindStudio, id: "5f426929d54b49daf7373a4348a9eef6", name: "Zeta Pictures"}}},
		{name: "music parent studios", path: "/Studios?EnableTotalRecordCount=true&ParentId=" + musicParent.ID.Dashed(), want: []applicationTaxonomyExpectation{{kind: item.KindStudio, id: "473daa6355fd3679b72f9c6e2c9fa912", name: "Indie House"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applicationAssertAuthenticatedTaxonomyRoute(t, app, token, tc.path, tc.want)
		})
	}

	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("rescan NFO taxonomy fixtures: %v", err)
	} else if result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("unchanged NFO taxonomy rescan = %#v, want exact no-op", result)
	}
	if got := applicationNFOTaxonomyItems(t, items); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("unchanged NFO taxonomy rescan persisted different entities\nbefore: %#v\nafter:  %#v", baseline, got)
	}
}

func TestApplicationScannerRollsBackNFOTaxonomyWhenMaterializationFails(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	source := t.TempDir()
	movie := filepath.Join(source, "Atomic Movie.mkv")
	for path, contents := range map[string]string{
		movie:                              "atomic movie",
		filepath.Join(source, "movie.nfo"): `<movie><title>Atomic Movie</title><genre>Rollback Genre</genre><studio>Rollback Studio</studio></movie>`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write atomic NFO fixture %q: %v", path, err)
		}
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add atomic NFO library: %v", err)
	}
	if err := storetest.InstallSQLiteItemKindInsertFailure(t.Context(), app.stores, item.KindGenre); err != nil {
		t.Fatalf("install NFO taxonomy materialization failure: %v", err)
	}

	result, err := app.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with injected taxonomy materialization failure: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("scan with injected taxonomy materialization failure reported no folder error")
	}
	items := store.NewItemStore(app.stores)
	if got, err := items.Query(t.Context(), store.ItemQuery{Path: movie}); err != nil {
		t.Fatalf("query failed NFO materialization plan: %v", err)
	} else if len(got.Items) != 0 {
		t.Fatalf("failed NFO materialization retained scanned movie = %#v, want none", got.Items)
	}
	for _, kind := range []item.Kind{item.KindGenre, item.KindStudio} {
		result, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{kind}})
		if err != nil {
			t.Fatalf("query failed NFO materialized %s entities: %v", kind, err)
		}
		if len(result.Items) != 0 {
			t.Fatalf("failed NFO materialization retained %s entities = %#v, want none", kind, result.Items)
		}
	}
}

type applicationTaxonomyExpectation struct {
	kind item.Kind
	id   string
	name string
}

func applicationNFOTaxonomyItems(t *testing.T, items *store.ItemStore) []applicationTaxonomyExpectation {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindGenre, item.KindMusicGenre, item.KindStudio}})
	if err != nil {
		t.Fatalf("query persisted NFO taxonomy: %v", err)
	}
	got := make([]applicationTaxonomyExpectation, len(result.Items))
	for index, value := range result.Items {
		if value.Name == nil {
			t.Fatalf("persisted NFO taxonomy entity %s has no name: %#v", value.ID, value)
		}
		got[index] = applicationTaxonomyExpectation{kind: value.Kind, id: value.ID.String(), name: *value.Name}
	}
	sort.Slice(got, func(left, right int) bool {
		if got[left].kind != got[right].kind {
			return got[left].kind.String() < got[right].kind.String()
		}
		if got[left].name != got[right].name {
			return got[left].name < got[right].name
		}
		return got[left].id < got[right].id
	})
	return got
}

func assertApplicationNFOTaxonomyItems(t *testing.T, got, want []applicationTaxonomyExpectation) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted NFO taxonomy = %#v, want %#v", got, want)
	}
}

func applicationAssertAuthenticatedTaxonomyRoute(t *testing.T, app *application, token, path string, want []applicationTaxonomyExpectation) {
	t.Helper()
	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d: %s", path, rec.Code, http.StatusOK, rec.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode GET %s: %v; body: %s", path, err, rec.Body.String())
	}
	got := make([]applicationTaxonomyExpectation, len(page.Items))
	for index, value := range page.Items {
		kind, err := item.ParseKind(string(value.Type))
		if err != nil {
			t.Fatalf("GET %s item %d Type = %q, want a known taxonomy kind: %v", path, index, value.Type, err)
		}
		got[index] = applicationTaxonomyExpectation{kind: kind, id: value.ID.String(), name: value.Name}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GET %s items = %#v, want %#v", path, got, want)
	}
	if page.TotalRecordCount != len(want) {
		t.Fatalf("GET %s total = %d, want %d", path, page.TotalRecordCount, len(want))
	}
}
