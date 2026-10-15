// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationReadRoutesDoNotWaitOnAHeldWriter models writer contention. A
// library scan holds the single SQLite writer connection for long write
// transactions. Read-only routes, including the per-request authorization
// device refresh and a parent-scoped item query, must complete from the
// reader pool while the writer is held. The device refresh that is due
// reaches the database after the writer is free, when the application closes.
func TestApplicationReadRoutesDoNotWaitOnAHeldWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")
	app := newCompositionApplication(t, root)
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	admin, err := app.UserService().GetByName(t.Context(), "captain")
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	roots, err := store.NewItemStore(app.stores).Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindUserRootFolder}})
	if err != nil || len(roots.Items) != 1 {
		t.Fatalf("query user root = %d items, %v; want 1", len(roots.Items), err)
	}
	rootID := roots.Items[0].ID.Dashed()

	// Hold the writer as a scan transaction does. The writer pool has one
	// connection and begins with BEGIN IMMEDIATE, so the open transaction
	// holds both the connection and the database write lock.
	held, err := app.stores.Writer().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin held writer transaction: %v", err)
	}

	paths := []string{
		"/System/Info",
		"/UserViews?userId=" + admin.ID.Dashed(),
		"/System/ActivityLog/Entries?startIndex=0&limit=7",
		"/web/ConfigurationPages?enableInMainMenu=true",
		"/Users/Me",
		"/Items?userId=" + admin.ID.Dashed() + "&ParentId=" + rootID,
	}
	for _, path := range paths {
		// A new client version makes the device refresh due on each request.
		rec := readWithHeldWriter(t, app.Router(), path, token, "2.0")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with the writer held = %d, want %d; body: %s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	if err := held.Rollback(); err != nil {
		t.Fatalf("release held writer transaction: %v", err)
	}
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("close application: %v", err)
	}
	app = newCompositionApplication(t, root)
	device, err := store.NewIdentity(app.stores).DeviceByToken(t.Context(), token)
	if err != nil {
		t.Fatalf("load refreshed device: %v", err)
	}
	if device.AppVersion != "2.0" {
		t.Fatalf("device AppVersion after close = %q, want the queued refresh %q", device.AppVersion, "2.0")
	}
}

// readWithHeldWriter bounds the request so that a route that waits on the
// writer fails with a status instead of hanging the test. The bound is a
// failure guard, not a sleep: a passing request returns at once.
func readWithHeldWriter(t *testing.T, handler http.Handler, path, token, version string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:8096"
	req.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="`+version+`", Token="`+token+`"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
