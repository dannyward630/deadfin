// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestSubtitleStreamRouteLedgerIsAuthorizedGETOnly(t *testing.T) {
	routes := subtitleRoutes(&subtitleStreamerStub{})
	want := []router.Route{
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/subtitles.m3u8", OperationID: "GetSubtitlePlaylist", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/Stream.{routeFormat}", OperationID: "GetSubtitle", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/{routeStartPositionTicks}/Stream.{routeFormat}", OperationID: "GetSubtitleWithTicks", Policy: string(auth.PolicyDefaultAuthorization)},
	}
	for _, route := range want {
		got, found := subtitleRouteByOperation(routes.Routes(), route.OperationID)
		if !found || got != route {
			t.Errorf("route %s = %#v, found %t; want %#v", route.OperationID, got, found, route)
		}
	}
	for _, path := range []string{
		"/Videos/00000000-0000-0000-0000-000000000901/source/Subtitles/1/subtitles.m3u8?SegmentLength=30",
		"/Videos/00000000-0000-0000-0000-000000000901/source/Subtitles/1/Stream.vtt",
		"/Videos/00000000-0000-0000-0000-000000000901/source/Subtitles/1/0/Stream.vtt",
	} {
		record := httptest.NewRecorder()
		routes.ServeHTTP(record, httptest.NewRequest(http.MethodPost, path, nil))
		if record.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, record.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestSubtitlePlaylistRequiresAuthorizationAndProjectsIdentity(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000902")
	identity := auth.Identity{Authenticated: true, Token: "verified-subtitle-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-000000000903")}
	streamer := &subtitleStreamerStub{playlistResponse: playback.SubtitleResponse{ContentType: "application/x-mpegURL", Body: []byte("#EXTM3U\n")}}
	path := "/VIDEOS/" + itemID.Dashed() + "/source-1/subtitles/2/SUBTITLES.M3U8?segmentlength=30"

	unauthenticated := httptest.NewRecorder()
	subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthenticated.Code != http.StatusUnauthorized || streamer.playlistCalls != 0 {
		t.Fatalf("unauthenticated playlist status/calls = %d/%d, want 401/0", unauthenticated.Code, streamer.playlistCalls)
	}

	request := httptest.NewRequest(http.MethodGet, path+"&ApiKey=untrusted-query", nil)
	request.Header.Set("X-Emby-Token", identity.Token)
	record := httptest.NewRecorder()
	subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("authorized playlist status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	if got, want := streamer.playlistRequest, (playback.SubtitlePlaylistRequest{UserID: identity.UserID, ItemID: itemID, MediaSourceID: "source-1", Index: 2, SegmentLengthSeconds: 30, APIKey: identity.Token}); got != want {
		t.Errorf("playlist request = %#v, want %#v", got, want)
	}
	if got := record.Header().Get("Content-Type"); got != "application/x-mpegURL" {
		t.Errorf("playlist Content-Type = %q, want application/x-mpegURL", got)
	}
	if got, want := record.Header().Get("Content-Length"), strconv.Itoa(len("#EXTM3U\n")); got != want {
		t.Errorf("playlist Content-Length = %q, want %q", got, want)
	}
	if got := record.Header().Get("Cache-Control"); got != "" {
		t.Errorf("playlist Cache-Control = %q, want no cache header", got)
	}
	if got := record.Header().Get("Expires"); got != "" {
		t.Errorf("playlist Expires = %q, want no cache header", got)
	}
}

func TestSubtitleStreamBindsCaseInsensitiveQueryOverridesAndTickRoute(t *testing.T) {
	routeItem := subtitleUUID(t, "00000000-0000-0000-0000-000000000904")
	queryItem := subtitleUUID(t, "00000000-0000-0000-0000-000000000905")
	identity := auth.Identity{Authenticated: true, Token: "subtitle-stream-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-000000000906")}
	streamer := &subtitleStreamerStub{streamResponse: playback.SubtitleResponse{ContentType: "text/vtt", Body: []byte("WEBVTT\n")}}
	path := "/videos/" + routeItem.Dashed() + "/route-source/subtitles/2/700/stream.vtt" +
		"?ITEMID=" + queryItem.Dashed() + "&MEDIASOURCEID=query-source&INDEX=9&FORMAT=SRT&STARTPOSITIONTICKS=800&ENDPOSITIONTICKS=900&COPYTIMESTAMPS=true&ADDVTTTIMEMAP=true"

	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Emby-Token", identity.Token)
	record := httptest.NewRecorder()
	subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET tick subtitle stream status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	want := playback.SubtitleStreamRequest{
		UserID: identity.UserID, ItemID: queryItem, MediaSourceID: "query-source", Index: 9, Format: "SRT",
		StartPositionTicks: 800, EndPositionTicks: 900, CopyTimestamps: true, AddVTTTimeMap: true,
	}
	if got := streamer.streamRequest; got != want {
		t.Errorf("stream request = %#v, want %#v", got, want)
	}
	if streamer.streamCalls != 1 {
		t.Errorf("Stream calls = %d, want 1", streamer.streamCalls)
	}
	if got := record.Header().Get("Content-Type"); got != "text/vtt" {
		t.Errorf("stream Content-Type = %q, want text/vtt", got)
	}
	if got, want := record.Header().Get("Content-Length"), strconv.Itoa(len("WEBVTT\n")); got != want {
		t.Errorf("stream Content-Length = %q, want %q", got, want)
	}
	if got := record.Header().Get("Cache-Control"); got != "" {
		t.Errorf("stream Cache-Control = %q, want no cache header", got)
	}
}

func TestSubtitleStreamPlainRouteDefaultsWindowAndWritesServiceContentType(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-00000000090b")
	identity := auth.Identity{Authenticated: true, Token: "subtitle-plain-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-00000000090c")}
	streamer := &subtitleStreamerStub{streamResponse: playback.SubtitleResponse{ContentType: "application/x-subrip", Body: []byte("1\n00:00:00,000 --> 00:00:01,000\ncaption\n")}}
	request := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/source/Subtitles/4/Stream.srt", nil)
	request.Header.Set("X-Emby-Token", identity.Token)
	record := httptest.NewRecorder()
	subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET plain subtitle stream status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	want := playback.SubtitleStreamRequest{UserID: identity.UserID, ItemID: itemID, MediaSourceID: "source", Index: 4, Format: "srt"}
	if got := streamer.streamRequest; got != want {
		t.Errorf("plain stream request = %#v, want %#v", got, want)
	}
	if got := record.Header().Get("Content-Type"); got != "application/x-subrip" {
		t.Errorf("plain stream Content-Type = %q, want application/x-subrip", got)
	}
	if got, want := record.Header().Get("Content-Length"), strconv.Itoa(record.Body.Len()); got != want {
		t.Errorf("plain stream Content-Length = %q, want %q", got, want)
	}
	if got := record.Header().Get("Cache-Control"); got != "" {
		t.Errorf("plain stream Cache-Control = %q, want no cache header", got)
	}
}

// TestSubtitleStreamAcceptsDashedItemBareHexMediaSourceAndSubripFormat proves
// the live Swiftfin shape dispatches: /Videos/{dashed-uuid}/{bare-hex}/Subtitles/
// {index}/0/Stream.subrip. The router matches /Videos/ case-insensitively;
// dto.ParseUUID accepts both dashed and bare hex IDs; the route extension
// supplies the subrip format for the service to normalize to srt. The table
// exercises the exact live shape and the uppercase route variant.
func TestSubtitleStreamAcceptsDashedItemBareHexMediaSourceAndSubripFormat(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-00000000090d")
	bareHex := itemID.String()
	identity := auth.Identity{Authenticated: true, Token: "subrip-route-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-00000000090e")}

	cases := []struct {
		name string
		path string
	}{
		{
			name: "mixed-case-live",
			path: "/Videos/" + itemID.Dashed() + "/" + bareHex + "/Subtitles/3/0/Stream.subrip",
		},
		{
			name: "lowercase-route",
			path: "/videos/" + itemID.Dashed() + "/" + bareHex + "/Subtitles/3/0/Stream.subrip",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &subtitleStreamerStub{streamResponse: playback.SubtitleResponse{ContentType: "application/octet-stream", Body: []byte("1\n00:00:00,000 --> 00:00:01,000\ncaption\n")}}
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("X-Emby-Token", identity.Token)
			record := httptest.NewRecorder()
			subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s subrip subtitle stream status = %d, want 200; body=%q", tc.name, record.Code, record.Body.String())
			}
			want := playback.SubtitleStreamRequest{UserID: identity.UserID, ItemID: itemID, MediaSourceID: bareHex, Index: 3, Format: "subrip", StartPositionTicks: 0}
			if got := streamer.streamRequest; got != want {
				t.Errorf("%s stream request = %#v, want %#v", tc.name, got, want)
			}
			if streamer.streamCalls != 1 {
				t.Errorf("%s Stream calls = %d, want 1", tc.name, streamer.streamCalls)
			}
			if got := record.Header().Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("%s stream Content-Type = %q, want application/octet-stream", tc.name, got)
			}
		})
	}
}

func TestSubtitleStreamRejectsMalformedRoutesAndInvalidPlaylistSegment(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000907")
	identity := auth.Identity{Authenticated: true, Token: "subtitle-validation-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-000000000908")}
	for _, path := range []string{
		"/Videos/not-a-uuid/source/Subtitles/1/subtitles.m3u8?SegmentLength=30",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/not-an-index/subtitles.m3u8?SegmentLength=30",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/1/subtitles.m3u8",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/1/subtitles.m3u8?SegmentLength=0",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/1/subtitles.m3u8?SegmentLength=-1",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/2147483648/subtitles.m3u8?SegmentLength=30",
		"/Videos/not-a-uuid/source/Subtitles/1/Stream.vtt",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/not-an-index/Stream.vtt",
		"/Videos/" + itemID.Dashed() + "/source/Subtitles/1/not-a-tick/Stream.vtt",
	} {
		t.Run(path, func(t *testing.T) {
			streamer := &subtitleStreamerStub{}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-Emby-Token", identity.Token)
			record := httptest.NewRecorder()
			subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
			if record.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want 400; body=%q", path, record.Code, record.Body.String())
			}
			if streamer.playlistCalls != 0 || streamer.streamCalls != 0 {
				t.Fatalf("GET %s invoked streamer playlist/stream = %d/%d, want 0/0", path, streamer.playlistCalls, streamer.streamCalls)
			}
		})
	}
}

func TestSubtitleStreamMapsPlaylistAndStreamErrorsExactly(t *testing.T) {
	itemID := subtitleUUID(t, "00000000-0000-0000-0000-000000000909")
	identity := auth.Identity{Authenticated: true, Token: "subtitle-errors-token", UserID: subtitleUUID(t, "00000000-0000-0000-0000-00000000090a")}
	for _, tc := range []struct {
		name     string
		playlist bool
		err      error
		want     int
	}{
		{name: "playlist missing item", playlist: true, err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "playlist missing source wins over wrapped item", playlist: true, err: playback.ErrMediaSourceNotFound, want: http.StatusInternalServerError},
		{name: "playlist invalid", playlist: true, err: playback.ErrSubtitleInvalid, want: http.StatusBadRequest},
		{name: "playlist converter failure", playlist: true, err: errors.New("playlist failure"), want: http.StatusInternalServerError},
		{name: "stream missing item", err: playback.ErrItemNotFound, want: http.StatusBadRequest},
		{name: "stream missing source wins over wrapped item", err: playback.ErrMediaSourceNotFound, want: http.StatusInternalServerError},
		{name: "stream missing index", err: playback.ErrSubtitleStreamNotFound, want: http.StatusInternalServerError},
		{name: "stream invalid", err: playback.ErrSubtitleInvalid, want: http.StatusBadRequest},
		{name: "stream missing external file", err: fs.ErrNotExist, want: http.StatusNotFound},
		{name: "stream stale selected source", err: sourcefile.ErrStaleSource, want: http.StatusNotFound},
		{name: "stream converter failure", err: errors.New("stream failure"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &subtitleStreamerStub{playlistErr: tc.err, streamErr: tc.err}
			path := "/Videos/" + itemID.Dashed() + "/source/Subtitles/1/Stream.vtt"
			if tc.playlist {
				path = "/Videos/" + itemID.Dashed() + "/source/Subtitles/1/subtitles.m3u8?SegmentLength=30"
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-Emby-Token", identity.Token)
			record := httptest.NewRecorder()
			subtitleAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
			if record.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", record.Code, tc.want, record.Body.String())
			}
		})
	}
}

type subtitleStreamerStub struct {
	playlistResponse playback.SubtitleResponse
	playlistErr      error
	playlistCalls    int
	playlistRequest  playback.SubtitlePlaylistRequest
	streamResponse   playback.SubtitleResponse
	streamErr        error
	streamCalls      int
	streamRequest    playback.SubtitleStreamRequest
}

func (s *subtitleStreamerStub) Playlist(_ context.Context, request playback.SubtitlePlaylistRequest) (playback.SubtitleResponse, error) {
	s.playlistCalls++
	s.playlistRequest = request
	return s.playlistResponse, s.playlistErr
}

func (s *subtitleStreamerStub) Stream(_ context.Context, request playback.SubtitleStreamRequest) (playback.SubtitleResponse, error) {
	s.streamCalls++
	s.streamRequest = request
	return s.streamResponse, s.streamErr
}

func subtitleRoutes(streamer playback.SubtitleStreamer) *router.Router {
	routes := router.New()
	handlers.RegisterSubtitleStreams(routes, streamer)
	return routes
}

func subtitleAuthenticatedRoutes(identity auth.Identity, streamer playback.SubtitleStreamer) *router.Router {
	authorizer := middleware.NewAuthorizer(subtitleTokenResolver{identity: identity}, subtitleAllowEvaluator{}, subtitleIdentityUsers{})
	routes := router.New(authorizer.Wrap)
	handlers.RegisterSubtitleStreams(routes, streamer)
	return routes
}

type subtitleTokenResolver struct{ identity auth.Identity }

func (r subtitleTokenResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	token := request.Header.Get("X-Emby-Token")
	if token == "" {
		token = bind.QueryFirst(request, "ApiKey")
	}
	if token != r.identity.Token {
		return auth.Identity{}, nil
	}
	return r.identity, nil
}

type subtitleAllowEvaluator struct{}

func (subtitleAllowEvaluator) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if !request.Identity.Authenticated {
		return auth.DecisionUnauthenticated, nil
	}
	return auth.DecisionAllowed, nil
}

type subtitleIdentityUsers struct{}

func (subtitleIdentityUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id}, nil
}

func subtitleUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	parsed, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse subtitle UUID %q: %v", value, err)
	}
	return parsed
}

func subtitleRouteByOperation(routes []router.Route, operation string) (router.Route, bool) {
	for _, route := range routes {
		if route.OperationID == operation {
			return route, true
		}
	}
	return router.Route{}, false
}

var _ playback.SubtitleStreamer = (*subtitleStreamerStub)(nil)
