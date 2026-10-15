// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// The MediaInfo controller applies the default authorization policy to both
// PlaybackInfo operations and the live-stream operations it exposes.
func TestPlaybackInfoRoutesPinMethodsPathsOperationsAndPolicy(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, nil, nil)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/PlaybackInfo", OperationID: "GetPlaybackInfo", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Items/{itemId}/PlaybackInfo", OperationID: "GetPostedPlaybackInfo", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/LiveStreams/Open", OperationID: "OpenLiveStream", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/LiveStreams/Close", OperationID: "CloseLiveStream", Policy: string(auth.PolicyDefaultAuthorization)},
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

func TestPostedPlaybackInfoUsesQueryScalarsOverBodyAndPreservesExplicitEmptyProfile(t *testing.T) {
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}, PlaySessionID: "session-42"}}
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	itemID := "00000000-0000-0000-0000-000000000402"
	body := `{"UserId":"00000000-0000-0000-0000-000000000411","MaxStreamingBitrate":111,"StartTimeTicks":222,"AudioStreamIndex":3,"EnableDirectPlay":false,"DeviceProfile":{"TranscodingProfiles":[]}}`
	rec := request(rt, http.MethodPost, "/iTeMs/"+itemID+"/pLaYbAcKiNfO?UserId=00000000-0000-0000-0000-000000000412&MaxStreamingBitrate=999&StartTimeTicks=888&AudioStreamIndex=7&EnableDirectPlay=true", body, "application/playbackinfo+json", "")
	assertStatus(t, rec, http.StatusOK)
	got := service.lastPlayback()
	if got.UserID.String() != "00000000000000000000000000000412" || got.MaxStreamingBitrate == nil || *got.MaxStreamingBitrate != 999 || got.StartTimeTicks == nil || *got.StartTimeTicks != 888 || got.AudioStreamIndex == nil || *got.AudioStreamIndex != 7 || !got.EnableDirectPlay {
		t.Errorf("POST query values did not override body: %#v", got)
	}
	profile := got.DeviceProfile
	if profile == nil || profile.TranscodingProfiles == nil || len(profile.TranscodingProfiles) != 0 {
		t.Errorf("explicit empty DeviceProfile.TranscodingProfiles was not preserved: %#v", got.DeviceProfile)
	}
	var pascal map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &pascal); err != nil {
		t.Fatalf("decode playback response: %v", err)
	}
	if string(pascal["PlaySessionId"]) != `"session-42"` || string(pascal["MediaSources"]) != `[]` {
		t.Errorf("Pascal playback response = %s, want PlaySessionId and empty MediaSources", rec.Body.String())
	}

	camel := request(rt, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo", `{}`, "text/json", `application/json; profile="CamelCase"`)
	assertStatus(t, camel, http.StatusOK)
	var camelBody map[string]json.RawMessage
	if err := json.Unmarshal(camel.Body.Bytes(), &camelBody); err != nil {
		t.Fatalf("decode camel playback response: %v", err)
	}
	if string(camelBody["playSessionId"]) != `"session-42"` || string(camelBody["mediaSources"]) != `[]` {
		t.Errorf("Camel playback response = %s, want camel DTO fields", camel.Body.String())
	}
}

// TestPostedPlaybackInfoReadsQuotedNumbers pins quoted numbers in a posted
// PlaybackInfo body. jellyfin-web 12.0.0 sends the selected stream indexes
// as JSON strings; upstream reads them with
// JsonNumberHandling.AllowReadingFromString (JsonDefaults.cs:34).
func TestPostedPlaybackInfoReadsQuotedNumbers(t *testing.T) {
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}, PlaySessionID: "session-490"}}
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	itemID := "00000000-0000-0000-0000-000000000490"
	body := `{"UserId":"00000000-0000-0000-0000-000000000491","StartTimeTicks":"0","IsPlayback":true,"AutoOpenLiveStream":true,` +
		`"AudioStreamIndex":"2","SubtitleStreamIndex":"0","MaxStreamingBitrate":"1000","MediaSourceId":"source-490",` +
		`"DeviceProfile":{"MaxStreamingBitrate":"2000","TranscodingProfiles":[{"Container":"ts","Type":"Video","MinSegments":"1"}]}}`
	rec := request(rt, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo", body, "application/json", "")
	assertStatus(t, rec, http.StatusOK)
	got := service.lastPlayback()
	if got.AudioStreamIndex == nil || *got.AudioStreamIndex != 2 {
		t.Errorf("AudioStreamIndex = %v, want 2", got.AudioStreamIndex)
	}
	if got.SubtitleStreamIndex == nil || *got.SubtitleStreamIndex != 0 {
		t.Errorf("SubtitleStreamIndex = %v, want 0", got.SubtitleStreamIndex)
	}
	if got.MaxStreamingBitrate == nil || *got.MaxStreamingBitrate != 1000 {
		t.Errorf("MaxStreamingBitrate = %v, want 1000", got.MaxStreamingBitrate)
	}
	if got.StartTimeTicks == nil || *got.StartTimeTicks != 0 {
		t.Errorf("StartTimeTicks = %v, want 0", got.StartTimeTicks)
	}
	if got.MediaSourceID != "source-490" {
		t.Errorf("MediaSourceID = %q, want source-490", got.MediaSourceID)
	}
	profile := got.DeviceProfile
	if profile == nil || profile.MaxStreamingBitrate == nil || *profile.MaxStreamingBitrate != 2000 || len(profile.TranscodingProfiles) != 1 || profile.TranscodingProfiles[0].MinSegments != 1 {
		t.Errorf("DeviceProfile = %#v, want quoted profile numbers read", profile)
	}

	invalid := request(rt, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo", `{"AudioStreamIndex":"2x"}`, "application/json", "")
	assertStatus(t, invalid, http.StatusBadRequest)
}

func TestPlaybackInfoRejectsInvalidRouteQueryAndBodyInputs(t *testing.T) {
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}}}
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	for _, tc := range []struct{ name, method, path, body, contentType string }{
		{"invalid route UUID", http.MethodGet, "/Items/not-a-guid/PlaybackInfo", "", ""},
		{"invalid scalar", http.MethodPost, "/Items/00000000-0000-0000-0000-000000000402/PlaybackInfo?MaxStreamingBitrate=nope", `{}`, "application/json"},
		{"malformed body", http.MethodPost, "/Items/00000000-0000-0000-0000-000000000402/PlaybackInfo", `{`, "application/json"},
		{"unsupported content type", http.MethodPost, "/Items/00000000-0000-0000-0000-000000000402/PlaybackInfo", `{}`, "text/plain"},
		{"close missing live stream", http.MethodPost, "/LiveStreams/Close", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertStatus(t, request(rt, tc.method, tc.path, tc.body, tc.contentType, ""), http.StatusBadRequest)
		})
	}
}

// Playback query scalars select both account state and media delivery state.
// Case variants must therefore reject conflicting values before PlaybackInfo
// can return a source assembled for either selection.
func TestPlaybackInfoRejectsConflictingCaseFoldedQueryScalarsBeforeService(t *testing.T) {
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{
		MediaSources:  []playback.MediaSourceInfo{{ID: "target-source", Path: "/private/target.mkv"}},
		PlaySessionID: "target-session",
	}}
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	itemID := "00000000-0000-0000-0000-000000000402"
	first := "00000000-0000-0000-0000-000000000413"
	second := "00000000-0000-0000-0000-000000000414"

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "user ID", query: "userId=" + first + "&USERID=" + second},
		{name: "media source ID", query: "mediaSourceId=source-a&MEDIASOURCEID=source-b"},
		{name: "audio stream index", query: "audioStreamIndex=1&AUDIOSTREAMINDEX=2"},
		{name: "subtitle stream index", query: "subtitleStreamIndex=3&SUBTITLESTREAMINDEX=4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(service.requests)
			rec := request(rt, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo?"+tc.query, `{}`, "application/json", "")
			assertStatus(t, rec, http.StatusBadRequest)
			if got := len(service.requests); got != before {
				t.Fatalf("conflicting %s invoked PlaybackInfo %d times, want %d", tc.name, got, before)
			}
			if strings.Contains(rec.Body.String(), "target-source") || strings.Contains(rec.Body.String(), "target-session") {
				t.Fatalf("conflicting %s leaked assembled target data: %q", tc.name, rec.Body.String())
			}
		})
	}

	t.Run("identical user aliases preserve the selected user", func(t *testing.T) {
		before := len(service.requests)
		rec := request(rt, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo?userId="+first+"&USERID="+first, `{}`, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		if got := len(service.requests); got != before+1 {
			t.Fatalf("identical aliases invoked PlaybackInfo %d times, want %d", got, before+1)
		}
		if got := service.lastPlayback().UserID.Dashed(); got != first {
			t.Fatalf("identical aliases selected user %s, want %s", got, first)
		}
	})
}

// Source: upstream/jellyfin/Jellyfin.Api/Controllers/MediaInfoController.cs:213;
// MediaBrowser.Common/Extensions/HttpContextExtensions.cs:28-39.
//
// The server boundary, rather than an individual endpoint, establishes this
// trusted client-address fact. Playback must consume that fact after the
// authorization middleware has preserved it; falling back to the direct peer
// would classify a reverse proxy instead of its client.
func TestPlaybackInfoUsesTrustedClientAddressInsteadOfProxyPeer(t *testing.T) {
	identity := auth.Identity{Authenticated: true, UserID: playbackTestUUID(t, "00000000-0000-0000-0000-0000000004a1"), DeviceID: "network-device"}
	itemID := "00000000-0000-0000-0000-000000000402"

	trustedClientA := playbackTestNetworkAddress("trusted-client-a")
	trustedClientB := playbackTestNetworkAddress("trusted-client-b")
	proxyPeerA := playbackTestNetworkAddress("proxy-peer-a")
	proxyPeerB := playbackTestNetworkAddress("proxy-peer-b")
	for _, tc := range []struct {
		name          string
		trustedClient netip.Addr
		proxyPeer     netip.Addr
	}{
		{name: "first trusted client differs from proxy peer", trustedClient: trustedClientA, proxyPeer: proxyPeerB},
		{name: "second trusted client differs from proxy peer", trustedClient: trustedClientB, proxyPeer: proxyPeerA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}, PlaySessionID: "network-session"}}
			authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
			rt := router.New(authorizer.Wrap)
			handlers.RegisterPlaybackInfo(rt, service, playbackUsers{})

			request := httptest.NewRequest(http.MethodGet, "/Items/"+itemID+"/PlaybackInfo", nil)
			request.RemoteAddr = net.JoinHostPort(tc.proxyPeer.String(), "0")
			request = request.WithContext(middleware.WithClientAddress(request.Context(), tc.trustedClient))
			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, request)

			assertStatus(t, recorder, http.StatusOK)
			if got := service.lastPlayback().ClientAddress; got != tc.trustedClient {
				t.Fatalf("PlaybackInfo ClientAddress = %s, want trusted client %s (not proxy %s)", got, tc.trustedClient, tc.proxyPeer)
			}
		})
	}
}

func playbackTestNetworkAddress(role string) netip.Addr {
	digest := sha256.Sum256([]byte(role))
	var address [16]byte
	copy(address[:], digest[:])
	return netip.AddrFrom16(address)
}

type playbackInfoFake struct {
	mu       sync.Mutex
	requests []playback.PlaybackInfoRequest
	response playback.PlaybackInfoResponse
	err      error
}

func (f *playbackInfoFake) PlaybackInfo(_ context.Context, request playback.PlaybackInfoRequest) (playback.PlaybackInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	return f.response, f.err
}

func (f *playbackInfoFake) OpenLiveStream(context.Context, playback.OpenLiveStreamRequest) (playback.LiveStreamResponse, error) {
	return playback.LiveStreamResponse{}, nil
}
func (f *playbackInfoFake) CloseLiveStream(context.Context, string) error { return nil }
func (f *playbackInfoFake) lastPlayback() playback.PlaybackInfoRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

var _ playback.PlaybackInfoService = (*playbackInfoFake)(nil)

func TestPlaybackInfoGETUsesAuthenticatedIdentityAndOnlyAssemblyFields(t *testing.T) {
	identityUser := playbackTestUUID(t, "00000000-0000-0000-0000-000000000421")
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}}}
	identity := auth.Identity{Authenticated: true, UserID: identityUser, Client: "Authenticated Browser", DeviceID: "current-device"}
	authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterPlaybackInfo(rt, service, playbackUsers{})
	itemID := "00000000-0000-0000-0000-000000000402"
	path := "/iTeMs/" + itemID + "/pLaYbAcKiNfO?userId=" + identityUser.Dashed() + "&MaxStreamingBitrate=999&StartTimeTicks=888&AudioStreamIndex=7&SubtitleStreamIndex=8&MaxAudioChannels=2&MediaSourceId=source-a&LiveStreamId=live-a&AutoOpenLiveStream=true&EnableDirectPlay=false&EnableDirectStream=false&EnableTranscoding=false&AllowVideoStreamCopy=false&AllowAudioStreamCopy=false"
	assertStatus(t, request(rt, http.MethodGet, path, "", "", ""), http.StatusOK)
	got := service.lastPlayback()
	if got.UserID != identityUser || got.Client != identity.Client || got.DeviceID != "current-device" || !got.AssemblyOnly || got.DeviceProfile != nil || got.MaxStreamingBitrate != nil || got.StartTimeTicks != nil || got.AudioStreamIndex != nil || got.SubtitleStreamIndex != nil || got.MaxAudioChannels != nil || got.MediaSourceID != "" || got.LiveStreamID != "" || got.AutoOpenLiveStream || got.EnableDirectPlay || got.EnableDirectStream || got.EnableTranscoding || got.AllowVideoStreamCopy || got.AllowAudioStreamCopy {
		t.Errorf("GET PlaybackInfo request = %#v, want authenticated identity and assembly-only fields", got)
	}

	assertStatus(t, request(rt, http.MethodGet, "/Items/"+itemID+"/PlaybackInfo", "", "", ""), http.StatusOK)
	if got := service.lastPlayback(); got.UserID != identityUser || got.Client != identity.Client || got.DeviceID != "current-device" || !got.AssemblyOnly {
		t.Errorf("GET PlaybackInfo omitted authenticated assembly defaults: %#v", got)
	}
}

// TestPlaybackInfoRoutesAuthorizeSelectedUserThroughMiddleware keeps the
// RequestHelpers selected-user contract at the PlaybackInfo HTTP boundary.
// The test crosses the production router, authorizer, persisted user service,
// and handler: an arbitrary UserId must not let a member borrow another
// account's playback permissions.
//
// Source: upstream/jellyfin/Jellyfin.Api/Helpers/RequestHelpers.cs:67-85.
func TestPlaybackInfoRoutesAuthorizeSelectedUserThroughMiddleware(t *testing.T) {
	h := newP104Harness(t)
	actor := h.createUser(t, "Playback Actor", "password", json.RawMessage(`{}`), json.RawMessage(`{}`))
	target := h.createUser(t, "Playback Target", "password", json.RawMessage(`{}`), json.RawMessage(`{}`))
	missing := playbackTestUUID(t, "00000000-0000-0000-0000-000000000499")
	h.identities["actor"] = auth.Identity{Authenticated: true, UserID: actor.ID, DeviceID: "actor-device"}
	h.identities["admin"] = auth.Identity{Authenticated: true, Administrator: true, UserID: actor.ID, DeviceID: "admin-device"}
	service := &playbackInfoFake{response: playback.PlaybackInfoResponse{
		MediaSources:  []playback.MediaSourceInfo{{ID: "playable-source", Path: "/library/playable.mp4"}},
		PlaySessionID: "target-play-session",
	}}
	handlers.RegisterPlaybackInfo(h.rt, service, h.users)
	itemID := "00000000-0000-0000-0000-000000000402"

	for _, tc := range []struct {
		name, method, path, body, contentType, actor string
		want                                         int
		wantTarget                                   bool
	}{
		{name: "member GET cannot target another user", method: http.MethodGet, path: "/Items/" + itemID + "/PlaybackInfo?UserId=" + target.ID.Dashed(), actor: "actor", want: http.StatusForbidden},
		{name: "member POST cannot target another user", method: http.MethodPost, path: "/Items/" + itemID + "/PlaybackInfo", body: `{"UserId":"` + target.ID.Dashed() + `"}`, contentType: "application/json", actor: "actor", want: http.StatusForbidden},
		{name: "administrator GET can target an existing user", method: http.MethodGet, path: "/Items/" + itemID + "/PlaybackInfo?UserId=" + target.ID.Dashed(), actor: "admin", want: http.StatusOK, wantTarget: true},
		{name: "administrator POST can target an existing user", method: http.MethodPost, path: "/Items/" + itemID + "/PlaybackInfo", body: `{"UserId":"` + target.ID.Dashed() + `"}`, contentType: "application/json", actor: "admin", want: http.StatusOK, wantTarget: true},
		{name: "administrator GET maps missing selected user", method: http.MethodGet, path: "/Items/" + itemID + "/PlaybackInfo?UserId=" + missing.Dashed(), actor: "admin", want: http.StatusNotFound},
		{name: "administrator POST maps missing selected user", method: http.MethodPost, path: "/Items/" + itemID + "/PlaybackInfo", body: `{"UserId":"` + missing.Dashed() + `"}`, contentType: "application/json", actor: "admin", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(service.requests)
			rec := p104Call(h.rt, tc.method, tc.path, tc.body, tc.contentType, "", tc.actor, p104MediaBrowserMetadata)
			assertStatus(t, rec, tc.want)
			if !tc.wantTarget {
				if got := len(service.requests); got != before {
					t.Fatalf("PlaybackInfo invoked service %d times after rejected target, want %d", got, before)
				}
				return
			}
			response := decodeJSONObject(t, rec.Body.Bytes())
			if string(response["PlaySessionId"]) != `"target-play-session"` || string(response["MediaSources"]) == "[]" {
				t.Fatalf("administrator response = %s, want assembled source and nonempty session ID", rec.Body.String())
			}
			if got := service.lastPlayback(); got.UserID != target.ID {
				t.Fatalf("selected user = %s, want administrator target %s", got.UserID, target.ID)
			}
		})
	}
}

func TestPlaybackInfoMapsMissingItemAndNoCompatibleStreamSeparately(t *testing.T) {
	itemID := "00000000-0000-0000-0000-000000000402"
	missing := &playbackInfoFake{err: playback.ErrItemNotFound}
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, missing, nil)
	assertStatus(t, request(rt, http.MethodGet, "/Items/"+itemID+"/PlaybackInfo", "", "", ""), http.StatusNotFound)

	noCompatible := &playbackInfoFake{response: playback.PlaybackInfoResponse{MediaSources: []playback.MediaSourceInfo{}, ErrorCode: playback.PlaybackInfoNoCompatibleStream}}
	rt = router.New()
	handlers.RegisterPlaybackInfo(rt, noCompatible, nil)
	rec := request(rt, http.MethodGet, "/Items/"+itemID+"/PlaybackInfo", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if got := string(decodeJSONObject(t, rec.Body.Bytes())["ErrorCode"]); got != `"NoCompatibleStream"` {
		t.Errorf("NoCompatibleStream response ErrorCode = %s, want stock 200 response code", got)
	}
}

func TestPlaybackInfoRoutesDifferentiateGETAndPOSTProfileNegotiation(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f2")
	directProfile := &playback.DeviceProfile{DirectPlayProfiles: []playback.DirectPlayProfile{{
		Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
	}}}

	newService := func(profiles playback.DeviceProfileReader) *playback.PlaybackInfoApplication {
		container, path := "mp4", "/library/concrete.mp4"
		return playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
			Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{{
				ID: itemID, Path: &path, Container: &container,
				MediaStreams: []playback.MediaStream{
					{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
					{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
				},
			}}},
			DeviceProfiles: profiles,
			Permissions:    playbackConcretePermissionReader{},
			Network:        playbackConcreteNetwork{},
			NewPlaySessionID: func() string {
				return "concrete-play-session"
			},
		})
	}

	newRawSourceService := func(profiles playback.DeviceProfileReader) *playback.PlaybackInfoApplication {
		unmatchablePath, unmatchableName, unmatchableContainer := "/library/raw-unmatchable.mkv", "raw-unmatchable", "mkv"
		unmatchableSize, unmatchableRuntime, unmatchableBitrate := int64(7_000_001), int64(8_000_001), 9_000_001
		unmatchableWidth, unmatchableStreamBitrate := 1_920, 4_000_001
		unmatchableLanguage := "und"
		compatibleID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f3")
		compatiblePath, compatibleName, compatibleContainer := "/library/raw-compatible.mp4", "raw-compatible", "mp4"
		compatibleSize, compatibleRuntime, compatibleBitrate := int64(7_000_002), int64(8_000_002), 9_000_002
		compatibleWidth, compatibleStreamBitrate := 1_280, 4_000_002
		compatibleLanguage := "en"
		return playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
			Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{
				{
					ID: itemID, Path: &unmatchablePath, Name: &unmatchableName, Container: &unmatchableContainer,
					Size: &unmatchableSize, RunTimeTicks: &unmatchableRuntime, TotalBitrate: &unmatchableBitrate,
					MediaStreams: []playback.MediaStream{
						{Index: 0, Type: playback.StreamTypeVideo, Codec: "vp9", Width: &unmatchableWidth, BitRate: &unmatchableStreamBitrate},
						{Index: 1, Type: playback.StreamTypeAudio, Codec: "opus", Language: &unmatchableLanguage, IsDefault: true},
					},
					MediaAttachments: []playback.MediaAttachment{{Index: 2, Codec: "subrip", FileName: "raw-unmatchable.srt", MimeType: "application/x-subrip"}},
				},
				{
					ID: compatibleID, Path: &compatiblePath, Name: &compatibleName, Container: &compatibleContainer,
					Size: &compatibleSize, RunTimeTicks: &compatibleRuntime, TotalBitrate: &compatibleBitrate,
					MediaStreams: []playback.MediaStream{
						{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Width: &compatibleWidth, BitRate: &compatibleStreamBitrate},
						{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: &compatibleLanguage, IsDefault: true},
					},
					MediaAttachments: []playback.MediaAttachment{{Index: 2, Codec: "subrip", FileName: "raw-compatible.srt", MimeType: "application/x-subrip"}},
				},
			}},
			DeviceProfiles: profiles,
			Permissions:    playbackConcretePermissionReader{},
			Network:        playbackConcreteNetwork{},
			NewPlaySessionID: func() string {
				return "raw-assembled-session"
			},
		})
	}

	t.Run("GET assembles sources and session without reading a stored profile", func(t *testing.T) {
		profiles := &playbackConcreteProfileReader{err: errors.New("GET must not read stored profile")}
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, newService(profiles), nil)

		rec := request(rt, http.MethodGet, "/Items/"+itemID.Dashed()+"/PlaybackInfo?DeviceId=query-device", "", "", "")
		assertStatus(t, rec, http.StatusOK)
		assertConcretePlaybackResponse(t, rec.Body.Bytes(), itemID)
		if profiles.calls != 0 {
			t.Fatalf("GET loaded %d stored DeviceProfile values, want none", profiles.calls)
		}
	})

	t.Run("POST reads the stored profile when the body omits one", func(t *testing.T) {
		profiles := &playbackConcreteProfileReader{fallback: directProfile}
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, newService(profiles), nil)

		rec := request(rt, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", `{"DeviceId":"stored-device"}`, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		assertConcretePlaybackResponse(t, rec.Body.Bytes(), itemID)
		if profiles.calls != 1 || profiles.lastDeviceID != "stored-device" {
			t.Fatalf("POST stored DeviceProfile calls = %d for %q, want one stored-device lookup", profiles.calls, profiles.lastDeviceID)
		}
	})

	t.Run("POST without an explicit or stored profile returns raw assembled sources", func(t *testing.T) {
		profiles := &playbackConcreteProfileReader{}
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, newRawSourceService(profiles), nil)

		rec := request(rt, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", `{"DeviceId":"unknown-device"}`, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		assertRawAssembledPlaybackResponse(t, rec.Body.Bytes(), itemID)
		if profiles.calls != 1 || profiles.lastDeviceID != "unknown-device" {
			t.Fatalf("POST absent DeviceProfile calls = %d for %q, want one unknown-device lookup", profiles.calls, profiles.lastDeviceID)
		}
	})

	t.Run("POST with an explicit empty profile negotiates instead of returning raw sources", func(t *testing.T) {
		profiles := &playbackConcreteProfileReader{err: errors.New("explicit empty profile must not read stored profile")}
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, newRawSourceService(profiles), nil)

		body := `{"DeviceId":"explicit-empty-device","DeviceProfile":{"DirectPlayProfiles":[],"TranscodingProfiles":[]}}`
		rec := request(rt, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		var response playback.PlaybackInfoResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode explicit-empty PlaybackInfo response: %v; body=%s", err, rec.Body.Bytes())
		}
		if response.ErrorCode != playback.PlaybackInfoNoCompatibleStream || len(response.MediaSources) != 0 {
			t.Fatalf("explicit-empty DeviceProfile response = %#v, want NoCompatibleStream without raw sources", response)
		}
		if profiles.calls != 0 {
			t.Fatalf("POST explicit-empty DeviceProfile loaded %d stored profiles, want none", profiles.calls)
		}
	})

	t.Run("POST applies an explicit body profile without stored lookup", func(t *testing.T) {
		profiles := &playbackConcreteProfileReader{err: errors.New("explicit POST profile must not read stored profile")}
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, newService(profiles), nil)

		body := `{"DeviceId":"body-device","DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mp4","VideoCodec":"h264","AudioCodec":"aac"}]}}`
		rec := request(rt, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		assertConcretePlaybackResponse(t, rec.Body.Bytes(), itemID)
		if profiles.calls != 0 {
			t.Fatalf("POST with explicit DeviceProfile loaded %d stored profiles, want none", profiles.calls)
		}
	})
}

// PlaybackInfo is an item-bearing endpoint: its source snapshot must observe
// the same selected-user visibility policy as the item routes. In particular,
// checking only source assembly would disclose another root's physical path.
func TestPlaybackInfoRoutesScopePrimaryAndAlternateSourcesToSelectedUser(t *testing.T) {
	fixture := newPlaybackInfoAccessFixture(t)
	for _, tc := range []struct {
		name, method, actor string
		status              int
		wantSourceIDs       []dto.UUID
	}{
		{name: "GET assembly-only hides inaccessible primary", method: http.MethodGet, actor: "other", status: http.StatusNotFound},
		{name: "POST hides inaccessible primary", method: http.MethodPost, actor: "other", status: http.StatusNotFound},
		{name: "GET assembly-only retains allowed primary but filters inaccessible alternate", method: http.MethodGet, actor: "allowed", status: http.StatusOK, wantSourceIDs: []dto.UUID{fixture.primary.ID}},
		{name: "POST retains allowed primary but filters inaccessible alternate", method: http.MethodPost, actor: "allowed", status: http.StatusOK, wantSourceIDs: []dto.UUID{fixture.primary.ID}},
		{name: "GET administrator retains both roots", method: http.MethodGet, actor: "admin", status: http.StatusOK, wantSourceIDs: []dto.UUID{fixture.primary.ID, fixture.alternate.ID}},
		{name: "POST administrator retains both roots", method: http.MethodPost, actor: "admin", status: http.StatusOK, wantSourceIDs: []dto.UUID{fixture.primary.ID, fixture.alternate.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := fixture.call(tc.method, fixture.primary.ID, tc.actor)
			assertStatus(t, recorder, tc.status)
			if tc.status == http.StatusNotFound {
				for _, forbidden := range []string{*fixture.primary.Path, *fixture.alternate.Path, "MediaSources", "mediaSources"} {
					if strings.Contains(recorder.Body.String(), forbidden) {
						t.Fatalf("inaccessible PlaybackInfo response leaked %q: %s", forbidden, recorder.Body.String())
					}
				}
				return
			}

			var response playback.PlaybackInfoResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, recorder.Body.String())
			}
			got := make(map[dto.UUID]string, len(response.MediaSources))
			for _, source := range response.MediaSources {
				id, err := dto.ParseUUID(source.ID)
				if err != nil {
					t.Fatalf("decode source ID %q: %v", source.ID, err)
				}
				got[id] = source.Path
			}
			if len(got) != len(tc.wantSourceIDs) {
				t.Fatalf("PlaybackInfo source IDs = %#v, want %v", got, tc.wantSourceIDs)
			}
			for _, id := range tc.wantSourceIDs {
				path, found := got[id]
				if !found {
					t.Fatalf("PlaybackInfo omitted allowed source %s; got %#v", id, got)
				}
				wantPath := *fixture.primary.Path
				if id == fixture.alternate.ID {
					wantPath = *fixture.alternate.Path
				}
				if path != wantPath {
					t.Errorf("source %s path = %q, want %q", id, path, wantPath)
				}
			}
		})
	}
}

func TestLiveStreamRoutesUseConcreteApplicationWithoutProvider(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{}), nil)

	opened := request(rt, http.MethodPost, "/LiveStreams/Open", `{"OpenToken":"unavailable"}`, "application/json", "")
	assertStatus(t, opened, http.StatusInternalServerError)
	if got := opened.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("OpenLiveStream Content-Type = %q, want stock plain-text type", got)
	}
	if got := opened.Body.String(); got != "Error processing request." {
		t.Errorf("OpenLiveStream body = %q, want stock error without a media source", got)
	}

	closed := request(rt, http.MethodPost, "/LiveStreams/Close?liveStreamId=missing-live-stream", "", "", "")
	assertStatus(t, closed, http.StatusNoContent)
	if got := closed.Header().Get("Content-Type"); got != "" {
		t.Errorf("CloseLiveStream Content-Type = %q, want none", got)
	}
	if got := closed.Body.String(); got != "" {
		t.Errorf("CloseLiveStream body = %q, want empty", got)
	}
}

// The stock fixture's probe facts are authoritative: a persisted ColorRange
// is deliberately omitted from PlaybackInfo wire output, while exact posted
// profiles choose their stock delivery method and URL fact ordering.
func TestPlaybackInfoFreshCorpusProfilesPreserveSourceProvenanceAndNegotiation(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f4")
	container, path := "mkv", "/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv"
	bitrate := 899439
	videoBitrate, width, height, bitDepth, refFrames := 707439, 320, 180, 8, 1
	audioBitrate, audioChannels, sampleRate := 192000, 1, 48000
	level := float64(1)
	frameRate := float32(24)
	profile, pixelFormat, channelLayout, colorRange := "Simple Profile", "yuv420p", "mono", "tv"
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{{
			ID: itemID, Path: &path, Container: &container, TotalBitrate: &bitrate,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", BitRate: &videoBitrate, Width: &width, Height: &height, BitDepth: &bitDepth, RefFrames: &refFrames, AverageFrameRate: &frameRate, RealFrameRate: &frameRate, Level: &level, Profile: &profile, PixelFormat: &pixelFormat, ColorRange: &colorRange},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", BitRate: &audioBitrate, Channels: &audioChannels, SampleRate: &sampleRate, ChannelLayout: &channelLayout, IsDefault: true, Profile: playbackString("LC")},
			},
		}}},
		Permissions: playbackConcretePermissionReader{},
		Network:     playbackConcreteNetwork{},
		NewPlaySessionID: func() string {
			return "test-only-session"
		},
	})

	requestPlayback := func(method, body string) map[string]json.RawMessage {
		rt := router.New()
		handlers.RegisterPlaybackInfo(rt, service, nil)
		rec := request(rt, method, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
		assertStatus(t, rec, http.StatusOK)
		return decodeJSONObject(t, rec.Body.Bytes())
	}
	assertSourceProvenance := func(t *testing.T, body map[string]json.RawMessage) playback.MediaSourceInfo {
		t.Helper()
		var response playback.PlaybackInfoResponse
		if encoded, err := json.Marshal(body); err != nil {
			t.Fatalf("re-encode PlaybackInfo body: %v", err)
		} else if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatalf("decode PlaybackInfo body: %v", err)
		}
		if response.PlaySessionID == "" || len(response.MediaSources) != 1 {
			t.Fatalf("PlaybackInfo session/sources = %#v, want a session and one source", response)
		}
		if response.MediaSources[0].MediaStreams[0].ColorRange != nil {
			t.Errorf("video ColorRange = %q, want wire omission despite persisted probe fact", *response.MediaSources[0].MediaStreams[0].ColorRange)
		}
		var raw struct {
			MediaSources []struct {
				MediaStreams []map[string]json.RawMessage `json:"MediaStreams"`
			} `json:"MediaSources"`
		}
		if encoded, err := json.Marshal(body); err != nil {
			t.Fatalf("re-encode raw PlaybackInfo body: %v", err)
		} else if err := json.Unmarshal(encoded, &raw); err != nil {
			t.Fatalf("decode raw PlaybackInfo body: %v", err)
		}
		if _, exists := raw.MediaSources[0].MediaStreams[0]["ColorRange"]; exists {
			t.Errorf("raw video stream unexpectedly emitted ColorRange: %s", body["MediaSources"])
		}
		return response.MediaSources[0]
	}

	t.Run("GET remains raw and does not negotiate a profile", func(t *testing.T) {
		source := assertSourceProvenance(t, requestPlayback(http.MethodGet, ""))
		if !source.SupportsDirectPlay || !source.SupportsDirectStream || !source.SupportsTranscoding || source.TranscodingURL != "" {
			t.Errorf("raw GET source delivery = %#v, want unchanged source capabilities without URL", source)
		}
	})

	for _, tc := range []struct {
		name, fixture, wantStaticQuery string
		wantDirectStream               bool
		wantTranscoding                bool
	}{
		{
			name:            "Chrome",
			fixture:         "../../playback/testdata/DeviceProfile-Chrome.json",
			wantStaticQuery: "VideoCodec=av1,hevc,h264,vp9&AudioCodec=aac,opus,flac&AudioStreamIndex=1&VideoBitrate=119808000&AudioBitrate=192000&MaxFramerate=24&SegmentContainer=mp4&MinSegments=2&TranscodingMaxAudioChannels=2&RequireAvc=false&EnableAudioVbrEncoding=true&mpeg4-level=1&mpeg4-videobitdepth=8&mpeg4-profile=simpleprofile&av1-profile=main&av1-rangetype=SDR,HDR10,HLG&av1-level=19&vp9-rangetype=SDR,HDR10,HLG&hevc-profile=main,main10&hevc-rangetype=SDR,HDR10,HLG&hevc-level=183&hevc-deinterlace=true&h264-profile=high,main,baseline,constrainedbaseline,high10&h264-rangetype=SDR&h264-level=52&h264-deinterlace=true&TranscodeReasons=ContainerNotSupported,VideoCodecNotSupported,AudioCodecNotSupported",
			wantTranscoding: true,
		},
		{
			name:            "LowBandwidth",
			fixture:         "../../playback/testdata/DeviceProfile-LowBandwidth.json",
			wantStaticQuery: "VideoCodec=h264,hevc&AudioCodec=aac,mp3,ac3&AudioStreamIndex=1&VideoBitrate=64000&AudioBitrate=128000&MaxFramerate=24&SegmentContainer=ts&TranscodingMaxAudioChannels=6&RequireAvc=false&EnableAudioVbrEncoding=true&mpeg4-level=1&mpeg4-videobitdepth=8&mpeg4-profile=simpleprofile&TranscodeReasons=VideoCodecNotSupported,AudioCodecNotSupported,ContainerBitrateExceedsLimit",
			wantTranscoding: true,
		},
		{
			name:             "DirectMedia",
			fixture:          "../../playback/testdata/DeviceProfile-DirectMedia.json",
			wantDirectStream: true,
		},
	} {
		t.Run("POST "+tc.name+" profile", func(t *testing.T) {
			profile, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read %s: %v", tc.fixture, err)
			}
			source := assertSourceProvenance(t, requestPlayback(http.MethodPost, `{"DeviceId":"fixture-device","DeviceProfile":`+string(profile)+`}`))
			if source.SupportsDirectStream != tc.wantDirectStream || source.SupportsTranscoding != tc.wantTranscoding {
				t.Errorf("%s delivery flags = directStream:%t transcoding:%t, want %t/%t", tc.name, source.SupportsDirectStream, source.SupportsTranscoding, tc.wantDirectStream, tc.wantTranscoding)
			}
			if tc.wantStaticQuery == "" {
				if source.TranscodingURL != "" {
					t.Errorf("%s TranscodingUrl = %q, want absent for direct media", tc.name, source.TranscodingURL)
				}
				return
			}
			if source.TranscodingURL == "" {
				t.Fatalf("%s TranscodingUrl is absent", tc.name)
			}
			if got := playbackStaticURLQuery(source.TranscodingURL); got != tc.wantStaticQuery {
				t.Errorf("%s static TranscodingUrl query = %q\nwant %q", tc.name, got, tc.wantStaticQuery)
			}
		})
	}
}

// TestPlaybackInfoFreshCorpusProfilesPreserveDirectPlayFields pins the stock
// direct-play contract for a file source that no posted profile negotiated
// away: SupportsDirectPlay, SupportsDirectStream, and SupportsTranscoding
// are true; Protocol is File; Path is the server-local path; TranscodingUrl is
// absent. This matches the stock PlaybackInfo response that lets a client
// decide whether it can direct-play a file it cannot reach by filesystem path.
func TestPlaybackInfoFreshCorpusProfilesPreserveDirectPlayFields(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f5")
	container, path := "mkv", "/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv"
	bitrate := 899439
	videoBitrate, width, height, bitDepth, refFrames := 707439, 320, 180, 8, 1
	audioBitrate, audioChannels, sampleRate := 192000, 1, 48000
	level := float64(1)
	frameRate := float32(24)
	profile, pixelFormat, channelLayout, colorRange := "Simple Profile", "yuv420p", "mono", "tv"
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{{
			ID: itemID, Path: &path, Container: &container, TotalBitrate: &bitrate,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", BitRate: &videoBitrate, Width: &width, Height: &height, BitDepth: &bitDepth, RefFrames: &refFrames, AverageFrameRate: &frameRate, RealFrameRate: &frameRate, Level: &level, Profile: &profile, PixelFormat: &pixelFormat, ColorRange: &colorRange},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", BitRate: &audioBitrate, Channels: &audioChannels, SampleRate: &sampleRate, ChannelLayout: &channelLayout, IsDefault: true, Profile: playbackString("LC")},
			},
		}}},
		Permissions: playbackConcretePermissionReader{},
		Network:     playbackConcreteNetwork{},
		NewPlaySessionID: func() string {
			return "test-only-session"
		},
	})

	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	rec := request(rt, http.MethodGet, "/Items/"+itemID.Dashed()+"/PlaybackInfo", "", "application/json", "")
	assertStatus(t, rec, http.StatusOK)

	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, rec.Body.String())
	}
	if len(response.MediaSources) != 1 {
		t.Fatalf("MediaSources count = %d, want 1", len(response.MediaSources))
	}
	source := response.MediaSources[0]
	if !source.SupportsDirectPlay || !source.SupportsDirectStream || !source.SupportsTranscoding {
		t.Errorf("direct-play fields = directPlay:%t directStream:%t transcoding:%t, want all true", source.SupportsDirectPlay, source.SupportsDirectStream, source.SupportsTranscoding)
	}
	if source.Protocol != playback.MediaProtocolFile {
		t.Errorf("Protocol = %v, want File", source.Protocol)
	}
	if source.Path != path {
		t.Errorf("Path = %q, want server-local path %q", source.Path, path)
	}
	if source.TranscodingURL != "" {
		t.Errorf("TranscodingUrl = %q, want absent for direct-play source", source.TranscodingURL)
	}
	// The struct-level check cannot distinguish an absent key from a present
	// empty-string key; assert the raw wire body omits TranscodingUrl.
	if strings.Contains(rec.Body.String(), `"TranscodingUrl"`) {
		t.Errorf("raw body contains TranscodingUrl key, want absent: %s", rec.Body.String())
	}
}

func playbackStaticURLQuery(value string) string {
	_, query, found := strings.Cut(value, "?")
	if !found {
		return ""
	}
	parts := strings.Split(query, "&")
	stable := parts[:0]
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		switch name {
		case "DeviceId", "MediaSourceId", "PlaySessionId", "ApiKey", "Tag":
			continue
		}
		stable = append(stable, part)
	}
	return strings.Join(stable, "&")
}

func playbackString(value string) *string { return &value }

func assertConcretePlaybackResponse(t *testing.T, body []byte, itemID dto.UUID) {
	t.Helper()
	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode concrete PlaybackInfo response: %v; body=%s", err, body)
	}
	if response.PlaySessionID != "concrete-play-session" || response.ErrorCode != "" || len(response.MediaSources) != 1 {
		t.Fatalf("concrete PlaybackInfo response = %#v, want one playable source with the composed session", response)
	}
	source := response.MediaSources[0]
	if source.ID != itemID.String() || source.Path != "/library/concrete.mp4" || !source.SupportsDirectPlay {
		t.Errorf("concrete PlaybackInfo media source = %#v, want the assembled direct-play source", source)
	}
}

func assertRawAssembledPlaybackResponse(t *testing.T, body []byte, requestedID dto.UUID) {
	t.Helper()
	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode raw assembled PlaybackInfo response: %v; body=%s", err, body)
	}
	if response.PlaySessionID == "" || response.ErrorCode != "" || len(response.MediaSources) != 2 {
		t.Fatalf("raw assembled PlaybackInfo response = %#v, want session ID and both sources without a negotiation error", response)
	}
	wants := []struct {
		id, path, name, container, videoCodec, audioCodec, language, attachment string
		size, runtime                                                           int64
		bitrate                                                                 int
	}{
		{requestedID.String(), "/library/raw-unmatchable.mkv", "raw-unmatchable", "mkv", "vp9", "opus", "und", "raw-unmatchable.srt", 7_000_001, 8_000_001, 9_000_001},
		{"000000000000000000000000000004f3", "/library/raw-compatible.mp4", "raw-compatible", "mp4", "h264", "aac", "en", "raw-compatible.srt", 7_000_002, 8_000_002, 9_000_002},
	}
	for index, want := range wants {
		source := response.MediaSources[index]
		if source.ID != want.id || source.Path != want.path || source.Name != want.name || source.Container != want.container || source.Size == nil || *source.Size != want.size || source.RunTimeTicks == nil || *source.RunTimeTicks != want.runtime || source.Bitrate == nil || *source.Bitrate != want.bitrate {
			t.Errorf("raw source[%d] = %#v, want assembled source fields for %q", index, source, want.path)
		}
		if !source.SupportsTranscoding || !source.SupportsDirectStream || !source.SupportsDirectPlay || source.TranscodingURL != "" || source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != 1 || source.DefaultSubtitleStreamIndex != nil {
			t.Errorf("raw source[%d] negotiation state = %#v, want assembly capability, default audio index, and no delivery decisions", index, source)
		}
		if len(source.MediaStreams) != 2 || source.MediaStreams[0].Index != 0 || source.MediaStreams[0].Type != playback.StreamTypeVideo || source.MediaStreams[0].Codec != want.videoCodec || source.MediaStreams[1].Index != 1 || source.MediaStreams[1].Type != playback.StreamTypeAudio || source.MediaStreams[1].Codec != want.audioCodec || source.MediaStreams[1].Language == nil || *source.MediaStreams[1].Language != want.language || !source.MediaStreams[1].IsDefault {
			t.Errorf("raw source[%d] streams = %#v, want untouched assembled streams", index, source.MediaStreams)
		}
		if len(source.MediaAttachments) != 1 || source.MediaAttachments[0].Index != 2 || source.MediaAttachments[0].Codec != "subrip" || source.MediaAttachments[0].FileName != want.attachment || source.MediaAttachments[0].MimeType != "application/x-subrip" || source.MediaAttachments[0].DeliveryURL != "" {
			t.Errorf("raw source[%d] attachments = %#v, want untouched assembled attachments", index, source.MediaAttachments)
		}
	}
}

type playbackConcreteSourceReader struct{ records []playback.MediaSourceRecord }

func (reader playbackConcreteSourceReader) MediaSourceRecords(context.Context, dto.UUID) ([]playback.MediaSourceRecord, error) {
	return reader.records, nil
}

type playbackConcreteProfileReader struct {
	fallback     *playback.DeviceProfile
	err          error
	calls        int
	lastClient   string
	lastDeviceID string
}

func (reader *playbackConcreteProfileReader) DeviceProfile(_ context.Context, client, deviceID string) (*playback.DeviceProfile, error) {
	reader.calls++
	reader.lastClient = client
	reader.lastDeviceID = deviceID
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.fallback, nil
}

type playbackConcretePermissionReader struct{}

func (playbackConcretePermissionReader) PlaybackPermissions(context.Context, dto.UUID) (playback.PlaybackPermissions, error) {
	return playback.PlaybackPermissions{EnableAudioTranscoding: true, EnableVideoTranscoding: true, EnableRemuxing: true}, nil
}

type playbackConcreteNetwork struct{}

func (playbackConcreteNetwork) IsLAN(netip.Addr) bool { return true }

type playbackResolver struct{ identity auth.Identity }

func (r playbackResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return r.identity, nil
}

type playbackEvaluator struct{}

func (playbackEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}

type playbackUsers struct{}

func (playbackUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id}, nil
}

type playbackInfoAccessFixture struct {
	routes             *router.Router
	primary, alternate item.Item
}

func newPlaybackInfoAccessFixture(t *testing.T) playbackInfoAccessFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open PlaybackInfo access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close PlaybackInfo access store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	allowedRootID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b1")
	otherRootID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b2")
	allowedUserID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b3")
	otherUserID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b4")
	adminUserID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b5")
	primaryID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b6")
	alternateID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004b7")
	allowedRootName, otherRootName := "allowed root", "other root"
	primaryName, alternateName := "allowed movie", "other alternate"
	primaryPath, alternatePath, container := "/private/allowed-root/primary.mkv", "/private/other-root/alternate.mkv", "mkv"
	allowedRoot := item.Item{ID: allowedRootID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &allowedRootName, SortName: &allowedRootName}
	otherRoot := item.Item{ID: otherRootID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &otherRootName, SortName: &otherRootName}
	primary := item.Item{ID: primaryID, Kind: item.KindMovie, ParentID: &allowedRootID, Name: &primaryName, SortName: &primaryName, Path: &primaryPath, Container: &container, LinkedChildren: []item.LinkedChildRef{{ItemID: alternateID, Type: item.LinkedChildTypeLinkedAlternateVersion}}}
	alternate := item.Item{ID: alternateID, Kind: item.KindMovie, ParentID: &otherRootID, Name: &alternateName, SortName: &alternateName, Path: &alternatePath, Container: &container}
	for _, value := range []item.Item{allowedRoot, otherRoot, alternate, primary} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed PlaybackInfo access item %s: %v", value.ID, err)
		}
	}
	users := playbackInfoAccessUsers{
		allowedUserID: {ID: allowedUserID, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + allowedRootID.Dashed() + `"]}`)},
		otherUserID:   {ID: otherUserID, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + otherRootID.Dashed() + `"]}`)},
		adminUserID:   {ID: adminUserID, Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":[]}`)},
	}
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: items,
		Access:  store.NewPlaybackItemAccessReader(items, users),
		NewPlaySessionID: func() string {
			return "access-filtered-session"
		},
	})
	identities := map[string]auth.Identity{
		"allowed": {Authenticated: true, UserID: allowedUserID},
		"other":   {Authenticated: true, UserID: otherUserID},
		"admin":   {Authenticated: true, Administrator: true, UserID: adminUserID},
	}
	authorizer := middleware.NewAuthorizer(playbackInfoAccessResolver{identities: identities}, playbackEvaluator{}, users)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterPlaybackInfo(routes, service, users)
	return playbackInfoAccessFixture{routes: routes, primary: primary, alternate: alternate}
}

func (fixture playbackInfoAccessFixture) call(method string, itemID dto.UUID, actor string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	request := httptest.NewRequest(method, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body)
	request.Header.Set("X-PlaybackInfo-Access-Actor", actor)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.routes.ServeHTTP(recorder, request)
	return recorder
}

type playbackInfoAccessUsers map[dto.UUID]store.User

func (users playbackInfoAccessUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	user, found := users[id]
	if !found {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

type playbackInfoAccessResolver struct{ identities map[string]auth.Identity }

func (resolver playbackInfoAccessResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	return resolver.identities[request.Header.Get("X-PlaybackInfo-Access-Actor")], nil
}

func playbackTestUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}

func decodeJSONObject(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON object: %v; body=%s", err, body)
	}
	return value
}

// TestPlaybackInfoNonSDRVideoRangeOmitsTranscodingUrl verifies the endpoint
// response for a non-SDR source omits TranscodingUrl when the encoder cannot
// transcode it. A client that cannot direct play the source receives
// SupportsTranscoding: false and no stream URL, instead of a URL that errors.
func TestPlaybackInfoNonSDRVideoRangeOmitsTranscodingUrl(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f6")
	container, path := "mkv", "/library/hdr10-source.mkv"
	transfer := "smpte2084"
	bitrate := 899439
	videoBitrate, audioBitrate := 707439, 192000
	width, height := 1920, 1080
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{{
			ID: itemID, Path: &path, Container: &container, TotalBitrate: &bitrate,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc", BitRate: &videoBitrate, Width: &width, Height: &height, ColorTransfer: &transfer},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", BitRate: &audioBitrate, IsDefault: true},
			},
		}}},
		Permissions: playbackConcretePermissionReader{},
		Network:     playbackConcreteNetwork{},
		NewPlaySessionID: func() string {
			return "hdr-test-session"
		},
	})
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, service, nil)
	body := `{"DeviceId":"hdr-device","DeviceProfile":{"DirectPlayProfiles":[],"TranscodingProfiles":[{"Type":"Video","Container":"ts","VideoCodec":"h264","AudioCodec":"aac","Protocol":"http","Context":"streaming"}]}}`
	rec := request(rt, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
	assertStatus(t, rec, http.StatusOK)

	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, rec.Body.Bytes())
	}
	if len(response.MediaSources) != 1 {
		t.Fatalf("MediaSources count = %d, want 1", len(response.MediaSources))
	}
	source := response.MediaSources[0]
	if source.SupportsTranscoding {
		t.Error("SupportsTranscoding = true, want false for a non-SDR source the encoder cannot transcode")
	}
	if source.TranscodingURL != "" {
		t.Errorf("TranscodingUrl = %q, want empty for a non-SDR source", source.TranscodingURL)
	}
	if strings.Contains(rec.Body.String(), `"TranscodingUrl"`) {
		t.Errorf("raw response body contains TranscodingUrl key, want absent: %s", rec.Body.String())
	}
}
