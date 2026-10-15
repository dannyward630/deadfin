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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// TestProgressiveVideoStreamRemuxesRealLocalH264AC3 verifies the complete
// public progressive-remux path: a canonical StreamInfo URL is selected against
// persisted source facts, encoded into ffmpeg argv, streamed over HTTP, and
// decoded by ffprobe. It keeps the real process manager in the loop because a
// valid builder command alone cannot prove the growing-output reader lifecycle.
func TestProgressiveVideoStreamRemuxesRealLocalH264AC3(t *testing.T) {
	ffmpegPath, ffprobePath := requireRealRemuxTools(t)
	mediaPath := createH264AC3RemuxFixture(t, ffmpegPath)
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-0000000006f1")

	for _, tc := range []struct {
		name, container, contentType, outputSuffix, expectedFormat string
		startTimeTicks                                             int64
	}{
		{
			name:           "TS at start",
			container:      "ts",
			contentType:    "video/mp2t",
			outputSuffix:   ".ts",
			expectedFormat: "mpegts",
		},
		{
			name:           "fragmented MP4 after one second",
			container:      "mp4",
			contentType:    "video/mp4",
			outputSuffix:   ".mp4",
			expectedFormat: "mp4",
			startTimeTicks: 10_000_000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcodesDir := filepath.Join(t.TempDir(), "owned-transcodes")
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
			managerClosed := false
			t.Cleanup(func() {
				if managerClosed {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = manager.Close(ctx)
			})

			source := &persistedRemuxDirectFileSource{
				itemID: itemID,
				source: persistedH264AC3Source(t, mediaPath),
			}
			service := playback.NewProgressiveVideoService(playback.ProgressiveServiceConfig{
				Sources:      source,
				TranscodeDir: transcodesDir,
				Opener:       realProgressiveManagerOpener{manager: manager},
			})
			routes := router.New()
			handlers.RegisterDirectVideoStreams(routes, source, service)
			server := httptest.NewServer(routes)
			t.Cleanup(server.Close)

			responsePath := filepath.Join(t.TempDir(), "stream"+tc.outputSuffix)
			response := openRealProgressiveHTTPResponse(t, server.URL, itemID, tc.container, tc.startTimeTicks)
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				t.Fatalf("GET stream.%s status = %d, want %d; body=%q", tc.container, response.StatusCode, http.StatusOK, body)
			}
			if got := response.Header.Get("Content-Type"); got != tc.contentType {
				_ = response.Body.Close()
				t.Fatalf("GET stream.%s Content-Type = %q, want %q", tc.container, got, tc.contentType)
			}
			for _, header := range []string{"Content-Length", "Accept-Ranges"} {
				if got := response.Header.Get(header); got != "" {
					_ = response.Body.Close()
					t.Fatalf("GET stream.%s %s = %q, want absent for a growing remux", tc.container, header, got)
				}
			}
			readCount, byteCount := saveIncrementalHTTPResponse(t, response.Body, responsePath)
			if err := response.Body.Close(); err != nil {
				t.Fatalf("close GET stream.%s response: %v", tc.container, err)
			}
			if readCount < 2 {
				t.Fatalf("GET stream.%s produced %d nonempty HTTP read(s), want incremental output", tc.container, readCount)
			}
			if byteCount == 0 {
				t.Fatalf("GET stream.%s saved no response bytes", tc.container)
			}

			assertRemuxProbe(t, ffprobePath, responsePath, tc.expectedFormat)

			closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			err = manager.Close(closeContext)
			cancelClose()
			managerClosed = true
			if err != nil {
				t.Fatalf("TranscodeManager.Close() after GET stream.%s: %v", tc.container, err)
			}
			assertOwnedRemuxArtifacts(t, transcodesDir, tc.outputSuffix)
		})
	}
}

type persistedRemuxDirectFileSource struct {
	itemID dto.UUID
	source playback.MediaSourceInfo
}

func (source *persistedRemuxDirectFileSource) StaticSource(_ context.Context, _ dto.UUID, itemID dto.UUID, mediaSourceID string) (playback.MediaSourceInfo, error) {
	if itemID != source.itemID {
		return playback.MediaSourceInfo{}, fmt.Errorf("persisted source belongs to item %s, not %s", source.itemID, itemID)
	}
	if mediaSourceID != source.source.ID {
		return playback.MediaSourceInfo{}, fmt.Errorf("persisted source %q was not selected", mediaSourceID)
	}
	return source.source, nil
}

type realProgressiveManagerOpener struct {
	manager *ffmpeg.TranscodeManager
}

func (opener realProgressiveManagerOpener) Open(ctx context.Context, request playback.ProgressiveOpenRequest) (io.ReadCloser, error) {
	return opener.manager.OpenProgressive(ctx, ffmpeg.ProgressiveRequest{
		OutputPath:     request.OutputPath,
		Args:           request.Args,
		PlaySessionID:  request.PlaySessionID,
		DeviceID:       request.DeviceID,
		SourceRevision: request.SourceRevision,
		OpenSource:     request.OpenSource,
	})
}

func persistedH264AC3Source(t *testing.T, path string) playback.MediaSourceInfo {
	t.Helper()
	pinned, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("pin real progressive source: %v", err)
	}
	fingerprint := pinned.Fingerprint()
	if err := pinned.Close(); err != nil {
		t.Fatalf("close real progressive source snapshot: %v", err)
	}
	inode := int64(fingerprint.Ino)
	nalLengthSize := "4"
	profile := "High"
	level := 30.0
	refFrames := 3
	bitDepth := 8
	width, height := 320, 180
	isAVC := true
	channels, sampleRate, bitRate := 6, 48_000, 192_000
	return playback.MediaSourceInfo{
		ID:             "persisted-local-h264-ac3-mp4",
		Protocol:       playback.MediaProtocolFile,
		Path:           path,
		Container:      "mp4",
		SourceRevision: "real-progressive-source-revision",
		FileFacts: playback.SourceFileFacts{
			Size:       &fingerprint.Size,
			ModifiedAt: &fingerprint.MTime,
			Inode:      &inode,
			ChangedAt:  &fingerprint.CTime,
		},
		SupportsTranscoding:  true,
		SupportsDirectStream: true,
		MediaStreams: []playback.MediaStream{
			{
				Type:          playback.StreamTypeVideo,
				Index:         0,
				Codec:         "h264",
				NalLengthSize: &nalLengthSize,
				Profile:       &profile,
				Level:         &level,
				RefFrames:     &refFrames,
				BitDepth:      &bitDepth,
				Width:         &width,
				Height:        &height,
				IsAVC:         &isAVC,
			},
			{
				Type:       playback.StreamTypeAudio,
				Index:      1,
				Codec:      "ac3",
				Channels:   &channels,
				SampleRate: &sampleRate,
				BitRate:    &bitRate,
				IsDefault:  true,
			},
		},
	}
}

func openRealProgressiveHTTPResponse(t *testing.T, serverURL string, itemID dto.UUID, container string, startTimeTicks int64) *http.Response {
	t.Helper()
	query := url.Values{
		"MediaSourceId":       {"persisted-local-h264-ac3-mp4"},
		"VideoCodec":          {"h264,hevc"},
		"AudioCodec":          {"ac3,aac"},
		"VideoStreamIndex":    {"0"},
		"AudioStreamIndex":    {"1"},
		"SubtitleStreamIndex": {"-1"},
		"StartTimeTicks":      {strconv.FormatInt(startTimeTicks, 10)},
		"DeviceId":            {"real-remux-device"},
		"PlaySessionId":       {"real-remux-session-" + container},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/Videos/"+itemID.Dashed()+"/stream."+container+"?"+query.Encode(), nil)
	if err != nil {
		t.Fatalf("build GET stream.%s request: %v", container, err)
	}
	request.Header.Set("User-Agent", "Deadfin progressive real-remux integration")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatalf("GET stream.%s: %v", container, err)
	}
	return response
}

func saveIncrementalHTTPResponse(t *testing.T, body io.Reader, destination string) (int, int64) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatalf("create saved HTTP response %q: %v", destination, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatalf("close saved HTTP response %q: %v", destination, err)
		}
	}()

	buffer := make([]byte, 1024)
	readCount := 0
	var byteCount int64
	for {
		count, readErr := body.Read(buffer)
		if count > 0 {
			readCount++
			written, writeErr := file.Write(buffer[:count])
			byteCount += int64(written)
			if writeErr != nil {
				t.Fatalf("save HTTP response %q: %v", destination, writeErr)
			}
			if written != count {
				t.Fatalf("save HTTP response %q: wrote %d bytes, want %d", destination, written, count)
			}
		}
		if readErr == io.EOF {
			return readCount, byteCount
		}
		if readErr != nil {
			t.Fatalf("read incremental HTTP response: %v", readErr)
		}
	}
}

type remuxProbe struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func assertRemuxProbe(t *testing.T, ffprobePath, path, expectedFormat string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_entries", "format=format_name",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		t.Fatalf("ffprobe saved progressive output %q: %v", path, err)
	}
	var probe remuxProbe
	if err := json.Unmarshal(output, &probe); err != nil {
		t.Fatalf("decode ffprobe JSON for %q: %v; output=%s", path, err, output)
	}
	if !containsRemuxToken(strings.Split(probe.Format.FormatName, ","), expectedFormat) {
		t.Fatalf("ffprobe format_name = %q, want container token %q", probe.Format.FormatName, expectedFormat)
	}
	codecs := make(map[string]string, len(probe.Streams))
	for _, stream := range probe.Streams {
		codecs[stream.CodecType] = stream.CodecName
	}
	if got := codecs["video"]; got != "h264" {
		t.Fatalf("ffprobe video codec = %q, want copied h264", got)
	}
	if got := codecs["audio"]; got != "ac3" {
		t.Fatalf("ffprobe audio codec = %q, want copied ac3", got)
	}
}

func assertOwnedRemuxArtifacts(t *testing.T, transcodesDir, outputSuffix string) {
	t.Helper()
	entries, err := os.ReadDir(transcodesDir)
	if err != nil {
		t.Fatalf("read owned transcodes directory %q after Close: %v", transcodesDir, err)
	}
	var outputCount, stderrCount int
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			t.Fatalf("owned transcodes directory contains non-file artifact %q after Close", entry.Name())
		}
		path := filepath.Join(transcodesDir, entry.Name())
		relative, err := filepath.Rel(transcodesDir, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("transcode artifact %q escaped owned directory %q", path, transcodesDir)
		}
		switch {
		case strings.HasSuffix(entry.Name(), outputSuffix):
			outputCount++
		case strings.HasSuffix(entry.Name(), outputSuffix+".stderr.log"):
			stderrCount++
		default:
			t.Fatalf("unexpected retained transcode artifact %q after Close", entry.Name())
		}
	}
	if outputCount != 1 || stderrCount != 1 {
		t.Fatalf("owned transcodes directory has outputs=%d stderr logs=%d, want one retained completed remux and its log", outputCount, stderrCount)
	}
}

func requireRealRemuxTools(t *testing.T) (string, string) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("real progressive-remux integration requires ffmpeg executable: %v", err)
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("real progressive-remux integration requires ffprobe executable: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	encoders, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		t.Fatalf("inspect ffmpeg encoders at %q: %v", ffmpegPath, err)
	}
	for _, encoder := range []string{"libx264", "ac3"} {
		if !ffmpegAdvertisesEncoder(string(encoders), encoder) {
			t.Skipf("real progressive-remux integration requires ffmpeg encoder %q, but %q does not advertise it", encoder, ffmpegPath)
		}
	}
	return ffmpegPath, ffprobePath
}

func ffmpegAdvertisesEncoder(encoders, required string) bool {
	for _, line := range strings.Split(encoders, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == required {
			return true
		}
	}
	return false
}

func createH264AC3RemuxFixture(t *testing.T, ffmpegPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "real-h264-ac3.mp4")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
		"-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast", "-profile:v", "high", "-level:v", "3.0", "-refs", "3", "-pix_fmt", "yuv420p",
		"-g", "24", "-keyint_min", "24", "-sc_threshold", "0",
		"-c:a", "ac3", "-ac", "6", "-ar", "48000", "-b:a", "192k",
		"-movflags", "+faststart",
		path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("synthesize H264/AC3 MP4 fixture: %v\n%s", err, output)
	}
	return path
}

func containsRemuxToken(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
