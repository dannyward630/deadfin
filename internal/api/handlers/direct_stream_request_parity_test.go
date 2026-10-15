// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestDirectAudioRequestContainerRoutePrecedenceAndBlankQuery keeps output
// container selection at the direct-route boundary: the route suffix wins over
// a conflicting query value, the bare route accepts its query value, and a
// present-but-blank container is invalid rather than absent.
func TestDirectAudioRequestContainerRoutePrecedenceAndBlankQuery(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000751")
	const contents = "direct-request-container-source"
	source := parityAudioSource(t, "request-container.mp3", contents, "mp3", nil)
	routes := parityAudioRoutes(auth.Identity{}, paritySourceService{byID: map[string]playback.MediaSourceInfo{"": source}})

	for _, tc := range []struct {
		name            string
		path            string
		wantStatus      int
		wantContentType string
	}{
		{
			name:            "route suffix takes precedence over Container query",
			path:            "/Audio/" + itemID.Dashed() + "/stream.mp3?Static=true&Container=flac",
			wantStatus:      http.StatusOK,
			wantContentType: "audio/mpeg",
		},
		{
			name:            "bare route uses Container query",
			path:            "/Audio/" + itemID.Dashed() + "/stream?Static=true&Container=flac",
			wantStatus:      http.StatusOK,
			wantContentType: "audio/flac",
		},
		{
			name:       "whitespace-only Container query is bad request",
			path:       "/Audio/" + itemID.Dashed() + "/stream?Static=true&Container=%20%20",
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := parityRequest(routes, http.MethodGet, tc.path)

			if record.Code != tc.wantStatus {
				t.Errorf("GET %s status = %d, want %d; body=%q", tc.path, record.Code, tc.wantStatus, record.Body.String())
			}
			if tc.wantContentType != "" {
				if got := record.Header().Get("Content-Type"); got != tc.wantContentType {
					t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.wantContentType)
				}
				if got := record.Body.String(); got != contents {
					t.Errorf("GET %s body = %q, want direct source bytes", tc.path, got)
				}
			}
		})
	}
}

// TestDirectAudioRequestPackedStaticWhitespaceOverridesScalar pins the legacy
// packed fourth slot's literal boolean parsing. A non-literal value still wins
// over scalar Static=true, but it is false and must prevent direct delivery.
func TestDirectAudioRequestPackedStaticWhitespaceOverridesScalar(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000752")
	const contents = "packed-static-whitespace-source"
	source := parityAudioSource(t, "packed-static.mp3", contents, "mp3", nil)
	routes := parityAudioRoutes(auth.Identity{}, paritySourceService{byID: map[string]playback.MediaSourceInfo{"": source}})

	record := parityRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/stream?Static=true&Params=;;;%20true%20")

	if record.Code != http.StatusNotImplemented {
		t.Errorf("GET direct audio with whitespace-packed Static status = %d, want %d; body=%q", record.Code, http.StatusNotImplemented, record.Body.String())
	}
	if got := record.Body.String(); got == contents {
		t.Errorf("GET direct audio with whitespace-packed Static returned direct source bytes")
	}
}
