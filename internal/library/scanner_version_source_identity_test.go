// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestScannerVersionMediaSourcesPreserveBackingItemIdentity protects the stock
// server-local rule for alternate versions: each MediaSource Id is the Id of
// the item backing that source, and its Path is that item's file. Item IDs are
// opaque and intentionally not compared with another server's generated IDs.
func TestScannerVersionMediaSourcesPreserveBackingItemIdentity(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  versionSourceIdentityProbe{},
	})

	primaryPath := scannerPath(t, harness.source, "Synthetic Feature (2024)", "Synthetic Feature (2024) - 1080p.mkv")
	alternatePath := scannerPath(t, harness.source, "Synthetic Feature (2024)", "Synthetic Feature (2024) - 720p.mkv")
	for _, path := range []string{primaryPath, alternatePath} {
		writeScannerFile(t, path, "fixture media")
	}
	if result, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan alternate-version fixture: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan alternate-version fixture errors = %v, want none", result.Errors)
	}

	primary := scannerItemAtPath(t, harness.items, primaryPath)
	alternate := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	if primary.ID == alternate.ID {
		t.Fatalf("scanned version item IDs both equal %s, want distinct primary and alternate identities", primary.ID)
	}
	if alternate.PrimaryVersionID == nil || *alternate.PrimaryVersionID != primary.ID {
		t.Fatalf("alternate PrimaryVersionID = %v, want primary ID %s", alternate.PrimaryVersionID, primary.ID)
	}
	if len(primary.LinkedChildren) != 1 || primary.LinkedChildren[0].ItemID != alternate.ID || primary.LinkedChildren[0].Type != item.LinkedChildTypeLocalAlternateVersion {
		t.Fatalf("primary local alternate relation = %#v, want one LocalAlternateVersion child %s", primary.LinkedChildren, alternate.ID)
	}
	expected := []versionSourceIdentity{{id: primary.ID, path: primaryPath}, {id: alternate.ID, path: alternatePath}}

	for _, requested := range []dto.UUID{primary.ID, alternate.ID} {
		records, err := harness.items.MediaSourceRecords(t.Context(), requested)
		if err != nil {
			t.Fatalf("load source records for %s: %v", requested, err)
		}
		versionSourceAssertRecords(t, records, expected)
	}

	itemsRoutes := router.New()
	builder := dtobuild.NewBuilder(harness.items, dtobuild.NewStoreEnricher(harness.items, nil, nil))
	handlers.RegisterItems(itemsRoutes, harness.items, builder, clock.NewReal())
	itemID, sources := versionSourceItemMediaSources(t, itemsRoutes, primary.ID)
	if itemID != primary.ID.String() {
		t.Fatalf("/Items primary item Id = %q, want %q", itemID, primary.ID.String())
	}
	versionSourceAssertWire(t, sources, expected)

	playbackRoutes := router.New()
	playbackService := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:          harness.items,
		NewPlaySessionID: func() string { return "version-source-identity" },
	})
	handlers.RegisterPlaybackInfo(playbackRoutes, playbackService, nil)
	for _, requested := range expected {
		t.Run("PlaybackInfo/"+requested.id.String(), func(t *testing.T) {
			sources := versionSourcePlaybackMediaSources(t, playbackRoutes, requested.id)
			versionSourceAssertWire(t, sources, expected)
			if gotID, gotPath := versionSourceWireID(t, sources[0]), versionSourceWirePath(t, sources[0]); gotID != requested.id.String() || gotPath != requested.path {
				t.Fatalf("PlaybackInfo requested source = (%q, %q), want (%q, %q)", gotID, gotPath, requested.id.String(), requested.path)
			}
		})
	}
}

type versionSourceIdentity struct {
	id   dto.UUID
	path string
}

func versionSourceAssertRecords(t *testing.T, records []playback.MediaSourceRecord, expected []versionSourceIdentity) {
	t.Helper()
	if len(records) != len(expected) {
		t.Fatalf("stored MediaSourceRecords count = %d, want %d", len(records), len(expected))
	}
	byID := make(map[dto.UUID]playback.MediaSourceRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	if len(byID) != len(expected) {
		t.Fatalf("stored MediaSourceRecord IDs = %d distinct values, want %d", len(byID), len(expected))
	}
	for _, want := range expected {
		record, found := byID[want.id]
		if !found {
			t.Errorf("stored MediaSourceRecord IDs missing %s", want.id)
			continue
		}
		if record.Path == nil || *record.Path != want.path {
			t.Errorf("stored MediaSourceRecord %s Path = %v, want %q", want.id, record.Path, want.path)
		}
		if record.Type != playback.MediaSourceDefault {
			t.Errorf("stored MediaSourceRecord %s Type = %v, want Default for scanner-local alternate", want.id, record.Type)
		}
	}
}

func versionSourceItemMediaSources(t *testing.T, routes *router.Router, id dto.UUID) (string, []map[string]json.RawMessage) {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items?Ids="+id.String()+"&Fields=MediaSources", nil)
	request.Header.Set("Accept", "application/json")
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items with MediaSources status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"Items"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /Items response: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("/Items response item count = %d, want one; body=%s", len(body.Items), record.Body.String())
	}
	var itemID string
	if err := json.Unmarshal(body.Items[0]["Id"], &itemID); err != nil {
		t.Fatalf("decode /Items item Id: %v", err)
	}
	return itemID, versionSourceWireSources(t, body.Items[0], "MediaSources")
}

func versionSourcePlaybackMediaSources(t *testing.T, routes *router.Router, id dto.UUID) []map[string]json.RawMessage {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items/"+id.Dashed()+"/PlaybackInfo", nil)
	request.Header.Set("Accept", "application/json")
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET PlaybackInfo status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v", err)
	}
	return versionSourceWireSources(t, body, "MediaSources")
}

func versionSourceWireSources(t *testing.T, body map[string]json.RawMessage, key string) []map[string]json.RawMessage {
	t.Helper()
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(body[key], &sources); err != nil {
		t.Fatalf("decode %s: %v; body=%s", key, err, body[key])
	}
	return sources
}

func versionSourceAssertWire(t *testing.T, sources []map[string]json.RawMessage, expected []versionSourceIdentity) {
	t.Helper()
	if len(sources) != len(expected) {
		t.Fatalf("MediaSources count = %d, want %d", len(sources), len(expected))
	}
	byID := make(map[string]map[string]json.RawMessage, len(sources))
	for _, source := range sources {
		byID[versionSourceWireID(t, source)] = source
	}
	if len(byID) != len(expected) {
		t.Fatalf("MediaSources has %d distinct IDs, want %d", len(byID), len(expected))
	}
	for _, want := range expected {
		source, found := byID[want.id.String()]
		if !found {
			t.Errorf("MediaSources missing source Id %s", want.id)
			continue
		}
		if got := versionSourceWirePath(t, source); got != want.path {
			t.Errorf("MediaSource %s Path = %q, want %q", want.id, got, want.path)
		}
		var sourceType string
		if err := json.Unmarshal(source["Type"], &sourceType); err != nil {
			t.Errorf("decode MediaSource %s Type: %v", want.id, err)
		} else if sourceType != "Default" {
			t.Errorf("MediaSource %s Type = %q, want Default for scanner-local alternate", want.id, sourceType)
		}
	}
}

func versionSourceWireID(t *testing.T, source map[string]json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(source["Id"], &value); err != nil {
		t.Fatalf("decode MediaSource Id: %v", err)
	}
	return value
}

func versionSourceWirePath(t *testing.T, source map[string]json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(source["Path"], &value); err != nil {
		t.Fatalf("decode MediaSource Path: %v", err)
	}
	return value
}

type versionSourceIdentityProbe struct{}

func (versionSourceIdentityProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	result := library.MediaProbeResult{Container: "mkv"}
	height, ok := map[string]int{
		"Synthetic Feature (2024) - 1080p.mkv": 1080,
		"Synthetic Feature (2024) - 720p.mkv":  720,
	}[filepath.Base(*input.Item.Path)]
	if !ok {
		return result, nil
	}
	width := 320
	result.Streams = []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", Width: &width, Height: &height}}
	return result, nil
}
