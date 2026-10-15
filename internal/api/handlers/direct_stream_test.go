// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestDirectVideoStreamsPhysicalFileCompatibility pins the range behavior of
// Jellyfin's PhysicalFile static branch rather than Go's more permissive
// multiple-range behavior. Every request uses the registered public router.
func TestDirectVideoStreamsPhysicalFileCompatibility(t *testing.T) {
	const contents = "direct-file-range-fixture"
	modTime := time.Unix(1_700_000_000, 0).UTC()
	media := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(media, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temporary media: %v", err)
	}
	if err := os.Chtimes(media, modTime, modTime); err != nil {
		t.Fatalf("set temporary media modification time: %v", err)
	}

	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f0")
	routes, sources := directVideoStreamRoutes(playback.MediaSourceInfo{
		ID:        "direct-file-source",
		Protocol:  playback.MediaProtocolFile,
		Path:      media,
		Container: "mkv",
	})
	basePath := "/Videos/" + itemID.Dashed() + "/stream?Static=true"
	lastModified := modTime.Format(http.TimeFormat)

	for _, tc := range []struct {
		name, method, path, wantRange string
		ranges                        []string
		wantStatus                    int
		wantBody                      []byte
		wantLength                    int
		checkLastModified             bool
	}{
		{
			name: "suffix range", method: http.MethodGet, path: basePath + "&Tag=client-cache", ranges: []string{"bytes=-7"},
			wantStatus: http.StatusPartialContent, wantBody: []byte(contents[len(contents)-7:]), wantLength: 7, wantRange: "bytes " + strconv.Itoa(len(contents)-7) + "-" + strconv.Itoa(len(contents)-1) + "/" + strconv.Itoa(len(contents)), checkLastModified: true,
		},
		{
			name: "malformed range falls back to full file", method: http.MethodGet, path: basePath, ranges: []string{"not-a-range"},
			wantStatus: http.StatusOK, wantBody: []byte(contents), wantLength: len(contents), checkLastModified: true,
		},
		{
			name: "comma range falls back to full file", method: http.MethodGet, path: basePath, ranges: []string{"bytes=0-1,3-4"},
			wantStatus: http.StatusOK, wantBody: []byte(contents), wantLength: len(contents), checkLastModified: true,
		},
		{
			name: "repeated range falls back to full file", method: http.MethodGet, path: basePath, ranges: []string{"bytes=0-1", "bytes=3-4"},
			wantStatus: http.StatusOK, wantBody: []byte(contents), wantLength: len(contents), checkLastModified: true,
		},
		{
			name: "unsatisfiable range", method: http.MethodGet, path: basePath, ranges: []string{"bytes=" + strconv.Itoa(len(contents)) + "-"},
			wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */" + strconv.Itoa(len(contents)),
		},
		{
			name: "HEAD range", method: http.MethodHead, path: basePath, ranges: []string{"bytes=2-5"},
			wantStatus: http.StatusPartialContent, wantLength: 4, wantRange: "bytes 2-5/" + strconv.Itoa(len(contents)), checkLastModified: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := directVideoStreamRequest(routes, tc.method, tc.path, tc.ranges...)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s status = %d, want %d; body=%q", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
			}
			if got, want := rec.Header().Get("Content-Range"), tc.wantRange; got != want {
				t.Errorf("Content-Range = %q, want %q", got, want)
			}
			if tc.wantStatus != http.StatusRequestedRangeNotSatisfiable {
				if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(tc.wantLength); got != want {
					t.Errorf("Content-Length = %q, want %q", got, want)
				}
				if !bytes.Equal(rec.Body.Bytes(), tc.wantBody) {
					t.Errorf("body = %q, want %q", rec.Body.Bytes(), tc.wantBody)
				}
				if tc.checkLastModified {
					if got := rec.Header().Get("Last-Modified"); got != lastModified {
						t.Errorf("Last-Modified = %q, want %q", got, lastModified)
					}
				}
			}
			if got := rec.Header().Get("ETag"); got != "" {
				t.Errorf("ETag = %q, want no response ETag", got)
			}
		})
	}

	if sources.calls != 6 {
		t.Errorf("StaticSource calls = %d, want one per static request", sources.calls)
	}
}

// TestDirectVideoStreamsUnsatisfiableMKVRangeHeaderParity preserves the static
// file response headers observed from Jellyfin when a requested byte range lies
// entirely beyond an MKV file.
func TestDirectVideoStreamsUnsatisfiableMKVRangeHeaderParity(t *testing.T) {
	const contents = "direct-file-range-fixture"
	modTime := time.Unix(1_700_000_000, 0).UTC()
	media := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(media, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temporary media: %v", err)
	}
	if err := os.Chtimes(media, modTime, modTime); err != nil {
		t.Fatalf("set temporary media modification time: %v", err)
	}

	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f2")
	routes, _ := directVideoStreamRoutes(playback.MediaSourceInfo{
		ID:        "direct-file-source",
		Protocol:  playback.MediaProtocolFile,
		Path:      media,
		Container: "mkv",
	})
	rec := directVideoStreamRequest(
		routes,
		http.MethodGet,
		"/Videos/"+itemID.Dashed()+"/stream?Static=true",
		"bytes="+strconv.Itoa(len(contents))+"-",
	)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusRequestedRangeNotSatisfiable, rec.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Type":   "video/x-matroska",
		"Last-Modified":  modTime.Format(http.TimeFormat),
		"Content-Length": "0",
		"Accept-Ranges":  "bytes",
		"Content-Range":  "bytes */" + strconv.Itoa(len(contents)),
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
		t.Errorf("X-Content-Type-Options = %q, want absent", got)
	}
	if got := rec.Body.Bytes(); len(got) != 0 {
		t.Errorf("body = %q, want empty body", got)
	}
}

func TestDirectVideoStreamsRejectRequestsWithoutStatic(t *testing.T) {
	routes, sources := directVideoStreamRoutes(playback.MediaSourceInfo{})
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f1")

	for _, path := range []string{
		"/Videos/" + itemID.Dashed() + "/stream",
		"/Videos/" + itemID.Dashed() + "/stream?Static=false",
	} {
		rec := directVideoStreamRequest(routes, http.MethodGet, path)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("GET %s status = %d, want %d; body=%q", path, rec.Code, http.StatusNotImplemented, rec.Body.String())
		}
	}
	if sources.calls != 0 {
		t.Errorf("StaticSource calls = %d, want 0 when Static is absent or false", sources.calls)
	}
}

// TestDirectVideoStreamAcceptsBareHexItemID proves that a live Swiftfin direct
// play request with a bare hex item ID (N format, no dashes) dispatches to the
// static file source. The router normalizes route casing; dto.ParseUUID accepts
// both N (bare hex) and D (dashed) formats. The table exercises the exact live
// shape (lowercase static=true) and the dashed-ID fallback.
func TestDirectVideoStreamAcceptsBareHexItemID(t *testing.T) {
	const contents = "bare-hex-direct-fixture"
	media := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(media, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temporary media: %v", err)
	}
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f3")
	routes, sources := directVideoStreamRoutes(playback.MediaSourceInfo{
		ID:        "bare-hex-source",
		Protocol:  playback.MediaProtocolFile,
		Path:      media,
		Container: "mkv",
	})

	cases := []struct {
		name string
		path string
	}{
		{
			name: "bare-hex-live-static",
			path: "/Videos/" + itemID.String() + "/stream?static=true",
		},
		{
			name: "dashed-static",
			path: "/Videos/" + itemID.Dashed() + "/stream?static=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := directVideoStreamRequest(routes, http.MethodGet, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s stream status = %d, want 200; body=%q", tc.path, rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != contents {
				t.Errorf("body = %q, want %q", got, contents)
			}
		})
	}
	if sources.calls != len(cases) {
		t.Errorf("StaticSource calls = %d, want %d", sources.calls, len(cases))
	}
}

type directVideoStreamSourceStub struct {
	source          playback.MediaSourceInfo
	err             error
	calls           int
	byMediaSourceID map[string]playback.MediaSourceInfo
}

func (s *directVideoStreamSourceStub) StaticSource(_ context.Context, _ dto.UUID, _ dto.UUID, mediaSourceID string) (playback.MediaSourceInfo, error) {
	s.calls++
	if source, found := s.byMediaSourceID[mediaSourceID]; found {
		return source, s.err
	}
	return s.source, s.err
}

func (s *directVideoStreamSourceStub) DownloadSource(_ context.Context, _ dto.UUID, _ dto.UUID) (playback.MediaSourceInfo, error) {
	return s.source, s.err
}

func directVideoStreamRoutes(source playback.MediaSourceInfo) (*router.Router, *directVideoStreamSourceStub) {
	service := &directVideoStreamSourceStub{source: source}
	routes := router.New()
	handlers.RegisterDirectVideoStreams(routes, service)
	return routes, service
}

func directVideoStreamRequest(handler http.Handler, method, path string, ranges ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for _, value := range ranges {
		req.Header.Add("Range", value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestUniversalAudioContainerAlternativesServeDirectSource(t *testing.T) {
	const contents = "universal-audio-direct-source"
	media := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(media, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temporary media: %v", err)
	}

	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f3")
	bitrate := 320000
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "repeated alternatives", query: "Container=flac&Container=mp3"},
		{name: "CSV alternatives", query: "Container=flac,mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes, _ := directAudioStreamRoutes(t, playback.MediaSourceInfo{
				ID: "direct-audio-source", Protocol: playback.MediaProtocolFile, Path: media, Container: "mp3", Bitrate: &bitrate, SupportsDirectPlay: true, SupportsDirectStream: true, SupportsTranscoding: true,
				MediaStreams: []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}},
			})
			rec := directVideoStreamRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/universal?"+tc.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET universal audio status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := rec.Body.String(); got != contents {
				t.Errorf("GET universal audio body = %q, want direct source %q", got, contents)
			}
			if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
				t.Errorf("GET universal audio Content-Type = %q, want %q", got, "audio/mpeg")
			}
		})
	}
}

func TestUniversalAudioRejectsAbsentIncompatibleAndNonDirectSources(t *testing.T) {
	const contents = "universal-audio-must-not-be-served"
	media := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(media, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temporary media: %v", err)
	}

	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f4")
	bitrate := 320000
	for _, tc := range []struct {
		name       string
		query      string
		direct     bool
		wantStatus int
	}{
		{name: "missing Container", wantStatus: http.StatusInternalServerError},
		{name: "all alternatives incompatible", query: "Container=flac&Container=wav", direct: true, wantStatus: http.StatusNotImplemented},
		{name: "direct streaming disabled", query: "Container=mp3", wantStatus: http.StatusNotImplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes, _ := directAudioStreamRoutes(t, playback.MediaSourceInfo{
				ID: "direct-audio-source", Protocol: playback.MediaProtocolFile, Path: media, Container: "mp3", Bitrate: &bitrate, SupportsDirectPlay: tc.direct, SupportsDirectStream: tc.direct, SupportsTranscoding: true,
				MediaStreams: []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}},
			})
			path := "/Audio/" + itemID.Dashed() + "/universal"
			if tc.query != "" {
				path += "?" + tc.query
			}
			rec := directVideoStreamRequest(routes, http.MethodGet, path)

			if rec.Code != tc.wantStatus {
				t.Fatalf("GET universal audio status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got, want := rec.Body.String(), http.StatusText(tc.wantStatus)+"\n"; got != want {
				t.Errorf("GET universal audio error body = %q, want %q", got, want)
			}
			if got := rec.Header().Get("Accept-Ranges"); got != "" {
				t.Errorf("GET universal audio Accept-Ranges = %q, want no media response headers", got)
			}
		})
	}
}

func TestItemDownloadIgnoresMediaSourceIDQuery(t *testing.T) {
	const canonicalContents = "canonical-item-source"
	const alternateContents = "alternate-media-source"
	canonical := filepath.Join(t.TempDir(), "canonical.mp3")
	if err := os.WriteFile(canonical, []byte(canonicalContents), 0o600); err != nil {
		t.Fatalf("write canonical media: %v", err)
	}
	alternate := filepath.Join(t.TempDir(), "alternate.mp3")
	if err := os.WriteFile(alternate, []byte(alternateContents), 0o600); err != nil {
		t.Fatalf("write alternate media: %v", err)
	}

	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f5")
	routes, sources := directDownloadRoutes(t, playback.MediaSourceInfo{
		ID: "canonical-source", Protocol: playback.MediaProtocolFile, Path: canonical, Container: "mp3",
	})
	sources.byMediaSourceID = map[string]playback.MediaSourceInfo{
		"other-source": {ID: "other-source", Protocol: playback.MediaProtocolFile, Path: alternate, Container: "mp3"},
	}
	rec := directVideoStreamRequest(routes, http.MethodGet, "/Items/"+itemID.Dashed()+"/Download?MediaSourceId=other-source")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET item download status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Body.String(); got != canonicalContents {
		t.Errorf("GET item download body = %q, want canonical item source %q", got, canonicalContents)
	}
}

func directAudioStreamRoutes(t *testing.T, source playback.MediaSourceInfo) (*router.Router, *directVideoStreamSourceStub) {
	t.Helper()
	identity := auth.Identity{Authenticated: true, UserID: playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f6")}
	authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
	service := &directVideoStreamSourceStub{source: source}
	routes := router.New(authorizer.Wrap)
	handlers.RegisterDirectAudioStreams(routes, service)
	return routes, service
}

func directDownloadRoutes(t *testing.T, source playback.MediaSourceInfo) (*router.Router, *directVideoStreamSourceStub) {
	t.Helper()
	identity := auth.Identity{Authenticated: true, UserID: playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f7")}
	authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
	service := &directVideoStreamSourceStub{source: source}
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItemDownloads(routes, service)
	return routes, service
}
