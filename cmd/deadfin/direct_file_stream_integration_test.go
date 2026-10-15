// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationServesScannedMovieDirectFile proves the public direct-file
// path starts from a persisted scanner result rather than a handler-only
// fixture. The container-suffixed URL is a distinct Jellyfin route shape that
// must select that same source file.
func TestApplicationServesScannedMovieDirectFile(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("ffprobe is required for real scanner direct-file streaming: %v", err)
	}

	fixtures := applicationProbeFixtureRoot(t)
	source := t.TempDir()
	media := filepath.Join(source, "movies", "Standalone Movie (2023)", "Standalone Movie (2023).mkv")
	copyApplicationProbeFixture(t, filepath.Join(fixtures, "movies", "Standalone Movie (2023)", "Standalone Movie (2023).mkv"), media)
	copyApplicationProbeFixture(t, filepath.Join(fixtures, "movies", "Standalone Movie (2023)", "movie.nfo"), filepath.Join(filepath.Dir(media), "movie.nfo"))
	wantFile, err := os.ReadFile(media)
	if err != nil {
		t.Fatalf("read direct-file fixture: %v", err)
	}
	if len(wantFile) < 16 {
		t.Fatalf("direct-file fixture size = %d, want at least 16 bytes for a range response", len(wantFile))
	}

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Direct Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: filepath.Join(source, "movies")}}}); err != nil {
		t.Fatalf("add direct-file fixture library: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())

	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan direct-file movie: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan direct-file movie errors = %#v", result.Errors)
	}
	stored := applicationScannedItem(t, store.NewItemStore(app.stores), media, item.KindMovie)

	for _, tc := range []struct {
		name, method, suffix, byteRange, mediaSourceID, wantErrorBody string
		wantStatus, wantLength                                        int
		wantBody                                                      []byte
		wantContentRange                                              string
		wantAcceptRanges                                              bool
	}{
		{name: "GET bare stream", method: http.MethodGet, suffix: "/stream", wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile, wantAcceptRanges: true},
		{name: "HEAD bare stream", method: http.MethodHead, suffix: "/stream", wantStatus: http.StatusOK, wantLength: len(wantFile)},
		{name: "GET container stream", method: http.MethodGet, suffix: "/stream.mkv", wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile, wantAcceptRanges: true},
		{name: "HEAD container stream", method: http.MethodHead, suffix: "/stream.mkv", wantStatus: http.StatusOK, wantLength: len(wantFile)},
		{name: "GET prefix range", method: http.MethodGet, suffix: "/stream", byteRange: "bytes=0-15", wantStatus: http.StatusPartialContent, wantLength: 16, wantBody: wantFile[:16], wantContentRange: "bytes 0-15/" + strconv.Itoa(len(wantFile)), wantAcceptRanges: true},
		{name: "GET invalid range", method: http.MethodGet, suffix: "/stream", byteRange: "bytes=" + strconv.Itoa(len(wantFile)) + "-", wantStatus: http.StatusRequestedRangeNotSatisfiable, wantLength: 0, wantContentRange: "bytes */" + strconv.Itoa(len(wantFile)), wantAcceptRanges: true},
		{name: "GET source mismatch", method: http.MethodGet, suffix: "/stream", mediaSourceID: "00000000-0000-0000-0000-00000000d1e0", wantStatus: http.StatusBadRequest, wantErrorBody: "Error processing request."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/Videos/" + stored.ID.Dashed() + tc.suffix + "?Static=true"
			if tc.mediaSourceID != "" {
				path += "&MediaSourceId=" + tc.mediaSourceID
			}
			req := httptest.NewRequest(tc.method, path, nil)
			req.RemoteAddr = "127.0.0.1:8096"
			req.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="1.0", Token="`+token+`"`)
			if tc.byteRange != "" {
				req.Header.Set("Range", tc.byteRange)
			}
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s status = %d, want %d; body: %s", tc.method, path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrorBody != "" {
				if got := rec.Header().Get("Content-Type"); got != "text/plain" {
					t.Errorf("%s %s Content-Type = %q, want %q", tc.method, path, got, "text/plain")
				}
				if got := rec.Body.String(); got != tc.wantErrorBody {
					t.Errorf("%s %s error body = %q, want %q", tc.method, path, got, tc.wantErrorBody)
				}
				return
			}
			if tc.wantAcceptRanges {
				if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
					t.Errorf("%s %s Accept-Ranges = %q, want %q", tc.method, path, got, "bytes")
				}
			}
			if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(tc.wantLength); got != want {
				t.Errorf("%s %s Content-Length = %q, want %q", tc.method, path, got, want)
			}
			if got := rec.Header().Get("Content-Range"); got != tc.wantContentRange {
				t.Errorf("%s %s Content-Range = %q, want %q", tc.method, path, got, tc.wantContentRange)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.wantBody) {
				t.Errorf("%s %s body = %d bytes, want selected media bytes (%d bytes)", tc.method, path, rec.Body.Len(), len(tc.wantBody))
			}
		})
	}
	t.Run("PlaySessionId from PlaybackInfo authenticates the Swiftfin URL", func(t *testing.T) {
		info := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Items/"+stored.ID.Dashed()+"/PlaybackInfo", []byte(`{"DeviceId":"deadfin-test-device"}`), token)
		if info.Code != http.StatusOK {
			t.Fatalf("PlaybackInfo status = %d; body: %s", info.Code, info.Body.String())
		}
		var response struct{ PlaySessionId string }
		if err := json.Unmarshal(info.Body.Bytes(), &response); err != nil || response.PlaySessionId == "" {
			t.Fatalf("PlaybackInfo PlaySessionId = %q, err %v", response.PlaySessionId, err)
		}
		absent := "00000000000000000000000000d1e2ff"
		for _, step := range []struct {
			name, path string
			want       int
		}{
			{"only PlaySessionId", "/Videos/" + stored.ID.String() + "/stream?static=true&tag=abc&PlaySessionId=" + response.PlaySessionId + "&MediaSourceId=" + stored.ID.String(), http.StatusOK},
			{"wrong item", "/Videos/" + absent + "/stream?static=true&PlaySessionId=" + response.PlaySessionId + "&MediaSourceId=" + absent, http.StatusUnauthorized},
			{"no PlaySessionId", "/Videos/" + stored.ID.String() + "/stream?static=true&MediaSourceId=" + stored.ID.String(), http.StatusUnauthorized},
			// The deadfin-web media element shape: api_key with legacy
			// authorization off (the default), no PlaySessionId.
			{"api_key query token", "/Videos/" + stored.ID.String() + "/stream.mkv?static=true&api_key=" + token + "&mediaSourceId=" + stored.ID.String(), http.StatusOK},
			{"wrong api_key query token", "/Videos/" + stored.ID.String() + "/stream.mkv?static=true&api_key=not-a-token", http.StatusUnauthorized},
		} {
			req := httptest.NewRequest(http.MethodGet, step.path, nil)
			req.RemoteAddr = "127.0.0.1:8096"
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != step.want {
				t.Fatalf("%s: status = %d, want %d; body: %s", step.name, rec.Code, step.want, rec.Body.String())
			}
			if step.want == http.StatusOK && !bytes.Equal(rec.Body.Bytes(), wantFile) {
				t.Fatalf("%s: body = %d bytes, want the media file", step.name, rec.Body.Len())
			}
		}
	})
}

// TestApplicationAudioDirectFileRouteLedger pins Slice B's route contract at
// composition time, including the authenticated direct-stream endpoints
// (D-54) and the authorization boundaries on universal streaming and
// download.
func TestApplicationAudioDirectFileRouteLedger(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/stream", OperationID: "GetAudioStream", Policy: string(auth.PolicyDefaultAuthorization), PlaySessionCredential: true},
		{Method: http.MethodHead, Pattern: "/Audio/{itemId}/stream", OperationID: "HeadAudioStream", Policy: string(auth.PolicyDefaultAuthorization), PlaySessionCredential: true},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/stream.{container}", OperationID: "GetAudioStreamByContainer", Policy: string(auth.PolicyDefaultAuthorization), PlaySessionCredential: true},
		{Method: http.MethodHead, Pattern: "/Audio/{itemId}/stream.{container}", OperationID: "HeadAudioStreamByContainer", Policy: string(auth.PolicyDefaultAuthorization), PlaySessionCredential: true},
		{Method: http.MethodGet, Pattern: "/Audio/{itemId}/universal", OperationID: "GetUniversalAudioStream", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodHead, Pattern: "/Audio/{itemId}/universal", OperationID: "HeadUniversalAudioStream", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Download", OperationID: "GetDownload", Policy: string(auth.PolicyDownload)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := directFileRouteByOperation(app.Router().Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got != want {
				t.Fatalf("route %q = %#v, want %#v", want.OperationID, got, want)
			}
		})
	}
}

// TestApplicationServesScannedAudioDirectFilesAndDownload exercises Slice B
// through a scanned audio item, so every route must select the persisted file
// source and hand it to the established static-file responder. One range per
// direct behavior is intentional: the exhaustive range matrix belongs to the
// Slice A responder test.
func TestApplicationServesScannedAudioDirectFilesAndDownload(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("ffprobe is required for real scanner direct-file streaming: %v", err)
	}

	fixtures := applicationProbeFixtureRoot(t)
	source := t.TempDir()
	media := filepath.Join(source, "Goldenrec Generator", "Synthetic Album", "1 - Synthetic Track 1.mp3")
	copyApplicationProbeFixture(t, filepath.Join(fixtures, "music", "Goldenrec Generator", "Synthetic Album", "1 - Synthetic Track 1.mp3"), media)
	wantFile, err := os.ReadFile(media)
	if err != nil {
		t.Fatalf("read direct-file fixture: %v", err)
	}
	if len(wantFile) < 16 {
		t.Fatalf("direct-file fixture size = %d, want at least 16 bytes for a range response", len(wantFile))
	}

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Direct Audio", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add direct-file audio library: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan direct-file audio: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan direct-file audio errors = %#v", result.Errors)
	}
	stored := applicationScannedItem(t, store.NewItemStore(app.stores), media, item.KindAudio)
	selectedSource := stored.ID.Dashed()
	member, err := app.UserService().Create(t.Context(), "restricted-listener", "listener password")
	if err != nil {
		t.Fatalf("create restricted member: %v", err)
	}
	if err := app.UserService().UpdatePolicy(t.Context(), member.ID, []byte(`{"EnableAllFolders":false,"EnabledFolders":[],"EnableRemoteAccess":true}`)); err != nil {
		t.Fatalf("restrict member folders: %v", err)
	}
	memberLogin := composedLogin(t, app, "restricted-listener", "listener password", "restricted-listener-device")
	assertCompositionLoginStatus(t, memberLogin, http.StatusOK)
	restrictedToken := compositionAccessToken(t, memberLogin)
	missingSource := "00000000-0000-0000-0000-00000000d1e0"
	missingItem := "00000000-0000-0000-0000-00000000d1e1"

	for _, tc := range []struct {
		name, method, path, byteRange, token string
		wantStatus, wantLength               int
		wantBody                             []byte
		wantRange                            string
		wantErrorBody                        string
		wantDisposition                      string
	}{
		{name: "anonymous GET audio stream is unauthorized", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource, wantStatus: http.StatusUnauthorized},
		{name: "anonymous GET container audio stream is unauthorized", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream.mp3?Static=true&MediaSourceId=" + selectedSource, wantStatus: http.StatusUnauthorized},
		{name: "authenticated GET audio stream selects source", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource, token: token, wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile},
		{name: "ApiKey query authenticates GET audio stream", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource + "&ApiKey=" + token, wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile},
		{name: "authenticated HEAD audio stream has no body", method: http.MethodHead, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource, token: token, wantStatus: http.StatusOK, wantLength: len(wantFile)},
		{name: "authenticated GET container audio stream selects source", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream.mp3?Static=true&MediaSourceId=" + selectedSource, token: token, wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile},
		{name: "authenticated HEAD container audio stream has no body", method: http.MethodHead, path: "/Audio/" + stored.ID.Dashed() + "/stream.mp3?Static=true&MediaSourceId=" + selectedSource, token: token, wantStatus: http.StatusOK, wantLength: len(wantFile)},
		{name: "audio stream outside the user's libraries is not found", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource, token: restrictedToken, wantStatus: http.StatusNotFound},
		{name: "audio stream range uses shared responder", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + selectedSource, token: token, byteRange: "bytes=0-15", wantStatus: http.StatusPartialContent, wantLength: 16, wantBody: wantFile[:16], wantRange: "bytes 0-15/" + strconv.Itoa(len(wantFile))},
		{name: "audio source mismatch is a bad request", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/stream?Static=true&MediaSourceId=" + missingSource, token: token, wantStatus: http.StatusBadRequest, wantErrorBody: "Error processing request."},
		{name: "authenticated GET universal audio selects source", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/universal?MediaSourceId=" + selectedSource + "&Container=mp3", token: token, wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile},
		{name: "authenticated HEAD universal audio has no body", method: http.MethodHead, path: "/Audio/" + stored.ID.Dashed() + "/universal?MediaSourceId=" + selectedSource + "&Container=mp3", token: token, wantStatus: http.StatusOK, wantLength: len(wantFile)},
		{name: "universal audio range uses shared responder", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/universal?MediaSourceId=" + selectedSource + "&Container=mp3", token: token, byteRange: "bytes=0-15", wantStatus: http.StatusPartialContent, wantLength: 16, wantBody: wantFile[:16], wantRange: "bytes 0-15/" + strconv.Itoa(len(wantFile))},
		{name: "universal source mismatch is a bad request", method: http.MethodGet, path: "/Audio/" + stored.ID.Dashed() + "/universal?MediaSourceId=" + missingSource + "&Container=mp3", token: token, wantStatus: http.StatusBadRequest, wantErrorBody: "Error processing request."},
		{name: "download attaches selected audio filename", method: http.MethodGet, path: "/Items/" + stored.ID.Dashed() + "/Download", token: token, wantStatus: http.StatusOK, wantLength: len(wantFile), wantBody: wantFile, wantDisposition: `attachment; filename="1 - Synthetic Track 1.mp3"; filename*=UTF-8''1%20-%20Synthetic%20Track%201.mp3`},
		{name: "download missing item is not found", method: http.MethodGet, path: "/Items/" + missingItem + "/Download", token: token, wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = "127.0.0.1:8096"
			if tc.token != "" {
				req.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="1.0", Token="`+tc.token+`"`)
			}
			if tc.byteRange != "" {
				req.Header.Set("Range", tc.byteRange)
			}
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s status = %d, want %d; body: %s", tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrorBody != "" {
				if got := rec.Header().Get("Content-Type"); got != "text/plain" {
					t.Errorf("%s %s Content-Type = %q, want %q", tc.method, tc.path, got, "text/plain")
				}
				if got := rec.Body.String(); got != tc.wantErrorBody {
					t.Errorf("%s %s error body = %q, want %q", tc.method, tc.path, got, tc.wantErrorBody)
				}
				return
			}
			if tc.wantStatus != http.StatusOK && tc.wantStatus != http.StatusPartialContent {
				return
			}
			if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("%s %s Accept-Ranges = %q, want %q", tc.method, tc.path, got, "bytes")
			}
			if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(tc.wantLength); got != want {
				t.Errorf("%s %s Content-Length = %q, want %q", tc.method, tc.path, got, want)
			}
			if got := rec.Header().Get("Content-Range"); got != tc.wantRange {
				t.Errorf("%s %s Content-Range = %q, want %q", tc.method, tc.path, got, tc.wantRange)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.wantBody) {
				t.Errorf("%s %s body = %d bytes, want selected media bytes (%d bytes)", tc.method, tc.path, rec.Body.Len(), len(tc.wantBody))
			}
			if tc.wantDisposition != "" {
				if got := rec.Header().Get("Content-Disposition"); got != tc.wantDisposition {
					t.Errorf("Content-Disposition = %q, want %q", got, tc.wantDisposition)
				}
			}
		})
	}
}

func directFileRouteByOperation(routes []router.Route, operationID string) (router.Route, bool) {
	for _, route := range routes {
		if route.OperationID == operationID {
			return route, true
		}
	}
	return router.Route{}, false
}
