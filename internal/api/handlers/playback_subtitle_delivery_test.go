// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// The pure resolver test pins subtitle delivery facts. This handler test uses
// the fresh DirectMedia profile at a known-compatible source to preserve
// case-insensitive route matching and camel-response wrapper coverage.
func TestPostedPlaybackInfo_DirectMediaProfilePreservesWrapperNegotiation(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f5")
	profile, err := os.ReadFile("../../playback/testdata/DeviceProfile-DirectMedia.json")
	if err != nil {
		t.Fatalf("read DirectMedia profile: %v", err)
	}

	routes := subtitleDeliveryRoutes(itemID)
	rec := request(
		routes,
		http.MethodPost,
		"/iTeMs/"+itemID.Dashed()+"/pLaYbAcKiNfO",
		`{"DeviceId":"subtitle-device","DeviceProfile":`+string(profile)+`}`,
		"application/json",
		`application/json; profile="CamelCase"`,
	)
	assertStatus(t, rec, http.StatusOK)

	body := decodeJSONObject(t, rec.Body.Bytes())
	if _, found := body["mediaSources"]; !found {
		t.Fatalf("camel PlaybackInfo response lacks mediaSources: %s", rec.Body.Bytes())
	}
	if _, found := body["MediaSources"]; found {
		t.Fatalf("camel PlaybackInfo response unexpectedly emitted MediaSources: %s", rec.Body.Bytes())
	}

	source := subtitleDeliveryResponseSource(t, rec.Body.Bytes())
	if !source.SupportsDirectPlay || !source.SupportsDirectStream || source.TranscodingURL != "" {
		t.Errorf("DirectMedia handler source = %#v, want direct playback without a transcode URL", source)
	}
}

func TestPostedPlaybackInfo_SubtitleDeliveryProfileAbsenceAndNonmatchPreserveOmission(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004f6")
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "absent profile returns raw streams", body: `{}`},
		{name: "nonmatching embedded profile retains Encode omission", body: `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"SubtitleProfiles":[{"Format":"ass","Method":"Embed"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(subtitleDeliveryRoutes(itemID), http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", tc.body, "application/json", "")
			assertStatus(t, rec, http.StatusOK)

			source := subtitleDeliveryResponseSource(t, rec.Body.Bytes())
			if !source.SupportsDirectPlay || !source.SupportsDirectStream || source.TranscodingURL != "" {
				t.Errorf("profile %q source = %#v, want a compatible direct source without delivery projection", tc.name, source)
			}
		})
	}
}

func subtitleDeliveryRoutes(itemID dto.UUID) *router.Router {
	container, path := "mkv", "/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv"
	bitrate := 899439
	videoBitrate, width, height, bitDepth, refFrames := 707439, 320, 180, 8, 1
	audioBitrate, audioChannels, sampleRate := 192000, 1, 48000
	level, frameRate := float64(1), float32(24)
	profile, pixelFormat, channelLayout := "Simple Profile", "yuv420p", "mono"
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{{
			ID: itemID, Path: &path, Container: &container, TotalBitrate: &bitrate,
			MediaStreams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", BitRate: &videoBitrate, Width: &width, Height: &height, BitDepth: &bitDepth, RefFrames: &refFrames, AverageFrameRate: &frameRate, RealFrameRate: &frameRate, Level: &level, Profile: &profile, PixelFormat: &pixelFormat},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", BitRate: &audioBitrate, Channels: &audioChannels, SampleRate: &sampleRate, ChannelLayout: &channelLayout, IsDefault: true, Profile: playbackString("LC")},
			},
		}}},
		Permissions: playbackConcretePermissionReader{},
		Network:     playbackConcreteNetwork{},
		NewPlaySessionID: func() string {
			return "subtitle-delivery-session"
		},
	})
	routes := router.New()
	handlers.RegisterPlaybackInfo(routes, service, nil)
	return routes
}

func subtitleDeliveryResponseSource(t *testing.T, body []byte) playback.MediaSourceInfo {
	t.Helper()
	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, body)
	}
	if len(response.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo MediaSources = %d, want one; body=%s", len(response.MediaSources), body)
	}
	return response.MediaSources[0]
}
