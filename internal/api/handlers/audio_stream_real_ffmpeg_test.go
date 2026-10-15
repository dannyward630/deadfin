// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const realAudioSourceID = "4a7f00000000000000000000000004a7"

// TestUniversalAudioTranscodesRealLocalFLAC drives /Audio/{id}/universal and
// /Audio/{id}/stream.{container} through the real progressive service and
// ffmpeg process manager. It checks the upstream decision
// (UniversalAudioController.GetUniversalAudioStream), the progressive audio
// command line (EncodingHelper.GetProgressiveAudioFullCommandLine), and the
// decoded output.
func TestUniversalAudioTranscodesRealLocalFLAC(t *testing.T) {
	ffmpegPath, ffprobePath, caps := requireRealAudioTools(t)
	mediaPath := createFLACAudioFixture(t, ffmpegPath)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004a7")
	source := persistedFLACSource(t, mediaPath)

	for _, tc := range []struct {
		name, path   string
		query        url.Values
		contentType  string
		outputSuffix string
		wantFormat   string
		wantCodec    string
		wantChannels int
		maxBitrate   int
		static       bool
		head         bool
	}{
		{
			name: "universal plays a supported FLAC directly", path: "universal",
			query:       url.Values{"container": {"opus,mp3|mp3,flac"}},
			contentType: "audio/flac", wantFormat: "flac", wantCodec: "flac", wantChannels: 2, static: true,
		},
		{
			name: "universal transcodes to mp3 over http", path: "universal",
			query:       url.Values{"container": {"mp3"}, "transcodingContainer": {"mp3"}, "audioCodec": {"mp3"}},
			contentType: "audio/mpeg", outputSuffix: ".mp3", wantFormat: "mp3", wantCodec: "mp3", wantChannels: 2, maxBitrate: 256000,
		},
		{
			name: "universal transcodes to aac under a bitrate limit", path: "universal",
			query:       url.Values{"container": {"flac"}, "transcodingContainer": {"aac"}, "audioCodec": {"aac"}, "maxStreamingBitrate": {"96000"}, "maxAudioChannels": {"1"}},
			contentType: "audio/aac", outputSuffix: ".aac", wantFormat: "aac", wantCodec: "aac", wantChannels: 1, maxBitrate: 96000,
		},
		{
			name: "stream with a container extension transcodes", path: "stream.mp3",
			query:       url.Values{"audioCodec": {"mp3"}, "audioBitRate": {"128000"}, "MediaSourceId": {realAudioSourceID}},
			contentType: "audio/mpeg", outputSuffix: ".mp3", wantFormat: "mp3", wantCodec: "mp3", wantChannels: 2, maxBitrate: 128000,
		},
		{
			name: "HEAD stream returns the content type without a process", path: "stream.mp3",
			query:       url.Values{"audioCodec": {"mp3"}, "MediaSourceId": {realAudioSourceID}},
			contentType: "audio/mpeg", head: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcodesDir := filepath.Join(t.TempDir(), "owned-transcodes")
			manager := newRealAudioManager(t, ffmpegPath, transcodesDir)
			files := &persistedRemuxDirectFileSource{itemID: itemID, source: source}
			service := playback.NewProgressiveService(playback.ProgressiveServiceConfig{
				Sources:      files,
				TranscodeDir: transcodesDir,
				Opener:       realProgressiveManagerOpener{manager: manager.manager},
				Capabilities: func() encoding.Capabilities { return caps },
			})
			routes := realAudioRoutes(t, files, realUniversalDecision{source: source}, service)
			server := httptest.NewServer(routes)
			t.Cleanup(server.Close)

			method := http.MethodGet
			if tc.head {
				method = http.MethodHead
			}
			response := openRealAudioResponse(t, method, server.URL+"/Audio/"+itemID.Dashed()+"/"+tc.path+"?"+tc.query.Encode())
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("%s %s status = %d, want 200; body=%q", method, tc.path, response.StatusCode, body)
			}
			if got := response.Header.Get("Content-Type"); got != tc.contentType {
				t.Fatalf("%s %s Content-Type = %q, want %q", method, tc.path, got, tc.contentType)
			}
			wantRanges := "none"
			if tc.static {
				wantRanges = "bytes"
			}
			if got := response.Header.Get("Accept-Ranges"); got != wantRanges {
				t.Errorf("%s %s Accept-Ranges = %q, want %q", method, tc.path, got, wantRanges)
			}
			if tc.head {
				if body, _ := io.ReadAll(response.Body); len(body) != 0 {
					t.Errorf("HEAD %s body has %d bytes, want none", tc.path, len(body))
				}
				manager.close(t)
				if entries, _ := os.ReadDir(transcodesDir); len(entries) != 0 {
					t.Errorf("HEAD %s left transcode artifacts %v, want none", tc.path, entries)
				}
				return
			}
			saved := filepath.Join(t.TempDir(), "response"+tc.outputSuffix)
			if _, size := saveIncrementalHTTPResponse(t, response.Body, saved); size == 0 {
				t.Fatalf("%s %s saved no bytes", method, tc.path)
			}
			probe := probeRealAudio(t, ffprobePath, saved)
			if !containsRemuxToken(strings.Split(probe.Format.FormatName, ","), tc.wantFormat) {
				t.Errorf("ffprobe format = %q, want %q", probe.Format.FormatName, tc.wantFormat)
			}
			if len(probe.Streams) != 1 || probe.Streams[0].CodecType != "audio" || probe.Streams[0].CodecName != tc.wantCodec || probe.Streams[0].Channels != tc.wantChannels {
				t.Fatalf("ffprobe streams = %+v, want one %s audio stream with %d channel(s)", probe.Streams, tc.wantCodec, tc.wantChannels)
			}
			if tc.maxBitrate > 0 {
				if bitrate := probe.Format.bitrate(); bitrate <= 0 || bitrate > tc.maxBitrate*5/4 {
					t.Errorf("ffprobe bitrate = %d, want near or below %d", bitrate, tc.maxBitrate)
				}
			}
			manager.close(t)
			if !tc.static {
				assertOwnedRemuxArtifacts(t, transcodesDir, tc.outputSuffix)
			}
		})
	}
}

// TestUniversalAudioHLSDispatchesAudioOnlyMaster checks that
// TranscodingProtocol=hls hands the negotiated values to the dynamic HLS
// master playlist (UniversalAudioController.cs:162-211).
func TestUniversalAudioHLSDispatchesAudioOnlyMaster(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000004a7")
	source := persistedFLACSource(t, filepath.Join(t.TempDir(), "absent.flac"))
	files := &persistedRemuxDirectFileSource{itemID: itemID, source: source}
	hls := &recordingHLSStreamer{}
	routes := realAudioRoutes(t, files, realUniversalDecision{source: source}, hls)
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)

	query := url.Values{
		"container": {"mp3"}, "transcodingContainer": {"ts"}, "transcodingProtocol": {"hls"},
		"audioCodec": {"aac"}, "maxStreamingBitrate": {"192000"}, "deviceId": {"universal-device"},
	}
	response := openRealAudioResponse(t, http.MethodGet, server.URL+"/Audio/"+itemID.Dashed()+"/universal?"+query.Encode())
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "#EXTM3U\n" {
		t.Fatalf("GET universal hls = %d %q, want the master playlist", response.StatusCode, body)
	}
	got := hls.master
	if hls.calls != 1 || !got.AudioOnly || got.SegmentContainer != "ts" || got.AudioCodec != "aac" ||
		got.MediaSourceID != realAudioSourceID || got.PlaySessionID != "universal-real-session" || got.DeviceID != "universal-device" ||
		got.AudioBitrate == nil || *got.AudioBitrate != 192000 {
		t.Fatalf("HLS master request = %+v (calls %d), want audio-only ts/aac at 192000 for the negotiated session", got, hls.calls)
	}
	if got.TranscodeReasons != "ContainerNotSupported,VideoCodecNotSupported,AudioCodecNotSupported" {
		t.Errorf("HLS master TranscodeReasons = %q", got.TranscodeReasons)
	}
}

type realUniversalDecision struct{ source playback.MediaSourceInfo }

func (decision realUniversalDecision) UniversalAudio(_ context.Context, query playback.UniversalAudioQuery) (playback.UniversalAudioDecision, error) {
	result, err := playback.DecideUniversalAudio(query.Request, decision.source, query.ItemID.Dashed(), nil)
	result.PlaySessionID = "universal-real-session"
	return result, err
}

type recordingHLSStreamer struct {
	playback.HLSVideoStreamer
	calls  int
	master playback.HLSRequest
}

func (streamer *recordingHLSStreamer) Master(_ context.Context, request playback.HLSRequest) (playback.HLSResponse, error) {
	streamer.calls++
	streamer.master = request
	return playback.HLSResponse{Body: io.NopCloser(strings.NewReader("#EXTM3U\n")), ContentType: "application/vnd.apple.mpegurl"}, nil
}

func realAudioRoutes(t *testing.T, files *persistedRemuxDirectFileSource, dependencies ...any) *router.Router {
	t.Helper()
	identity := auth.Identity{Authenticated: true, UserID: playbackTestUUID(t, "00000000-0000-0000-0000-0000000005f6"), Token: "real-audio-token"}
	authorizer := middleware.NewAuthorizer(playbackResolver{identity: identity}, playbackEvaluator{}, playbackUsers{})
	routes := router.New(authorizer.Wrap)
	handlers.RegisterDirectAudioStreams(routes, files, dependencies...)
	return routes
}

type realAudioManager struct {
	manager *ffmpeg.TranscodeManager
	closed  bool
}

func newRealAudioManager(t *testing.T, ffmpegPath, transcodesDir string) *realAudioManager {
	t.Helper()
	manager, err := ffmpeg.NewTranscodeManager(ffmpegPath, transcodesDir, ffmpeg.TranscodeManagerOptions{
		OutputPollInterval:     5 * time.Millisecond,
		ReaderRetryInterval:    5 * time.Millisecond,
		ReaderStallTimeout:     10 * time.Second,
		ProgressiveIdleTimeout: time.Second,
		KillGracePeriod:        time.Second,
	})
	if err != nil {
		t.Fatalf("NewTranscodeManager(%q): %v", transcodesDir, err)
	}
	owned := &realAudioManager{manager: manager}
	t.Cleanup(func() {
		if !owned.closed {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}
	})
	return owned
}

func (owned *realAudioManager) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owned.closed = true
	if err := owned.manager.Close(ctx); err != nil {
		t.Fatalf("TranscodeManager.Close(): %v", err)
	}
}

func openRealAudioResponse(t *testing.T, method, target string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, target, err)
	}
	request.Header.Set("User-Agent", "Deadfin universal audio integration")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return response
}

func requireRealAudioTools(t *testing.T) (string, string, encoding.Capabilities) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("real audio transcode integration requires ffmpeg: %v", err)
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("real audio transcode integration requires ffprobe: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		t.Fatalf("inspect ffmpeg encoders at %q: %v", ffmpegPath, err)
	}
	caps := encoding.Capabilities{AACEncoders: []string{"aac"}}
	for _, encoder := range []string{"flac", "libmp3lame", "aac"} {
		if !ffmpegAdvertisesEncoder(string(output), encoder) {
			t.Skipf("real audio transcode integration requires ffmpeg encoder %q", encoder)
		}
		caps.Encoders = append(caps.Encoders, encoder)
	}
	return ffmpegPath, ffprobePath, caps
}

func createFLACAudioFixture(t *testing.T, ffmpegPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "real-sine.flac")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
		"-ac", "2", "-c:a", "flac", path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("synthesize FLAC fixture: %v\n%s", err, output)
	}
	return path
}

func persistedFLACSource(t *testing.T, path string) playback.MediaSourceInfo {
	t.Helper()
	source := playback.NewMediaSourceInfo()
	if pinned, err := sourcefile.Open(path, sourcefile.Expected{}); err == nil {
		fingerprint := pinned.Fingerprint()
		if err := pinned.Close(); err != nil {
			t.Fatalf("close FLAC source snapshot: %v", err)
		}
		inode := int64(fingerprint.Ino)
		source.FileFacts = playback.SourceFileFacts{Size: &fingerprint.Size, ModifiedAt: &fingerprint.MTime, Inode: &inode, ChangedAt: &fingerprint.CTime}
	}
	bitrate, channels, sampleRate, bitDepth := 900_000, 2, 48_000, 16
	runtime := int64(30_000_000)
	source.ID = realAudioSourceID
	source.Protocol = playback.MediaProtocolFile
	source.Path = path
	source.Container = "flac"
	source.Bitrate = &bitrate
	source.RunTimeTicks = &runtime
	source.SourceRevision = "real-flac-source-revision"
	source.MediaStreams = []playback.MediaStream{{
		Type: playback.StreamTypeAudio, Index: 0, Codec: "flac", IsDefault: true,
		Channels: &channels, SampleRate: &sampleRate, BitDepth: &bitDepth, BitRate: &bitrate,
	}}
	return source
}

type realAudioProbe struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Channels  int    `json:"channels"`
	} `json:"streams"`
	Format realAudioProbeFormat `json:"format"`
}

type realAudioProbeFormat struct {
	FormatName string `json:"format_name"`
	BitRate    string `json:"bit_rate"`
}

func (format realAudioProbeFormat) bitrate() int {
	var value int
	_, _ = fmt.Sscan(format.BitRate, &value)
	return value
}

func probeRealAudio(t *testing.T, ffprobePath, path string) realAudioProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffprobePath, "-v", "error",
		"-show_entries", "format=format_name,bit_rate",
		"-show_entries", "stream=codec_type,codec_name,channels",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %q: %v", path, err)
	}
	var probe realAudioProbe
	if err := json.Unmarshal(output, &probe); err != nil {
		t.Fatalf("decode ffprobe JSON for %q: %v; output=%s", path, err, output)
	}
	return probe
}
