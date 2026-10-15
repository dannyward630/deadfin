// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"

	"golang.org/x/net/websocket"
)

// TestApplicationAuthenticatedWebSocketUpgrade makes sure that an authenticated
// /socket upgrade receives status 101, not 503. The routeServices literal must
// include the webSockets field; a nil server returns 503.
func TestApplicationAuthenticatedWebSocketUpgrade(t *testing.T) {
	app := newCompositionApplication(t, t.TempDir())
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())

	authorization := `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="1.0", Token="` + token + `"`
	server := httptest.NewServer(app.Router())
	defer server.Close()

	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/socket"
	config, err := websocket.NewConfig(socketURL, server.URL)
	if err != nil {
		t.Fatalf("new websocket config: %v", err)
	}
	config.Header.Set("Authorization", authorization)
	socket, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	}()

	// A successful dial means the server returned 101 Switching Protocols.
	// A nil webSockets server returns 503 and the dial fails.
	if socket == nil {
		t.Fatal("websocket connection is nil after successful dial")
	}
}

// TestApplicationCompositionExpandsVirtualPath makes sure the composition root
// wires the VirtualPathExpander from config.Paths into the item DTO builder.
// The test stores an item with a raw %MetadataPath% token and observes the
// expanded configured path in the API response. A missing or zero-value
// expander would return the raw token.
func TestApplicationCompositionExpandsVirtualPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")
	app := newCompositionApplication(t, root)
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())

	items := store.NewItemStore(app.stores)
	itemID := dto.UUID{0x2a}
	name := "virtual path probe"
	storedPath := "%MetadataPath%/movies/probe.mkv"
	if err := items.Upsert(t.Context(), item.Item{
		ID: itemID, Kind: item.KindMovie, Name: &name, Path: &storedPath,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet,
		"/Items/"+itemID.Dashed(), nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Items/%s status = %d, want 200; body=%s", itemID.Dashed(), rec.Code, rec.Body.String())
	}

	var dto struct {
		Path string `json:"Path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}

	wantPath := filepath.Join(root, "metadata", "movies", "probe.mkv")
	if dto.Path != wantPath {
		t.Fatalf("response Path = %q, want expanded %q (composition must wire the expander)", dto.Path, wantPath)
	}
	if strings.Contains(dto.Path, "%MetadataPath%") {
		t.Fatalf("response Path contains raw token: %q", dto.Path)
	}
}
