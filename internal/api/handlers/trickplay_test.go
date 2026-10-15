// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/trickplay"
)

// The TrickplayController exposes both the images-only HLS manifest and its
// JPEG tiles through DefaultAuthorization; neither response is anonymous.
func TestTrickplayRoutesPinMethodsPathsOperationsAndPolicy(t *testing.T) {
	rt := router.New()
	handlers.RegisterTrickplay(rt, &trickplayRouteReader{})

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/Trickplay/{width}/tiles.m3u8", OperationID: "GetTrickplayHlsPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/Trickplay/{width}/{index}.jpg", OperationID: "GetTrickplayTileImage", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy || got.Anonymous {
				t.Errorf("route %q = %#v, want non-anonymous %#v", want.OperationID, got, want)
			}
		})
	}
}

// A request's authenticated identity and raw token must retain their distinct
// roles: identity scopes authorization while the token is embedded unchanged
// in the emitted relative tile URI.
func TestTrickplayPlaylistForwardsAuthenticatedIdentityRawTokenAndMediaSourceOverride(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := trickplayRouteUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	userID := trickplayRouteUUID(t, "a1b2c3d4-e5f6-4789-9abc-def012345678")
	identity := auth.Identity{Authenticated: true, APIKey: true, Token: "opaque+key&unescaped=✓", UserID: userID, Administrator: true}
	reader := &trickplayRouteReader{playlist: "#EXTM3U\n0.jpg?MediaSourceId=" + mediaSourceID.String() + "&ApiKey=opaque+key&unescaped=✓\n", playlistFound: true}
	rt := trickplayRouteRouterWithIdentity(reader, identity)

	record := trickplayRouteRequest(rt, "/vIdEoS/"+itemID.Dashed()+"/tRiCkPlAy/320/tIlEs.M3u8?MediaSourceId="+mediaSourceID.Dashed())
	assertStatus(t, record, http.StatusOK)
	if got, want := record.Header().Get("Content-Type"), "application/x-mpegURL; charset=utf-8"; got != want {
		t.Errorf("playlist Content-Type = %q, want %q", got, want)
	}
	if got, want := record.Header().Get("Cache-Control"), "private, no-store"; got != want {
		t.Errorf("API-key playlist Cache-Control = %q, want %q", got, want)
	}
	if got, want := record.Body.String(), reader.playlist; got != want {
		t.Errorf("playlist body = %q, want %q", got, want)
	}
	if got, want := reader.playlistRequests, []trickplay.ReadRequest{{ItemID: itemID, MediaSourceID: &mediaSourceID, UserID: userID, Administrator: true, Width: 320, APIKey: "opaque+key&unescaped=✓"}}; !trickplayRouteRequestsEqual(got, want) {
		t.Fatalf("playlist requests = %#v, want %#v", got, want)
	}
}

// An omitted MediaSourceId is distinguishable from an explicit item ID, so
// the domain layer can apply source selection without losing request intent.
func TestTrickplayPlaylistPreservesOmittedMediaSourceID(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	reader := &trickplayRouteReader{playlist: "#EXTM3U\n", playlistFound: true}

	assertStatus(t, trickplayRouteRequest(trickplayRouteRouter(reader, "token"), "/Videos/"+itemID.Dashed()+"/Trickplay/320/tiles.m3u8"), http.StatusOK)
	if got, want := reader.playlistRequests, []trickplay.ReadRequest{{ItemID: itemID, Width: 320, APIKey: "token"}}; !trickplayRouteRequestsEqual(got, want) {
		t.Fatalf("playlist requests = %#v, want omitted MediaSourceID %#v", got, want)
	}
}

// Malformed path components are rejected before a reader can select an item
// or open an artifact. Well-formed signed int32 values are reader input.
func TestTrickplayRejectsMalformedIdentifiersAndIntegersBeforeReader(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := trickplayRouteUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	validPlaylist := "/Videos/" + itemID.Dashed() + "/Trickplay/320/tiles.m3u8?MediaSourceId=" + mediaSourceID.Dashed()
	validTile := "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg?MediaSourceId=" + mediaSourceID.Dashed()

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "playlist item ID", path: strings.Replace(validPlaylist, itemID.Dashed(), "not-a-guid", 1)},
		{name: "playlist width is not an integer", path: strings.Replace(validPlaylist, "/320/", "/wide/", 1)},
		{name: "playlist media source ID", path: strings.Replace(validPlaylist, mediaSourceID.Dashed(), "not-a-guid", 1)},
		{name: "tile item ID", path: strings.Replace(validTile, itemID.Dashed(), "not-a-guid", 1)},
		{name: "tile width is not an integer", path: strings.Replace(validTile, "/320/", "/wide/", 1)},
		{name: "tile index is not an integer", path: strings.Replace(validTile, "/0.jpg", "/first.jpg", 1)},
		{name: "tile media source ID", path: strings.Replace(validTile, mediaSourceID.Dashed(), "not-a-guid", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &trickplayRouteReader{}
			record := trickplayRouteRequest(trickplayRouteRouter(reader, "token"), test.path)
			assertStatus(t, record, http.StatusBadRequest)
			if len(reader.playlistRequests) != 0 || len(reader.tileRequests) != 0 {
				t.Fatalf("reader calls after invalid %s = playlists:%#v tiles:%#v, want none", test.name, reader.playlistRequests, reader.tileRequests)
			}
		})
	}
}

// Signed int32 path values, including zero and negatives, are valid transport
// values: lookup decides whether the requested rendition or tile exists. A
// syntactically valid zero UUID follows the same non-disclosing lookup path.
func TestTrickplayRoutesSignedInt32AndZeroUUIDValuesToLookup(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	zeroID := dto.NilUUID

	for _, test := range []struct {
		name          string
		path          string
		playlist      bool
		wantItemID    dto.UUID
		wantWidth     int
		wantTileIndex int
	}{
		{name: "playlist zero item ID", path: "/Videos/" + zeroID.Dashed() + "/Trickplay/320/tiles.m3u8", playlist: true, wantItemID: zeroID, wantWidth: 320},
		{name: "tile zero item ID", path: "/Videos/" + zeroID.Dashed() + "/Trickplay/320/0.jpg", wantItemID: zeroID, wantWidth: 320, wantTileIndex: 0},
		{name: "playlist zero width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/0/tiles.m3u8", playlist: true, wantItemID: itemID, wantWidth: 0},
		{name: "playlist negative width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/-320/tiles.m3u8", playlist: true, wantItemID: itemID, wantWidth: -320},
		{name: "tile zero width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/0/0.jpg", wantItemID: itemID, wantWidth: 0, wantTileIndex: 0},
		{name: "tile negative width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/-320/0.jpg", wantItemID: itemID, wantWidth: -320, wantTileIndex: 0},
		{name: "tile zero index", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg", wantItemID: itemID, wantWidth: 320, wantTileIndex: 0},
		{name: "tile negative index", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/-1.jpg", wantItemID: itemID, wantWidth: 320, wantTileIndex: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &trickplayRouteReader{playlistErr: trickplay.ErrNotFound, tileErr: trickplay.ErrNotFound}
			record := trickplayRouteRequest(trickplayRouteRouter(reader, "token"), test.path)
			assertStatus(t, record, http.StatusNotFound)
			if test.playlist {
				if got, want := reader.playlistRequests, []trickplay.ReadRequest{{ItemID: test.wantItemID, Width: test.wantWidth, APIKey: "token"}}; !trickplayRouteRequestsEqual(got, want) {
					t.Fatalf("playlist requests = %#v, want %#v", got, want)
				}
				return
			}
			if got, want := reader.tileRequests, []trickplayRouteTileRequest{{request: trickplay.ReadRequest{ItemID: test.wantItemID, Width: test.wantWidth, APIKey: "token"}, index: test.wantTileIndex}}; !trickplayRouteTileRequestsEqual(got, want) {
				t.Fatalf("tile requests = %#v, want %#v", got, want)
			}
		})
	}
}

// Width and tile index are persisted and eventually used in filesystem paths,
// so route parsing accepts the complete int32 domain but rejects overflow
// before invoking the reader.
func TestTrickplayRejectsTransportIntegersOutsideInt32BeforeReader(t *testing.T) {
	const maxInt32 = 1<<31 - 1
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	max := strconv.Itoa(maxInt32)
	overflow := strconv.FormatInt(int64(maxInt32)+1, 10)

	for _, test := range []struct {
		name          string
		path          string
		playlist      bool
		wantStatus    int
		wantWidth     int
		wantTileIndex int
	}{
		{name: "playlist maximum width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/" + max + "/tiles.m3u8", playlist: true, wantStatus: http.StatusOK, wantWidth: maxInt32},
		{name: "playlist width above int32", path: "/Videos/" + itemID.Dashed() + "/Trickplay/" + overflow + "/tiles.m3u8", playlist: true, wantStatus: http.StatusBadRequest},
		{name: "tile maximum width", path: "/Videos/" + itemID.Dashed() + "/Trickplay/" + max + "/0.jpg", wantStatus: http.StatusOK, wantWidth: maxInt32, wantTileIndex: 0},
		{name: "tile width above int32", path: "/Videos/" + itemID.Dashed() + "/Trickplay/" + overflow + "/0.jpg", wantStatus: http.StatusBadRequest},
		{name: "tile maximum index", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/" + max + ".jpg", wantStatus: http.StatusOK, wantWidth: 320, wantTileIndex: maxInt32},
		{name: "tile index above int32", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/" + overflow + ".jpg", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &trickplayRouteReader{
				playlist:      "#EXTM3U\n",
				playlistFound: true,
				tile:          trickplay.Artifact{Body: &trickplayRouteBody{Reader: strings.NewReader("JPEG-TILE")}, ContentType: "image/jpeg"},
			}

			record := trickplayRouteRequest(trickplayRouteRouter(reader, "token"), test.path)
			assertStatus(t, record, test.wantStatus)
			if test.wantStatus == http.StatusBadRequest {
				if len(reader.playlistRequests) != 0 || len(reader.tileRequests) != 0 {
					t.Fatalf("reader calls after %s = playlists:%#v tiles:%#v, want none", test.name, reader.playlistRequests, reader.tileRequests)
				}
				return
			}
			if test.playlist {
				if got, want := reader.playlistRequests, []trickplay.ReadRequest{{ItemID: itemID, Width: test.wantWidth, APIKey: "token"}}; !trickplayRouteRequestsEqual(got, want) {
					t.Fatalf("playlist requests = %#v, want %#v", got, want)
				}
				return
			}
			if got, want := reader.tileRequests, []trickplayRouteTileRequest{{request: trickplay.ReadRequest{ItemID: itemID, Width: test.wantWidth, APIKey: "token"}, index: test.wantTileIndex}}; !trickplayRouteTileRequestsEqual(got, want) {
				t.Fatalf("tile requests = %#v, want %#v", got, want)
			}
		})
	}
}

func TestTrickplayTileServesClosableJPEGAttachmentAndForwardsIdentityMediaSourceOverride(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := trickplayRouteUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	userID := trickplayRouteUUID(t, "a1b2c3d4-e5f6-4789-9abc-def012345678")
	identity := auth.Identity{Authenticated: true, APIKey: true, Token: "identity-token", UserID: userID, Administrator: true}
	body := &trickplayRouteBody{Reader: strings.NewReader("JPEG-TILE")}
	reader := &trickplayRouteReader{tile: trickplay.Artifact{Body: body, ContentType: "image/jpeg"}}
	rt := trickplayRouteRouterWithIdentity(reader, identity)

	record := trickplayRouteRequest(rt, "/vIdEoS/"+itemID.Dashed()+"/tRiCkPlAy/320/0.JpG?MediaSourceId="+mediaSourceID.Dashed())
	assertStatus(t, record, http.StatusOK)
	if got, want := record.Header().Get("Content-Type"), "image/jpeg"; got != want {
		t.Errorf("tile Content-Type = %q, want %q", got, want)
	}
	if got, want := record.Header().Get("Content-Disposition"), "attachment"; got != want {
		t.Errorf("tile Content-Disposition = %q, want %q", got, want)
	}
	if got, want := record.Body.String(), "JPEG-TILE"; got != want {
		t.Errorf("tile body = %q, want %q", got, want)
	}
	if body.closeCalls != 1 {
		t.Errorf("tile body Close calls = %d, want 1 after response copy", body.closeCalls)
	}
	if got, want := reader.tileRequests, []trickplayRouteTileRequest{{request: trickplay.ReadRequest{ItemID: itemID, MediaSourceID: &mediaSourceID, UserID: userID, Administrator: true, Width: 320, APIKey: "identity-token"}, index: 0}}; !trickplayRouteTileRequestsEqual(got, want) {
		t.Fatalf("tile requests = %#v, want %#v", got, want)
	}
}

// A trickplay tile is a completed physical file, not a growing stream. Its
// HTTP representation therefore preserves the opened descriptor's length and
// modification time, supports conditional requests, and closes the descriptor
// after both body and 304 paths.
func TestTrickplayTilePinsPhysicalFileLengthLastModifiedAndConditional304(t *testing.T) {
	itemID := trickplayRouteUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := trickplay.Metadata{ItemID: itemID, Width: 320, Height: 180, TileWidth: 2, TileHeight: 2, ThumbnailCount: 1}
	root := t.TempDir()
	contents := []byte("physical JPEG tile\n")
	path := trickplayRoutePhysicalTilePath(root, metadata, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create tile directory: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write tile: %v", err)
	}
	modified := time.Date(2024, time.February, 29, 12, 34, 56, 0, time.UTC)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set tile modification time: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat tile: %v", err)
	}

	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{
		Metadata:    trickplayRouteMetadataSource{metadata: metadata},
		Access:      trickplayRouteAllowAccess{},
		Publication: trickplay.NewPublicationCoordinator(),
		Root:        root,
	})
	reader := &trickplayRoutePhysicalReader{service: service}
	rt := trickplayRouteRouter(reader, "token")
	requestPath := "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg"

	record := trickplayRouteRequest(rt, requestPath)
	assertStatus(t, record, http.StatusOK)
	if got, want := record.Header().Get("Content-Length"), strconv.FormatInt(info.Size(), 10); got != want {
		t.Errorf("tile Content-Length = %q, want %q", got, want)
	}
	if got, want := record.Header().Get("Last-Modified"), info.ModTime().UTC().Format(http.TimeFormat); got != want {
		t.Errorf("tile Last-Modified = %q, want %q", got, want)
	}
	if got, want := record.Header().Get("Content-Disposition"), "attachment"; got != want {
		t.Errorf("tile Content-Disposition = %q, want %q", got, want)
	}
	if got, want := record.Body.Bytes(), contents; string(got) != string(want) {
		t.Errorf("tile body = %q, want %q", got, want)
	}
	trickplayRouteAssertBodiesClosed(t, reader.bodies, 1)

	conditionalRequest := httptest.NewRequest(http.MethodGet, requestPath, nil)
	conditionalRequest.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
	conditionalRecord := httptest.NewRecorder()
	rt.ServeHTTP(conditionalRecord, conditionalRequest)
	assertStatus(t, conditionalRecord, http.StatusNotModified)
	if got := conditionalRecord.Body.Len(); got != 0 {
		t.Errorf("conditional tile body length = %d, want 0", got)
	}
	trickplayRouteAssertBodiesClosed(t, reader.bodies, 2)
}

// A replacement can retain its predecessor's HTTP-date timestamp. The
// persisted generation identity is therefore the tile representation identity:
// a stale ETag must beat a matching If-Modified-Since, while an exact current
// ETag returns an empty 304 response.
func TestTrickplayTileGenerationETagInvalidatesSameSecondReplacement(t *testing.T) {
	itemID := trickplayRouteUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := trickplay.Metadata{ItemID: itemID, Width: 320, Height: 180, TileWidth: 2, TileHeight: 2, ThumbnailCount: 1, GenerationToken: "generation-first"}
	root := t.TempDir()
	path := trickplayRoutePhysicalTilePath(root, metadata, 0)
	first := []byte("first JPEG tile")
	second := []byte("next JPEG tile!")
	modified := time.Date(2024, time.February, 29, 12, 34, 56, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create tile directory: %v", err)
	}
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("write first tile: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set first tile modification time: %v", err)
	}

	source := &trickplayRouteMetadataSource{metadata: metadata}
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{
		Metadata:    source,
		Access:      trickplayRouteAllowAccess{},
		Publication: trickplay.NewPublicationCoordinator(),
		Root:        root,
	})
	reader := &trickplayRoutePhysicalReader{service: service}
	rt := trickplayRouteRouter(reader, "token")
	requestPath := "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg"

	firstRecord := trickplayRouteRequest(rt, requestPath)
	assertStatus(t, firstRecord, http.StatusOK)
	if got, want := firstRecord.Header().Get("ETag"), "\"generation-first\""; got != want {
		t.Fatalf("first tile ETag = %q, want %q", got, want)
	}
	if got, want := firstRecord.Header().Get("Cache-Control"), "private, no-cache"; got != want {
		t.Errorf("first tile Cache-Control = %q, want %q", got, want)
	}
	if got, want := firstRecord.Body.Bytes(), first; string(got) != string(want) {
		t.Errorf("first tile body = %q, want %q", got, want)
	}

	if err := os.WriteFile(path, second, 0o644); err != nil {
		t.Fatalf("replace tile: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("preserve replacement modification time: %v", err)
	}
	source.metadata.GenerationToken = "generation-second"
	staleRequest := httptest.NewRequest(http.MethodGet, requestPath, nil)
	staleRequest.Header.Set("If-Modified-Since", modified.Format(http.TimeFormat))
	staleRequest.Header.Set("If-None-Match", firstRecord.Header().Get("ETag"))
	staleRecord := httptest.NewRecorder()
	rt.ServeHTTP(staleRecord, staleRequest)
	assertStatus(t, staleRecord, http.StatusOK)
	if got, want := staleRecord.Header().Get("ETag"), "\"generation-second\""; got != want {
		t.Errorf("replacement tile ETag = %q, want %q", got, want)
	}
	if got, want := staleRecord.Header().Get("Cache-Control"), "private, no-cache"; got != want {
		t.Errorf("replacement tile Cache-Control = %q, want %q", got, want)
	}
	if got, want := staleRecord.Body.Bytes(), second; string(got) != string(want) {
		t.Errorf("replacement tile body = %q, want %q", got, want)
	}

	matchingRequest := httptest.NewRequest(http.MethodGet, requestPath, nil)
	matchingRequest.Header.Set("If-None-Match", staleRecord.Header().Get("ETag"))
	matchingRecord := httptest.NewRecorder()
	rt.ServeHTTP(matchingRecord, matchingRequest)
	assertStatus(t, matchingRecord, http.StatusNotModified)
	if got, want := matchingRecord.Header().Get("ETag"), "\"generation-second\""; got != want {
		t.Errorf("matching tile ETag = %q, want %q", got, want)
	}
	if got, want := matchingRecord.Header().Get("Cache-Control"), "private, no-cache"; got != want {
		t.Errorf("matching tile Cache-Control = %q, want %q", got, want)
	}
	if got := matchingRecord.Body.Len(); got != 0 {
		t.Errorf("matching tile body length = %d, want 0", got)
	}
	trickplayRouteAssertBodiesClosed(t, reader.bodies, 3)
}

// Missing metadata/artifacts are ordinary absent resources. Reader failures,
// including a canceled operation before a response starts, retain the generic
// handler error mapping rather than inventing a transport-specific status.
func TestTrickplayMapsReaderOutcomesToHTTPStatus(t *testing.T) {
	itemID := trickplayRouteUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	for _, test := range []struct {
		name          string
		path          string
		playlistFound bool
		playlistErr   error
		tileErr       error
		wantStatus    int
	}{
		{name: "playlist absent metadata", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/tiles.m3u8", playlistErr: trickplay.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "playlist has no thumbnails", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/tiles.m3u8", wantStatus: http.StatusNotFound},
		{name: "playlist reader failure", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/tiles.m3u8", playlistFound: true, playlistErr: errors.New("metadata unavailable"), wantStatus: http.StatusInternalServerError},
		{name: "playlist canceled", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/tiles.m3u8", playlistFound: true, playlistErr: context.Canceled, wantStatus: http.StatusInternalServerError},
		{name: "tile absent artifact", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg", tileErr: trickplay.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "tile reader failure", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg", tileErr: errors.New("artifact unavailable"), wantStatus: http.StatusInternalServerError},
		{name: "tile canceled", path: "/Videos/" + itemID.Dashed() + "/Trickplay/320/0.jpg", tileErr: context.Canceled, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &trickplayRouteReader{playlistFound: test.playlistFound, playlistErr: test.playlistErr, tileErr: test.tileErr}
			record := trickplayRouteRequest(trickplayRouteRouter(reader, "token"), test.path)
			assertStatus(t, record, test.wantStatus)
		})
	}
}

type trickplayRouteReader struct {
	playlist         string
	playlistFound    bool
	playlistErr      error
	tile             trickplay.Artifact
	tileErr          error
	playlistRequests []trickplay.ReadRequest
	tileRequests     []trickplayRouteTileRequest
}

func (reader *trickplayRouteReader) Playlist(_ context.Context, request trickplay.ReadRequest) (string, bool, error) {
	reader.playlistRequests = append(reader.playlistRequests, request)
	return reader.playlist, reader.playlistFound, reader.playlistErr
}

func (reader *trickplayRouteReader) Tile(_ context.Context, request trickplay.ReadRequest, index int) (trickplay.Artifact, error) {
	reader.tileRequests = append(reader.tileRequests, trickplayRouteTileRequest{request: request, index: index})
	return reader.tile, reader.tileErr
}

type trickplayRouteTileRequest struct {
	request trickplay.ReadRequest
	index   int
}

type trickplayRouteBody struct {
	*strings.Reader
	closeCalls int
}

func (body *trickplayRouteBody) Close() error {
	body.closeCalls++
	return nil
}

type trickplayRouteMetadataSource struct {
	metadata trickplay.Metadata
}

func (source trickplayRouteMetadataSource) Trickplay(context.Context, dto.UUID, int) (trickplay.Metadata, error) {
	return source.metadata, nil
}

type trickplayRouteAllowAccess struct{}

func (trickplayRouteAllowAccess) Visible(context.Context, dto.UUID, dto.UUID, bool) (bool, error) {
	return true, nil
}

type trickplayRoutePhysicalReader struct {
	service *trickplay.ReaderService
	bodies  []io.ReadSeekCloser
}

func (reader *trickplayRoutePhysicalReader) Playlist(ctx context.Context, request trickplay.ReadRequest) (string, bool, error) {
	return reader.service.Playlist(ctx, request)
}

func (reader *trickplayRoutePhysicalReader) Tile(ctx context.Context, request trickplay.ReadRequest, index int) (trickplay.Artifact, error) {
	artifact, err := reader.service.Tile(ctx, request, index)
	if artifact.Body != nil {
		reader.bodies = append(reader.bodies, artifact.Body)
	}
	return artifact, err
}

type trickplayRouteResolver struct {
	identity auth.Identity
}

func (resolver trickplayRouteResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return resolver.identity, nil
}

type trickplayRouteEvaluator struct{}

func (trickplayRouteEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}

type trickplayRouteUsers struct{}

func (trickplayRouteUsers) Get(context.Context, dto.UUID) (store.User, error) {
	return store.User{}, nil
}

type trickplayRouteService interface {
	Playlist(context.Context, trickplay.ReadRequest) (string, bool, error)
	Tile(context.Context, trickplay.ReadRequest, int) (trickplay.Artifact, error)
}

func trickplayRouteRouter(reader trickplayRouteService, token string) *router.Router {
	return trickplayRouteRouterWithIdentity(reader, auth.Identity{Authenticated: true, APIKey: true, Token: token})
}

func trickplayRouteRouterWithIdentity(reader trickplayRouteService, identity auth.Identity) *router.Router {
	authorizer := middleware.NewAuthorizer(
		trickplayRouteResolver{identity: identity},
		trickplayRouteEvaluator{},
		trickplayRouteUsers{},
	)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterTrickplay(rt, reader)
	return rt
}

func trickplayRouteRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
	return record
}

func trickplayRoutePhysicalTilePath(root string, metadata trickplay.Metadata, index int) string {
	id := metadata.ItemID.Dashed()
	resolution := strconv.Itoa(metadata.Width) + " - " + strconv.Itoa(metadata.TileWidth) + "x" + strconv.Itoa(metadata.TileHeight)
	return filepath.Join(root, id[:2], id, resolution, strconv.Itoa(index)+".jpg")
}

func trickplayRouteAssertBodiesClosed(t *testing.T, bodies []io.ReadSeekCloser, wantCount int) {
	t.Helper()
	if len(bodies) != wantCount {
		t.Fatalf("opened artifact bodies = %d, want %d", len(bodies), wantCount)
	}
	for index, body := range bodies {
		if _, err := body.Read(make([]byte, 1)); err == nil {
			t.Fatalf("artifact body %d remains readable after handler response", index)
		}
	}
}

func trickplayRouteUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", text, err)
	}
	return id
}

func trickplayRouteRequestsEqual(got, want []trickplay.ReadRequest) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].ItemID != want[index].ItemID || got[index].UserID != want[index].UserID || got[index].Administrator != want[index].Administrator || got[index].Width != want[index].Width || got[index].APIKey != want[index].APIKey {
			return false
		}
		if got[index].MediaSourceID == nil || want[index].MediaSourceID == nil {
			if got[index].MediaSourceID != want[index].MediaSourceID {
				return false
			}
			continue
		}
		if *got[index].MediaSourceID != *want[index].MediaSourceID {
			return false
		}
	}
	return true
}

func trickplayRouteTileRequestsEqual(got, want []trickplayRouteTileRequest) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].index != want[index].index || !trickplayRouteRequestsEqual([]trickplay.ReadRequest{got[index].request}, []trickplay.ReadRequest{want[index].request}) {
			return false
		}
	}
	return true
}

var _ io.ReadSeekCloser = (*trickplayRouteBody)(nil)
