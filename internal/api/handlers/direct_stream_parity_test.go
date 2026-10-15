// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestDirectAudioPackedParamsOverrideQuerySourceAndStatic pins the legacy
// packed request slots that clients use when a scalar query conflicts with the
// route's source and static selections. The delivered bytes prove both slots
// are applied before the file response is opened.
func TestDirectAudioPackedParamsOverrideQuerySourceAndStatic(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000701")
	querySource := parityAudioSource(t, "query.mp3", "query-source", "mp3", nil)
	packedSource := parityAudioSource(t, "packed.mp3", "packed-source", "mp3", nil)
	routes := parityAudioRoutes(auth.Identity{}, paritySourceService{byID: map[string]playback.MediaSourceInfo{
		"query-source":  querySource,
		"packed-source": packedSource,
	}})

	record := parityRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/stream?Static=false&MediaSourceId=query-source&Params=;;packed-source;true")
	if record.Code != http.StatusOK {
		t.Fatalf("packed direct audio status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got := record.Body.String(); got != "packed-source" {
		t.Errorf("packed direct audio body = %q, want source selected by Params slot 2", got)
	}
}

// TestDirectAudioContainerControlsDeliveryAndRejectsMalformedValues makes the
// output format a request contract rather than an incidental source property.
// Invalid containers must not reach a file response, so their media headers
// and body must remain absent.
func TestDirectAudioContainerControlsDeliveryAndRejectsMalformedValues(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000702")
	const contents = "container-validation-media"
	source := parityAudioSource(t, "source.mp3", contents, "mp3", nil)
	routes := parityAudioRoutes(auth.Identity{}, paritySourceService{byID: map[string]playback.MediaSourceInfo{"": source}})
	longContainer := strings.Repeat("a", 41)

	for _, tc := range []struct {
		name            string
		path            string
		wantStatus      int
		wantContentType string
	}{
		{name: "bare Container query controls MIME", path: "/Audio/" + itemID.Dashed() + "/stream?Static=true&Container=flac", wantStatus: http.StatusOK, wantContentType: "audio/flac"},
		{name: "valid suffix controls MIME", path: "/Audio/" + itemID.Dashed() + "/stream.flac?Static=true", wantStatus: http.StatusOK, wantContentType: "audio/flac"},
		{name: "illegal suffix is bad request", path: "/Audio/" + itemID.Dashed() + "/stream.flac!?Static=true", wantStatus: http.StatusBadRequest},
		{name: "overlong suffix is bad request", path: "/Audio/" + itemID.Dashed() + "/stream." + longContainer + "?Static=true", wantStatus: http.StatusBadRequest},
		{name: "illegal bare Container query is bad request", path: "/Audio/" + itemID.Dashed() + "/stream?Static=true&Container=flac%21", wantStatus: http.StatusBadRequest},
		{name: "overlong bare Container query is bad request", path: "/Audio/" + itemID.Dashed() + "/stream?Static=true&Container=" + longContainer, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := parityRequest(routes, http.MethodGet, tc.path)
			if record.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d; body=%q", tc.path, record.Code, tc.wantStatus, record.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if got := record.Header().Get("Content-Type"); got != tc.wantContentType {
					t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.wantContentType)
				}
				if got := record.Body.String(); got != contents {
					t.Errorf("GET %s body = %q, want direct media bytes", tc.path, got)
				}
				return
			}
			if got := record.Header().Get("Accept-Ranges"); got != "" {
				t.Errorf("GET %s Accept-Ranges = %q, want no file response", tc.path, got)
			}
			if got := record.Body.String(); got == contents {
				t.Errorf("GET %s returned source bytes after rejecting its container", tc.path)
			}
		})
	}
}

// TestUniversalAudioResolvesRequestedUserBeforeDirectDelivery pins the same
// cross-user selection boundary as other item routes: an administrator may
// select the requested user's source access, while another user cannot probe
// that target through universal streaming.
func TestUniversalAudioResolvesRequestedUserBeforeDirectDelivery(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000703")
	administratorID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000704")
	memberID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000705")
	targetID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000706")
	targetSource := parityAudioSource(t, "target.mp3", "target-user-source", "mp3", []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}})
	memberSource := parityAudioSource(t, "member.mp3", "member-user-source", "mp3", []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}})
	service := paritySourceService{byUser: map[dto.UUID]map[string]playback.MediaSourceInfo{
		targetID: {"": targetSource},
		memberID: {"": memberSource},
	}}

	for _, tc := range []struct {
		name       string
		identity   auth.Identity
		wantStatus int
		wantBody   string
	}{
		{name: "administrator selects requested user", identity: auth.Identity{Authenticated: true, Administrator: true, UserID: administratorID}, wantStatus: http.StatusOK, wantBody: "target-user-source"},
		{name: "member cannot select another user", identity: auth.Identity{Authenticated: true, UserID: memberID}, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes := parityAudioRoutes(tc.identity, service)
			record := parityRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/universal?UserId="+targetID.Dashed()+"&Container=mp3")
			if record.Code != tc.wantStatus {
				t.Fatalf("universal user selection status = %d, want %d; body=%q", record.Code, tc.wantStatus, record.Body.String())
			}
			if tc.wantBody != "" && record.Body.String() != tc.wantBody {
				t.Errorf("administrator universal body = %q, want target user's accessible source", record.Body.String())
			}
			if tc.wantStatus == http.StatusForbidden && record.Header().Get("Accept-Ranges") != "" {
				t.Errorf("cross-user rejection exposed file response headers: Accept-Ranges=%q", record.Header().Get("Accept-Ranges"))
			}
		})
	}
}

// TestUniversalAudioCodecAndLimitsGateDirectDelivery covers the source facts
// that must keep a universal request out of direct delivery. Every equal-limit
// row is paired with its just-below boundary to defend against off-by-one
// eligibility errors.
func TestUniversalAudioCodecAndLimitsGateDirectDelivery(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000707")
	channels, sampleRate, bitDepth, bitrate := 2, 48_000, 16, 128_000
	source := parityAudioSource(t, "constrained.mp3", "constrained-source", "mp3", []playback.MediaStream{{
		Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true,
		Channels: &channels, SampleRate: &sampleRate, BitDepth: &bitDepth, BitRate: &bitrate,
	}})
	source.Bitrate = &bitrate
	identity := auth.Identity{Authenticated: true, UserID: playbackTestUUID(t, "00000000-0000-0000-0000-000000000708")}

	for _, tc := range []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "container codec suffix rejects mismatched source codec", query: "Container=mp3%7Caac", wantStatus: http.StatusNotImplemented},
		{name: "container codec suffix accepts matching source codec", query: "Container=mp3%7Cmp3", wantStatus: http.StatusOK},
		{name: "max channels below source rejects direct delivery", query: "Container=mp3&MaxAudioChannels=1", wantStatus: http.StatusNotImplemented},
		{name: "max channels equal source accepts direct delivery", query: "Container=mp3&MaxAudioChannels=2", wantStatus: http.StatusOK},
		{name: "max sample rate below source rejects direct delivery", query: "Container=mp3&MaxAudioSampleRate=47999", wantStatus: http.StatusNotImplemented},
		{name: "max sample rate equal source accepts direct delivery", query: "Container=mp3&MaxAudioSampleRate=48000", wantStatus: http.StatusOK},
		{name: "max bit depth below source rejects direct delivery", query: "Container=mp3&MaxAudioBitDepth=15", wantStatus: http.StatusNotImplemented},
		{name: "max bit depth equal source accepts direct delivery", query: "Container=mp3&MaxAudioBitDepth=16", wantStatus: http.StatusOK},
		{name: "max stream bitrate below source rejects direct delivery", query: "Container=mp3&MaxStreamingBitrate=127999", wantStatus: http.StatusNotImplemented},
		{name: "max stream bitrate equal source accepts direct delivery", query: "Container=mp3&MaxStreamingBitrate=128000", wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes := parityAudioRoutes(identity, paritySourceService{byID: map[string]playback.MediaSourceInfo{"": source}})
			record := parityRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/universal?"+tc.query)
			if record.Code != tc.wantStatus {
				t.Fatalf("GET universal audio?%s status = %d, want %d; body=%q", tc.query, record.Code, tc.wantStatus, record.Body.String())
			}
			if tc.wantStatus == http.StatusOK && record.Body.String() != "constrained-source" {
				t.Errorf("GET universal audio?%s body = %q, want direct source bytes", tc.query, record.Body.String())
			}
		})
	}
}

type paritySourceService struct {
	byID   map[string]playback.MediaSourceInfo
	byUser map[dto.UUID]map[string]playback.MediaSourceInfo
}

func (service paritySourceService) StaticSource(_ context.Context, userID, _ dto.UUID, mediaSourceID string) (playback.MediaSourceInfo, error) {
	sources := service.byID
	if service.byUser != nil {
		var found bool
		sources, found = service.byUser[userID]
		if !found {
			return playback.MediaSourceInfo{}, playback.ErrItemNotFound
		}
	}
	source, found := sources[mediaSourceID]
	if !found {
		return playback.MediaSourceInfo{}, playback.ErrMediaSourceNotFound
	}
	return source, nil
}

func parityAudioRoutes(identity auth.Identity, service playback.DirectFileSourceService) *router.Router {
	authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
	routes := router.New(authorizer.Wrap)
	handlers.RegisterDirectAudioStreams(routes, service)
	return routes
}

func parityAudioSource(t *testing.T, name, contents, container string, streams []playback.MediaStream) playback.MediaSourceInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	// A source without a bitrate counts as 40 Mbps, above the universal
	// 8 Mbps profile default.
	bitrate := 320000
	return playback.MediaSourceInfo{
		ID:                   name,
		Protocol:             playback.MediaProtocolFile,
		Path:                 path,
		Container:            container,
		Bitrate:              &bitrate,
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams:         streams,
	}
}

func parityRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	return record
}
