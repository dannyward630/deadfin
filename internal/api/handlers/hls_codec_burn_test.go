// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// The PlaybackInfo URL is a public handoff to the HLS handler. A selected
// external subtitle remains externally delivered unless the client explicitly
// asks to burn it while the video is being transcoded.
func TestPlaybackInfoGeneratedHLSURLPropagatesForcedExternalSubtitleBurn(t *testing.T) {
	for _, test := range []struct {
		name, requestBurn string
		wantBurn          bool
	}{
		{name: "forced burn for video transcode", requestBurn: "true", wantBurn: true},
		{name: "ordinary external subtitle remains external", requestBurn: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007c8")
			container, path := "mkv", "/media/codec-burn-feature.mkv"
			videoProfile := "Simple Profile"
			externalPath := filepath.Join(t.TempDir(), "codec-burn-feature.en.srt")
			if err := os.WriteFile(externalPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nSubtitle\n"), 0o600); err != nil {
				t.Fatalf("write external subtitle: %v", err)
			}
			videoBitrate, audioBitrate, totalBitrate := 800_000, 128_000, 928_000
			width, height := 1280, 720
			playbackInfo := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
				Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{hlsCanonicalTestRecord(playback.MediaSourceRecord{
					ID: itemID, Path: &path, Container: &container, TotalBitrate: &totalBitrate,
					MediaStreams: []playback.MediaStream{
						{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", Profile: &videoProfile, BitRate: &videoBitrate, Width: &width, Height: &height},
						{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", BitRate: &audioBitrate, IsDefault: true},
						{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, SupportsExternalStream: true, Path: externalPath},
					},
				})}},
				Permissions: playbackConcretePermissionReader{},
				Network:     playbackConcreteNetwork{},
				NewPlaySessionID: func() string {
					return "codec-burn-session"
				},
			})
			manager := &codecBurnHLSManager{}
			hlsService := playback.NewHLSService(playback.HLSServiceConfig{
				Sources:      playbackInfo,
				TranscodeDir: "/cache",
				Capabilities: encoding.Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}},
				Manager:      manager,
			})
			routes := router.New()
			handlers.RegisterPlaybackInfo(routes, playbackInfo, nil)
			handlers.RegisterHLSStreams(routes, hlsService, slog.New(slog.NewTextHandler(io.Discard, nil)))

			body := `{"DeviceId":"codec-burn-device","MaxStreamingBitrate":1000000,"SubtitleStreamIndex":2,"AlwaysBurnInSubtitleWhenTranscoding":` + test.requestBurn + `,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Context":"Streaming","Container":"ts","VideoCodec":"h264","AudioCodec":"aac","Protocol":"hls"}],"SubtitleProfiles":[{"Format":"srt","Method":"External"}]}}`
			playbackRecord := request(routes, http.MethodPost, "/Items/"+itemID.Dashed()+"/PlaybackInfo", body, "application/json", "")
			assertStatus(t, playbackRecord, http.StatusOK)

			var resolved playback.PlaybackInfoResponse
			if err := json.Unmarshal(playbackRecord.Body.Bytes(), &resolved); err != nil {
				t.Fatalf("decode PlaybackInfo response: %v; body=%s", err, playbackRecord.Body.Bytes())
			}
			if len(resolved.MediaSources) != 1 || resolved.MediaSources[0].TranscodingURL == "" {
				t.Fatalf("PlaybackInfo sources = %#v, want exactly one generated HLS URL", resolved.MediaSources)
			}
			source := resolved.MediaSources[0]
			if len(source.MediaStreams) != 3 || source.MediaStreams[2].DeliveryMethod == nil || *source.MediaStreams[2].DeliveryMethod != playback.SubtitleExternal {
				t.Fatalf("PlaybackInfo external subtitle delivery = %#v, want External", source.MediaStreams)
			}

			generated, err := url.Parse(source.TranscodingURL)
			if err != nil {
				t.Fatalf("parse generated HLS URL %q: %v", source.TranscodingURL, err)
			}
			query := generated.Query()
			if got := query.Get("alwaysBurnInSubtitleWhenTranscoding"); (got == "true") != test.wantBurn {
				t.Fatalf("generated HLS URL burn query = %q, want requested=%t; URL=%q", got, test.wantBurn, source.TranscodingURL)
			}
			if got := query.Get("SubtitleStreamIndex"); (got == "2") != test.wantBurn {
				t.Fatalf("generated HLS URL subtitle selection = %q, want present only for forced burn=%t; URL=%q", got, test.wantBurn, source.TranscodingURL)
			}

			// Reuse the generated URL's exact negotiated query on the dynamic
			// segment endpoint, whose handler reaches the production encoding job.
			query.Set("runtimeTicks", "0")
			query.Set("actualSegmentLengthTicks", "60000000")
			segmentURL := "/Videos/" + itemID.Dashed() + "/hls1/codec-burn/0.ts?" + query.Encode()
			segmentRecord := request(routes, http.MethodGet, segmentURL, "", "", "")
			assertStatus(t, segmentRecord, http.StatusOK)
			if manager.restartCalls != 1 {
				t.Fatalf("HLS segment restart calls = %d, want one", manager.restartCalls)
			}
			gotBurn := codecBurnSubtitleFilter(manager.lastRestart.Args, externalPath)
			if gotBurn != test.wantBurn {
				t.Fatalf("HLS EncodingJob subtitle burn = %t, want %t; argv=%q", gotBurn, test.wantBurn, manager.lastRestart.Args)
			}
		})
	}
}

// A downmix boost is an audio-transcode adjustment, not a general gain control:
// it belongs only on the multichannel-to-stereo rendition that needs it.
func TestHLSSegmentProjectsDownmixBoostOnlyWhenReducingChannels(t *testing.T) {
	for _, test := range []struct {
		name           string
		sourceChannels int
		wantFilter     string
	}{
		{name: "six channel source downmixed to stereo", sourceChannels: 6, wantFilter: "volume=2"},
		{name: "stereo source remains stereo", sourceChannels: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			itemID := hlsTestUUID(t, "00000000-0000-0000-0000-0000000007c9")
			container, path := "mkv", "/media/downmix-feature.mkv"
			videoProfile := "Simple Profile"
			videoBitrate, audioBitrate, totalBitrate := 800_000, 128_000, 928_000
			width, height := 1280, 720
			playbackInfo := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
				Sources: playbackConcreteSourceReader{records: []playback.MediaSourceRecord{hlsCanonicalTestRecord(playback.MediaSourceRecord{
					ID: itemID, Path: &path, Container: &container, TotalBitrate: &totalBitrate,
					MediaStreams: []playback.MediaStream{
						{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", Profile: &videoProfile, BitRate: &videoBitrate, Width: &width, Height: &height},
						{Index: 1, Type: playback.StreamTypeAudio, Codec: "ac3", BitRate: &audioBitrate, Channels: &test.sourceChannels, IsDefault: true},
					},
				})}},
				Permissions: playbackConcretePermissionReader{},
				Network:     playbackConcreteNetwork{},
				NewPlaySessionID: func() string {
					return "downmix-session"
				},
			})
			manager := &codecBurnHLSManager{}
			hlsService := playback.NewHLSService(playback.HLSServiceConfig{
				Sources:      playbackInfo,
				TranscodeDir: "/cache",
				Capabilities: encoding.Capabilities{H264Encoder: "libx264", AACEncoders: []string{"aac"}},
				Manager:      manager,
			})
			routes := router.New()
			handlers.RegisterHLSStreams(routes, hlsService, slog.New(slog.NewTextHandler(io.Discard, nil)))
			segmentURL := "/Videos/" + itemID.Dashed() + "/hls1/downmix/0.ts?MediaSourceId=" + itemID.Dashed() + "&DeviceId=downmix-device&PlaySessionId=downmix-session&SegmentContainer=ts&SegmentLength=6&VideoCodec=h264&AudioCodec=aac&VideoBitRate=800000&AudioBitRate=128000&MaxAudioChannels=2&runtimeTicks=0&actualSegmentLengthTicks=60000000&downMixAudioBoost=2"
			record := request(routes, http.MethodGet, segmentURL, "", "", "")
			assertStatus(t, record, http.StatusOK)
			if manager.restartCalls != 1 {
				t.Fatalf("HLS segment restart calls = %d, want one", manager.restartCalls)
			}
			filter := ""
			for index := 0; index+1 < len(manager.lastRestart.Args); index++ {
				if manager.lastRestart.Args[index] == "-af" {
					filter = manager.lastRestart.Args[index+1]
					break
				}
			}
			if test.wantFilter == "" {
				if strings.Contains(filter, "volume=") {
					t.Fatalf("stereo-preserving HLS argv audio filter = %q, must not apply a downmix gain; argv=%q", filter, manager.lastRestart.Args)
				}
				return
			}
			if !strings.Contains(filter, test.wantFilter) {
				t.Fatalf("downmixed HLS argv audio filter = %q, want %q; argv=%q", filter, test.wantFilter, manager.lastRestart.Args)
			}
		})
	}
}

func codecBurnSubtitleFilter(args []string, subtitlePath string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-vf" && strings.Contains(args[index+1], "subtitles=f='"+subtitlePath+"'") {
			return true
		}
	}
	return false
}

type codecBurnHLSManager struct {
	restartCalls int
	lastRestart  playback.HLSJobRequest
}

func (manager *codecBurnHLSManager) StartHLS(context.Context, playback.HLSJobRequest) error {
	return nil
}

func (manager *codecBurnHLSManager) RestartHLSAt(_ context.Context, request playback.HLSJobRequest, _ int) error {
	manager.restartCalls++
	manager.lastRestart = request
	return nil
}

func (manager *codecBurnHLSManager) WaitSegment(context.Context, string, int, *playback.HLSSegmentTiming) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("segment")), nil
}

func (manager *codecBurnHLSManager) WaitPlaylist(context.Context, string, int) (string, error) {
	return "#EXTM3U\n", nil
}

func (manager *codecBurnHLSManager) PingPlaySession(string) bool { return true }

func (manager *codecBurnHLSManager) StopPlaySession(context.Context, string, string) error {
	return nil
}

var _ playback.HLSManager = (*codecBurnHLSManager)(nil)
