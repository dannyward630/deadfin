// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestProgressiveVideoStreamForwardsTransportFacts exercises the public video
// route with a fake process-facing playback service. The URL facts must remain
// observable at this seam; service internals may normalize them afterward.
func TestProgressiveVideoStreamForwardsTransportFacts(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{
		Body:        &progressiveReadCloser{Reader: bytes.NewBufferString("growing-copy-output")},
		ContentType: "video/mp2t",
	}}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a0")

	record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream.ts?Container=mp4&MediaSourceId=selected-source&VideoCodec=h264,hevc&AudioCodec=ac3,aac&VideoStreamIndex=7&AudioStreamIndex=8&SubtitleStreamIndex=9&SubtitleMethod=Embed&StartTimeTicks=900000000&DeviceId=device-id&PlaySessionId=play-session", "Jellyfin Web/10.10")
	if record.Code != http.StatusOK {
		t.Fatalf("GET progressive stream status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got := record.Body.String(); got != "growing-copy-output" {
		t.Errorf("GET progressive stream body = %q, want streamed process body", got)
	}
	if got := record.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("GET progressive stream Content-Type = %q, want %q", got, "video/mp2t")
	}
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if got := record.Header().Get(header); got != "" {
			t.Errorf("GET progressive stream %s = %q, want absent", header, got)
		}
	}

	if streamer.calls != 1 {
		t.Fatalf("OpenProgressive calls = %d, want 1", streamer.calls)
	}
	request := streamer.request
	if request.ItemID != itemID || request.MediaSourceID != "selected-source" || request.Container != "mp4" {
		t.Errorf("progressive identity/container = item:%s source:%q container:%q, want item:%s source:%q container:%q; progressive query Container overrides the suffix", request.ItemID, request.MediaSourceID, request.Container, itemID, "selected-source", "mp4")
	}
	if got, want := strings.Join(request.VideoCodecs, ","), "h264,hevc"; got != want {
		t.Errorf("VideoCodecs = %q, want parsed canonical list %q", got, want)
	}
	if got, want := strings.Join(request.AudioCodecs, ","), "ac3,aac"; got != want {
		t.Errorf("AudioCodecs = %q, want parsed canonical list %q", got, want)
	}
	if !request.EnableAutoStreamCopy || !request.AllowVideoStreamCopy || !request.AllowAudioStreamCopy {
		t.Errorf("copy gate defaults = auto:%t video:%t audio:%t, want all true", request.EnableAutoStreamCopy, request.AllowVideoStreamCopy, request.AllowAudioStreamCopy)
	}
	assertProgressiveStreamIndex(t, "VideoStreamIndex", request.VideoStreamIndex, 7)
	assertProgressiveStreamIndex(t, "AudioStreamIndex", request.AudioStreamIndex, 8)
	assertProgressiveStreamIndex(t, "SubtitleStreamIndex", request.SubtitleStreamIndex, 9)
	if request.SubtitleDeliveryMethod == nil || *request.SubtitleDeliveryMethod != playback.SubtitleEmbed {
		t.Errorf("SubtitleDeliveryMethod = %v, want %v", request.SubtitleDeliveryMethod, playback.SubtitleEmbed)
	}
	if request.StartTimeTicks != 900000000 || request.DeviceID != "device-id" || request.PlaySessionID != "play-session" || request.UserAgent != "Jellyfin Web/10.10" {
		t.Errorf("progressive transport facts = ticks:%d device:%q session:%q agent:%q, want exact URL/header values", request.StartTimeTicks, request.DeviceID, request.PlaySessionID, request.UserAgent)
	}
	if request.Head {
		t.Error("GET progressive request Head = true, want false")
	}
	if body := streamer.response.Body.(*progressiveReadCloser); body.closeCalls != 1 {
		t.Errorf("GET progressive response Body.Close calls = %d, want 1 after complete copy", body.closeCalls)
	}
}

// TestProgressiveVideoStreamForwardsCopyConstraints pins the stock controller
// query names. The streamer, not the HTTP adapter, decides whether each fact
// permits remux copy.
func TestProgressiveVideoStreamForwardsCopyConstraints(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a7")
	for _, tc := range []struct {
		name, framerateQuery string
		wantFramerate        float32
		wantMaxFramerate     float32
		hasMaxFramerate      bool
	}{
		{name: "preserves distinct framerate and maximum", framerateQuery: "Framerate=23.5&MaxFramerate=24.5", wantFramerate: 23.5, wantMaxFramerate: 24.5, hasMaxFramerate: true},
		{name: "preserves framerate without maximum", framerateQuery: "Framerate=23.5", wantFramerate: 23.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("constraints")}, ContentType: "video/mp4"}}
			path := "/Videos/" + itemID.Dashed() + "/stream.ts?Container=mp4&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=constraints-session&SubtitleStreamIndex=9&RequireAvc=true&RequireNonAnamorphic=true&DeInterlace=true&Width=1280&Height=720&MaxWidth=1920&MaxHeight=1080&" + tc.framerateQuery + "&VideoBitRate=8100000&AudioBitRate=384000&MaxAudioChannels=6&AudioSampleRate=48000&MaxAudioBitDepth=24&CopyTimestamps=true"
			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "constraints-client")
			if record.Code != http.StatusOK {
				t.Fatalf("GET progressive constraints status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
			}
			request := streamer.request
			if !request.RequireAvc || !request.RequireNonAnamorphic || !request.DeInterlace || !request.CopyTimestamps {
				t.Errorf("copy constraints flags = avc:%t non-anamorphic:%t deinterlace:%t timestamps:%t, want all true", request.RequireAvc, request.RequireNonAnamorphic, request.DeInterlace, request.CopyTimestamps)
			}
			assertProgressiveStreamIndex(t, "SubtitleStreamIndex", request.SubtitleStreamIndex, 9)
			if request.SubtitleDeliveryMethod == nil || *request.SubtitleDeliveryMethod != playback.SubtitleEncode {
				t.Errorf("omitted SubtitleMethod with selected subtitle = %v, want default Encode", request.SubtitleDeliveryMethod)
			}
			assertProgressiveConstraintInt(t, "MaxWidth", request.MaxWidth, 1920)
			assertProgressiveConstraintInt(t, "MaxHeight", request.MaxHeight, 1080)
			assertProgressiveConstraintInt(t, "Width", request.Width, 1280)
			assertProgressiveConstraintInt(t, "Height", request.Height, 720)
			assertProgressiveConstraintInt(t, "VideoBitrate", request.VideoBitrate, 8_100_000)
			assertProgressiveConstraintInt(t, "AudioBitrate", request.AudioBitrate, 384_000)
			assertProgressiveConstraintInt(t, "MaxAudioChannels", request.MaxAudioChannels, 6)
			assertProgressiveConstraintInt(t, "AudioSampleRate", request.AudioSampleRate, 48_000)
			assertProgressiveConstraintInt(t, "MaxAudioBitDepth", request.MaxAudioBitDepth, 24)
			if request.Framerate == nil || *request.Framerate != tc.wantFramerate {
				t.Errorf("Framerate = %v, want %v", request.Framerate, tc.wantFramerate)
			}
			if tc.hasMaxFramerate {
				if request.MaxFramerate == nil || *request.MaxFramerate != tc.wantMaxFramerate {
					t.Errorf("MaxFramerate = %v, want %v", request.MaxFramerate, tc.wantMaxFramerate)
				}
			} else if request.MaxFramerate != nil {
				t.Errorf("MaxFramerate = %v, want nil when absent", request.MaxFramerate)
			}
		})
	}
}

// TestProgressiveVideoStreamDefaultsOmittedSubtitleMethodForAbsentAndDisabledSubtitle
// pins the controller's unconditional stock default independently of selection.
func TestProgressiveVideoStreamDefaultsOmittedSubtitleMethodForAbsentAndDisabledSubtitle(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a9")
	disabledIndex := -1
	for _, tc := range []struct {
		name, query string
		wantIndex   *int
	}{
		{name: "no subtitle index", query: ""},
		{name: "disabled subtitle index", query: "&SubtitleStreamIndex=-1", wantIndex: &disabledIndex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("subtitle-default")}, ContentType: "video/mp2t"}}
			path := "/Videos/" + itemID.Dashed() + "/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=subtitle-default-session" + tc.query
			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "")
			if record.Code != http.StatusOK {
				t.Fatalf("GET progressive omitted subtitle method status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
			}
			if streamer.request.SubtitleDeliveryMethod == nil || *streamer.request.SubtitleDeliveryMethod != playback.SubtitleEncode {
				t.Errorf("omitted SubtitleMethod for %s = %v, want Encode", tc.name, streamer.request.SubtitleDeliveryMethod)
			}
			if tc.wantIndex == nil {
				if streamer.request.SubtitleStreamIndex != nil {
					t.Errorf("SubtitleStreamIndex = %v, want nil", streamer.request.SubtitleStreamIndex)
				}
			} else {
				assertProgressiveStreamIndex(t, "SubtitleStreamIndex", streamer.request.SubtitleStreamIndex, *tc.wantIndex)
			}
		})
	}
}

// The direct physical-file branch retains its own suffix-first output rule;
// only the progressive StreamingHelpers path gives query Container precedence.
func TestStaticVideoStreamSuffixStillOverridesContainerQuery(t *testing.T) {
	const contents = "static-suffix-container"
	path := t.TempDir() + "/source.mkv"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write static source: %v", err)
	}
	sources := &directVideoStreamSourceStub{source: playback.MediaSourceInfo{ID: "static-suffix-source", Protocol: playback.MediaProtocolFile, Path: path, Container: "mkv"}}
	routes := router.New()
	handlers.RegisterDirectVideoStreams(routes, sources, &progressiveVideoStreamerStub{})
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a8")

	record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream.mkv?Static=true&Container=mp4", "")
	if record.Code != http.StatusOK || record.Body.String() != contents {
		t.Fatalf("GET static suffix/container = %d %q, want %d %q", record.Code, record.Body.String(), http.StatusOK, contents)
	}
	if got := record.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Errorf("GET static suffix/container Content-Type = %q, want suffix-derived %q", got, "video/x-matroska")
	}
}

// TestProgressiveVideoCodecListsSplitCanonicalAlternatives preserves the
// comma-delimited codec alternatives emitted by StreamInfo URLs.
func TestProgressiveVideoCodecListsSplitCanonicalAlternatives(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("codec-copy")}, ContentType: "video/mp2t"}}
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006ab")
	record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264,hevc&AudioCodec=aac,ac3&PlaySessionId=codec-session", "")
	if record.Code != http.StatusOK {
		t.Fatalf("GET canonical codec alternatives status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got, want := strings.Join(streamer.request.VideoCodecs, ","), "h264,hevc"; got != want {
		t.Errorf("VideoCodecs = %q, want split canonical alternatives %q", got, want)
	}
	if got, want := strings.Join(streamer.request.AudioCodecs, ","), "aac,ac3"; got != want {
		t.Errorf("AudioCodecs = %q, want split canonical alternatives %q", got, want)
	}
}

// TestVideoStreamPackedParamsStaticOverridesScalarModeBeforeProgressiveDispatch
// preserves legacy slot-three static selection before progressive dispatch.
func TestVideoStreamPackedParamsStaticOverridesScalarModeBeforeProgressiveDispatch(t *testing.T) {
	const contents = "packed-static-video"
	path := t.TempDir() + "/packed.mkv"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write static source: %v", err)
	}
	sources := &directVideoStreamSourceStub{source: playback.MediaSourceInfo{ID: "packed-static-source", Protocol: playback.MediaProtocolFile, Path: path, Container: "mkv"}}
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("must-not-open")}}}
	routes := router.New()
	handlers.RegisterDirectVideoStreams(routes, sources, streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006aa")

	for _, query := range []string{"Params=;;;true", "Static=false&Params=;;;true"} {
		record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?"+query, "")
		if record.Code != http.StatusOK || record.Body.String() != contents {
			t.Errorf("GET packed static query %q = %d %q, want %d direct body %q", query, record.Code, record.Body.String(), http.StatusOK, contents)
		}
	}
	if sources.calls != 2 {
		t.Errorf("StaticSource calls = %d, want one for each packed static request", sources.calls)
	}
	if streamer.calls != 0 {
		t.Errorf("OpenProgressive calls = %d, want 0 when Params slot 3 selects static delivery", streamer.calls)
	}
}

// TestProgressiveVideoStreamProjectsPackedParamsIntoOpenRequest exercises the
// legacy Params path at the HTTP boundary. Every asserted value is intentionally
// nondefault so successful parsing alone cannot satisfy the route contract.
func TestProgressiveVideoStreamProjectsPackedParamsIntoOpenRequest(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{
		Body:        &progressiveReadCloser{Reader: bytes.NewBufferString("packed-progressive")},
		ContentType: "video/mp2t",
	}}
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006b0")
	slots := make([]string, 25)
	slots[1] = "packed-device"
	slots[2] = "packed-source"
	slots[4] = "h264,hevc"
	slots[5] = "ac3,aac"
	slots[6] = "12"
	slots[7] = "13"
	slots[14] = "900000000"
	slots[20] = "packed-session"
	slots[24] = "true"

	record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&Params="+strings.Join(slots, ";"), "packed-client")
	if record.Code != http.StatusOK {
		t.Fatalf("GET packed progressive status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if streamer.calls != 1 {
		t.Fatalf("OpenProgressive calls = %d, want 1", streamer.calls)
	}
	request := streamer.request
	if request.MediaSourceID != "packed-source" || request.DeviceID != "packed-device" || request.PlaySessionID != "packed-session" {
		t.Errorf("packed identity = source:%q device:%q session:%q, want packed values", request.MediaSourceID, request.DeviceID, request.PlaySessionID)
	}
	if got, want := strings.Join(request.VideoCodecs, ","), "h264,hevc"; got != want {
		t.Errorf("packed VideoCodecs = %q, want %q", got, want)
	}
	if got, want := strings.Join(request.AudioCodecs, ","), "ac3,aac"; got != want {
		t.Errorf("packed AudioCodecs = %q, want %q", got, want)
	}
	assertProgressiveStreamIndex(t, "packed AudioStreamIndex", request.AudioStreamIndex, 12)
	assertProgressiveStreamIndex(t, "packed SubtitleStreamIndex", request.SubtitleStreamIndex, 13)
	if request.StartTimeTicks != 900000000 || !request.CopyTimestamps {
		t.Errorf("packed transport/copy = ticks:%d copy:%t, want 900000000/true", request.StartTimeTicks, request.CopyTimestamps)
	}
}

// TestProgressiveVideoStreamPackedParamsOverrideOnlyNonblankScalarQueryValues
// keeps the route aligned with DecodeStreamParams: a populated legacy slot wins,
// while an empty slot leaves the explicitly bound scalar intact.
func TestProgressiveVideoStreamPackedParamsOverrideOnlyNonblankScalarQueryValues(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{
		Body:        &progressiveReadCloser{Reader: bytes.NewBufferString("packed-precedence")},
		ContentType: "video/mp2t",
	}}
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006b1")
	slots := make([]string, 25)
	slots[2] = "packed-source"
	slots[5] = "aac,ac3"
	slots[7] = "15"
	slots[14] = "750000000"
	slots[24] = "true"
	path := "/Videos/" + itemID.Dashed() + "/stream?Container=ts&MediaSourceId=query-source&DeviceId=query-device&PlaySessionId=query-session&VideoCodec=vp9&AudioCodec=opus&AudioStreamIndex=4&SubtitleStreamIndex=5&StartTimeTicks=1&CopyTimestamps=false&Params=" + strings.Join(slots, ";")

	record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "")
	if record.Code != http.StatusOK {
		t.Fatalf("GET packed precedence status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	request := streamer.request
	if request.MediaSourceID != "packed-source" || request.DeviceID != "query-device" || request.PlaySessionID != "query-session" {
		t.Errorf("packed/query identity = source:%q device:%q session:%q, want packed source and preserved query device/session", request.MediaSourceID, request.DeviceID, request.PlaySessionID)
	}
	if got, want := strings.Join(request.VideoCodecs, ","), "vp9"; got != want {
		t.Errorf("blank packed VideoCodec replaced query codecs: got %q, want %q", got, want)
	}
	if got, want := strings.Join(request.AudioCodecs, ","), "aac,ac3"; got != want {
		t.Errorf("packed AudioCodecs = %q, want %q", got, want)
	}
	assertProgressiveStreamIndex(t, "blank packed AudioStreamIndex", request.AudioStreamIndex, 4)
	assertProgressiveStreamIndex(t, "packed SubtitleStreamIndex", request.SubtitleStreamIndex, 15)
	if request.StartTimeTicks != 750000000 || !request.CopyTimestamps {
		t.Errorf("packed precedence transport/copy = ticks:%d copy:%t, want 750000000/true", request.StartTimeTicks, request.CopyTimestamps)
	}
}

func TestProgressiveVideoStreamRejectsMalformedPackedParamsBeforeOpening(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006b2")
	for _, tc := range []struct {
		name string
		slot int
		text string
	}{
		{name: "audio stream index", slot: 6, text: "not-an-index"},
		{name: "start ticks", slot: 14, text: "not-a-tick"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{}
			slots := make([]string, tc.slot+1)
			slots[tc.slot] = tc.text
			path := "/Videos/" + itemID.Dashed() + "/stream?Container=ts&Params=" + strings.Join(slots, ";")

			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "")
			if record.Code != http.StatusBadRequest {
				t.Fatalf("GET malformed packed %s status = %d, want %d; body=%q", tc.name, record.Code, http.StatusBadRequest, record.Body.String())
			}
			if streamer.calls != 0 {
				t.Errorf("OpenProgressive calls = %d, want 0 after malformed packed %s", streamer.calls, tc.name)
			}
		})
	}
}

func TestProgressiveVideoStreamForwardsCopyGatesAndCodecOptions(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006ac")
	for _, tc := range []struct {
		name, gates string
		allow       bool
	}{
		{name: "defaults enabled", allow: true},
		{name: "explicit false preserved", gates: "&EnableAutoStreamCopy=false&AllowVideoStreamCopy=false&AllowAudioStreamCopy=false", allow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("gates-options")}, ContentType: "video/mp2t"}}
			path := "/Videos/" + itemID.Dashed() + "/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=gates-session&Profile=high&Level=4.1&MaxRefFrames=4&MaxVideoBitDepth=10&AudioChannels=6&TranscodingMaxAudioChannels=8&MaxAudioBitDepth=24&h264-profile=high10&h264-level=4.2&h264-maxrefframes=5&h264-videobitdepth=10&ac3-audiochannels=6&ac3-audiobitdepth=24" + tc.gates
			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "")
			if record.Code != http.StatusOK {
				t.Fatalf("GET progressive gates/options status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
			}
			request := streamer.request
			if request.EnableAutoStreamCopy != tc.allow || request.AllowVideoStreamCopy != tc.allow || request.AllowAudioStreamCopy != tc.allow {
				t.Errorf("copy gates = auto:%t video:%t audio:%t, want all %t", request.EnableAutoStreamCopy, request.AllowVideoStreamCopy, request.AllowAudioStreamCopy, tc.allow)
			}
			if request.Profile == nil || *request.Profile != "high" || request.Level == nil || *request.Level != "4.1" {
				t.Errorf("Profile/Level = %v/%v, want high/4.1", request.Profile, request.Level)
			}
			assertProgressiveConstraintInt(t, "MaxRefFrames", request.MaxRefFrames, 4)
			assertProgressiveConstraintInt(t, "MaxVideoBitDepth", request.MaxVideoBitDepth, 10)
			assertProgressiveConstraintInt(t, "AudioChannels", request.AudioChannels, 6)
			assertProgressiveConstraintInt(t, "TranscodingMaxAudioChannels", request.TranscodingMaxAudioChannels, 8)
			assertProgressiveConstraintInt(t, "MaxAudioBitDepth", request.MaxAudioBitDepth, 24)
			for key, want := range map[string]string{
				"h264-profile": "high10", "h264-level": "4.2", "h264-maxrefframes": "5", "h264-videobitdepth": "10", "ac3-audiochannels": "6", "ac3-audiobitdepth": "24",
			} {
				if got := request.StreamOptions.Get(key); got != want {
					t.Errorf("StreamOptions[%q] = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// TestProgressiveVideoStreamStreamsBeforeBodyCompletion prevents handlers from
// collecting an ffmpeg output into memory before exposing its first bytes.
func TestProgressiveVideoStreamStreamsBeforeBodyCompletion(t *testing.T) {
	body := newGatedProgressiveReadCloser("first-", "last")
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: body, ContentType: "video/mp2t"}}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a1")
	writer := newProgressiveStreamingResponseWriter()
	done := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=streaming-session", nil)
		routes.ServeHTTP(writer, request)
		close(done)
	}()

	select {
	case <-writer.firstWrite:
		// A first response write before the body gate opens proves incremental delivery.
	case <-done:
		t.Fatal("progressive route completed before the growing body was released")
	}
	body.release()
	<-done
	if got := writer.body.String(); got != "first-last" {
		t.Errorf("progressive streamed body = %q, want ordered growing bytes", got)
	}
	if body.closeCalls != 1 {
		t.Errorf("growing body Close calls = %d, want 1", body.closeCalls)
	}
	if got := writer.Header().Get("Content-Length"); got != "" {
		t.Errorf("streaming Content-Length = %q, want absent", got)
	}
}

func TestProgressiveVideoStreamHEADValidatesWithoutBody(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{ContentType: "video/mp4"}}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a2")

	record := progressiveVideoStreamRequest(t, routes, http.MethodHead, "/Videos/"+itemID.Dashed()+"/stream?Container=mp4&VideoCodec=h264&AudioCodec=ac3&VideoStreamIndex=0&AudioStreamIndex=1&PlaySessionId=head-session&StartTimeTicks=0", "head-client")
	if record.Code != http.StatusOK {
		t.Fatalf("HEAD progressive stream status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got := record.Body.Len(); got != 0 {
		t.Errorf("HEAD progressive stream body length = %d, want 0", got)
	}
	if streamer.calls != 1 || !streamer.request.Head {
		t.Errorf("HEAD OpenProgressive = calls:%d head:%t, want exactly one head request", streamer.calls, streamer.request.Head)
	}
	if streamer.request.Container != "mp4" || streamer.request.StartTimeTicks != 0 || streamer.request.UserAgent != "head-client" {
		t.Errorf("HEAD request facts = container:%q ticks:%d agent:%q, want mp4, zero, and raw User-Agent", streamer.request.Container, streamer.request.StartTimeTicks, streamer.request.UserAgent)
	}
	if got := record.Header().Get("Content-Length"); got != "" {
		t.Errorf("HEAD progressive Content-Length = %q, want absent", got)
	}
}

func TestProgressiveVideoStreamClosesBodyWhenClientWriteFails(t *testing.T) {
	body := &progressiveReadCloser{Reader: bytes.NewBufferString("bytes-before-disconnect")}
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: body, ContentType: "video/mp2t"}}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a6")
	writer := &progressiveWriteFailResponseWriter{header: make(http.Header), err: errors.New("client disconnected")}

	routes.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=disconnect-session", nil))

	if body.closeCalls != 1 {
		t.Errorf("disconnected progressive Body.Close calls = %d, want 1", body.closeCalls)
	}
}

func TestProgressiveVideoStreamKeepsStaticDeliveryOutOfStreamer(t *testing.T) {
	const contents = "legacy-static-video"
	path := t.TempDir() + "/legacy.mkv"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write static source: %v", err)
	}
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("must-not-open")}}}
	sources := &directVideoStreamSourceStub{source: playback.MediaSourceInfo{ID: "static-source", Protocol: playback.MediaProtocolFile, Path: path, Container: "mkv"}}
	routes := router.New()
	handlers.RegisterDirectVideoStreams(routes, sources, streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a3")

	record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Static=true&MediaSourceId=static-source", "")
	if record.Code != http.StatusOK || record.Body.String() != contents {
		t.Fatalf("GET static video = %d %q, want %d %q", record.Code, record.Body.String(), http.StatusOK, contents)
	}
	if streamer.calls != 0 {
		t.Errorf("OpenProgressive calls = %d, want 0 for Static=true", streamer.calls)
	}
}

func TestProgressiveVideoStreamRejectsMalformedQueryBeforeOpening(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a4")

	record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&StartTimeTicks=not-a-tick&PlaySessionId=session", "")
	if record.Code != http.StatusBadRequest {
		t.Fatalf("GET malformed progressive stream status = %d, want %d", record.Code, http.StatusBadRequest)
	}
	if streamer.calls != 0 {
		t.Errorf("OpenProgressive calls = %d, want 0 after binding failure", streamer.calls)
	}
}

func TestProgressiveVideoStreamRejectsMalformedLevelBeforeOpening(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{}
	routes := progressiveVideoStreamRoutes(streamer)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006ad")

	record := progressiveVideoStreamRequest(t, routes, http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&Level=not-a-number&PlaySessionId=session", "")
	if record.Code != http.StatusBadRequest {
		t.Fatalf("GET malformed progressive Level status = %d, want %d; body=%q", record.Code, http.StatusBadRequest, record.Body.String())
	}
	if streamer.calls != 0 {
		t.Errorf("OpenProgressive calls = %d, want 0 after malformed Level", streamer.calls)
	}
}

func TestProgressiveVideoStreamAcceptsEmptyGlobalLevelAndForwardsCodecLevel(t *testing.T) {
	streamer := &progressiveVideoStreamerStub{response: playback.ProgressiveResponse{Body: &progressiveReadCloser{Reader: bytes.NewBufferString("empty-level")}, ContentType: "video/mp2t"}}
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006af")
	record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&Level=&h264-level=4.1&PlaySessionId=session", "")

	if record.Code != http.StatusOK {
		t.Fatalf("GET empty progressive Level status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if streamer.calls != 1 {
		t.Fatalf("OpenProgressive calls = %d, want 1 for optional empty Level", streamer.calls)
	}
	if streamer.request.Level != nil {
		t.Errorf("empty global Level = %v, want omitted nil transport value", streamer.request.Level)
	}
	if got := streamer.request.StreamOptions.Get("h264-level"); got != "4.1" {
		t.Errorf("h264-level option = %q, want %q", got, "4.1")
	}
}

func TestProgressiveVideoStreamRejectsMalformedCodecListsBeforeOpening(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006ae")
	overlong := strings.Repeat("a", 41)
	for _, tc := range []struct {
		name, codecs string
	}{
		{name: "malformed video codec", codecs: "VideoCodec=h264!&AudioCodec=ac3"},
		{name: "overlong video codec", codecs: "VideoCodec=" + overlong + "&AudioCodec=ac3"},
		{name: "malformed audio codec", codecs: "VideoCodec=h264&AudioCodec=ac3!"},
		{name: "overlong audio codec", codecs: "VideoCodec=h264&AudioCodec=" + overlong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{}
			path := "/Videos/" + itemID.Dashed() + "/stream?Container=ts&PlaySessionId=session&" + tc.codecs
			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, path, "")
			if record.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want %d; body=%q", tc.name, record.Code, http.StatusBadRequest, record.Body.String())
			}
			if streamer.calls != 0 {
				t.Errorf("OpenProgressive calls = %d, want 0 after invalid codec list", streamer.calls)
			}
		})
	}
}

func TestProgressiveVideoStreamMapsServiceFailures(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006a5")
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "missing item", err: playback.ErrItemNotFound, want: http.StatusNotFound},
		{name: "source mismatch", err: playback.ErrMediaSourceNotFound, want: http.StatusBadRequest},
		{name: "unsupported copy", err: playback.ErrProgressiveUnsupported, want: http.StatusNotImplemented},
		{name: "process failure", err: errors.New("ffmpeg startup failed"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamer := &progressiveVideoStreamerStub{err: tc.err}
			record := progressiveVideoStreamRequest(t, progressiveVideoStreamRoutes(streamer), http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=h264&AudioCodec=ac3&PlaySessionId=session", "")
			if record.Code != tc.want {
				t.Errorf("GET progressive stream %s status = %d, want %d; body=%q", tc.name, record.Code, tc.want, record.Body.String())
			}
		})
	}
}

type progressiveVideoStreamerStub struct {
	response playback.ProgressiveResponse
	err      error
	calls    int
	request  playback.ProgressiveRequest
}

func (s *progressiveVideoStreamerStub) OpenProgressive(_ context.Context, request playback.ProgressiveRequest) (playback.ProgressiveResponse, error) {
	s.calls++
	s.request = request
	return s.response, s.err
}

func progressiveVideoStreamRoutes(streamer playback.ProgressiveVideoStreamer) *router.Router {
	routes := router.New()
	handlers.RegisterDirectVideoStreams(routes, &directVideoStreamSourceStub{}, streamer)
	return routes
}

func progressiveVideoStreamRequest(t *testing.T, routes http.Handler, method, path, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, request)
	return record
}

func assertProgressiveStreamIndex(t *testing.T, name string, actual *int, want int) {
	t.Helper()
	if actual == nil || *actual != want {
		t.Errorf("%s = %v, want %d", name, actual, want)
	}
}

type progressiveReadCloser struct {
	io.Reader
	closeCalls int
}

func (r *progressiveReadCloser) Close() error {
	r.closeCalls++
	return nil
}

type gatedProgressiveReadCloser struct {
	first, last []byte
	state       int
	gate        chan struct{}
	once        sync.Once
	closeCalls  int
}

func newGatedProgressiveReadCloser(first, last string) *gatedProgressiveReadCloser {
	return &gatedProgressiveReadCloser{first: []byte(first), last: []byte(last), gate: make(chan struct{})}
}

func (r *gatedProgressiveReadCloser) Read(dst []byte) (int, error) {
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

func (r *gatedProgressiveReadCloser) Close() error {
	r.closeCalls++
	return nil
}

func (r *gatedProgressiveReadCloser) release() { r.once.Do(func() { close(r.gate) }) }

type progressiveStreamingResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	status     int
	firstWrite chan struct{}
	once       sync.Once
}

func newProgressiveStreamingResponseWriter() *progressiveStreamingResponseWriter {
	return &progressiveStreamingResponseWriter{header: make(http.Header), firstWrite: make(chan struct{})}
}

func (w *progressiveStreamingResponseWriter) Header() http.Header { return w.header }

func (w *progressiveStreamingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *progressiveStreamingResponseWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.once.Do(func() { close(w.firstWrite) })
	return w.body.Write(value)
}

type progressiveWriteFailResponseWriter struct {
	header http.Header
	err    error
}

func (w *progressiveWriteFailResponseWriter) Header() http.Header { return w.header }

func (w *progressiveWriteFailResponseWriter) WriteHeader(int) {}

func (w *progressiveWriteFailResponseWriter) Write(value []byte) (int, error) {
	return 0, w.err
}

func assertProgressiveConstraintInt(t *testing.T, name string, actual *int, want int) {
	t.Helper()
	if actual == nil || *actual != want {
		t.Errorf("%s = %v, want %d", name, actual, want)
	}
}
