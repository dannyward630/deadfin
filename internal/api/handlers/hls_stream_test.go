// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestHLSStreamRouteLedger pins the hidden controller surface intentionally
// absent from the stable OpenAPI. DynamicHlsController is class-authorized;
// only the two legacy segment families retain their compatibility anonymity.
func TestHLSStreamRouteLedger(t *testing.T) {
	routes := hlsRoutes(&hlsStreamerStub{})
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
	} {
		got, ok := hlsRouteByOperation(routes.Routes(), want.OperationID)
		if !ok {
			t.Errorf("missing HLS route %s", want.OperationID)
			continue
		}
		if got != want {
			t.Errorf("HLS route %s = %#v, want %#v", want.OperationID, got, want)
		}
	}
}

// TestHLSMasterProjectsExplicitTransportAndStreamFacts proves the handler, not
// the streamer, binds the complete externally supplied stream projection.
func TestHLSMasterProjectsExplicitTransportAndStreamFacts(t *testing.T) {
	body := &hlsReadCloser{Reader: strings.NewReader("#EXTM3U\n")}
	streamer := &hlsStreamerStub{masterResponse: playback.HLSResponse{Body: body, ContentType: "application/x-mpegURL"}}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a0")
	query := hlsFullQuery("source-query", "device-query", "session-query")

	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+query, "Jellyfin Web/10.10")
	if record.Code != http.StatusOK {
		t.Fatalf("GET HLS master status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got := record.Header().Get("Content-Type"); got != "application/x-mpegURL" {
		t.Errorf("master Content-Type = %q, want playlist MIME", got)
	}
	if got := record.Body.String(); got != "#EXTM3U\n" {
		t.Errorf("master body = %q, want streamed playlist", got)
	}
	if body.closeCalls != 1 {
		t.Errorf("master body Close calls = %d, want 1", body.closeCalls)
	}
	if streamer.masterCalls != 1 {
		t.Fatalf("Master calls = %d, want 1", streamer.masterCalls)
	}
	assertHLSProjection(t, streamer.masterRequest, itemID, query, "source-query", "device-query", "session-query")
}

// TestHLSMasterAcceptsLowercaseRouteAndBareHexMediaSourceID proves the live
// Swiftfin HLS shape: lowercase /videos/{dashed-uuid}/master.m3u8 with a bare
// hex (N format) MediaSourceId query value. The router normalizes /videos/
// case-insensitively; the MediaSourceId value passes through as an opaque
// string for source selection. The table exercises the lowercase live shape
// and the uppercase stock shape.
func TestHLSMasterAcceptsLowercaseRouteAndBareHexMediaSourceID(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b0")
	bareHex := itemID.String()
	query := "MediaSourceId=" + bareHex + "&SegmentContainer=ts&SegmentLength=6&PlaySessionId=session"

	cases := []struct {
		name string
		path string
	}{
		{
			name: "lowercase-live",
			path: "/videos/" + itemID.Dashed() + "/master.m3u8?" + query,
		},
		{
			name: "uppercase-stock",
			path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + query,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &hlsReadCloser{Reader: strings.NewReader("#EXTM3U\n")}
			streamer := &hlsStreamerStub{masterResponse: playback.HLSResponse{Body: body, ContentType: "application/x-mpegURL"}}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path, "Swiftfin-tvOS/1.6")
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s HLS master status = %d, want %d; body=%q", tc.name, record.Code, http.StatusOK, record.Body.String())
			}
			if streamer.masterCalls != 1 {
				t.Fatalf("Master calls = %d, want 1", streamer.masterCalls)
			}
			if streamer.masterRequest.ItemID != itemID {
				t.Errorf("master ItemID = %v, want %v", streamer.masterRequest.ItemID, itemID)
			}
			if streamer.masterRequest.MediaSourceID != bareHex {
				t.Errorf("master MediaSourceID = %q, want bare-hex %q", streamer.masterRequest.MediaSourceID, bareHex)
			}
		})
	}
}

// TestHLSParamsPackedValuesOverrideExplicitQuery ensures compatibility params
// are decoded before the HLS service sees the request. Blank slots preserving
// explicit values is covered by the pure decoder; this route test protects the
// boundary where packed values must actually be applied.
func TestHLSParamsPackedValuesOverrideExplicitQuery(t *testing.T) {
	streamer := &hlsStreamerStub{mainResponse: playback.HLSResponse{Body: io.NopCloser(strings.NewReader("#EXTM3U\n")), ContentType: "application/x-mpegURL"}}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a1")
	slots := make([]string, 34)
	slots[1], slots[2], slots[4], slots[5] = "device-packed", "source-packed", "hevc", "opus"
	slots[6], slots[7], slots[8], slots[9], slots[10] = "6", "7", "8000000", "192000", "8"
	slots[11], slots[12], slots[13], slots[14], slots[15] = "23.976", "1920", "1080", "900000000", "4.1"
	slots[16], slots[17], slots[18], slots[20], slots[22] = "4", "10", "high", "session-packed", "live-packed"
	slots[24], slots[25], slots[26], slots[27], slots[28], slots[29], slots[30], slots[31], slots[32], slots[33] = "true", "Embed", "6", "true", "tag-packed", "true", "webvtt", "true", "true", "reason-packed"
	values := url.Values{
		"MediaSourceId": {"source-explicit"}, "DeviceId": {"device-explicit"}, "PlaySessionId": {"session-explicit"},
		"VideoCodec": {"h264"}, "AudioCodec": {"aac"}, "AudioStreamIndex": {"1"}, "SubtitleStreamIndex": {"2"}, "VideoBitRate": {"1"}, "AudioBitRate": {"2"},
		"SegmentContainer": {"ts"}, "SegmentLength": {"6"}, "Params": {strings.Join(slots, ";")},
		"h264-level": {"4.2"},
	}
	query := values.Encode()
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/main.m3u8?"+query, "packed-client")
	if record.Code != http.StatusOK {
		t.Fatalf("GET HLS main packed params status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if streamer.mainCalls != 1 {
		t.Fatalf("Main calls = %d, want 1", streamer.mainCalls)
	}
	r := streamer.mainRequest
	if r.MediaSourceID != "source-packed" || r.DeviceID != "device-packed" || r.PlaySessionID != "session-packed" || r.LiveStreamID != "live-packed" || r.Tag != "tag-packed" || r.SubtitleCodec != "webvtt" || r.TranscodeReasons != "reason-packed" || !r.EnableSubtitlesInManifest || r.VideoCodec != "hevc" || r.AudioCodec != "opus" || r.StartTimeTicks != 900000000 || r.UserAgent != "packed-client" {
		t.Errorf("packed request identity/codecs/ticks = %#v, want packed values and transport agent", r)
	}
	assertHLSInt(t, "VideoStreamIndex", r.VideoStreamIndex, 6)
	assertHLSInt(t, "AudioStreamIndex", r.AudioStreamIndex, 6)
	assertHLSInt(t, "SubtitleStreamIndex", r.SubtitleStreamIndex, 7)
	assertHLSInt(t, "VideoBitrate", r.VideoBitrate, 8_000_000)
	assertHLSInt(t, "AudioBitrate", r.AudioBitrate, 192_000)
	assertHLSInt(t, "MaxAudioChannels", r.MaxAudioChannels, 8)
	assertHLSInt(t, "MaxWidth", r.MaxWidth, 1920)
	assertHLSInt(t, "MaxHeight", r.MaxHeight, 1080)
	assertHLSInt(t, "MaxRefFrames", r.MaxRefFrames, 4)
	assertHLSInt(t, "MaxVideoBitDepth", r.MaxVideoBitDepth, 10)
	assertHLSInt(t, "TranscodingMaxAudioChannels", r.TranscodingMaxAudioChannels, 6)
	if r.MaxFramerate == nil || *r.MaxFramerate != 23.976 || r.Profile == nil || *r.Profile != "high" || r.Level == nil || *r.Level != "4.1" || r.SubtitleDeliveryMethod == nil || *r.SubtitleDeliveryMethod != playback.SubtitleEmbed {
		t.Errorf("packed optional projection = %#v, want packed full StreamParams projection", r)
	}
	got := r.StreamOptions.Get("h264-level")
	if !r.CopyTimestamps || !r.RequireAvc || !r.RequireNonAnamorphic || !r.DeInterlace || got != "4.2" {
		t.Errorf("packed flags/options = copy:%t avc:%t nonAnamorphic:%t deinterlace:%t h264-level:%q", r.CopyTimestamps, r.RequireAvc, r.RequireNonAnamorphic, r.DeInterlace, got)
	}
	if r.RawQuery != query {
		t.Errorf("RawQuery = %q, want byte-preserved %q", r.RawQuery, query)
	}
}

func TestHLSMasterControllerDefaultsProjectExternalSubtitlesAndVideoTrickplay(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a8")
	for _, tc := range []struct {
		name, suffix  string
		wantTrickplay bool
	}{
		{name: "video master enables trickplay", wantTrickplay: true},
		{name: "explicit false disables trickplay", suffix: "&EnableTrickplay=false", wantTrickplay: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery()+tc.suffix, "")
			if record.Code != http.StatusOK {
				t.Fatalf("GET video HLS master status = %d, want 200; body=%q", record.Code, record.Body.String())
			}
			if streamer.masterRequest.SubtitleDeliveryMethod == nil || *streamer.masterRequest.SubtitleDeliveryMethod != playback.SubtitleExternal {
				t.Errorf("omitted SubtitleMethod = %v, want External", streamer.masterRequest.SubtitleDeliveryMethod)
			}
			if streamer.masterRequest.EnableTrickplay != tc.wantTrickplay {
				t.Errorf("EnableTrickplay = %t, want %t", streamer.masterRequest.EnableTrickplay, tc.wantTrickplay)
			}
		})
	}
}

func TestHLSRoutesProjectSubtitleManifestByControllerDefaultAndOverride(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007ab")
	for _, endpoint := range []struct {
		name, path, query string
		defaultValue      bool
		request           func(*hlsStreamerStub) playback.HLSRequest
	}{
		{name: "video master", path: "/Videos/" + itemID.Dashed() + "/master.m3u8", query: hlsMasterQuery(), request: func(s *hlsStreamerStub) playback.HLSRequest { return s.masterRequest }},
		{name: "video live", path: "/Videos/" + itemID.Dashed() + "/live.m3u8", query: hlsRequiredQuery(), defaultValue: true, request: func(s *hlsStreamerStub) playback.HLSRequest { return s.liveRequest }},
		{name: "video main", path: "/Videos/" + itemID.Dashed() + "/main.m3u8", query: hlsRequiredQuery(), request: func(s *hlsStreamerStub) playback.HLSRequest { return s.mainRequest }},
		{name: "video segment", path: "/Videos/" + itemID.Dashed() + "/hls1/opaque/0.ts", query: hlsSegmentQuery(), request: func(s *hlsStreamerStub) playback.HLSRequest { return s.segmentRequest }},
	} {
		for _, setting := range []struct {
			name, explicit, packed string
			want                   func(bool) bool
		}{
			{name: "omitted", want: func(defaultValue bool) bool { return defaultValue }},
			{name: "explicit true", explicit: "true", want: func(bool) bool { return true }},
			{name: "explicit false", explicit: "false", want: func(bool) bool { return false }},
			{name: "packed true", packed: "true", want: func(bool) bool { return true }},
			{name: "packed false", packed: "false", want: func(bool) bool { return false }},
		} {
			t.Run(endpoint.name+"/"+setting.name, func(t *testing.T) {
				query := endpoint.query
				if setting.explicit != "" {
					query += "&EnableSubtitlesInManifest=" + setting.explicit
				}
				if setting.packed != "" {
					slots := make([]string, 28)
					slots[27] = setting.packed
					query += "&Params=" + strings.Join(slots, ";")
				}
				streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), liveResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), mainResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), segmentResponse: hlsResponse("video/mp2t", "segment")}
				record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, endpoint.path+"?"+query, "")
				if record.Code != http.StatusOK {
					t.Fatalf("GET HLS %s %s status = %d, want 200; body=%q", endpoint.name, setting.name, record.Code, record.Body.String())
				}
				if got, want := endpoint.request(streamer).EnableSubtitlesInManifest, setting.want(endpoint.defaultValue); got != want {
					t.Errorf("%s %s EnableSubtitlesInManifest = %t, want %t", endpoint.name, setting.name, got, want)
				}
			})
		}
	}
}

func TestHLSRawSemicolonParamsAreRecoveredBeforeBinding(t *testing.T) {
	streamer := &hlsStreamerStub{mainResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a9")
	packed := ";device-from-raw;source-from-raw"
	query := hlsRequiredQuery() + "&Params=" + packed
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/main.m3u8?"+query, "")
	if record.Code != http.StatusOK {
		t.Fatalf("GET HLS raw Params status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	if streamer.mainCalls != 1 || streamer.mainRequest.DeviceID != "device-from-raw" || streamer.mainRequest.MediaSourceID != "source-from-raw" || streamer.mainRequest.RawQuery != query {
		t.Errorf("raw semicolon Params projection = calls:%d request:%#v, want packed device/source and exact raw query", streamer.mainCalls, streamer.mainRequest)
	}
}

func TestHLSLowercaseQueryOptionsRetainOriginalKeysAndJoinValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?hevc-rangetype=hdr&hevc-rangetype=sdr&hevc-profile=main&hevc-level=5.1&av1-profile=main&av1-level=4.0&aac-profile=lc&custom-option=fast&Uppercase-option=exclude&HEVC-rangetype=exclude", nil)
	got := make(map[string]string)
	for _, option := range bind.LowercaseQueryOptions(request) {
		got[option.Name] = option.Value
	}
	want := map[string]string{
		"hevc-rangetype": "hdr,sdr", "hevc-profile": "main", "hevc-level": "5.1", "av1-profile": "main", "av1-level": "4.0", "aac-profile": "lc", "custom-option": "fast",
	}
	if len(got) != len(want) {
		t.Fatalf("LowercaseQueryOptions = %#v, want exactly %#v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("LowercaseQueryOptions[%q] = %q, want %q", name, got[name], value)
		}
	}
}

func TestHLSStreamOptionsPreserveLowercaseKeysAndCommaJoinRepeatedValues(t *testing.T) {
	streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b1")
	query := hlsMasterQuery() + "&h264-level=4.2&hevc-rangetype=hdr&hevc-rangetype=sdr&hevc-profile=main&hevc-level=5.1&av1-profile=main&av1-level=4.0&aac-profile=lc&custom-option=fast&Uppercase-option=exclude&HEVC-rangetype=exclude"
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+query, "")
	if record.Code != http.StatusOK {
		t.Fatalf("GET HLS master stream options status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	for key, want := range map[string]string{
		"h264-level": "4.2", "hevc-rangetype": "hdr,sdr", "hevc-profile": "main", "hevc-level": "5.1", "av1-profile": "main", "av1-level": "4.0", "aac-profile": "lc", "custom-option": "fast",
	} {
		if got := streamer.masterRequest.StreamOptions.Get(key); got != want {
			t.Errorf("StreamOptions[%q] = %q, want %q", key, got, want)
		}
	}
	encoded, err := json.Marshal(streamer.masterRequest.StreamOptions)
	if err != nil {
		t.Fatalf("marshal projected stream options: %v", err)
	}
	for _, key := range []string{"Uppercase-option", "HEVC-rangetype"} {
		if strings.Contains(string(encoded), "\""+key+"\"") {
			t.Errorf("StreamOptions JSON unexpectedly retains uppercase-leading key %q: %s", key, encoded)
		}
	}
}

// TestHLSLowercaseQueryOptionsFoldCaseAliasesInWireOrder ensures the HLS
// request preserves the source query's encounter order before it folds option
// aliases case-insensitively. In particular, a later casing variant contributes
// another value to the first lowercase spelling rather than becoming a distinct
// option or depending on url.Values map iteration.
func TestHLSLowercaseQueryOptionsFoldCaseAliasesInWireOrder(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b6")
	query := hlsMasterQuery() + "&hevc-level=153&interleaved-option=first&hevc-Level=120&HEVC-level=exclude&interleaved-option=second&hevc-level=90&Uppercase-first=exclude"
	const wantJSON = `{"hevc-level":"153,120,90","interleaved-option":"first,second"}`

	for run := 0; run < 20; run++ {
		streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
		record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+query, "")
		if record.Code != http.StatusOK {
			t.Fatalf("run %d GET HLS master status = %d, want 200; body=%q", run, record.Code, record.Body.String())
		}
		encoded, err := json.Marshal(streamer.masterRequest.StreamOptions)
		if err != nil {
			t.Fatalf("run %d marshal stream options: %v", run, err)
		}
		if got := string(encoded); got != wantJSON {
			t.Errorf("run %d stream options = %s, want source-order folded %s", run, got, wantJSON)
		}
		if got := streamer.masterRequest.StreamOptions.Get("hevc-level"); got != "153,120,90" {
			t.Errorf("run %d StreamOptions[hevc-level] = %q, want all aliases in wire order", run, got)
		}
	}
}

func TestHLSVerifiedIdentityTokenOverridesQueryAndAnonymousQueryRemainsMetadata(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b2")
	identity := auth.Identity{Authenticated: true, Token: "verified-token", UserID: hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b3")}
	streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	request := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery()+"&api_key=stale-query&apiKey=stale-legacy", nil)
	request.Header.Set("X-Emby-Token", "verified-token")
	record := httptest.NewRecorder()
	hlsAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET authenticated HLS master status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	if streamer.masterRequest.APIKey != "verified-token" || streamer.masterRequest.UserID != identity.UserID {
		t.Errorf("authenticated HLS token projection = APIKey:%q UserID:%s, want verified token and identity user", streamer.masterRequest.APIKey, streamer.masterRequest.UserID)
	}

	anonymous := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	anonymousRecord := hlsRequest(t, hlsRoutes(anonymous), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery()+"&api_key=anonymous-query", "")
	if anonymousRecord.Code != http.StatusOK || anonymous.masterRequest.APIKey != "anonymous-query" {
		t.Errorf("anonymous HLS query token = status:%d APIKey:%q, want 200/anonymous-query; body=%q", anonymousRecord.Code, anonymous.masterRequest.APIKey, anonymousRecord.Body.String())
	}
}

// TestHLSAuthenticatedQueryCanonicalization ensures a verified header
// credential becomes the only generated-URI credential, without disturbing an
// anonymous caller's original query bytes.
func TestHLSAuthenticatedQueryCanonicalization(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007d0")
	identity := auth.Identity{Authenticated: true, Token: "resolved+token&exact=✓", UserID: hlsTestUUID(t, "00000000-0000-0000-0000-0000000007d1")}
	baseQuery := hlsMasterQuery() + "&preserved=%2Fraw%2F"

	for _, tc := range []struct {
		name, query, wantRaw string
		authenticated        bool
	}{
		{
			name:          "authenticated header only adds resolved canonical token",
			query:         baseQuery,
			wantRaw:       baseQuery + "&ApiKey=resolved%2Btoken%26exact%3D%E2%9C%93",
			authenticated: true,
		},
		{
			name:          "authenticated query aliases are replaced",
			query:         baseQuery + "&ApiKey=stale-canonical&api_key=stale-legacy&apiKey=stale-camel&%41PI%4bEY=stale-encoded-case",
			wantRaw:       baseQuery + "&ApiKey=resolved%2Btoken%26exact%3D%E2%9C%93",
			authenticated: true,
		},
		{
			name:    "anonymous query token remains exact",
			query:   baseQuery + "&api_key=anonymous%2Btoken%26exact%3D%E2%9C%93",
			wantRaw: baseQuery + "&api_key=anonymous%2Btoken%26exact%3D%E2%9C%93",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
			request := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+tc.query, nil)
			if tc.authenticated {
				request.Header.Set("X-Emby-Token", identity.Token)
			}
			record := httptest.NewRecorder()
			if tc.authenticated {
				hlsAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
			} else {
				hlsRoutes(streamer).ServeHTTP(record, request)
			}
			if record.Code != http.StatusOK {
				t.Fatalf("GET HLS master status = %d, want 200; body=%q", record.Code, record.Body.String())
			}
			if got := streamer.masterRequest.RawQuery; got != tc.wantRaw {
				t.Fatalf("HLS RawQuery = %q, want canonical query %q", got, tc.wantRaw)
			}
		})
	}
}

// TestHLSAuthenticatedGeneratedURIsRetainResolvedToken crosses the actual HLS
// service and handlers: a header-only master must produce query-authenticated
// main and segment URLs after the header is no longer available to the player.
func TestHLSAuthenticatedGeneratedURIsRetainResolvedToken(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007d2")
	identity := auth.Identity{Authenticated: true, Token: "resolved+token&exact=✓", UserID: hlsTestUUID(t, "00000000-0000-0000-0000-0000000007d3")}
	path := "/media/auth-query.mp4"
	runtimeTicks := int64(600_000_000)
	videoBitrate, audioBitrate := 2_600_000, 384_000
	width, height, refFrames, videoBitDepth := 1280, 720, 4, 8
	videoProfile, videoLevel, framerate := "High", float64(41), float32(30)
	audioChannels, sampleRate, audioBitDepth := 6, 48_000, 16
	source := hlsAuthQuerySource{source: hlsCanonicalTestSource(playback.MediaSourceInfo{
		ID: itemID.Dashed(), Protocol: playback.MediaProtocolFile, Path: path, Container: "mov,mp4,m4a,3gp,3g2,mj2", RunTimeTicks: &runtimeTicks,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Profile: &videoProfile, Level: &videoLevel, RefFrames: &refFrames, BitDepth: &videoBitDepth, BitRate: &videoBitrate, Width: &width, Height: &height, AverageFrameRate: &framerate},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "ac3", BitRate: &audioBitrate, Channels: &audioChannels, SampleRate: &sampleRate, BitDepth: &audioBitDepth, IsDefault: true},
		},
	})}
	streamer := playback.NewHLSService(playback.HLSServiceConfig{
		Sources:      source,
		TranscodeDir: "/cache",
		Capabilities: encoding.Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}},
		Manager:      &codecBurnHLSManager{},
	})
	routes := hlsAuthenticatedRoutes(identity, streamer)
	masterQuery := "MediaSourceId=" + itemID.Dashed() + "&SegmentContainer=ts&SegmentLength=6&PlaySessionId=session&VideoCodec=h264&AudioCodec=aac"
	master := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+masterQuery, nil)
	master.Header.Set("X-Emby-Token", identity.Token)
	masterRecord := httptest.NewRecorder()
	routes.ServeHTTP(masterRecord, master)
	if masterRecord.Code != http.StatusOK {
		t.Fatalf("GET authenticated master = %d, want 200; body=%q", masterRecord.Code, masterRecord.Body.String())
	}
	mainURI := hlsPlaylistURI(t, masterRecord.Body.String(), "main.m3u8?")
	assertHLSOnlyResolvedAPIKey(t, mainURI, identity.Token)

	main := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/"+mainURI, nil)
	mainRecord := httptest.NewRecorder()
	routes.ServeHTTP(mainRecord, main)
	if mainRecord.Code != http.StatusOK {
		t.Fatalf("GET generated main %q = %d, want 200; body=%q", mainURI, mainRecord.Code, mainRecord.Body.String())
	}
	segmentURI := hlsPlaylistURI(t, mainRecord.Body.String(), "hls1/main/0.ts?")
	assertHLSOnlyResolvedAPIKey(t, segmentURI, identity.Token)
}

func hlsPlaylistURI(t *testing.T, playlist, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(playlist, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("playlist %q has no URI with prefix %q", playlist, prefix)
	return ""
}

func assertHLSOnlyResolvedAPIKey(t *testing.T, uri, wantToken string) {
	t.Helper()
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse generated HLS URI %q: %v", uri, err)
	}
	keys := parsed.Query()
	if got := keys["ApiKey"]; len(got) != 1 || got[0] != wantToken {
		t.Fatalf("generated HLS URI %q ApiKey = %#v, want exactly resolved token %q", uri, got, wantToken)
	}
	for name := range keys {
		if strings.EqualFold(strings.ReplaceAll(name, "_", ""), "apikey") && name != "ApiKey" {
			t.Fatalf("generated HLS URI %q retains noncanonical API key alias %q", uri, name)
		}
	}
}

// TestHLSDynamicEndpointsDispatchAndNormalizeSegmentFacts catches route drift
// across every dynamic action, including case-insensitive literal matching and
// fMP4's initialization segment index -1.
func TestHLSDynamicEndpointsDispatchAndNormalizeSegmentFacts(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a2")
	for _, tc := range []struct {
		name, method, path, call, contentType string
		segmentIndex                          int
		live                                  bool
	}{
		{name: "live video", method: http.MethodGet, path: "/vIdEoS/" + itemID.Dashed() + "/LiVe.m3u8?" + hlsRequiredQuery(), call: "live", contentType: "application/x-mpegURL", live: true},
		{name: "video main", method: http.MethodGet, path: "/Videos/" + itemID.Dashed() + "/main.m3u8?" + hlsRequiredQuery(), call: "main", contentType: "application/x-mpegURL"},
		{name: "audio main", method: http.MethodGet, path: "/Audio/" + itemID.Dashed() + "/main.m3u8?" + hlsRequiredQuery(), call: "main", contentType: "application/x-mpegURL"},
		{name: "video TS segment", method: http.MethodGet, path: "/Videos/" + itemID.Dashed() + "/hls1/opaque/3.TS?" + hlsSegmentQuery(), call: "segment", contentType: "video/mp2t", segmentIndex: 3},
		{name: "audio MP4 init segment", method: http.MethodGet, path: "/Audio/" + itemID.Dashed() + "/hls1/opaque/-1.MP4?" + hlsSegmentQuery(), call: "segment", contentType: "video/mp4", segmentIndex: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{liveResponse: hlsResponse(tc.contentType, tc.call), mainResponse: hlsResponse(tc.contentType, tc.call), segmentResponse: hlsResponse(tc.contentType, tc.call)}
			record := hlsRequest(t, hlsRoutes(streamer), tc.method, tc.path, "dynamic-client")
			if record.Code != http.StatusOK || record.Body.String() != tc.call {
				t.Fatalf("%s response = %d %q, want %d %q", tc.name, record.Code, record.Body.String(), http.StatusOK, tc.call)
			}
			if got := record.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("%s Content-Type = %q, want %q", tc.name, got, tc.contentType)
			}
			switch tc.call {
			case "live":
				if streamer.liveCalls != 1 || !streamer.liveRequest.Live {
					t.Errorf("Live calls/request = %d/%#v, want live request", streamer.liveCalls, streamer.liveRequest)
				}
			case "main":
				if streamer.mainCalls != 1 {
					t.Errorf("Main calls = %d, want 1", streamer.mainCalls)
				}
			case "segment":
				if streamer.segmentCalls != 1 || streamer.segmentRequest.SegmentIndex != tc.segmentIndex {
					t.Errorf("Segment calls/index = %d/%d, want 1/%d", streamer.segmentCalls, streamer.segmentRequest.SegmentIndex, tc.segmentIndex)
				}
				wantContainer := "ts"
				if tc.segmentIndex == -1 {
					wantContainer = "fmp4"
				}
				if streamer.segmentRequest.SegmentContainer != wantContainer || streamer.segmentRequest.RuntimeTicks != 100 || streamer.segmentRequest.ActualSegmentLengthTicks != 50 {
					t.Errorf("segment normalized facts = %#v, want container:%q runtime:100 length:50", streamer.segmentRequest, wantContainer)
				}
			}
		})
	}
}

func TestHLSMainGeneratedMP4InitAndSegmentURLsDispatch(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007ae")
	segmentQuery := strings.Replace(hlsSegmentQuery(), "SegmentContainer=ts", "SegmentContainer=fmp4", 1)
	mainQuery := strings.Replace(hlsRequiredQuery(), "SegmentContainer=ts", "SegmentContainer=fmp4", 1)
	generated := "#EXTM3U\n#EXT-X-MAP:URI=\"hls1/opaque/-1.mp4?" + segmentQuery + "\"\nhls1/opaque/0.mp4?" + segmentQuery + "\n"
	main := &hlsStreamerStub{mainResponse: hlsResponse("application/x-mpegURL", generated)}
	mainRecord := hlsRequest(t, hlsRoutes(main), http.MethodGet, "/Videos/"+itemID.Dashed()+"/main.m3u8?"+mainQuery, "")
	if mainRecord.Code != http.StatusOK || mainRecord.Body.String() != generated || main.mainRequest.SegmentContainer != "fmp4" {
		t.Fatalf("GET HLS main generated MP4 playlist = status:%d body:%q request:%#v, want 200, exact playlist, normalized fmp4", mainRecord.Code, mainRecord.Body.String(), main.mainRequest)
	}
	for _, tc := range []struct {
		name  string
		index int
	}{
		{name: "initialization", index: -1},
		{name: "first media segment", index: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{segmentResponse: hlsResponse("video/mp4", tc.name)}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/hls1/opaque/"+strconv.Itoa(tc.index)+".mp4?"+segmentQuery, "")
			if record.Code != http.StatusOK || record.Header().Get("Content-Type") != "video/mp4" || record.Body.String() != tc.name {
				t.Fatalf("GET generated MP4 %s = status:%d type:%q body:%q, want 200 video/mp4 %q", tc.name, record.Code, record.Header().Get("Content-Type"), record.Body.String(), tc.name)
			}
			if streamer.segmentCalls != 1 || streamer.segmentRequest.SegmentIndex != tc.index || streamer.segmentRequest.SegmentContainer != "fmp4" || streamer.segmentRequest.Container != "fmp4" {
				t.Errorf("generated MP4 %s request = %#v, want index %d and fmp4 service container", tc.name, streamer.segmentRequest, tc.index)
			}
		})
	}
}

func TestHLSRoutesDeferOmittedSegmentConfigurationToService(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007af")
	for _, tc := range []struct {
		name, path, query, wantSource, wantContainer string
		wantMin                                      int
		request                                      func(*hlsStreamerStub) playback.HLSRequest
	}{
		{name: "video master source only", path: "/Videos/" + itemID.Dashed() + "/master.m3u8", query: "MediaSourceId=source", wantSource: "source", wantMin: 3, request: func(s *hlsStreamerStub) playback.HLSRequest { return s.masterRequest }},
		{name: "video main no query", path: "/Videos/" + itemID.Dashed() + "/main.m3u8", wantMin: 3, request: func(s *hlsStreamerStub) playback.HLSRequest { return s.mainRequest }},
		{name: "video TS segment timing only", path: "/Videos/" + itemID.Dashed() + "/hls1/opaque/0.ts", query: "RuntimeTicks=0&ActualSegmentLengthTicks=1", wantContainer: "ts", wantMin: 3, request: func(s *hlsStreamerStub) playback.HLSRequest { return s.segmentRequest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "master"), mainResponse: hlsResponse("application/x-mpegURL", "main"), segmentResponse: hlsResponse("video/mp2t", "segment")}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path+"?"+tc.query, "")
			if record.Code != http.StatusOK {
				t.Fatalf("GET HLS %s status = %d, want 200; body=%q", tc.name, record.Code, record.Body.String())
			}
			request := tc.request(streamer)
			if request.MediaSourceID != tc.wantSource || request.SegmentContainer != tc.wantContainer || request.SegmentLengthSeconds != 0 || request.MinSegments != tc.wantMin {
				t.Errorf("%s deferred segment configuration = source:%q container:%q length:%d min:%d, want source:%q container:%q length:0 min:%d", tc.name, request.MediaSourceID, request.SegmentContainer, request.SegmentLengthSeconds, request.MinSegments, tc.wantSource, tc.wantContainer, tc.wantMin)
			}
		})
	}
}

func TestHLSMinSegmentsPreservesExplicitZeroAndLongSegmentDefault(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b0")
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{name: "long segment defaults to two", query: "MediaSourceId=source&SegmentLength=10", want: 2},
		{name: "explicit zero remains zero", query: "MediaSourceId=source&SegmentLength=6&MinSegments=0", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "master")}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+tc.query, "")
			if record.Code != http.StatusOK || streamer.masterRequest.MinSegments != tc.want {
				t.Errorf("GET HLS master %s = status:%d MinSegments:%d, want 200/%d; body=%q", tc.name, record.Code, streamer.masterRequest.MinSegments, tc.want, record.Body.String())
			}
		})
	}
}

// TestHLSMasterHEADSuppressesBody exercises the public HEAD operation: it
// reaches the master service with the head fact but sends no playlist bytes.
func TestHLSMasterHEADSuppressesBody(t *testing.T) {
	streamer := &hlsStreamerStub{masterResponse: playback.HLSResponse{ContentType: "application/x-mpegURL"}}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a3")
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodHead, "/Audio/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery(), "head-client")
	if record.Code != http.StatusOK || record.Body.Len() != 0 {
		t.Fatalf("HEAD HLS master = %d body:%q, want 200 with no body", record.Code, record.Body.String())
	}
	if streamer.masterCalls != 1 || !streamer.masterRequest.Head || streamer.masterRequest.UserAgent != "head-client" {
		t.Errorf("HEAD master request = calls:%d %#v, want one head request with agent", streamer.masterCalls, streamer.masterRequest)
	}
}

// TestHLSSegmentStreamsWithoutBuffering proves incremental delivery and close
// ownership at the HTTP boundary; an ffmpeg segment must not be accumulated.
func TestHLSSegmentStreamsWithoutBuffering(t *testing.T) {
	body := newGatedHLSReadCloser("first-", "last")
	streamer := &hlsStreamerStub{segmentResponse: playback.HLSResponse{Body: body, ContentType: "video/mp2t"}}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a4")
	writer := newHLSStreamingResponseWriter()
	done := make(chan struct{})
	go func() {
		hlsRoutes(streamer).ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/hls1/opaque/0.ts?"+hlsSegmentQuery(), nil))
		close(done)
	}()
	select {
	case <-writer.firstWrite:
	case <-done:
		t.Fatal("HLS segment completed before growing body was released")
	}
	body.release()
	<-done
	if got := writer.body.String(); got != "first-last" {
		t.Errorf("streamed segment = %q, want ordered growing bytes", got)
	}
	if body.closeCalls != 1 {
		t.Errorf("segment body Close calls = %d, want 1", body.closeCalls)
	}
}

// TestHLSOutOfOrderKeyframeSegmentURLDispatchesNegativeActualLength protects
// generated HLS URLs where an out-of-order keyframe has a negative duration.
// The route must preserve that signed tick value for the playback service.
func TestHLSOutOfOrderKeyframeSegmentURLDispatchesNegativeActualLength(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b7")
	segmentURI := "/Videos/" + itemID.Dashed() + "/hls1/opaque/0.ts?RuntimeTicks=90000000&actualSegmentLengthTicks=-70000000&MediaSourceId=source&SegmentContainer=ts&SegmentLength=6"
	playlist := "#EXTM3U\n#EXTINF:-7,\n" + segmentURI + "\n"
	mainBody := &hlsReadCloser{Reader: strings.NewReader(playlist)}
	segmentBody := &hlsReadCloser{Reader: strings.NewReader("keyframe-segment")}
	streamer := &hlsStreamerStub{
		mainResponse:    playback.HLSResponse{Body: mainBody, ContentType: "application/x-mpegURL"},
		segmentResponse: playback.HLSResponse{Body: segmentBody, ContentType: "video/mp2t"},
	}

	mainRecord := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/main.m3u8?"+hlsMasterQuery(), "")
	if mainRecord.Code != http.StatusOK || mainRecord.Body.String() != playlist || mainBody.closeCalls != 1 {
		t.Fatalf("GET generated HLS playlist = status:%d body:%q closes:%d, want 200 generated playlist/one close", mainRecord.Code, mainRecord.Body.String(), mainBody.closeCalls)
	}
	generated, err := url.Parse(segmentURI)
	if err != nil {
		t.Fatalf("parse generated segment URI %q: %v", segmentURI, err)
	}
	segmentRecord := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, generated.RequestURI(), "")
	if segmentRecord.Code != http.StatusOK || segmentRecord.Body.String() != "keyframe-segment" {
		t.Fatalf("GET generated negative-duration HLS segment = status:%d body:%q, want 200 streamed segment", segmentRecord.Code, segmentRecord.Body.String())
	}
	if streamer.segmentCalls != 1 || streamer.segmentRequest.ActualSegmentLengthTicks != -70000000 || streamer.segmentRequest.RuntimeTicks != 90000000 {
		t.Errorf("generated segment projection = calls:%d runtime:%d actual:%d, want one / 90000000 / -70000000", streamer.segmentCalls, streamer.segmentRequest.RuntimeTicks, streamer.segmentRequest.ActualSegmentLengthTicks)
	}
	if segmentBody.closeCalls != 1 {
		t.Errorf("generated segment body Close calls = %d, want 1", segmentBody.closeCalls)
	}
}

// TestHLSSegmentQuarantinesUnsafeDynamicTimingFromLifecycleAccounting keeps
// malformed generated timing metadata compatible at the HTTP route while
// preventing it from mutating the HLS process lifecycle's accounting state.
func TestHLSSegmentQuarantinesUnsafeDynamicTimingFromLifecycleAccounting(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007bb")
	for _, tc := range []struct {
		name, runtimeTicks, actualLengthTicks string
	}{
		{name: "zero actual length", runtimeTicks: "0", actualLengthTicks: "0"},
		{name: "negative actual length", runtimeTicks: "0", actualLengthTicks: "-1"},
		{name: "maximum int64 actual length", runtimeTicks: "0", actualLengthTicks: strconv.FormatInt(math.MaxInt64, 10)},
		{name: "runtime plus actual length overflows int64", runtimeTicks: strconv.FormatInt(math.MaxInt64, 10), actualLengthTicks: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &hlsTimingCapturingManager{codecBurnHLSManager: &codecBurnHLSManager{}}
			query := url.Values{
				"ActualSegmentLengthTicks": {tc.actualLengthTicks},
				"MediaSourceId":            {"source"},
				"PlaySessionId":            {"timing-session"},
				"RuntimeTicks":             {tc.runtimeTicks},
				"SegmentContainer":         {"ts"},
				"SegmentLength":            {"6"},
			}

			record := hlsRequest(t, hlsRoutes(hlsTimingService(itemID, manager)), http.MethodGet, "/Videos/"+itemID.Dashed()+"/hls1/timing/0.ts?"+query.Encode(), "")
			if record.Code != http.StatusOK || record.Body.String() != "timing-safe-segment" {
				t.Fatalf("GET HLS segment with %s = status:%d body:%q, want 200 normal segment", tc.name, record.Code, record.Body.String())
			}
			if manager.restartCalls != 1 || manager.waitCalls != 1 {
				t.Errorf("HLS lifecycle calls = restart:%d wait:%d, want one normal segment lifecycle", manager.restartCalls, manager.waitCalls)
			}
			if manager.timing != nil {
				t.Errorf("WaitSegment timing = %#v, want unsafe timing withheld", manager.timing)
			}
		})
	}
}

func TestHLSDynamicValidationRejectsMalformedRequestsBeforeStreaming(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a5")
	for _, tc := range []struct{ name, path string }{
		{name: "non UUID item", path: "/Videos/not-a-uuid/master.m3u8?" + hlsMasterQuery()},
		{name: "master missing source", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?SegmentContainer=ts&SegmentLength=6"},
		{name: "negative non init index", path: "/Videos/" + itemID.Dashed() + "/hls1/p/-2.ts?" + hlsSegmentQuery()},
		{name: "non numeric index", path: "/Videos/" + itemID.Dashed() + "/hls1/p/not-a-number.ts?" + hlsSegmentQuery()},
		{name: "invalid container", path: "/Videos/" + itemID.Dashed() + "/hls1/p/0.ts!?" + hlsSegmentQuery()},
		{name: "missing runtime", path: "/Videos/" + itemID.Dashed() + "/hls1/p/0.ts?ActualSegmentLengthTicks=50&SegmentContainer=ts&SegmentLength=6"},
		{name: "malformed actual length", path: "/Videos/" + itemID.Dashed() + "/hls1/p/0.ts?" + strings.Replace(hlsSegmentQuery(), "ActualSegmentLengthTicks=50", "ActualSegmentLengthTicks=not-a-number", 1)},
		{name: "overflow actual length", path: "/Videos/" + itemID.Dashed() + "/hls1/p/0.ts?" + strings.Replace(hlsSegmentQuery(), "ActualSegmentLengthTicks=50", "ActualSegmentLengthTicks=9223372036854775808", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path, "")
			if record.Code != http.StatusBadRequest {
				t.Errorf("%s status = %d, want 400; body=%q", tc.name, record.Code, record.Body.String())
			}
			if streamer.totalDynamicCalls() != 0 {
				t.Errorf("%s reached streamer after binding failure: %#v", tc.name, streamer)
			}
		})
	}
}

func TestHLSDynamicRejectsNativeQueryAndRouteInt32OverflowBeforeStreaming(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007ad")
	const overflow = "2147483648"
	for _, field := range []string{
		"VideoStreamIndex", "AudioStreamIndex", "SubtitleStreamIndex", "MaxWidth", "MaxHeight", "VideoBitRate", "AudioBitRate", "MaxRefFrames", "MaxVideoBitDepth", "Rotation", "MaxAudioChannels", "AudioSampleRate", "MaxAudioBitDepth", "AudioChannels", "TranscodingMaxAudioChannels", "h264-maxrefframes", "h264-videobitdepth", "h264-rotation", "ac3-audiochannels", "ac3-audiobitdepth", "SegmentLength", "MinSegments",
	} {
		t.Run(field, func(t *testing.T) {
			query := hlsMasterQuery() + "&" + field + "=" + overflow
			if field == "SegmentLength" {
				query = strings.Replace(query, "SegmentLength=6", "SegmentLength="+overflow, 1)
			}
			streamer := &hlsStreamerStub{}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+query, "")
			if record.Code != http.StatusBadRequest || streamer.masterCalls != 0 {
				t.Errorf("GET HLS master %s overflow = status:%d calls:%d, want 400 before Master; body=%q", field, record.Code, streamer.masterCalls, record.Body.String())
			}
		})
	}
	streamer := &hlsStreamerStub{}
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/hls1/opaque/"+overflow+".ts?"+hlsSegmentQuery(), "")
	if record.Code != http.StatusBadRequest || streamer.segmentCalls != 0 {
		t.Errorf("GET HLS segment index overflow = status:%d calls:%d, want 400 before Segment; body=%q", record.Code, streamer.segmentCalls, record.Body.String())
	}
}

func TestHLSMasterRequiresExplicitMediaSourceBeforePackedParams(t *testing.T) {
	streamer := &hlsStreamerStub{}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007aa")
	query := "SegmentContainer=ts&SegmentLength=6&Params=;;source-only-in-packed-params"
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+query, "")
	if record.Code != http.StatusBadRequest {
		t.Errorf("GET master with packed-only MediaSourceId status = %d, want 400; body=%q", record.Code, record.Body.String())
	}
	if streamer.masterCalls != 0 {
		t.Errorf("Master calls = %d, want 0 when the explicit required MediaSourceId query is absent", streamer.masterCalls)
	}
}

func TestHLSNonVideoMasterRoutesAllowAbsentOrPackedOnlyMediaSource(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007ac")
	for _, tc := range []struct {
		name, path, query, wantSource string
		request                       func(*hlsStreamerStub) playback.HLSRequest
	}{
		{name: "audio main absent", path: "/Audio/" + itemID.Dashed() + "/main.m3u8", query: "SegmentContainer=ts&SegmentLength=6", request: func(s *hlsStreamerStub) playback.HLSRequest { return s.mainRequest }},
		{name: "video live absent", path: "/Videos/" + itemID.Dashed() + "/live.m3u8", query: "SegmentContainer=ts&SegmentLength=6", request: func(s *hlsStreamerStub) playback.HLSRequest { return s.liveRequest }},
		{name: "audio segment absent", path: "/Audio/" + itemID.Dashed() + "/hls1/opaque/0.ts", query: "RuntimeTicks=0&ActualSegmentLengthTicks=1&SegmentContainer=ts&SegmentLength=6", request: func(s *hlsStreamerStub) playback.HLSRequest { return s.segmentRequest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), mainResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), liveResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), segmentResponse: hlsResponse("video/mp2t", "segment")}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path+"?"+tc.query, "")
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%q", tc.name, record.Code, record.Body.String())
			}
			if got := tc.request(streamer).MediaSourceID; got != tc.wantSource {
				t.Errorf("%s MediaSourceID = %q, want %q", tc.name, got, tc.wantSource)
			}
		})
	}
}

func TestHLSAudioMasterRequiresExplicitMediaSourceBeforeStreaming(t *testing.T) {
	identity := auth.Identity{Authenticated: true, Token: "audio-master-token", UserID: hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b4")}
	streamer := &hlsStreamerStub{}
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b5")
	request := httptest.NewRequest(http.MethodGet, "/Audio/"+itemID.Dashed()+"/master.m3u8?SegmentContainer=ts&SegmentLength=6", nil)
	request.Header.Set("X-Emby-Token", identity.Token)
	record := httptest.NewRecorder()
	hlsAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
	if record.Code != http.StatusBadRequest || streamer.masterCalls != 0 {
		t.Errorf("GET audio master without MediaSourceId = status:%d Master calls:%d, want 400/no call; body=%q", record.Code, streamer.masterCalls, record.Body.String())
	}
}

func TestHLSLegacyRoutesDelegateOpaqueArtifactsAndRejectTraversal(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a6")
	for _, tc := range []struct {
		name, path, wantBody, wantType string
		check                          func(*testing.T, *hlsStreamerStub)
	}{
		{name: "MP3 audio", path: "/Audio/" + itemID.Dashed() + "/hls/audio-segment/stream.mp3", wantBody: "mp3", wantType: "audio/mpeg", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacySegmentPlaylist != "" || s.legacySegmentID != "audio-segment" || s.legacySegmentContainer != "mp3" {
				t.Errorf("legacy MP3 request = %#v", s)
			}
		}},
		{name: "AAC audio", path: "/Audio/" + itemID.Dashed() + "/hls/audio-segment/stream.aac", wantBody: "aac", wantType: "audio/aac", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacySegmentPlaylist != "" || s.legacySegmentContainer != "aac" {
				t.Errorf("legacy AAC request = %#v", s)
			}
		}},
		{name: "video playlist", path: "/Videos/" + itemID.Dashed() + "/hls/video-playlist/stream.m3u8", wantBody: "playlist", wantType: "application/x-mpegURL", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacyPlaylistID != "video-playlist" {
				t.Errorf("legacy playlist ID = %q", s.legacyPlaylistID)
			}
		}},
		{name: "video segment", path: "/Videos/" + itemID.Dashed() + "/hls/video-playlist/segment.TS", wantBody: "segment", wantType: "video/mp2t", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacySegmentPlaylist != "video-playlist" || s.legacySegmentID != "segment" || s.legacySegmentContainer != "ts" {
				t.Errorf("legacy video segment = %#v", s)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{legacyPlaylistResponse: hlsResponse("application/x-mpegURL", "playlist"), legacySegmentResponse: hlsResponse(tc.wantType, tc.wantBody)}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path, "")
			if record.Code != http.StatusOK || record.Body.String() != tc.wantBody || record.Header().Get("Content-Type") != tc.wantType {
				t.Fatalf("%s response = %d %q %q, want 200 %q %q", tc.name, record.Code, record.Body.String(), record.Header().Get("Content-Type"), tc.wantBody, tc.wantType)
			}
			tc.check(t, streamer)
		})
	}
	for _, path := range []string{
		"/Audio/" + itemID.Dashed() + "/hls/%2e%2e%2fsecret/stream.mp3",
		"/Videos/" + itemID.Dashed() + "/hls/%2e%2e/stream.m3u8",
		"/Videos/" + itemID.Dashed() + "/hls/playlist/%2e%2e%2fsecret.ts",
	} {
		streamer := &hlsStreamerStub{}
		record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, path, "")
		if record.Code != http.StatusBadRequest || streamer.legacyPlaylistCalls+streamer.legacySegmentCalls != 0 {
			t.Errorf("legacy traversal %q = status:%d calls:%d, want 400 before artifact access", path, record.Code, streamer.legacyPlaylistCalls+streamer.legacySegmentCalls)
		}
	}
}

func TestHLSLegacyArtifactRoutesIgnoreOpaqueItemIDAndCloseBodies(t *testing.T) {
	identity := auth.Identity{Authenticated: true, Token: "legacy-token", UserID: hlsTestUUID(t, "00000000-0000-0000-0000-0000000007b6")}
	for _, tc := range []struct {
		name, path, contentType, wantBody string
		check                             func(*testing.T, *hlsStreamerStub)
	}{
		{name: "audio AAC", path: "/Audio/not-a-guid/hls/0/stream.aac", contentType: "audio/aac", wantBody: "legacy-aac", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacySegmentPlaylist != "" || s.legacySegmentID != "0" || s.legacySegmentContainer != "aac" {
				t.Errorf("legacy audio request = %#v", s)
			}
		}},
		{name: "video playlist", path: "/Videos/not-a-guid/hls/opaque-playlist/stream.m3u8", contentType: "application/x-mpegURL", wantBody: "legacy-playlist", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacyPlaylistID != "opaque-playlist" {
				t.Errorf("legacy playlist request = %#v", s)
			}
		}},
		{name: "video segment", path: "/Videos/not-a-guid/hls/opaque-playlist/0.ts", contentType: "video/mp2t", wantBody: "legacy-segment", check: func(t *testing.T, s *hlsStreamerStub) {
			if s.legacySegmentPlaylist != "opaque-playlist" || s.legacySegmentID != "0" || s.legacySegmentContainer != "ts" {
				t.Errorf("legacy video request = %#v", s)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &hlsReadCloser{Reader: strings.NewReader(tc.wantBody)}
			streamer := &hlsStreamerStub{legacyPlaylistResponse: playback.HLSResponse{Body: body, ContentType: tc.contentType}, legacySegmentResponse: playback.HLSResponse{Body: body, ContentType: tc.contentType}}
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("X-Emby-Token", identity.Token)
			record := httptest.NewRecorder()
			hlsAuthenticatedRoutes(identity, streamer).ServeHTTP(record, request)
			if record.Code != http.StatusOK || record.Header().Get("Content-Type") != tc.contentType || record.Body.String() != tc.wantBody {
				t.Fatalf("GET opaque legacy %s = status:%d type:%q body:%q, want 200 %q %q", tc.name, record.Code, record.Header().Get("Content-Type"), record.Body.String(), tc.contentType, tc.wantBody)
			}
			if body.closeCalls != 1 {
				t.Errorf("legacy %s body Close calls = %d, want 1", tc.name, body.closeCalls)
			}
			tc.check(t, streamer)
		})
	}
}

func TestHLSLegacyMissingArtifactAndDynamicErrorsMapToHTTP(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a7")
	legacyManagerAbsent := fmt.Errorf("open legacy HLS artifact: %w", fs.ErrNotExist)
	for _, tc := range []struct {
		name, path string
		err        error
		want       int
	}{
		{name: "legacy missing playlist", path: "/Videos/" + itemID.Dashed() + "/hls/missing/stream.m3u8", err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "legacy missing segment", path: "/Videos/" + itemID.Dashed() + "/hls/missing/segment.ts", err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "legacy manager missing playlist", path: "/Videos/" + itemID.Dashed() + "/hls/missing-manager/stream.m3u8", err: legacyManagerAbsent, want: http.StatusNotFound},
		{name: "legacy manager missing segment", path: "/Videos/" + itemID.Dashed() + "/hls/missing-manager/segment.ts", err: legacyManagerAbsent, want: http.StatusNotFound},
		{name: "dynamic invalid", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: playback.ErrProgressiveInvalid, want: http.StatusBadRequest},
		{name: "dynamic unsupported", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: playback.ErrProgressiveUnsupported, want: http.StatusNotImplemented},
		{name: "dynamic missing item", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "dynamic source mismatch", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: playback.ErrMediaSourceNotFound, want: http.StatusBadRequest},
		{name: "dynamic backend", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: playback.ErrProgressiveBackend, want: http.StatusInternalServerError},
		{name: "dynamic canceled context", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: context.Canceled, want: http.StatusInternalServerError},
		{name: "dynamic stale selected source", path: "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery(), err: sourcefile.ErrStaleSource, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterErr: tc.err, legacyPlaylistErr: tc.err, legacySegmentErr: tc.err}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path, "")
			if record.Code != tc.want {
				t.Errorf("%s status = %d, want %d; body=%q", tc.name, record.Code, tc.want, record.Body.String())
			}
		})
	}
}

func TestHLSUnsupportedFailureLogsContext(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007a9")
	videoIndex, audioIndex, subtitleIndex := 3, 1, 2
	wrappedErr := fmt.Errorf("%w: build HLS master playlist: HDR10 not supported", playback.ErrProgressiveUnsupported)

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	streamer := &hlsStreamerStub{masterErr: wrappedErr}
	routes := router.New()
	handlers.RegisterHLSStreams(routes, streamer, logger)

	path := "/Videos/" + itemID.Dashed() + "/master.m3u8?" + hlsMasterQuery() +
		"&VideoStreamIndex=" + strconv.Itoa(videoIndex) +
		"&AudioStreamIndex=" + strconv.Itoa(audioIndex) +
		"&SubtitleStreamIndex=" + strconv.Itoa(subtitleIndex)
	record := hlsRequest(t, routes, http.MethodGet, path, "")

	if record.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body=%q", record.Code, http.StatusNotImplemented, record.Body.String())
	}
	if record.Body.String() != http.StatusText(http.StatusNotImplemented)+"\n" {
		t.Fatalf("body = %q, want %q", record.Body.String(), http.StatusText(http.StatusNotImplemented)+"\n")
	}

	lines := strings.Split(strings.TrimSpace(logBuffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log records = %d, want 1; log=%q", len(lines), logBuffer.String())
	}

	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode log record: %v; log=%q", err, logBuffer.String())
	}

	if msg := string(bytes.Trim(entry["msg"], `"`)); msg != "HLS service cannot fulfill request" {
		t.Errorf("msg = %q, want %q", msg, "HLS service cannot fulfill request")
	}
	if level := string(bytes.Trim(entry["level"], `"`)); level != "ERROR" {
		t.Errorf("level = %q, want ERROR", level)
	}

	errText := string(bytes.Trim(entry["error"], `"`))
	if !strings.Contains(errText, "unsupported progressive stream copy") {
		t.Errorf("error = %q, want sentinel text", errText)
	}
	if !strings.Contains(errText, "HDR10 not supported") {
		t.Errorf("error = %q, want wrapped detail", errText)
	}

	if id := string(bytes.Trim(entry["item_id"], `"`)); id != itemID.Dashed() {
		t.Errorf("item_id = %q, want %q", id, itemID.Dashed())
	}

	assertStreamIndex(t, entry, "video_stream_index", videoIndex)
	assertStreamIndex(t, entry, "audio_stream_index", audioIndex)
	assertStreamIndex(t, entry, "subtitle_stream_index", subtitleIndex)
}

func assertStreamIndex(t *testing.T, entry map[string]json.RawMessage, key string, want int) {
	t.Helper()
	raw, ok := entry[key]
	if !ok {
		t.Errorf("%s absent from log record", key)
		return
	}
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Errorf("decode %s: %v", key, err)
		return
	}
	if got != want {
		t.Errorf("%s = %d, want %d", key, got, want)
	}
}

func hlsTimingService(itemID dto.UUID, manager playback.HLSManager) *playback.HLSService {
	path := "/media/timing.mp4"
	runtimeTicks := int64(600_000_000)
	videoBitrate, audioBitrate := 2_600_000, 384_000
	width, height, refFrames, videoBitDepth := 1280, 720, 4, 8
	videoProfile, videoLevel, framerate := "High", float64(41), float32(30)
	audioChannels, sampleRate, audioBitDepth := 6, 48_000, 16
	return playback.NewHLSService(playback.HLSServiceConfig{
		Sources: hlsAuthQuerySource{source: hlsCanonicalTestSource(playback.MediaSourceInfo{
			ID: itemID.Dashed(), Protocol: playback.MediaProtocolFile, Path: path, Container: "mov,mp4,m4a,3gp,3g2,mj2", RunTimeTicks: &runtimeTicks,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Profile: &videoProfile, Level: &videoLevel, RefFrames: &refFrames, BitDepth: &videoBitDepth, BitRate: &videoBitrate, Width: &width, Height: &height, AverageFrameRate: &framerate},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "ac3", BitRate: &audioBitrate, Channels: &audioChannels, SampleRate: &sampleRate, BitDepth: &audioBitDepth, IsDefault: true},
			},
		})},
		TranscodeDir: "/cache",
		Capabilities: encoding.Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}},
		Manager:      manager,
	})
}

type hlsTimingCapturingManager struct {
	*codecBurnHLSManager
	timing    *playback.HLSSegmentTiming
	waitCalls int
}

func (manager *hlsTimingCapturingManager) WaitSegment(_ context.Context, _ string, _ int, timing *playback.HLSSegmentTiming) (io.ReadCloser, error) {
	manager.waitCalls++
	manager.timing = timing
	return io.NopCloser(strings.NewReader("timing-safe-segment")), nil
}

func TestHLSDeleteStopsOnlyExactClientCorrelation(t *testing.T) {
	streamer := &hlsStreamerStub{}
	record := hlsRequest(t, hlsRoutes(streamer), http.MethodDelete, "/Videos/ActiveEncodings?DeviceId=device-A&PlaySessionId=session-B", "")
	if record.Code != http.StatusNoContent || record.Body.Len() != 0 {
		t.Fatalf("DELETE ActiveEncodings = %d body:%q, want 204 no body", record.Code, record.Body.String())
	}
	if streamer.stopCalls != 1 || streamer.stopDeviceID != "device-A" || streamer.stopPlaySessionID != "session-B" {
		t.Errorf("Stop correlation = calls:%d device:%q session:%q, want one exact pair", streamer.stopCalls, streamer.stopDeviceID, streamer.stopPlaySessionID)
	}
}

func TestHLSDeleteRequiresBothCorrelationFields(t *testing.T) {
	for _, tc := range []struct {
		name, query string
	}{
		{name: "missing device", query: "PlaySessionId=session-B"},
		{name: "missing session", query: "DeviceId=device-A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodDelete, "/Videos/ActiveEncodings?"+tc.query, "")
			if record.Code != http.StatusBadRequest {
				t.Errorf("DELETE ActiveEncodings %s status = %d, want 400; body=%q", tc.name, record.Code, record.Body.String())
			}
			if streamer.stopCalls != 0 {
				t.Errorf("DELETE ActiveEncodings %s Stop calls = %d, want 0 after required-field validation", tc.name, streamer.stopCalls)
			}
		})
	}
}

type hlsAuthQuerySource struct{ source playback.MediaSourceInfo }

func hlsCanonicalTestSource(source playback.MediaSourceInfo) playback.MediaSourceInfo {
	inode := int64(7_701)
	changedAt := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	source.SourceRevision = "handler-test-source-revision:" + source.Path
	source.FileFacts.Inode = &inode
	source.FileFacts.ChangedAt = &changedAt
	return source
}

func hlsCanonicalTestRecord(record playback.MediaSourceRecord) playback.MediaSourceRecord {
	inode := int64(7_702)
	changedAt := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	path := ""
	if record.Path != nil {
		path = *record.Path
	}
	record.FileInode = &inode
	record.FileChangedAt = &changedAt
	record.SourceRevision = "handler-test-source-revision:" + path
	return record
}

func (s hlsAuthQuerySource) StaticSource(context.Context, dto.UUID, dto.UUID, string) (playback.MediaSourceInfo, error) {
	return s.source, nil
}

type hlsStreamerStub struct {
	masterResponse, mainResponse, liveResponse, segmentResponse playback.HLSResponse
	legacyPlaylistResponse, legacySegmentResponse               playback.HLSResponse
	masterErr, mainErr, liveErr, segmentErr                     error
	legacyPlaylistErr, legacySegmentErr, stopErr                error
	masterCalls, mainCalls, liveCalls, segmentCalls             int
	legacyPlaylistCalls, legacySegmentCalls, stopCalls          int
	masterRequest, mainRequest, liveRequest, segmentRequest     playback.HLSRequest
	legacyPlaylistID, legacySegmentPlaylist, legacySegmentID    string
	legacySegmentContainer, stopDeviceID, stopPlaySessionID     string
}

func (s *hlsStreamerStub) Master(_ context.Context, request playback.HLSRequest) (playback.HLSResponse, error) {
	s.masterCalls++
	s.masterRequest = request
	return s.masterResponse, s.masterErr
}

func (s *hlsStreamerStub) Main(_ context.Context, request playback.HLSRequest) (playback.HLSResponse, error) {
	s.mainCalls++
	s.mainRequest = request
	return s.mainResponse, s.mainErr
}

func (s *hlsStreamerStub) Live(_ context.Context, request playback.HLSRequest) (playback.HLSResponse, error) {
	s.liveCalls++
	s.liveRequest = request
	return s.liveResponse, s.liveErr
}

func (s *hlsStreamerStub) Segment(_ context.Context, request playback.HLSRequest) (playback.HLSResponse, error) {
	s.segmentCalls++
	s.segmentRequest = request
	return s.segmentResponse, s.segmentErr
}

func (s *hlsStreamerStub) LegacyPlaylist(_ context.Context, playlistID string) (playback.HLSResponse, error) {
	s.legacyPlaylistCalls++
	s.legacyPlaylistID = playlistID
	return s.legacyPlaylistResponse, s.legacyPlaylistErr
}

func (s *hlsStreamerStub) LegacySegment(_ context.Context, playlistID, segmentID, container string) (playback.HLSResponse, error) {
	s.legacySegmentCalls++
	s.legacySegmentPlaylist, s.legacySegmentID, s.legacySegmentContainer = playlistID, segmentID, container
	return s.legacySegmentResponse, s.legacySegmentErr
}

func (s *hlsStreamerStub) Stop(_ context.Context, deviceID, playSessionID string) error {
	s.stopCalls++
	s.stopDeviceID, s.stopPlaySessionID = deviceID, playSessionID
	return s.stopErr
}

func (s *hlsStreamerStub) totalDynamicCalls() int {
	return s.masterCalls + s.mainCalls + s.liveCalls + s.segmentCalls
}

func hlsRoutes(streamer playback.HLSVideoStreamer) *router.Router {
	routes := router.New()
	handlers.RegisterHLSStreams(routes, streamer, discardLogger())
	return routes
}

func hlsAuthenticatedRoutes(identity auth.Identity, streamer playback.HLSVideoStreamer) *router.Router {
	authorizer := middleware.NewAuthorizer(hlsTokenResolver{identity: identity}, hlsAllowEvaluator{}, hlsIdentityUsers{})
	routes := router.New(authorizer.Wrap)
	handlers.RegisterHLSStreams(routes, streamer, discardLogger())
	return routes
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type hlsTokenResolver struct{ identity auth.Identity }

func (r hlsTokenResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	token := request.Header.Get("X-Emby-Token")
	if token == "" {
		token = bind.QueryFirst(request, "ApiKey")
	}
	if token != r.identity.Token {
		return auth.Identity{}, nil
	}
	return r.identity, nil
}

type hlsAllowEvaluator struct{}

func (hlsAllowEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}

type hlsIdentityUsers struct{}

func (hlsIdentityUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id}, nil
}

func hlsRequest(t *testing.T, routes http.Handler, method, path, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("User-Agent", userAgent)
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, request)
	return record
}

func hlsResponse(contentType, body string) playback.HLSResponse {
	return playback.HLSResponse{Body: io.NopCloser(strings.NewReader(body)), ContentType: contentType}
}

func hlsTestUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	value, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse HLS test UUID %q: %v", text, err)
	}
	return value
}

func hlsRouteByOperation(routes []router.Route, operation string) (router.Route, bool) {
	for _, route := range routes {
		if route.OperationID == operation {
			return route, true
		}
	}
	return router.Route{}, false
}

func hlsMasterQuery() string {
	return "MediaSourceId=source&SegmentContainer=ts&SegmentLength=6&PlaySessionId=session"
}
func hlsRequiredQuery() string { return hlsMasterQuery() }
func hlsSegmentQuery() string {
	return "RuntimeTicks=100&ActualSegmentLengthTicks=50&MediaSourceId=source&SegmentContainer=ts&SegmentLength=6&PlaySessionId=session"
}

func hlsFullQuery(source, device, session string) string {
	return "MediaSourceId=" + source + "&DeviceId=" + device + "&PlaySessionId=" + session + "&Tag=tag-query&LiveStreamId=live-query&EnableSubtitlesInManifest=false&SubtitleCodec=ass&TranscodeReasons=reason-query&Static=false&SegmentContainer=fMP4&SegmentLength=6&MinSegments=2&VideoCodec=h264,hevc&AudioCodec=aac,ac3&VideoStreamIndex=4&AudioStreamIndex=5&SubtitleStreamIndex=6&SubtitleMethod=External&StartTimeTicks=700000000&EnableAutoStreamCopy=false&AllowVideoStreamCopy=false&AllowAudioStreamCopy=false&RequireAvc=true&RequireNonAnamorphic=true&DeInterlace=true&Width=1280&Height=720&Framerate=23.976&MaxWidth=1920&MaxHeight=1080&MaxFramerate=24.5&VideoBitRate=8100000&AudioBitRate=384000&MaxAudioChannels=6&AudioChannels=2&AudioSampleRate=48000&MaxAudioBitDepth=24&Profile=high&Level=4.1&MaxRefFrames=4&MaxVideoBitDepth=10&TranscodingMaxAudioChannels=8&CopyTimestamps=true&h264-level=4.2"
}

func assertHLSProjection(t *testing.T, r playback.HLSRequest, itemID interface{ Dashed() string }, raw, source, device, session string) {
	t.Helper()
	if r.ItemID.Dashed() != itemID.Dashed() || r.MediaSourceID != source || r.DeviceID != device || r.PlaySessionID != session || r.UserAgent != "Jellyfin Web/10.10" || r.RawQuery != raw || r.Tag != "tag-query" || r.LiveStreamID != "live-query" || r.EnableSubtitlesInManifest || r.SubtitleCodec != "ass" || r.TranscodeReasons != "reason-query" || r.Static || r.SegmentContainer != "fmp4" || r.SegmentLengthSeconds != 6 || r.MinSegments != 2 {
		t.Errorf("HLS identity/route projection = %#v", r)
	}
	if strings.Join(r.VideoCodecs, ",") != "h264,hevc" || strings.Join(r.AudioCodecs, ",") != "aac,ac3" || r.VideoCodec != "h264,hevc" || r.AudioCodec != "aac,ac3" || r.StartTimeTicks != 700000000 {
		t.Errorf("HLS codec/tick projection = %#v", r)
	}
	assertHLSInt(t, "VideoStreamIndex", r.VideoStreamIndex, 4)
	assertHLSInt(t, "AudioStreamIndex", r.AudioStreamIndex, 5)
	assertHLSInt(t, "SubtitleStreamIndex", r.SubtitleStreamIndex, 6)
	assertHLSInt(t, "Width", r.Width, 1280)
	assertHLSInt(t, "Height", r.Height, 720)
	assertHLSInt(t, "MaxWidth", r.MaxWidth, 1920)
	assertHLSInt(t, "MaxHeight", r.MaxHeight, 1080)
	assertHLSInt(t, "VideoBitrate", r.VideoBitrate, 8_100_000)
	assertHLSInt(t, "AudioBitrate", r.AudioBitrate, 384_000)
	assertHLSInt(t, "MaxAudioChannels", r.MaxAudioChannels, 6)
	assertHLSInt(t, "AudioChannels", r.AudioChannels, 2)
	assertHLSInt(t, "AudioSampleRate", r.AudioSampleRate, 48_000)
	assertHLSInt(t, "MaxAudioBitDepth", r.MaxAudioBitDepth, 24)
	assertHLSInt(t, "MaxRefFrames", r.MaxRefFrames, 4)
	assertHLSInt(t, "MaxVideoBitDepth", r.MaxVideoBitDepth, 10)
	assertHLSInt(t, "TranscodingMaxAudioChannels", r.TranscodingMaxAudioChannels, 8)
	if r.Framerate == nil || *r.Framerate != 23.976 || r.MaxFramerate == nil || *r.MaxFramerate != 24.5 || r.Profile == nil || *r.Profile != "high" || r.Level == nil || *r.Level != "4.1" || r.SubtitleDeliveryMethod == nil || *r.SubtitleDeliveryMethod != playback.SubtitleExternal || r.EnableAutoStreamCopy || r.AllowVideoStreamCopy || r.AllowAudioStreamCopy || !r.RequireAvc || !r.RequireNonAnamorphic || !r.DeInterlace || !r.CopyTimestamps || r.StreamOptions.Get("h264-level") != "4.2" {
		t.Errorf("HLS optional stream projection = %#v", r)
	}
}

func assertHLSInt(t *testing.T, name string, actual *int, want int) {
	t.Helper()
	if actual == nil || *actual != want {
		t.Errorf("%s = %v, want %d", name, actual, want)
	}
}

type hlsReadCloser struct {
	io.Reader
	closeCalls int
}

func (r *hlsReadCloser) Close() error { r.closeCalls++; return nil }

type gatedHLSReadCloser struct {
	first, last []byte
	state       int
	gate        chan struct{}
	once        sync.Once
	closeCalls  int
}

func newGatedHLSReadCloser(first, last string) *gatedHLSReadCloser {
	return &gatedHLSReadCloser{first: []byte(first), last: []byte(last), gate: make(chan struct{})}
}

func (r *gatedHLSReadCloser) Read(dst []byte) (int, error) {
	switch r.state {
	case 0:
		r.state++
		return copy(dst, r.first), nil
	case 1:
		<-r.gate
		r.state++
		return copy(dst, r.last), nil
	default:
		return 0, io.EOF
	}
}
func (r *gatedHLSReadCloser) Close() error { r.closeCalls++; return nil }
func (r *gatedHLSReadCloser) release()     { r.once.Do(func() { close(r.gate) }) }

type hlsStreamingResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	code       int
	firstWrite chan struct{}
	once       sync.Once
}

func newHLSStreamingResponseWriter() *hlsStreamingResponseWriter {
	return &hlsStreamingResponseWriter{header: make(http.Header), firstWrite: make(chan struct{})}
}
func (w *hlsStreamingResponseWriter) Header() http.Header  { return w.header }
func (w *hlsStreamingResponseWriter) WriteHeader(code int) { w.code = code }
func (w *hlsStreamingResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.firstWrite) })
	return w.body.Write(data)
}
func (w *hlsStreamingResponseWriter) Flush() {}

var _ playback.HLSVideoStreamer = (*hlsStreamerStub)(nil)

// An HLS 500 must leave an ERROR line in the log, so that init and seek 500s
// have a log record of the cause.
func TestHLSInternalFailureLogsError(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007aa")
	backendErr := errors.New("HLS play session stopped during restart")

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	routes := router.New()
	handlers.RegisterHLSStreams(routes, &hlsStreamerStub{masterErr: backendErr}, logger)

	record := hlsRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery(), "")
	if record.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%q", record.Code, record.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(logBuffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log records = %d, want 1; log=%q", len(lines), logBuffer.String())
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode log record: %v; log=%q", err, logBuffer.String())
	}
	if level := string(bytes.Trim(entry["level"], `"`)); level != "ERROR" {
		t.Errorf("level = %q, want ERROR", level)
	}
	if got := string(bytes.Trim(entry["error"], `"`)); got != backendErr.Error() {
		t.Errorf("error = %q, want %q", got, backendErr.Error())
	}
	if id := string(bytes.Trim(entry["item_id"], `"`)); id != itemID.Dashed() {
		t.Errorf("item_id = %q, want %q", id, itemID.Dashed())
	}
	if _, ok := entry["play_session_id"]; !ok {
		t.Errorf("log record has no play_session_id: %q", lines[0])
	}
}
