// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestLibraryRefreshTaskResult proves POST /Library/Refresh routes through
// the scheduler and records a LastExecutionResult on the RefreshLibrary
// task. The failing scan yields Status == "Failed" with a non-empty
// ErrorMessage after the run drains. The success case yields "Completed".
// The endpoint returns 204 with no body in both cases. After scheduler
// shutdown the endpoint returns 503 because the adapter translates
// tasks.ErrSchedulerClosed to library.ErrScannerClosed.
func TestLibraryRefreshTaskResult(t *testing.T) {
	t.Run("failing scan records Failed", func(t *testing.T) {
		app, token := newLibraryRefreshTestApp(t)

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

		rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Library/Refresh", nil, token)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST /Library/Refresh status = %d, want %d; body: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("POST /Library/Refresh body = %q, want no body", rec.Body.String())
		}

		result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshLibraryID, 30*time.Second)
		if result["Status"] != tasks.StatusFailed {
			t.Fatalf("RefreshLibrary result status = %v, want %s; result: %#v", result["Status"], tasks.StatusFailed, result)
		}
		errMsg, _ := result["ErrorMessage"].(string)
		if errMsg == "" {
			t.Fatalf("RefreshLibrary failed result has no error message: %#v", result)
		}
	})

	t.Run("successful scan records Completed", func(t *testing.T) {
		app, token := newLibraryRefreshTestApp(t)

		source := filepath.Join(t.TempDir(), "movies")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatalf("create movie library source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(source, "Movie (2024).mkv"), []byte("movie"), 0o600); err != nil {
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

		rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Library/Refresh", nil, token)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST /Library/Refresh status = %d, want %d; body: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("POST /Library/Refresh body = %q, want no body", rec.Body.String())
		}

		result := waitForScheduledTaskResult(t, app.Router(), token, tasks.RefreshLibraryID, 30*time.Second)
		if result["Status"] != tasks.StatusCompleted {
			errMsg, _ := result["ErrorMessage"].(string)
			t.Fatalf("RefreshLibrary result status = %v, want %s; error: %s", result["Status"], tasks.StatusCompleted, errMsg)
		}
	})

	t.Run("scheduler shutdown returns 503", func(t *testing.T) {
		app, token := newLibraryRefreshTestApp(t)

		source := filepath.Join(t.TempDir(), "movies")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatalf("create movie library source: %v", err)
		}
		if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
			Enabled:   true,
			PathInfos: []library.MediaPathInfo{{Path: source}},
		}); err != nil {
			t.Fatalf("add movie library: %v", err)
		}

		if app.scheduler == nil {
			t.Fatal("composition application has no scheduler")
		}
		if err := app.scheduler.Close(context.Background()); err != nil {
			t.Fatalf("close scheduler: %v", err)
		}

		rec := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Library/Refresh", nil, token)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST /Library/Refresh after shutdown status = %d, want %d; body: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
		}
	})
}

// newLibraryRefreshTestApp creates a composition application, completes the
// startup wizard, creates an admin user, and returns the app and the
// access token for authenticated requests.
func newLibraryRefreshTestApp(t *testing.T) (*application, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "program-data")
	if err := os.MkdirAll(filepath.Join(root, "config", "plugins"), 0o700); err != nil {
		t.Fatalf("create config plugins dir: %v", err)
	}
	app := newCompositionApplication(t, root)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Configuration", []byte(`{"ServerName":"Test","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/User", []byte(`{"Name":"admin","Password":"correct horse battery staple"}`), http.StatusNoContent)
	assertHTTPStatus(t, app.Router(), http.MethodPost, "/Startup/Complete", nil, http.StatusNoContent)
	login := compositionRequestFrom(app.Router(), http.MethodPost, "/Users/AuthenticateByName", []byte(`{"Username":"admin","Pw":"correct horse battery staple"}`), "127.0.0.1:8096", "")
	token := compositionAccessToken(t, login)
	if token == "" {
		t.Fatal("login returned no access token")
	}
	return app, token
}
