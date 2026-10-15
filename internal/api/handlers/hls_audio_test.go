// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestHLSDynamicAudioRoutesProjectAudioOnlyWithoutChangingVideo(t *testing.T) {
	itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007ad")
	missingSource := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	missingRecord := hlsRequest(t, hlsRoutes(missingSource), http.MethodGet, "/Audio/"+itemID.Dashed()+"/master.m3u8?SegmentContainer=ts&SegmentLength=6&PlaySessionId=session", "audio-client")
	if missingRecord.Code != http.StatusBadRequest || missingSource.masterCalls != 0 {
		t.Fatalf("GET audio master without MediaSourceId = status:%d master-calls:%d, want 400 before dispatch; body=%q", missingRecord.Code, missingSource.masterCalls, missingRecord.Body.String())
	}

	for _, tc := range []struct {
		name, path string
		response   playback.HLSResponse
		request    func(*hlsStreamerStub) playback.HLSRequest
	}{
		{name: "audio main accepts packed source", path: "/Audio/" + itemID.Dashed() + "/main.m3u8?SegmentContainer=ts&SegmentLength=6&PlaySessionId=session&Params=;;packed-source", response: hlsResponse("application/x-mpegURL", "#EXTM3U\n"), request: func(s *hlsStreamerStub) playback.HLSRequest { return s.mainRequest }},
		{name: "audio segment accepts no source", path: "/Audio/" + itemID.Dashed() + "/hls1/opaque/0.ts?RuntimeTicks=0&ActualSegmentLengthTicks=1&SegmentContainer=ts&SegmentLength=6&PlaySessionId=session", response: hlsResponse("video/mp2t", "audio"), request: func(s *hlsStreamerStub) playback.HLSRequest { return s.segmentRequest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &hlsStreamerStub{masterResponse: tc.response, mainResponse: tc.response, segmentResponse: tc.response}
			record := hlsRequest(t, hlsRoutes(streamer), http.MethodGet, tc.path, "audio-client")
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%q", tc.name, record.Code, record.Body.String())
			}
			request := tc.request(streamer)
			if !request.AudioOnly {
				t.Errorf("%s request.AudioOnly = false, want true; request=%#v", tc.name, request)
			}
			if tc.name == "audio main accepts packed source" && request.MediaSourceID != "packed-source" {
				t.Errorf("audio packed source = %q, want packed-source", request.MediaSourceID)
			}
		})
	}

	video := &hlsStreamerStub{masterResponse: hlsResponse("application/x-mpegURL", "#EXTM3U\n")}
	record := hlsRequest(t, hlsRoutes(video), http.MethodGet, "/Videos/"+itemID.Dashed()+"/master.m3u8?"+hlsMasterQuery(), "video-client")
	if record.Code != http.StatusOK {
		t.Fatalf("GET video master status = %d, want 200; body=%q", record.Code, record.Body.String())
	}
	if video.masterRequest.AudioOnly {
		t.Errorf("video master request.AudioOnly = true, want false; request=%#v", video.masterRequest)
	}
}
