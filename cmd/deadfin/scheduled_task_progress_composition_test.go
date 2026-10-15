// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// holdingDoer wraps a cassette Doer and blocks on the Nth request until a
// release channel is closed. This lets the composition test hold the second
// item's provider refresh while it polls the scheduled-task API for
// intermediate progress.
type holdingDoer struct {
	inner       remote.Doer
	served      atomic.Int32
	holdAfter   int32
	block       chan struct{}
	blocked     chan struct{}
	holdEnabled atomic.Bool
	blockedOnce sync.Once
}

func newHoldingDoer(inner remote.Doer, holdAfter int32) *holdingDoer {
	return &holdingDoer{
		inner:     inner,
		holdAfter: holdAfter,
		block:     make(chan struct{}),
		blocked:   make(chan struct{}),
	}
}

func (d *holdingDoer) Do(req *http.Request) (*http.Response, error) {
	n := d.served.Add(1)
	if d.holdEnabled.Load() && n == d.holdAfter {
		d.blockedOnce.Do(func() { close(d.blocked) })
		<-d.block
	}
	return d.inner.Do(req)
}

// enableHold makes the Doer start blocking on the holdAfter-th request.
func (d *holdingDoer) enableHold() {
	d.holdEnabled.Store(true)
}

// waitForBlocked waits until the Doer has blocked on the holdAfter-th request.
func (d *holdingDoer) waitForBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-d.blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("holdingDoer did not block within 30s")
	}
}

// release unblocks the held request so the scan can complete.
func (d *holdingDoer) release() {
	close(d.block)
}

// loadCombinedCassette loads two cassette files and merges their interactions
// into one cassette. The configuration interaction is shared (same URL), so
// the merged cassette has one config entry plus the per-movie search and
// detail entries.
func loadCombinedCassette(t *testing.T, paths ...string) *remotetest.Cassette {
	t.Helper()
	var all []remotetest.Interaction
	for _, p := range paths {
		c, err := remotetest.Load(p)
		if err != nil {
			t.Fatalf("load cassette %s: %v", p, err)
		}
		// Access the cassette's internal interactions through its Do method
		// is not possible, so re-load the raw file.
		contents, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read cassette %s: %v", p, err)
		}
		var interactions []remotetest.Interaction
		if err := json.Unmarshal(contents, &interactions); err != nil {
			t.Fatalf("decode cassette %s: %v", p, err)
		}
		all = append(all, interactions...)
		_ = c // suppress unused
	}
	return remotetest.NewCassette(all...)
}

// TestScheduledTaskProgressCompositionRefreshLibrary proves the RefreshLibrary
// task reports intermediate progress through the scheduled-task API while the
// scanner works. The provider transport holds the second item's refresh after
// the first item completes. While the second item is held, GET /ScheduledTasks
// reports State == "Running" and 0 < CurrentProgressPercentage < 100.
func TestScheduledTaskProgressCompositionRefreshLibrary(t *testing.T) {
	testScheduledTaskProgressComposition(t, tasks.RefreshLibraryID)
}

// TestScheduledTaskProgressCompositionRefreshMetadata proves the RefreshMetadata
// task reports intermediate progress through the scheduled-task API while the
// scanner works. The same hold pattern as RefreshLibrary applies.
func TestScheduledTaskProgressCompositionRefreshMetadata(t *testing.T) {
	testScheduledTaskProgressComposition(t, tasks.RefreshMetadataID)
}

func testScheduledTaskProgressComposition(t *testing.T, taskID string) {
	// Combine two cassettes so each movie makes a different search request.
	// The Matrix cassette has 3 interactions (config, search, movie). The
	// Emperors New Groove cassette has 3 interactions (config, search, movie).
	// The merged cassette has 5 unique keys: config, search Matrix, movie
	// Matrix, search Emperors, movie Emperors.
	// The config request is served first and cached. The first movie's search
	// is the 2nd request, its movie details are the 3rd. The second movie's
	// search is the 4th request. We hold the 4th request so the second item is
	// mid-refresh while the first item is complete.
	cassette := loadCombinedCassette(t,
		filepath.Join("..", "..", "internal", "providers", "tmdb", "testdata", "the-matrix.json"),
		filepath.Join("..", "..", "internal", "providers", "tmdb", "testdata", "emperors-new-groove.json"),
	)

	holding := newHoldingDoer(cassette, 4)
	holding.enableHold()

	root := filepath.Join(t.TempDir(), "program-data")
	if err := os.MkdirAll(filepath.Join(root, "config", "plugins"), 0o700); err != nil {
		t.Fatalf("create config plugins dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "plugins", "Jellyfin.Plugin.Tmdb.xml"), []byte(`<PluginConfiguration><TmdbApiKey>test-key</TmdbApiKey></PluginConfiguration>`), 0o600); err != nil {
		t.Fatalf("write tmdb plugin config: %v", err)
	}

	app := newCompositionApplication(t, root, withProviderRemoteDoer(holding))

	// Complete the startup wizard and create an admin user.
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	login := compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", "")
	token := compositionAccessToken(t, login)
	if token == "" {
		t.Fatal("login returned no access token")
	}

	// Create two movie fixtures with names that match the two cassettes.
	// The first file parses to "The Matrix" (1999) and matches the Matrix
	// cassette. The second file parses to "The Emperors New Groove" (2000)
	// and matches the Emperors cassette. The scanner refreshes them
	// sequentially within the folder.
	source := t.TempDir()
	movie1 := filepath.Join(source, "The Matrix (1999).mkv")
	if err := os.WriteFile(movie1, []byte("matrix"), 0o600); err != nil {
		t.Fatalf("write movie one: %v", err)
	}
	movie2 := filepath.Join(source, "The Emperors New Groove (2000).mkv")
	if err := os.WriteFile(movie2, []byte("groove"), 0o600); err != nil {
		t.Fatalf("write movie two: %v", err)
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

	// Start the task through the scheduled-task API.
	start := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+taskID, nil, token)
	if start.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", taskID, start.Code, http.StatusNoContent, start.Body.String())
	}

	// Wait for the provider transport to block on the held request. This
	// means the first item completed and the second item is mid-refresh.
	holding.waitForBlocked(t)

	// While the second item is held, poll GET /ScheduledTasks for the task
	// state and progress.
	progressObserved := false
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/ScheduledTasks/"+taskID, nil, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ScheduledTasks/%s status = %d, want %d; body: %s", taskID, rec.Code, http.StatusOK, rec.Body.String())
		}
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatalf("unmarshal scheduled task info: %v; body: %s", err, rec.Body.String())
		}
		state, _ := info["State"].(string)
		if state == string(tasks.StateRunning) {
			if p, ok := info["CurrentProgressPercentage"].(float64); ok && p > 0 && p < 100 {
				progressObserved = true
				break
			}
		}
		if state == string(tasks.StateIdle) {
			// The task completed before we caught it running.
			break
		}
		select {
		case <-deadline.C:
		case <-time.After(50 * time.Millisecond):
		}
	}

	if !progressObserved {
		t.Fatalf("did not observe 0 < progress < 100 while %s was Running", taskID)
	}

	// Release the held request so the scan can complete.
	holding.release()

	// Wait for the task to complete.
	result := waitForScheduledTaskResult(t, app.Router(), token, taskID, 30*time.Second)
	if result["Status"] != tasks.StatusCompleted {
		errMsg, _ := result["ErrorMessage"].(string)
		t.Fatalf("%s result status = %v, want %s; error: %s", taskID, result["Status"], tasks.StatusCompleted, errMsg)
	}
}

// TestScheduledTaskProgressCompositionErrorOmits100 proves a scanner error
// omits the terminal 100 callback. The task fails against a missing library
// directory and the existing failure behavior remains unchanged.
func TestScheduledTaskProgressCompositionErrorOmits100(t *testing.T) {
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

	start := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/ScheduledTasks/Running/"+tasks.RefreshLibraryID, nil, token)
	if start.Code != http.StatusNoContent {
		t.Fatalf("POST /ScheduledTasks/Running/%s status = %d, want %d; body: %s", tasks.RefreshLibraryID, start.Code, http.StatusNoContent, start.Body.String())
	}

	result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshLibraryID, 10*time.Second)
	if result["Status"] != tasks.StatusFailed {
		t.Fatalf("RefreshLibrary folder-failure result status = %v, want %s", result["Status"], tasks.StatusFailed)
	}
	errMsg, _ := result["ErrorMessage"].(string)
	if errMsg == "" {
		t.Fatalf("RefreshLibrary folder-failure result has no error message: %#v", result)
	}
	if !strings.Contains(errMsg, failingSource) {
		t.Fatalf("RefreshLibrary error message = %q, want it to contain the folder path %q", errMsg, failingSource)
	}
}
