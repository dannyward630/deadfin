// SPDX-License-Identifier: GPL-2.0-only

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newTestRouter(t *testing.T) (*Router, *map[string]string) {
	t.Helper()
	rt := New()
	captured := map[string]string{}
	record := func(name string, params ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			captured["route"] = name
			for _, p := range params {
				captured[p] = chi.URLParam(r, p)
			}
			w.WriteHeader(http.StatusOK)
		}
	}
	rt.Handle("GET", "/System/Info/Public", Route{OperationID: "GetPublicSystemInfo", Anonymous: true}, record("public"))
	rt.Handle("GET", "/Users/{userId}/Items/Latest", Route{OperationID: "SHADOW:GetLatestLegacy"}, record("latest", "userId"))
	rt.Handle("GET", "/Videos/{itemId}/stream.{container}", Route{OperationID: "GetVideoStreamByContainer"}, record("streamdot", "itemId", "container"))
	rt.Handle("GET", "/Videos/{itemId}/hls1/{playlistId}/{segmentId}.{container}", Route{OperationID: "SHADOW:GetHlsVideoSegment"}, record("segment", "segmentId", "container"))
	rt.Handle("GET", "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/Stream.{routeFormat}", Route{OperationID: "GetSubtitle"}, record("subtitle", "routeFormat"))
	return rt, &captured
}

func get(t *testing.T, rt *Router, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	rt.ServeHTTP(rec, req)
	return rec, path
}

func TestMidSegmentParams(t *testing.T) {
	// The reason chi was chosen: params adjacent to literals in one segment.
	rt, captured := newTestRouter(t)

	rec, _ := get(t, rt, "/Videos/abc123/stream.mkv")
	if rec.Code != 200 || (*captured)["route"] != "streamdot" {
		t.Fatalf("stream.{container}: code=%d captured=%v", rec.Code, *captured)
	}
	if (*captured)["itemId"] != "abc123" || (*captured)["container"] != "mkv" {
		t.Errorf("params: %v", *captured)
	}

	rec, _ = get(t, rt, "/Videos/abc/hls1/pl/42.ts")
	if rec.Code != 200 || (*captured)["segmentId"] != "42" || (*captured)["container"] != "ts" {
		t.Fatalf("{segmentId}.{container}: code=%d captured=%v", rec.Code, *captured)
	}
}

func TestCaseInsensitiveLiteralSegments(t *testing.T) {
	rt, captured := newTestRouter(t)

	for _, path := range []string{
		"/System/Info/Public",
		"/system/info/public",
		"/SYSTEM/INFO/PUBLIC",
		"/sYsTeM/iNfO/pUbLiC",
	} {
		rec, p := get(t, rt, path)
		if rec.Code != 200 || (*captured)["route"] != "public" {
			t.Errorf("%s: code=%d route=%q", p, rec.Code, (*captured)["route"])
		}
	}
}

func TestParamCasingPreserved(t *testing.T) {
	rt, captured := newTestRouter(t)

	// Literal segments normalize; the param value keeps its exact casing.
	rec, _ := get(t, rt, "/users/AbCdEf123456/items/latest")
	if rec.Code != 200 || (*captured)["route"] != "latest" {
		t.Fatalf("legacy latest: code=%d captured=%v", rec.Code, *captured)
	}
	if (*captured)["userId"] != "AbCdEf123456" {
		t.Errorf("param must keep casing, got %q", (*captured)["userId"])
	}
}

func TestMixedSegmentLiteralPrefixNormalized(t *testing.T) {
	rt, captured := newTestRouter(t)

	// "stream.{container}" requested as "Stream.MKV": literal prefix folds to
	// the registered casing, the param part keeps its casing.
	rec, _ := get(t, rt, "/videos/abc/Stream.MKV")
	if rec.Code != 200 || (*captured)["route"] != "streamdot" {
		t.Fatalf("mixed-case stream: code=%d captured=%v", rec.Code, *captured)
	}
	if (*captured)["container"] != "MKV" {
		t.Errorf("container param must keep casing, got %q", (*captured)["container"])
	}

	// Upstream registers "Subtitles/.../Stream.{routeFormat}" with capital S;
	// clients request lowercase "stream.vtt" and must still match.
	rec, _ = get(t, rt, "/Videos/i/ms/subtitles/3/stream.vtt")
	if rec.Code != 200 || (*captured)["route"] != "subtitle" || (*captured)["routeFormat"] != "vtt" {
		t.Fatalf("subtitle stream: code=%d captured=%v", rec.Code, *captured)
	}
}

func TestUnknownPathsPassThrough(t *testing.T) {
	rt, _ := newTestRouter(t)
	rec, _ := get(t, rt, "/definitely/not/registered")
	if rec.Code != 404 {
		t.Errorf("unknown path: %d", rec.Code)
	}
}

func TestRouteTableExport(t *testing.T) {
	rt, _ := newTestRouter(t)
	routes := rt.Routes()
	if len(routes) != 5 {
		t.Fatalf("routes: %d", len(routes))
	}
	if routes[0].OperationID != "GetPublicSystemInfo" || !routes[0].Anonymous {
		t.Errorf("route meta: %+v", routes[0])
	}
}
