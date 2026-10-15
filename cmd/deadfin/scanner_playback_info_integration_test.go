// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationScannerPublishesProbedMovieFactsThroughPlaybackInfo proves the
// application path from a real ffprobe result, through scanner persistence, to
// both PlaybackInfo methods. The canonical fixture has no attachments, so its
// empty MediaAttachments and RequiredHttpHeaders collections are also wire
// contracts rather than omitted implementation details.
func TestApplicationScannerPublishesProbedMovieFactsThroughPlaybackInfo(t *testing.T) {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("ffprobe is required for real scanner PlaybackInfo composition: %v", err)
	}

	fixtures := applicationProbeFixtureRoot(t)
	source := t.TempDir()
	media := filepath.Join(source, "movies", "Standalone Movie (2023)", "Standalone Movie (2023).mkv")
	copyApplicationProbeFixture(t, filepath.Join(fixtures, "movies", "Standalone Movie (2023)", "Standalone Movie (2023).mkv"), media)
	copyApplicationProbeFixture(t, filepath.Join(fixtures, "movies", "Standalone Movie (2023)", "movie.nfo"), filepath.Join(filepath.Dir(media), "movie.nfo"))

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Canonical Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: filepath.Join(source, "movies")}}}); err != nil {
		t.Fatalf("add canonical fixture library: %v", err)
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())

	items := store.NewItemStore(app.stores)
	if result, err := items.Query(t.Context(), store.ItemQuery{Path: media}); err != nil {
		t.Fatalf("query unscanned canonical fixture: %v", err)
	} else if len(result.Items) != 0 {
		t.Fatalf("fixture-only canonical media produced %d persisted items before Scan, want 0", len(result.Items))
	}

	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan canonical movie with real ffprobe: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan canonical movie errors = %#v", result.Errors)
	}

	prober, err := ffmpeg.NewProber(t.Context(), ffprobePath)
	if err != nil {
		t.Fatalf("create standalone ffprobe: %v", err)
	}
	want := applicationStandaloneProbe(t, prober, media, false)
	stored := applicationScannedItem(t, items, media, item.KindMovie)
	persisted, err := items.MediaSourceRecord(t.Context(), stored.ID)
	if err != nil {
		t.Fatalf("read persisted canonical media source: %v", err)
	}
	assertApplicationScannedProbeFacts(t, persisted, want)

	for _, tc := range []struct {
		name, method, accept string
		body                 []byte
		camel                bool
	}{
		{name: "GET Pascal", method: http.MethodGet},
		{name: "GET Camel", method: http.MethodGet, accept: `application/json; profile="CamelCase"`, camel: true},
		{name: "POST Pascal", method: http.MethodPost, body: []byte(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}]}}`)},
		{name: "POST Camel", method: http.MethodPost, accept: `application/json; profile="CamelCase"`, body: []byte(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}]}}`), camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := scannedPlaybackInfoRequest(t, app, token, tc.method, "/Items/"+stored.ID.Dashed()+"/PlaybackInfo", tc.body, tc.accept)
			assertScannedPlaybackInfoResponse(t, response, want, media, tc.camel)
		})
	}
}

func scannedPlaybackInfoRequest(t *testing.T, app *application, token, method, path string, body []byte, accept string) map[string]json.RawMessage {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:8096"
	req.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="1.0", Token="`+token+`"`)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d, want %d; body: %s", method, path, rec.Code, http.StatusOK, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s %s response: %v; body: %s", method, path, err, rec.Body.String())
	}
	return response
}

func assertScannedPlaybackInfoResponse(t *testing.T, response map[string]json.RawMessage, want probe.MediaInfo, media string, camel bool) {
	t.Helper()

	mediaSourcesKey := scannedPlaybackInfoKey("MediaSources", camel)
	playSessionKey := scannedPlaybackInfoKey("PlaySessionId", camel)
	encodedSources, ok := response[mediaSourcesKey]
	if !ok {
		t.Fatalf("PlaybackInfo response has no %q field: %#v", mediaSourcesKey, response)
	}
	if _, ok := response[playSessionKey]; !ok {
		t.Fatalf("PlaybackInfo response has no %q field: %#v", playSessionKey, response)
	}
	var sources []json.RawMessage
	if err := json.Unmarshal(encodedSources, &sources); err != nil {
		t.Fatalf("decode %s: %v", mediaSourcesKey, err)
	}
	if len(sources) != 1 {
		t.Fatalf("PlaybackInfo %s count = %d, want one scanned movie source", mediaSourcesKey, len(sources))
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(sources[0], &wire); err != nil {
		t.Fatalf("decode scanned PlaybackInfo source fields: %v", err)
	}
	var got playback.MediaSourceInfo
	if err := json.Unmarshal(sources[0], &got); err != nil {
		t.Fatalf("decode scanned PlaybackInfo source: %v", err)
	}

	if got.Bitrate == nil || want.Bitrate == nil || *got.Bitrate != *want.Bitrate {
		t.Fatalf("PlaybackInfo bitrate = %v, want scanned probe bitrate %v", got.Bitrate, want.Bitrate)
	}
	if got.RunTimeTicks == nil || want.RunTimeTicks == nil || *got.RunTimeTicks != *want.RunTimeTicks {
		t.Fatalf("PlaybackInfo runtime = %v, want scanned probe runtime %v", got.RunTimeTicks, want.RunTimeTicks)
	}
	assertScannedPlaybackStreams(t, got.MediaStreams, want.MediaStreams)
	if len(got.MediaAttachments) != 0 {
		t.Fatalf("PlaybackInfo attachments = %#v, want canonical fixture's empty attachment list", got.MediaAttachments)
	}
	attachmentsKey := scannedPlaybackInfoKey("MediaAttachments", camel)
	attachments, ok := wire[attachmentsKey]
	if !ok || string(attachments) != "[]" {
		t.Fatalf("PlaybackInfo %s = %s, want explicit empty array", attachmentsKey, attachments)
	}

	wantName := strings.TrimSuffix(filepath.Base(media), filepath.Ext(media))
	if got.Name != wantName {
		t.Fatalf("PlaybackInfo source name = %q, want playable filename %q", got.Name, wantName)
	}
	if !reflect.DeepEqual(got.VideoType, want.VideoType) {
		t.Fatalf("PlaybackInfo video type = %v, want scanned probe video type %v", got.VideoType, want.VideoType)
	}
	if got.DefaultAudioStreamIndex == nil || *got.DefaultAudioStreamIndex != scannedPlaybackInfoFirstAudioIndex(t, want.MediaStreams) {
		t.Fatalf("PlaybackInfo default audio stream index = %v, want first scanned audio stream", got.DefaultAudioStreamIndex)
	}

	headersKey := scannedPlaybackInfoKey("RequiredHttpHeaders", camel)
	encodedHeaders, ok := wire[headersKey]
	if !ok {
		t.Fatalf("PlaybackInfo source has no %q field: %#v", headersKey, wire)
	}
	var headers map[string]string
	if err := json.Unmarshal(encodedHeaders, &headers); err != nil || len(headers) != 0 {
		t.Fatalf("PlaybackInfo required headers = %s, %v; want explicit empty object", encodedHeaders, err)
	}
}

func assertScannedPlaybackStreams(t *testing.T, got, want []playback.MediaStream) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("PlaybackInfo stream count = %d, want %d scanned streams", len(got), len(want))
	}
	video := scannedPlaybackInfoStream(t, got, playback.StreamTypeVideo, 0)
	wantVideo := scannedPlaybackInfoStream(t, want, playback.StreamTypeVideo, 0)
	if video.Codec != wantVideo.Codec || !reflect.DeepEqual(video.BitRate, wantVideo.BitRate) || !reflect.DeepEqual(video.Width, wantVideo.Width) || !reflect.DeepEqual(video.Height, wantVideo.Height) || !reflect.DeepEqual(video.AverageFrameRate, wantVideo.AverageFrameRate) {
		t.Fatalf("PlaybackInfo video stream = %#v, want scanned video facts %#v", video, wantVideo)
	}
	audio := scannedPlaybackInfoStream(t, got, playback.StreamTypeAudio, 1)
	wantAudio := scannedPlaybackInfoStream(t, want, playback.StreamTypeAudio, 1)
	if audio.Codec != wantAudio.Codec || !reflect.DeepEqual(audio.BitRate, wantAudio.BitRate) || !reflect.DeepEqual(audio.Channels, wantAudio.Channels) || !reflect.DeepEqual(audio.SampleRate, wantAudio.SampleRate) || !reflect.DeepEqual(audio.ChannelLayout, wantAudio.ChannelLayout) {
		t.Fatalf("PlaybackInfo audio stream = %#v, want scanned audio facts %#v", audio, wantAudio)
	}
}

func scannedPlaybackInfoStream(t *testing.T, streams []playback.MediaStream, kind playback.StreamType, index int) playback.MediaStream {
	t.Helper()
	for _, stream := range streams {
		if stream.Type == kind && stream.Index == index {
			return stream
		}
	}
	t.Fatalf("PlaybackInfo streams = %#v, want %v stream index %d", streams, kind, index)
	return playback.MediaStream{}
}

func scannedPlaybackInfoKey(pascal string, camel bool) string {
	if !camel {
		return pascal
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

func scannedPlaybackInfoFirstAudioIndex(t *testing.T, streams []playback.MediaStream) int {
	t.Helper()
	for _, stream := range streams {
		if stream.Type == playback.StreamTypeAudio {
			return stream.Index
		}
	}
	t.Fatal("canonical movie probe has no audio stream")
	return 0
}
