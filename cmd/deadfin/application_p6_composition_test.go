// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestP6RouteLedgerRegistersExactlyOneHLSAndTrickplaySurface pins the process
// ledger to the same registration entry point as the running application. The
// route export must neither omit these surfaces nor create ambiguous matches.
func TestP6RouteLedgerRegistersExactlyOneHLSAndTrickplaySurface(t *testing.T) {
	routes := routeLedger().Routes()

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/live.m3u8", OperationID: "SHADOW:GetLiveHlsStream", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/master.m3u8", OperationID: "SHADOW:GetMasterHlsVideoPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodHead, Pattern: "/Videos/{itemId}/master.m3u8", OperationID: "SHADOW:HeadMasterHlsVideoPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/master.m3u8", OperationID: "SHADOW:GetMasterHlsAudioPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodHead, Pattern: "/Audio/{itemId}/master.m3u8", OperationID: "SHADOW:HeadMasterHlsAudioPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/main.m3u8", OperationID: "SHADOW:GetVariantHlsVideoPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/main.m3u8", OperationID: "SHADOW:GetVariantHlsAudioPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/hls1/{playlistId}/{segmentId}.{container}", OperationID: "SHADOW:GetHlsVideoSegment", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/hls1/{playlistId}/{segmentId}.{container}", OperationID: "SHADOW:GetHlsAudioSegment", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/hls/{segmentId}/stream.mp3", OperationID: "SHADOW:GetHlsAudioSegmentLegacyMp3", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/hls/{segmentId}/stream.aac", OperationID: "SHADOW:GetHlsAudioSegmentLegacyAac", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/hls/{playlistId}/stream.m3u8", OperationID: "SHADOW:GetHlsPlaylistLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/hls/{playlistId}/{segmentId}.{segmentContainer}", OperationID: "SHADOW:GetHlsVideoSegmentLegacy", Anonymous: true},
		{Method: http.MethodDelete, Pattern: "/Videos/ActiveEncodings", OperationID: "SHADOW:StopEncodingProcess", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/subtitles.m3u8", OperationID: "GetSubtitlePlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/Stream.{routeFormat}", OperationID: "GetSubtitle", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/{routeStartPositionTicks}/Stream.{routeFormat}", OperationID: "GetSubtitleWithTicks", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/Trickplay/{width}/tiles.m3u8", OperationID: "GetTrickplayHlsPlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/Trickplay/{width}/{index}.jpg", OperationID: "GetTrickplayTileImage", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			matches := p6RoutesByOperation(routes, want.OperationID)
			if len(matches) != 1 {
				t.Fatalf("route operation %q occurrences = %d, want 1", want.OperationID, len(matches))
			}
			if got := matches[0]; got != want {
				t.Fatalf("route operation %q = %#v, want %#v", want.OperationID, got, want)
			}
		})
	}

	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Pattern
		if _, exists := seen[key]; exists {
			t.Fatalf("route ledger has duplicate method/pattern %q", key)
		}
		seen[key] = struct{}{}
	}
}

// TestApplicationDispatchesEveryQueuedTrickplayRegeneration proves the scanner
// completion bridge does not abandon later regeneration requests when the task
// queue rejects an earlier one.
func TestApplicationDispatchesEveryQueuedTrickplayRegeneration(t *testing.T) {
	first := dto.UUID{0x01}
	second := dto.UUID{0x02}
	var dispatched []dto.UUID

	dispatchRefreshTrickplay(library.RefreshResult{
		RegenerateTrickplayItemIDs: []dto.UUID{first, second},
	}, func(itemID dto.UUID) error {
		dispatched = append(dispatched, itemID)
		if itemID == first {
			return errors.New("first trigger rejected")
		}
		return nil
	})

	if want := []dto.UUID{first, second}; !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("trickplay runner dispatches = %#v, want %#v despite first trigger error", dispatched, want)
	}
}

// TestP6ApplicationComposesHLSWithTheExistingManagerWithoutGlobalImages
// verifies the ordinary construction seam remains safe for repeated tests
// while exposing all streamers through the one application graph. A tile
// composer is process-global-runtime-owned, so its absence intentionally leaves
// the scheduled runner inactive.
func TestP6ApplicationComposesHLSWithTheExistingManagerWithoutGlobalImages(t *testing.T) {
	app, err := newApplication(t.Context(), filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	if app.transcodes == nil {
		t.Fatal("application transcode manager = nil")
	}
	if app.progressive == nil {
		t.Fatal("application progressive streamer = nil")
	}
	if app.hls == nil {
		t.Fatal("application HLS streamer = nil")
	}
	if app.trickplayRunner != nil {
		t.Fatal("application started trickplay runner without a process-owned tile composer")
	}
}

// TestP6ApplicationDispatchesMasterAdvertisedSubtitlePlaylistURI verifies the
// production application graph wires a concrete subtitle service, rather than
// merely publishing its route metadata. The requested path has precisely the
// source/index shape advertised by an HLS master rendition.
func TestP6ApplicationDispatchesMasterAdvertisedSubtitlePlaylistURI(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())

	itemID := composedItemEnrichmentUUID(t, 0xd006)
	seedFullComposedMedia(t, store.NewItemStore(app.stores), itemID)
	token := loginComposedAdministrator(t, app.Router())

	path := "/Videos/" + itemID.Dashed() + "/" + itemID.String() + "/Subtitles/2/subtitles.m3u8?SegmentLength=30"
	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET master-advertised subtitle playlist status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "application/x-mpegURL"; got != want {
		t.Errorf("subtitle playlist Content-Type = %q, want %q", got, want)
	}
	wantBody := "#EXTM3U\n#EXT-X-TARGETDURATION:30\n#EXT-X-VERSION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:9,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=90000000&ApiKey=" + token + "\n#EXT-X-ENDLIST\n"
	if got := rec.Body.String(); got != wantBody {
		t.Errorf("subtitle playlist body = %q, want %q", got, wantBody)
	}
}

// TestP6ApplicationStartsTrickplayRunnerOnlyWhenComposerIsInjected verifies
// construction does not open the process-global image Runtime. A zero-value
// composer is sufficient here because the scheduled task is not triggered; the
// test is about application ownership and startup, not image processing.
func TestP6ApplicationStartsTrickplayRunnerOnlyWhenComposerIsInjected(t *testing.T) {
	app, err := newApplicationWithListen(
		t.Context(),
		filepath.Join(t.TempDir(), "program-data"),
		"127.0.0.1:8096",
		withTileComposer(&images.TileComposer{}),
	)
	if err != nil {
		t.Fatalf("new application with injected composer: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close application: %v", err)
		}
	})
	if app.trickplayRunner == nil {
		t.Fatal("application did not start trickplay runner with an injected tile composer")
	}
}

// TestP6HLSTranscodeManagerMapsEveryProcessFact verifies that HLS uses the
// application-owned manager through a lossless adapter. In particular source
// identity and seek ticks affect manager lifecycle identity, so they must never
// disappear at the composition boundary.
func TestP6HLSTranscodeManagerMapsEveryProcessFact(t *testing.T) {
	segment := &p6TrackingReadCloser{Reader: &p6StringReader{value: "segment"}}
	legacy := &p6TrackingReadCloser{Reader: &p6StringReader{value: "legacy"}}
	manager := &p6HLSManager{segment: segment, playlist: "#EXTM3U\n", ping: true, legacy: legacy}
	adapter := hlsTranscodeManager{manager: manager}
	sourcePath := filepath.Join(t.TempDir(), "hls-source.mp4")
	if err := os.WriteFile(sourcePath, []byte("pinned HLS source"), 0o600); err != nil {
		t.Fatalf("write HLS source: %v", err)
	}
	openCalls := 0
	openSource := func() (*sourcefile.Source, error) {
		openCalls++
		return sourcefile.Open(sourcePath, sourcefile.Expected{})
	}
	request := playback.HLSJobRequest{
		PlaylistPath:           "/transcodes/69df13ba0aa6d33f61b393b144578ea9.m3u8",
		Live:                   true,
		MinSegments:            3,
		SegmentPattern:         "/transcodes/69df13ba0aa6d33f61b393b144578ea9%d.ts",
		Args:                   []string{"-i", "file:/media/feature-with-embedded-subtitles.mkv", "-vf", "scale=1280:720,subtitles=f='/media/feature-with-embedded-subtitles.mkv':si=5", "-profile:v:0", "main", "-level:v:0", "4.0", "-maxrate", "800000", "-hls_segment_filename", "/transcodes/69df13ba0aa6d33f61b393b144578ea9%d.ts", "/transcodes/69df13ba0aa6d33f61b393b144578ea9.m3u8"},
		PlaySessionID:          "session",
		DeviceID:               "device",
		MediaSourceID:          "source-id",
		SourceRevision:         "exact-source-revision",
		EmbeddedSubtitleSource: "subtitles=f='/media/it'\\''s\\: feature.mkv'",
		OpenSource:             openSource,
		SegmentLengthSeconds:   6,
		RunTimeTicks:           987654321,
		StartTimeTicks:         123456789,
	}
	want := ffmpeg.HLSRequest{
		PlaylistPath:           request.PlaylistPath,
		Live:                   request.Live,
		MinSegments:            request.MinSegments,
		SegmentPattern:         request.SegmentPattern,
		Args:                   request.Args,
		PlaySessionID:          request.PlaySessionID,
		DeviceID:               request.DeviceID,
		MediaSourceID:          request.MediaSourceID,
		SegmentLengthSeconds:   request.SegmentLengthSeconds,
		RunTimeTicks:           request.RunTimeTicks,
		StartTimeTicks:         request.StartTimeTicks,
		SourceRevision:         request.SourceRevision,
		EmbeddedSubtitleSource: request.EmbeddedSubtitleSource,
	}

	if err := adapter.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("start HLS: %v", err)
	}
	started := manager.started
	started.OpenSource = nil
	if !reflect.DeepEqual(started, want) {
		t.Fatalf("StartHLS request = %#v, want %#v", started, want)
	}
	if err := adapter.RestartHLSAt(t.Context(), request, 17); err != nil {
		t.Fatalf("restart HLS: %v", err)
	}
	restarted := manager.restarted
	restarted.OpenSource = nil
	if !reflect.DeepEqual(restarted, want) || manager.restartIndex != 17 {
		t.Fatalf("RestartHLSAt request/index = %#v/%d, want %#v/17", restarted, manager.restartIndex, want)
	}
	if manager.started.OpenSource == nil || manager.restarted.OpenSource == nil {
		t.Fatal("HLS adapter did not pass through OpenSource")
	}
	if got, want := reflect.ValueOf(manager.started.OpenSource).Pointer(), reflect.ValueOf(request.OpenSource).Pointer(); got != want {
		t.Fatalf("StartHLS OpenSource identity = %#x, want %#x", got, want)
	}
	if got, want := reflect.ValueOf(manager.restarted.OpenSource).Pointer(), reflect.ValueOf(request.OpenSource).Pointer(); got != want {
		t.Fatalf("RestartHLSAt OpenSource identity = %#x, want %#x", got, want)
	}
	if openCalls != 0 {
		t.Fatalf("HLS adapter OpenSource calls = %d before manager ownership, want 0", openCalls)
	}
	opened, err := manager.started.OpenSource()
	if err != nil {
		t.Fatalf("forwarded OpenSource(): %v", err)
	}
	defer func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close forwarded source: %v", err)
		}
	}()
	if openCalls != 1 || opened.Path() != sourcePath {
		t.Fatalf("forwarded OpenSource() calls/path = %d/%q, want 1/%q", openCalls, opened.Path(), sourcePath)
	}
	external := request
	external.EmbeddedSubtitleSource = ""
	external.ExternalSubtitleSource = "subtitles=f='/media/external.srt'"
	external.ExternalSubtitleRevision = "external-subtitle-revision"
	external.OpenExternalSubtitle = openSource
	mappedExternal := adapter.request(external)
	if mappedExternal.ExternalSubtitleSource != external.ExternalSubtitleSource ||
		mappedExternal.ExternalSubtitleRevision != external.ExternalSubtitleRevision {
		t.Fatalf("external subtitle authority = %q/%q, want %q/%q",
			mappedExternal.ExternalSubtitleSource, mappedExternal.ExternalSubtitleRevision,
			external.ExternalSubtitleSource, external.ExternalSubtitleRevision,
		)
	}
	if got, want := reflect.ValueOf(mappedExternal.OpenExternalSubtitle).Pointer(), reflect.ValueOf(external.OpenExternalSubtitle).Pointer(); got != want {
		t.Fatalf("external subtitle opener identity = %#x, want %#x", got, want)
	}

	body, err := adapter.WaitSegment(t.Context(), request.PlaylistPath, 4, nil)
	if err != nil {
		t.Fatalf("wait segment: %v", err)
	}
	if body != segment {
		t.Fatalf("WaitSegment reader = %T %p, want original reader %p", body, body, segment)
	}
	if manager.segmentPlaylist != request.PlaylistPath || manager.segmentIndex != 4 {
		t.Fatalf("WaitSegment arguments = %q/%d, want %q/4", manager.segmentPlaylist, manager.segmentIndex, request.PlaylistPath)
	}
	if playlist, err := adapter.WaitPlaylist(t.Context(), request.PlaylistPath, request.MinSegments); err != nil || playlist != manager.playlist {
		t.Fatalf("WaitPlaylist = %q, %v; want %q, nil", playlist, err, manager.playlist)
	}
	if !adapter.PingPlaySession(request.PlaySessionID) || manager.pingSession != request.PlaySessionID {
		t.Fatalf("PingPlaySession did not forward %q", request.PlaySessionID)
	}
	if err := adapter.StopPlaySession(t.Context(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	if manager.stoppedSession != request.PlaySessionID || manager.stoppedDevice != request.DeviceID {
		t.Fatalf("StopPlaySession arguments = %q/%q, want %q/%q", manager.stoppedSession, manager.stoppedDevice, request.PlaySessionID, request.DeviceID)
	}
	legacyBody, err := adapter.OpenLegacyHLS(t.Context(), "playlist-id", "legacy.ts")
	if err != nil {
		t.Fatalf("open legacy HLS: %v", err)
	}
	if legacyBody != legacy || manager.legacyPlaylistID != "playlist-id" || manager.legacyArtifact != "legacy.ts" {
		t.Fatal("OpenLegacyHLS did not forward the original reader and opaque identifiers")
	}
}

type p6HLSManager struct {
	started, restarted              ffmpeg.HLSRequest
	restartIndex, segmentIndex      int
	segmentPlaylist, playlist       string
	pingSession, stoppedSession     string
	stoppedDevice, legacyPlaylistID string
	legacyArtifact                  string
	ping                            bool
	segment                         io.ReadCloser
	legacy                          io.ReadCloser
}

func (m *p6HLSManager) StartHLS(_ context.Context, request ffmpeg.HLSRequest) error {
	m.started = request
	return nil
}

func (m *p6HLSManager) RestartHLSAt(_ context.Context, request ffmpeg.HLSRequest, index int) error {
	m.restarted, m.restartIndex = request, index
	return nil
}

func (m *p6HLSManager) WaitSegment(_ context.Context, playlist string, index int, _ ...*ffmpeg.HLSSegmentTiming) (io.ReadCloser, error) {
	m.segmentPlaylist, m.segmentIndex = playlist, index
	return m.segment, nil
}

func (m *p6HLSManager) WaitPlaylist(_ context.Context, playlist string, _ int) (string, error) {
	m.segmentPlaylist = playlist
	return m.playlist, nil
}

func (m *p6HLSManager) PingPlaySession(session string) bool {
	m.pingSession = session
	return m.ping
}

func (m *p6HLSManager) StopPlaySession(_ context.Context, session, device string) error {
	m.stoppedSession, m.stoppedDevice = session, device
	return nil
}

func (m *p6HLSManager) OpenLegacyHLS(_ context.Context, playlistID, artifact string) (io.ReadCloser, error) {
	m.legacyPlaylistID, m.legacyArtifact = playlistID, artifact
	return m.legacy, nil
}

type p6TrackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *p6TrackingReadCloser) Close() error {
	r.closed = true
	return nil
}

type p6StringReader struct {
	value string
	read  bool
}

func (r *p6StringReader) Read(dst []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return copy(dst, r.value), nil
}

func p6RoutesByOperation(routes []router.Route, operationID string) []router.Route {
	var matches []router.Route
	for _, route := range routes {
		if route.OperationID == operationID {
			matches = append(matches, route)
		}
	}
	return matches
}
