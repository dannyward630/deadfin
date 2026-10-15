// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/playback"
)

type attachmentReaderStub struct {
	path        string
	contentType string
	err         error
	calls       int
	request     playback.AttachmentRequest
}

func (s *attachmentReaderStub) Attachment(_ context.Context, request playback.AttachmentRequest) (playback.AttachmentResponse, error) {
	s.calls++
	s.request = request
	if s.err != nil {
		return playback.AttachmentResponse{}, s.err
	}
	file, err := os.Open(s.path)
	if err != nil {
		return playback.AttachmentResponse{}, err
	}
	info, _ := file.Stat()
	return playback.AttachmentResponse{ContentType: s.contentType, File: file, Size: info.Size()}, nil
}

func attachmentRoutes(identity auth.Identity, reader handlers.AttachmentReader) *router.Router {
	authorizer := middleware.NewAuthorizer(subtitleTokenResolver{identity: identity}, subtitleAllowEvaluator{}, subtitleIdentityUsers{})
	routes := router.New(authorizer.Wrap)
	handlers.RegisterVideoAttachments(routes, reader)
	return routes
}

func TestVideoAttachmentRouteIsAnonymousGET(t *testing.T) {
	routes := attachmentRoutes(auth.Identity{}, &attachmentReaderStub{})
	got, found := subtitleRouteByOperation(routes.Routes(), "GetAttachment")
	want := router.Route{Method: http.MethodGet, Pattern: "/Videos/{videoId}/{mediaSourceId}/Attachments/{index}", OperationID: "GetAttachment", Anonymous: true, OptionalIdentity: true}
	if !found || got != want {
		t.Fatalf("GetAttachment route = %#v, found %t; want %#v", got, found, want)
	}
}

func TestVideoAttachmentStreamsFileWithMimeType(t *testing.T) {
	font := filepath.Join(t.TempDir(), "font.ttf")
	if err := os.WriteFile(font, []byte("\x00\x01\x00\x00font"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Authenticated: true, Token: "attachment-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-000000000a01")}
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000a02")
	reader := &attachmentReaderStub{path: font, contentType: "application/x-truetype-font"}
	path := "/Videos/" + itemID.Dashed() + "/0a1b2c3d4e5f60718293a4b5c6d7e8f9/Attachments/6"

	// The client fetches the URL directly with ApiKey query auth.
	record := httptest.NewRecorder()
	attachmentRoutes(identity, reader).ServeHTTP(record, httptest.NewRequest(http.MethodGet, path+"?ApiKey=attachment-token", nil))
	if record.Code != http.StatusOK || record.Body.String() != "\x00\x01\x00\x00font" {
		t.Fatalf("GET with ApiKey = %d %q", record.Code, record.Body.String())
	}
	if got := record.Header().Get("Content-Type"); got != "application/x-truetype-font" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := record.Header().Get("Content-Length"); got != "8" {
		t.Errorf("Content-Length = %q, want 8", got)
	}
	// A user token scopes the item lookup to that user, as upstream
	// GetItemById(videoId, User.GetUserId()).
	if want := (playback.AttachmentRequest{UserID: identity.UserID, ItemID: itemID, MediaSourceID: "0a1b2c3d4e5f60718293a4b5c6d7e8f9", Index: 6}); reader.request != want {
		t.Errorf("request = %#v, want %#v", reader.request, want)
	}

	// Upstream VideoAttachmentsController is anonymous; an anonymous or
	// invalid credential has no user scope.
	for _, target := range []string{path, path + "?ApiKey=not-a-token"} {
		record = httptest.NewRecorder()
		attachmentRoutes(identity, reader).ServeHTTP(record, httptest.NewRequest(http.MethodGet, target, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", target, record.Code)
		}
		if want := (playback.AttachmentRequest{ItemID: itemID, MediaSourceID: "0a1b2c3d4e5f60718293a4b5c6d7e8f9", Index: 6}); reader.request != want {
			t.Errorf("GET %s request = %#v, want %#v", target, reader.request, want)
		}
	}

	// An API key keeps the unscoped lookup, as on the other item routes.
	apiKey := auth.Identity{Authenticated: true, APIKey: true, Administrator: true, Token: "attachment-api-key"}
	record = httptest.NewRecorder()
	attachmentRoutes(apiKey, reader).ServeHTTP(record, httptest.NewRequest(http.MethodGet, path+"?ApiKey=attachment-api-key", nil))
	if record.Code != http.StatusOK || reader.request.UserID != (playback.AttachmentRequest{}).UserID {
		t.Fatalf("GET with API key = %d, request %#v; want 200 with no user scope", record.Code, reader.request)
	}
}

func TestVideoAttachmentContentTypeAllowlist(t *testing.T) {
	body := filepath.Join(t.TempDir(), "attachment")
	if err := os.WriteFile(body, []byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000a06")
	path := "/Videos/" + itemID.Dashed() + "/src/Attachments/1"
	for _, tc := range []struct {
		stored, want string
		download     bool
	}{
		{stored: "application/x-truetype-font", want: "application/x-truetype-font"},
		{stored: "font/ttf", want: "font/ttf"},
		{stored: "Font/OTF", want: "font/otf"},
		{stored: "application/vnd.ms-opentype", want: "application/vnd.ms-opentype"},
		{stored: "application/x-font-ttf", want: "application/x-font-ttf"},
		{stored: "image/jpeg", want: "image/jpeg"},
		{stored: "image/png", want: "image/png"},
		{stored: "image/webp", want: "image/webp"},
		{stored: "text/html", want: "application/octet-stream", download: true},
		{stored: "text/html; charset=utf-8", want: "application/octet-stream", download: true},
		{stored: "image/svg+xml", want: "application/octet-stream", download: true},
		{stored: "application/xhtml+xml", want: "application/octet-stream", download: true},
		{stored: "text/xml", want: "application/octet-stream", download: true},
		{stored: "application/pdf", want: "application/octet-stream", download: true},
		{stored: "text/plain", want: "application/octet-stream", download: true},
		{stored: "application/octet-stream", want: "application/octet-stream", download: true},
		{stored: "font/ttf, text/html", want: "application/octet-stream", download: true},
	} {
		t.Run(tc.stored, func(t *testing.T) {
			record := httptest.NewRecorder()
			attachmentRoutes(auth.Identity{}, &attachmentReaderStub{path: body, contentType: tc.stored}).ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", record.Code)
			}
			header := record.Header()
			if got := header.Get("Content-Type"); got != tc.want {
				t.Errorf("Content-Type = %q, want %q", got, tc.want)
			}
			wantDisposition := ""
			if tc.download {
				wantDisposition = "attachment"
			}
			if got := header.Get("Content-Disposition"); got != wantDisposition {
				t.Errorf("Content-Disposition = %q, want %q", got, wantDisposition)
			}
			if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := header.Get("Content-Security-Policy"); got != "sandbox; default-src 'none'" {
				t.Errorf("Content-Security-Policy = %q", got)
			}
		})
	}
}

func TestVideoAttachmentErrors(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000a03")
	for _, tc := range []struct {
		name string
		path string
		err  error
		want int
	}{
		{name: "missing attachment", path: "/Videos/" + itemID.Dashed() + "/src/Attachments/9", err: playback.ErrAttachmentNotFound, want: http.StatusNotFound},
		{name: "missing item", path: "/Videos/" + itemID.Dashed() + "/src/Attachments/9", err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "missing source", path: "/Videos/" + itemID.Dashed() + "/src/Attachments/9", err: playback.ErrMediaSourceNotFound, want: http.StatusNotFound},
		{name: "extraction failure", path: "/Videos/" + itemID.Dashed() + "/src/Attachments/9", err: errors.New("ffmpeg"), want: http.StatusInternalServerError},
		{name: "bad id", path: "/Videos/not-a-guid/src/Attachments/9", want: http.StatusBadRequest},
		{name: "bad index", path: "/Videos/" + itemID.Dashed() + "/src/Attachments/x", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			attachmentRoutes(auth.Identity{}, &attachmentReaderStub{err: tc.err}).ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if record.Code != tc.want {
				t.Fatalf("status = %d, want %d", record.Code, tc.want)
			}
		})
	}
}

func TestSubtitleStreamServesExtractedFile(t *testing.T) {
	sup := filepath.Join(t.TempDir(), "3.sup")
	if err := os.WriteFile(sup, []byte("PG\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(sup)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Authenticated: true, Token: "subtitle-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-000000000a04")}
	streamer := &subtitleStreamerStub{streamResponse: playback.SubtitleResponse{ContentType: "application/octet-stream", File: file, Size: 4}}
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000a05")
	record := httptest.NewRecorder()
	subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/src/Subtitles/3/0/Stream.pgssub?ApiKey=subtitle-token", nil))
	if record.Code != http.StatusOK || record.Body.String() != "PG\x00\x01" {
		t.Fatalf("GET Stream.pgssub = %d %q", record.Code, record.Body.String())
	}
	if record.Header().Get("Content-Type") != "application/octet-stream" || record.Header().Get("Content-Length") != "4" {
		t.Errorf("headers = %v", record.Header())
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("response file Stat() error = %v, want closed after the response", err)
	}
}
