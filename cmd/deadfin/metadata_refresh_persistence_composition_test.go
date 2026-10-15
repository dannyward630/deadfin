// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestMetadataRefreshCompositionPersistsProviderIDAndImage proves the manual
// RefreshMetadata scheduled task backfills a TMDb provider ID and a remote
// Primary image through the scanner, store, scheduler, and the scheduled-task
// REST API. It also asserts that a local field the cassette omits
// (RunTimeTicks) survives the backfill, and that the adapter logs
// refresh-evidence summary counts. The TMDb HTTP transport is a recorded
// cassette, so no live API is contacted.
//
// The test uses a two-phase setup so the omitted-field assertion is exact:
//  1. A library scan with TheMovieDb disabled in the Movie TypeOptions creates
//     the item with local metadata only, so no provider data or RunTimeTicks
//     is present.
//  2. RunTimeTicks is seeded onto the stored item.
//  3. TheMovieDb is enabled, and RefreshMetadata runs the backfill.
//     The TMDb provider enriches the item; the backfill preserves RunTimeTicks
//     because the cassette omits runtime.
func TestMetadataRefreshCompositionPersistsProviderIDAndImage(t *testing.T) {
	cassette, err := remotetest.Load(filepath.Join("..", "..", "internal", "providers", "tmdb", "testdata", "the-matrix.json"))
	if err != nil {
		t.Fatalf("load TMDb cassette: %v", err)
	}
	root := filepath.Join(t.TempDir(), "program-data")
	if err := os.MkdirAll(filepath.Join(root, "config", "plugins"), 0o700); err != nil {
		t.Fatalf("create config plugins dir: %v", err)
	}
	// Supply a test API key through the plugin configuration file the
	// application reads at construction (tmdb.LoadConfig). The cassette
	// matcher ignores the api_key query parameter, so the key only needs to
	// be non-empty so the provider is not inert.
	if err := os.WriteFile(filepath.Join(root, "config", "plugins", "Jellyfin.Plugin.Tmdb.xml"), []byte(`<PluginConfiguration><TmdbApiKey>test-key</TmdbApiKey></PluginConfiguration>`), 0o600); err != nil {
		t.Fatalf("write tmdb plugin config: %v", err)
	}
	app := newCompositionApplication(t, root, withProviderRemoteDoer(cassette))

	// Complete the startup wizard and create an admin user so the
	// scheduled-task API is reachable under elevation.
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	login := compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", "")
	token := compositionAccessToken(t, login)
	if token == "" {
		t.Fatal("login returned no access token")
	}

	// Add an NFO-less Movie fixture to a library whose Movie TypeOptions name
	// no TMDb fetcher. The filename parses to "The Matrix" with year 1999,
	// which the cassette search entry matches. The library scan creates the
	// item with local metadata only because TheMovieDb is not enabled.
	source := t.TempDir()
	movie := filepath.Join(source, "The Matrix (1999).mkv")
	if err := os.WriteFile(movie, []byte("matrix"), 0o600); err != nil {
		t.Fatalf("write movie fixture: %v", err)
	}
	folder, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                   true,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 []library.MediaPathInfo{{Path: source}},
		TypeOptions:               []library.TypeOptions{{Type: "Movie", MetadataFetchers: []string{"The Open Movie Database"}}},
	})
	if err != nil {
		t.Fatalf("add movie library: %v", err)
	}

	// Phase 1: Run a library scan so the item is created with local metadata
	// only. The Movie TypeOptions do not enable TheMovieDb.
	scanStart := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+tasks.RefreshLibraryID, nil, token)
	if scanStart.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", tasks.RefreshLibraryID, scanStart.Code, http.StatusNoContent, scanStart.Body.String())
	}
	scanResult := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshLibraryID, 10*time.Second)
	if scanResult["Status"] != tasks.StatusCompleted {
		errMsg, _ := scanResult["ErrorMessage"].(string)
		t.Fatalf("library scan result status = %v, want %s; error: %s", scanResult["Status"], tasks.StatusCompleted, errMsg)
	}

	items := store.NewItemStore(app.stores)

	// Load the scanned movie and assert it has no provider data yet.
	loaded, err := items.Query(t.Context(), store.ItemQuery{Path: movie})
	if err != nil {
		t.Fatalf("reload scanned movie: %v", err)
	}
	if len(loaded.Items) != 1 {
		t.Fatalf("reloaded movie count = %d, want 1", len(loaded.Items))
	}
	scanned := loaded.Items[0]
	if id, ok := scanned.ProviderIDs.Get("Tmdb"); ok && id != "" {
		t.Fatalf("library scan with TheMovieDb disabled set Tmdb provider ID = %q, want none", id)
	}

	// Phase 2: Seed RunTimeTicks, a local field the cassette omits. The
	// backfill must preserve it because the remote stage does not supply
	// runtime.
	wantRunTimeTicks := int64(12_345_678_000)
	scanned.RunTimeTicks = &wantRunTimeTicks
	if err := items.Upsert(t.Context(), scanned); err != nil {
		t.Fatalf("seed RunTimeTicks: %v", err)
	}

	// Drop the TypeOptions so every remote provider, TheMovieDb included,
	// runs during RefreshMetadata.
	enabled := library.LibraryOptions{
		Enabled:                   true,
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 []library.MediaPathInfo{{Path: source}},
	}
	if err := app.topology.UpdateLibraryOptions(t.Context(), folder.CollectionFolderID, enabled); err != nil {
		t.Fatalf("enable internet providers: %v", err)
	}

	// Capture log output so the refresh-evidence summary line can be asserted.
	var logBuf bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	// Phase 3: Start the RefreshMetadata task through the scheduled-task API.
	// The backfill enriches the stored item with TMDb data while preserving
	// the seeded RunTimeTicks.
	start := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+tasks.RefreshMetadataID, nil, token)
	if start.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", tasks.RefreshMetadataID, start.Code, http.StatusNoContent, start.Body.String())
	}

	// Wait for the task result through the same API. A Completed result with
	// no error is the success path; a Failed result carries the folder error.
	result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshMetadataID, 10*time.Second)
	if result["Status"] != tasks.StatusCompleted {
		errMsg, _ := result["ErrorMessage"].(string)
		t.Fatalf("RefreshMetadata result status = %v, want %s; error: %s", result["Status"], tasks.StatusCompleted, errMsg)
	}

	// Assert the refresh-evidence summary line carries exact provider and
	// backfill counts. The cassette supplies three provider IDs (Tmdb, Imdb,
	// TmdbCollection) and four images (one poster, two backdrops, one logo).
	// The updated and skipped counters come from
	// RefreshResult.BackfillUpdated and BackfillSkipped, which the scanner
	// produces. The scanner counts both the stored root Folder and the
	// stored movie as ForceRefresh-updated, so the production value for this
	// fixture is updated=2 skipped=0.
	evidence := logBuf.String()
	wantSummary := "refresh metadata: provider invocations=1 persisted_provider_ids=3 persisted_images=4 updated=2 skipped=0"
	if !strings.Contains(evidence, wantSummary) {
		t.Fatalf("refresh evidence missing summary counts; want %q; log:\n%s", wantSummary, evidence)
	}

	// Reload the Movie from the store and assert the backfilled provider ID
	// and remote Primary image with exact values.
	loaded2, err := items.Query(t.Context(), store.ItemQuery{Path: movie})
	if err != nil {
		t.Fatalf("reload movie after backfill: %v", err)
	}
	if len(loaded2.Items) != 1 {
		t.Fatalf("reloaded movie count after backfill = %d, want 1", len(loaded2.Items))
	}
	enriched := loaded2.Items[0]
	if id, ok := enriched.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
		t.Fatalf("reloaded movie Tmdb provider ID = %q,%t, want \"603\"", id, ok)
	}
	var remotePrimary string
	for _, image := range enriched.ImageInfos {
		if image.Type == item.ImageTypePrimary {
			remotePrimary = image.Path
			break
		}
	}
	// Assert the exact remote Primary image path. The cassette /movie/603
	// response lists /best-poster.jpg as the first poster, the TMDb image
	// base is https://image.tmdb.org/t/p/, and the size is original, so the
	// full path is fixed.
	wantImage := "https://image.tmdb.org/t/p/original/best-poster.jpg"
	if remotePrimary != wantImage {
		t.Fatalf("reloaded movie remote Primary image = %q, want %q", remotePrimary, wantImage)
	}

	// Assert the seeded RunTimeTicks survived the backfill. The cassette
	// omits runtime, so the backfill merge preserves the stored value.
	if enriched.RunTimeTicks == nil {
		t.Fatal("reloaded movie RunTimeTicks = nil, want a non-nil value preserved from the seeded row")
	}
	if *enriched.RunTimeTicks != wantRunTimeTicks {
		t.Fatalf("reloaded movie RunTimeTicks = %d, want %d (preserved from seeded row)", *enriched.RunTimeTicks, wantRunTimeTicks)
	}
}

// TestMetadataRefreshCompositionReportsFolderFailure proves a folder scan error
// produces a visible Failed task result that carries the folder path and the
// error. The inaccessible source is a regular file, so the failure is
// deterministic under root as well as unprivileged runs.
func TestMetadataRefreshCompositionReportsFolderFailure(t *testing.T) {
	cassette := remotetest.NewCassette()
	root := filepath.Join(t.TempDir(), "program-data")
	if err := os.MkdirAll(filepath.Join(root, "config", "plugins"), 0o700); err != nil {
		t.Fatalf("create config plugins dir: %v", err)
	}
	app := newCompositionApplication(t, root, withProviderRemoteDoer(cassette))

	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	login := compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", "")
	token := compositionAccessToken(t, login)
	if token == "" {
		t.Fatal("login returned no access token")
	}

	// Point the library at a directory, then remove it before the task runs.
	// AddVirtualFolder requires a directory, so the path is valid at add time;
	// the scanner's ReadDir fails on the now-missing path, which the
	// metadataRefreshAdapter folds into a Failed result with the path and the
	// enumerate error. Removing the directory makes the failure deterministic
	// under root as well as unprivileged runs.
	failingSource := filepath.Join(t.TempDir(), "missing-library")
	if err := os.MkdirAll(failingSource, 0o700); err != nil {
		t.Fatalf("create failing library source: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                   true,
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 []library.MediaPathInfo{{Path: failingSource}},
	}); err != nil {
		t.Fatalf("add failing movie library: %v", err)
	}
	if err := os.RemoveAll(failingSource); err != nil {
		t.Fatalf("remove failing library source: %v", err)
	}

	start := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+tasks.RefreshMetadataID, nil, token)
	if start.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", tasks.RefreshMetadataID, start.Code, http.StatusNoContent, start.Body.String())
	}

	result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshMetadataID, 10*time.Second)
	if result["Status"] != tasks.StatusFailed {
		t.Fatalf("RefreshMetadata folder-failure result status = %v, want %s", result["Status"], tasks.StatusFailed)
	}
	errMsg, _ := result["ErrorMessage"].(string)
	if errMsg == "" {
		t.Fatalf("RefreshMetadata folder-failure result has no error message: %#v", result)
	}
	if !strings.Contains(errMsg, failingSource) {
		t.Fatalf("RefreshMetadata error message = %q, want it to contain the folder path %q", errMsg, failingSource)
	}
}

// TestMetadataRefreshCompositionReportsProviderFailure proves a provider-stage
// failure (TMDb search returns HTTP 503) is logged with the provider, item, and
// cause, and that the task reports Failed because nothing persisted. The
// source directory exists and is readable, so the scanner reaches the
// refresher, which calls the TMDb search endpoint and receives a 503.
func TestMetadataRefreshCompositionReportsProviderFailure(t *testing.T) {
	// Build a cassette that returns 200 for /configuration (so the provider is
	// not inert) and 503 for /search/movie (so the provider-stage search fails).
	cassette := remotetest.NewCassette(
		remotetest.Interaction{
			Method: http.MethodGet,
			URL:    "https://api.themoviedb.org/3/configuration",
			Status: http.StatusOK,
			Body:   json.RawMessage(`{"images":{"secure_base_url":"https://image.tmdb.org/t/p/"}}`),
		},
		remotetest.Interaction{
			Method: http.MethodGet,
			URL:    "https://api.themoviedb.org/3/search/movie?query=The+Matrix&year=1999&language=en&include_adult=false",
			Status: http.StatusServiceUnavailable,
			Body:   json.RawMessage(`{"status_message":"Service unavailable"}`),
		},
	)
	root := filepath.Join(t.TempDir(), "program-data")
	if err := os.MkdirAll(filepath.Join(root, "config", "plugins"), 0o700); err != nil {
		t.Fatalf("create config plugins dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "plugins", "Jellyfin.Plugin.Tmdb.xml"), []byte(`<PluginConfiguration><TmdbApiKey>test-key</TmdbApiKey></PluginConfiguration>`), 0o600); err != nil {
		t.Fatalf("write tmdb plugin config: %v", err)
	}
	app := newCompositionApplication(t, root, withProviderRemoteDoer(cassette))

	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	login := compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", "")
	token := compositionAccessToken(t, login)
	if token == "" {
		t.Fatal("login returned no access token")
	}

	// Add a Movie fixture the scanner can enumerate. The directory exists and
	// is readable, so the failure is in the provider stage, not source
	// enumeration.
	source := t.TempDir()
	movie := filepath.Join(source, "The Matrix (1999).mkv")
	if err := os.WriteFile(movie, []byte("matrix"), 0o600); err != nil {
		t.Fatalf("write movie fixture: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                   true,
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: "en",
		MetadataCountryCode:       "US",
		PathInfos:                 []library.MediaPathInfo{{Path: source}},
	}); err != nil {
		t.Fatalf("add movie library: %v", err)
	}

	// Capture log output so the refresh-evidence failure-path line can be
	// asserted alongside the task result.
	var logBuf bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	start := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+tasks.RefreshMetadataID, nil, token)
	if start.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", tasks.RefreshMetadataID, start.Code, http.StatusNoContent, start.Body.String())
	}

	result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshMetadataID, 10*time.Second)
	if result["Status"] != tasks.StatusFailed {
		t.Fatalf("RefreshMetadata provider-failure result status = %v, want %s", result["Status"], tasks.StatusFailed)
	}
	// The failed provider persisted nothing, so the task reports the zero-work
	// failure. The 503 itself is logged by the registry with the provider, the
	// item, and the exact cause; it does not fail the library folder.
	errMsg, _ := result["ErrorMessage"].(string)
	if !strings.Contains(errMsg, "persisted no provider ID or image") {
		t.Fatalf("RefreshMetadata error message = %q, want the zero-work failure", errMsg)
	}
	providerCause := "remote: upstream returned status 503"
	if strings.Contains(errMsg, "failed path") || strings.Contains(errMsg, providerCause) {
		t.Fatalf("RefreshMetadata error message = %q, want the provider error logged, not a folder failure", errMsg)
	}
	evidence := logBuf.String()
	for _, want := range []string{"remote metadata provider failed", "provider=TheMovieDb", "kind=Movie", providerCause} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("log does not contain %q; log:\n%s", want, evidence)
		}
	}
}

// waitForScheduledTaskResult polls GET /ScheduledTasks/{taskId} until the task
// records a last execution result, then returns that result map.
func waitForScheduledTaskResult(t *testing.T, handler http.Handler, token, taskID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		rec := authenticatedCompositionRequest(handler, http.MethodGet, "/ScheduledTasks/"+taskID, nil, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ScheduledTasks/%s status = %d, want %d; body: %s", taskID, rec.Code, http.StatusOK, rec.Body.String())
		}
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatalf("unmarshal scheduled task info: %v; body: %s", err, rec.Body.String())
		}
		if last, ok := info["LastExecutionResult"].(map[string]any); ok && last != nil {
			if state, _ := info["State"].(string); state == string(tasks.StateIdle) || state == "" {
				return last
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("scheduled task %s did not record a result within %s; info: %s", taskID, timeout, formatTaskInfo(info))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// formatTaskInfo returns a compact debug string for a scheduled-task info map.
func formatTaskInfo(info map[string]any) string {
	state, _ := info["State"].(string)
	if last, ok := info["LastExecutionResult"].(map[string]any); ok && last != nil {
		status, _ := last["Status"].(string)
		return "state=" + state + " status=" + status
	}
	return "state=" + state
}
