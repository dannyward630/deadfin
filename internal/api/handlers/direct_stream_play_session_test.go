// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/auth/playgrant"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// tokenResolver authenticates a request whose Authorization header carries
// Token="user-token", and leaves every other request unauthenticated.
type tokenResolver struct{ userID dto.UUID }

func (resolver tokenResolver) Resolve(_ context.Context, r *http.Request) (auth.Identity, error) {
	if strings.Contains(r.Header.Get("Authorization"), `Token="user-token"`) {
		return auth.Identity{Authenticated: true, UserID: resolver.userID, Token: "user-token"}, nil
	}
	return auth.Identity{}, nil
}

// authenticatedEvaluator admits any authenticated identity and refuses the rest.
type authenticatedEvaluator struct{}

func (authenticatedEvaluator) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if request.Anonymous || request.Identity.Authenticated {
		return auth.DecisionAllowed, nil
	}
	return auth.DecisionUnauthenticated, nil
}

type grantPlaybackInfoService struct {
	playSessionID string
	sourceID      string
}

func (service grantPlaybackInfoService) PlaybackInfo(_ context.Context, request playback.PlaybackInfoRequest) (playback.PlaybackInfoResponse, error) {
	return playback.PlaybackInfoResponse{PlaySessionID: service.playSessionID, MediaSources: []playback.MediaSourceInfo{{ID: service.sourceID}}}, nil
}

func (grantPlaybackInfoService) OpenLiveStream(context.Context, playback.OpenLiveStreamRequest) (playback.LiveStreamResponse, error) {
	return playback.LiveStreamResponse{}, nil
}

func (grantPlaybackInfoService) CloseLiveStream(context.Context, string) error { return nil }

// TestDirectStreamAcceptsPlaySessionGrantFromPlaybackInfo covers D-54 with the
// URL that stock Swiftfin and SharkParty build for AVPlayer and VLCKit
// (MediaPlayerItem+Build.swift): no header and no ApiKey, only the
// PlaySessionId that the user's PlaybackInfo returned.
func TestDirectStreamAcceptsPlaySessionGrantFromPlaybackInfo(t *testing.T) {
	userID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000801")
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000802")
	otherItemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000803")
	const playSessionID = "4d0c3c4e5f6a4b7c8d9e0f1a2b3c4d5e"

	grants := playgrant.New(clock.NewFake(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)))
	source := &globalAPIKeyUniversalAudioService{
		source: parityAudioSource(t, "granted.mp3", "granted-source", "mp3", []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}}),
	}
	authorizer := middleware.NewAuthorizer(tokenResolver{userID: userID}, authenticatedEvaluator{}, playbackUsers{}).WithPlaySessionGrants(grants)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterPlaybackInfo(routes, grantPlaybackInfoService{playSessionID: playSessionID, sourceID: itemID.String()}, playbackUsers{}, grants)
	handlers.RegisterDirectVideoStreams(routes, source)
	handlers.RegisterDirectAudioStreams(routes, source)

	request := func(method, path string, authorization string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
		}
		record := httptest.NewRecorder()
		routes.ServeHTTP(record, r)
		return record
	}
	swiftfinURL := func(item dto.UUID, query string) string {
		return "/Videos/" + item.String() + "/stream?static=true&tag=8d1f&" + query
	}

	if got := request(http.MethodGet, swiftfinURL(itemID, "PlaySessionId="+playSessionID+"&MediaSourceId="+itemID.String()), "").Code; got != http.StatusUnauthorized {
		t.Fatalf("stream before PlaybackInfo status = %d, want 401", got)
	}
	if got := request(http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", `MediaBrowser Client="Swiftfin", Device="iPhone", DeviceId="swiftfin-device", Version="1.3.0", Token="user-token"`).Code; got != http.StatusOK {
		t.Fatalf("PlaybackInfo status = %d, want 200", got)
	}

	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{"Swiftfin URL with only PlaySessionId", swiftfinURL(itemID, "PlaySessionId="+playSessionID+"&MediaSourceId="+itemID.String()), http.StatusOK},
		{"PlaySessionId without MediaSourceId", swiftfinURL(itemID, "PlaySessionId="+playSessionID), http.StatusOK},
		{"audio stream of the granted item", "/Audio/" + itemID.String() + "/stream.mp3?static=true&PlaySessionId=" + playSessionID, http.StatusOK},
		{"granted PlaySessionId on another item", swiftfinURL(otherItemID, "PlaySessionId="+playSessionID+"&MediaSourceId="+otherItemID.String()), http.StatusUnauthorized},
		{"granted PlaySessionId with another source", swiftfinURL(itemID, "PlaySessionId="+playSessionID+"&MediaSourceId="+otherItemID.String()), http.StatusUnauthorized},
		{"unknown PlaySessionId", swiftfinURL(itemID, "PlaySessionId=ffffffffffffffffffffffffffffffff&MediaSourceId="+itemID.String()), http.StatusUnauthorized},
		{"no PlaySessionId", swiftfinURL(itemID, "MediaSourceId="+itemID.String()), http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			source.selectedUser = dto.NilUUID
			record := request(http.MethodGet, test.path, "")
			if record.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%q", record.Code, test.want, record.Body.String())
			}
			if test.want == http.StatusOK && source.selectedUser != userID {
				t.Fatalf("source lookup user = %v, want the PlaybackInfo user %v so library access applies", source.selectedUser, userID)
			}
		})
	}
}
