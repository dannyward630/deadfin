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
)

// TestScannerDiscMediaSourceWireShape protects the source graph of a .disc
// placeholder across the scanner, store, and public /Items projection. A disc
// has no probe streams, so its container belongs only to the nested source,
// which still exposes an explicitly empty MediaStreams array.
func TestScannerDiscMediaSourceWireShape(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	resolver := &scannerDiscMediaSourceResolver{}
	harness.chain = library.NewChain(resolver)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  scannerDiscMediaSourceProbe{},
	})

	discPath := scannerPath(t, harness.source, "Synthetic Disc.bluray.disc")
	writeScannerFile(t, discPath, "disc fixture")
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan disc fixture: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan disc fixture errors = %v, want none", result.Errors)
	}

	disc := scannerItemAtPath(t, harness.items, discPath)
	record, err := harness.items.MediaSourceRecord(t.Context(), disc.ID)
	if err != nil {
		t.Fatalf("load persisted disc media source: %v", err)
	}
	if record.ID != disc.ID || record.Path == nil || *record.Path != discPath {
		t.Fatalf("persisted disc identity = %#v, want ID %s and path %q", record, disc.ID, discPath)
	}
	if record.Container == nil || *record.Container != "disc" {
		t.Fatalf("persisted disc container = %v, want disc", record.Container)
	}
	if record.MediaStreams == nil || len(record.MediaStreams) != 0 {
		t.Fatalf("persisted disc streams = %#v, want present empty slice", record.MediaStreams)
	}

	routes := router.New()
	builder := dtobuild.NewBuilder(harness.items, dtobuild.NewStoreEnricher(harness.items, nil, nil))
	handlers.RegisterItems(routes, harness.items, builder, clock.NewReal())
	for _, profile := range []struct {
		name, accept, itemsKey string
		key                    func(string) string
	}{
		{name: "Pascal", accept: "application/json", itemsKey: "Items", key: func(value string) string { return value }},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", key: scannerDiscCamelKey},
	} {
		t.Run(profile.name, func(t *testing.T) {
			item, source := scannerDiscMediaSourceRow(t, routes, profile.accept, profile.itemsKey, profile.key, disc.ID)
			if raw, found := item[profile.key("Container")]; found {
				t.Errorf("disc item unexpectedly exposes %s = %s", profile.key("Container"), raw)
			}
			scannerDiscAssertRaw(t, source, profile.key("Id"), scannerDiscJSON(t, disc.ID.String()))
			scannerDiscAssertRaw(t, source, profile.key("Path"), scannerDiscJSON(t, discPath))
			scannerDiscAssertRaw(t, source, profile.key("Container"), `"disc"`)
			scannerDiscAssertRaw(t, source, profile.key("MediaStreams"), "[]")
			for _, field := range []string{"Bitrate", "DefaultAudioStreamIndex", "DefaultSubtitleStreamIndex", "RunTimeTicks"} {
				if raw, found := source[profile.key(field)]; found {
					t.Errorf("disc source invented probe field %s = %s", profile.key(field), raw)
				}
			}
		})
	}
}

func scannerDiscMediaSourceRow(t *testing.T, routes *router.Router, accept, itemsKey string, key func(string) string, id dto.UUID) (map[string]json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items?Ids="+id.String()+"&Fields=MediaSources", nil)
	request.Header.Set("Accept", accept)
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items?Fields=MediaSources status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /Items response: %v", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body[itemsKey], &items); err != nil || len(items) != 1 {
		t.Fatalf("decode %s items: %v; body=%s", itemsKey, err, record.Body.String())
	}
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(items[0][key("MediaSources")], &sources); err != nil || len(sources) != 1 {
		t.Fatalf("decode %s media source: %v; item=%s", key("MediaSources"), err, items[0][key("MediaSources")])
	}
	return items[0], sources[0]
}

func scannerDiscAssertRaw(t *testing.T, row map[string]json.RawMessage, key, want string) {
	t.Helper()
	if got, found := row[key]; !found || string(got) != want {
		t.Errorf("%s = %s (present=%t), want %s", key, got, found, want)
	}
}

func scannerDiscJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode expected JSON value: %v", err)
	}
	return string(encoded)
}

func scannerDiscCamelKey(value string) string {
	if value == "" {
		return value
	}
	return string(value[0]+32) + value[1:]
}

type scannerDiscMediaSourceResolver struct{ scannerFixtureResolver }

func (resolver *scannerDiscMediaSourceResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	value, err := resolver.scannerFixtureResolver.Resolve(ctx, input)
	if err != nil || input.Entry.IsDir || filepath.Base(input.Entry.Path) != "Synthetic Disc.bluray.disc" {
		return value, err
	}
	value.Name = scannerDiscString("Synthetic Disc")
	videoType := item.VideoTypeBluRay
	value.VideoType = &videoType
	value.IsPlaceHolder = true
	return value, nil
}

type scannerDiscMediaSourceProbe struct{}

func (scannerDiscMediaSourceProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	if filepath.Base(*input.Item.Path) != "Synthetic Disc.bluray.disc" {
		return library.MediaProbeResult{}, nil
	}
	videoType := item.VideoTypeBluRay
	return library.MediaProbeResult{Container: "disc", VideoType: &videoType}, nil
}

func scannerDiscString(value string) *string { return &value }
