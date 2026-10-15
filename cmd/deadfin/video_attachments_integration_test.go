// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationServesScannedMKVAttachments scans a real MKV with a font, a
// text/html and an image/svg+xml attachment. A token scopes the item lookup
// to its user (upstream GetItemById(videoId, User.GetUserId())); an anonymous
// request has no scope. Only the font keeps its stored type; the active
// document types are served as downloads (D-64).
func TestApplicationServesScannedMKVAttachments(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("MKV attachment fixtures require ffmpeg: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("the scanner requires ffprobe: %v", err)
	}
	fixtures := t.TempDir()
	font := []byte("\x00\x01\x00\x00fake font table")
	html := []byte("<!doctype html><script>document.title='owned'</script>")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	fontPath, htmlPath, svgPath := filepath.Join(fixtures, "Komika.ttf"), filepath.Join(fixtures, "page.html"), filepath.Join(fixtures, "image.svg")
	for path, body := range map[string][]byte{fontPath: font, htmlPath: html, svgPath: svg} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source := t.TempDir()
	media := filepath.Join(source, "Attached Movie (2024)", "Attached Movie (2024).mkv")
	runFixtureFFmpeg(t, ffmpegPath, "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1", "-c:v", "mpeg4",
		"-attach", fontPath, "-metadata:s:t:0", "mimetype=application/x-truetype-font",
		"-attach", htmlPath, "-metadata:s:t:1", "mimetype=text/html",
		"-attach", svgPath, "-metadata:s:t:2", "mimetype=image/svg+xml",
		mkdirFor(t, media))

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Attachment Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add attachment library: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	adminToken := loginComposedAdministrator(t, app.Router())
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan attachment movie: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan attachment movie errors = %#v", result.Errors)
	}
	stored := applicationScannedItem(t, store.NewItemStore(app.stores), media, item.KindMovie)

	info := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Items/"+stored.ID.Dashed()+"/PlaybackInfo", []byte(`{}`), adminToken)
	if info.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo status = %d; body: %s", info.Code, info.Body.String())
	}
	var playbackInfo struct {
		MediaSources []struct {
			ID               string `json:"Id"`
			MediaAttachments []struct {
				Index    int
				MimeType string
			}
		}
	}
	if err := json.Unmarshal(info.Body.Bytes(), &playbackInfo); err != nil || len(playbackInfo.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo = %s, err %v", info.Body.String(), err)
	}
	indexes := map[string]int{}
	for _, attachment := range playbackInfo.MediaSources[0].MediaAttachments {
		indexes[attachment.MimeType] = attachment.Index
	}
	for _, mimeType := range []string{"application/x-truetype-font", "text/html", "image/svg+xml"} {
		if _, found := indexes[mimeType]; !found {
			t.Fatalf("scanned attachments = %#v, want %s", playbackInfo.MediaSources[0].MediaAttachments, mimeType)
		}
	}
	attachmentPath := func(mimeType string) string {
		return "/Videos/" + stored.ID.String() + "/" + playbackInfo.MediaSources[0].ID + "/Attachments/" + strconv.Itoa(indexes[mimeType])
	}

	restricted, err := app.UserService().Create(t.Context(), "restricted-viewer", "viewer password")
	if err != nil {
		t.Fatalf("create restricted user: %v", err)
	}
	if err := app.UserService().UpdatePolicy(t.Context(), restricted.ID, []byte(`{"EnableAllFolders":false,"EnabledFolders":[],"EnableRemoteAccess":true}`)); err != nil {
		t.Fatalf("restrict user folders: %v", err)
	}
	restrictedLogin := composedLogin(t, app, "restricted-viewer", "viewer password", "restricted-viewer-device")
	assertCompositionLoginStatus(t, restrictedLogin, http.StatusOK)
	restrictedToken := compositionAccessToken(t, restrictedLogin)
	if _, err := app.UserService().Create(t.Context(), "permitted-viewer", "viewer password"); err != nil {
		t.Fatalf("create permitted user: %v", err)
	}
	permittedLogin := composedLogin(t, app, "permitted-viewer", "viewer password", "permitted-viewer-device")
	assertCompositionLoginStatus(t, permittedLogin, http.StatusOK)
	permittedToken := compositionAccessToken(t, permittedLogin)

	for _, tc := range []struct {
		name, mimeType, query, token string
		wantStatus                   int
		wantType, wantDisposition    string
		wantBody                     []byte
	}{
		{name: "anonymous font keeps its type", mimeType: "application/x-truetype-font", wantStatus: http.StatusOK, wantType: "application/x-truetype-font", wantBody: font},
		{name: "permitted user font", mimeType: "application/x-truetype-font", token: permittedToken, wantStatus: http.StatusOK, wantType: "application/x-truetype-font", wantBody: font},
		{name: "administrator font", mimeType: "application/x-truetype-font", token: adminToken, wantStatus: http.StatusOK, wantType: "application/x-truetype-font", wantBody: font},
		{name: "restricted user font is not found", mimeType: "application/x-truetype-font", token: restrictedToken, wantStatus: http.StatusNotFound},
		// With legacy authorization off (the default), api_key is not a
		// credential, so the request is anonymous, as upstream.
		{name: "legacy api_key query is anonymous", mimeType: "application/x-truetype-font", query: "?api_key=" + restrictedToken, wantStatus: http.StatusOK, wantType: "application/x-truetype-font", wantBody: font},
		{name: "restricted user ApiKey query is not found", mimeType: "text/html", query: "?ApiKey=" + restrictedToken, wantStatus: http.StatusNotFound},
		{name: "invalid token stays anonymous", mimeType: "application/x-truetype-font", query: "?ApiKey=not-a-token", wantStatus: http.StatusOK, wantType: "application/x-truetype-font", wantBody: font},
		{name: "text/html is a download", mimeType: "text/html", wantStatus: http.StatusOK, wantType: "application/octet-stream", wantDisposition: "attachment", wantBody: html},
		{name: "image/svg+xml is a download", mimeType: "image/svg+xml", token: permittedToken, wantStatus: http.StatusOK, wantType: "application/octet-stream", wantDisposition: "attachment", wantBody: svg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, attachmentPath(tc.mimeType)+tc.query, nil, tc.token)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			header := rec.Header()
			if got := header.Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}
			if got := header.Get("Content-Disposition"); got != tc.wantDisposition {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.wantDisposition)
			}
			if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := header.Get("Content-Security-Policy"); got != "sandbox; default-src 'none'" {
				t.Errorf("Content-Security-Policy = %q", got)
			}
			if got := rec.Body.String(); got != string(tc.wantBody) {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}
