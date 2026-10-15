// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// sidecarFirstSource returns a source whose MediaStreams list the external
// subtitles first, as FFProbeVideoInfo.cs:233-239 numbers them. The media
// file holds, in ffmpeg order: 0 h264, 1 ac3, 2 aac, 3 srt, 4 dvdsub.
// MediaStream indexes are those positions plus the number of sidecars.
func sidecarFirstSource(path string, sidecars int) playback.MediaSourceInfo {
	source := progressiveCopySource(path)
	video, ac3 := source.MediaStreams[0], source.MediaStreams[1]
	aacChannels, aacBitrate := 2, 128_000
	english := "eng"
	streams := make([]playback.MediaStream, 0, sidecars+5)
	for index := range sidecars {
		streams = append(streams, playback.MediaStream{
			Type:       playback.StreamTypeSubtitle,
			Index:      index,
			Codec:      "subrip",
			IsExternal: true,
			Path:       strings.TrimSuffix(path, filepath.Ext(path)) + "." + strconv.Itoa(index) + ".srt",
		})
	}
	video.Index = sidecars
	ac3.Index = sidecars + 1
	streams = append(streams,
		video,
		ac3,
		playback.MediaStream{Type: playback.StreamTypeAudio, Index: sidecars + 2, Codec: "aac", Channels: &aacChannels, BitRate: &aacBitrate, Language: &english},
		playback.MediaStream{Type: playback.StreamTypeSubtitle, Index: sidecars + 3, Codec: "srt", Language: &english},
		playback.MediaStream{Type: playback.StreamTypeSubtitle, Index: sidecars + 4, Codec: "dvdsub"},
	)
	source.MediaStreams = streams
	return source
}

func streamMapValues(args []string) []string {
	var maps []string
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-map" {
			maps = append(maps, args[index+1])
		}
	}
	return maps
}

func requireMaps(t *testing.T, args []string, want ...string) {
	t.Helper()
	got := streamMapValues(args)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ffmpeg -map values = %q, want %q; argv=%q", got, want, args)
	}
}

func TestHLSServiceMapsFileStreamIndexesWithSidecarSubtitles(t *testing.T) {
	for _, sidecars := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(sidecars)+" sidecars", func(t *testing.T) {
			t.Run("transcode with selected audio", func(t *testing.T) {
				manager := &hlsFakeManager{segment: "segment"}
				service := newHLSService(&progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}, manager)
				request := hlsTranscodeRequest()
				request.AudioStreamIndex = hlsInt(sidecars + 2)
				if _, err := service.Segment(t.Context(), request); err != nil {
					t.Fatalf("Segment(): %v", err)
				}
				requireMaps(t, manager.lastRestart.Args, "0:0", "0:2", "-0:s")
			})

			t.Run("remux with video copy", func(t *testing.T) {
				manager := &hlsFakeManager{segment: "segment"}
				service := newHLSService(&progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}, manager)
				request := hlsTranscodeRequest()
				request.AllowVideoStreamCopy = true
				request.AllowAudioStreamCopy = true
				request.EnableAutoStreamCopy = true
				request.VideoBitrate = hlsInt(2_600_000)
				request.AudioBitrate = hlsInt(384_000)
				request.MaxAudioChannels = hlsInt(6)
				request.AudioCodec = "ac3"
				request.AudioCodecs = []string{"ac3"}
				request.AudioStreamIndex = hlsInt(sidecars + 1)
				if _, err := service.Segment(t.Context(), request); err != nil {
					t.Fatalf("Segment(): %v", err)
				}
				args := manager.lastRestart.Args
				if !hlsArgPair(args, "-codec:v:0", "copy") {
					t.Fatalf("remux argv = %q, want video stream copy", args)
				}
				requireMaps(t, args, "0:0", "0:1", "-0:s")
			})

			t.Run("burn-in of embedded text subtitle", func(t *testing.T) {
				manager := &hlsFakeManager{segment: "segment"}
				service := newHLSService(&progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}, manager)
				request := hlsTranscodeRequest()
				delivery := playback.SubtitleEncode
				request.SubtitleStreamIndex = hlsInt(sidecars + 3)
				request.SubtitleDeliveryMethod = &delivery
				if _, err := service.Segment(t.Context(), request); err != nil {
					t.Fatalf("Segment(): %v", err)
				}
				args := manager.lastRestart.Args
				requireMaps(t, args, "0:0", "0:1")
				// si= counts the subtitle streams of the media file only.
				if filter := hlsConstraintArgumentValue(args, "-vf"); !strings.Contains(filter, ":si=0") {
					t.Fatalf("burn-in -vf = %q, want the first embedded subtitle (si=0)", filter)
				}
			})

			t.Run("burn-in of embedded graphical subtitle", func(t *testing.T) {
				manager := &hlsFakeManager{segment: "segment"}
				service := newHLSService(&progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}, manager)
				request := hlsTranscodeRequest()
				delivery := playback.SubtitleEncode
				request.SubtitleStreamIndex = hlsInt(sidecars + 4)
				request.SubtitleDeliveryMethod = &delivery
				if _, err := service.Segment(t.Context(), request); err != nil {
					t.Fatalf("Segment(): %v", err)
				}
				args := manager.lastRestart.Args
				requireMaps(t, args, "0:0", "0:1", "-0:0")
				graph := hlsConstraintArgumentValue(args, "-filter_complex")
				if !strings.HasPrefix(graph, "[0:4]") || !strings.Contains(graph, "[0:0]") {
					t.Fatalf("burn-in -filter_complex = %q, want subtitle [0:4] over video [0:0]", graph)
				}
			})

			t.Run("audio-only", func(t *testing.T) {
				manager := &hlsFakeManager{segment: "segment"}
				service := newHLSService(&progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}, manager)
				request := hlsAudioOnlyRequest()
				request.AudioStreamIndex = hlsInt(sidecars + 2)
				if _, err := service.Segment(t.Context(), request); err != nil {
					t.Fatalf("Segment(): %v", err)
				}
				requireMaps(t, manager.lastRestart.Args, "0:2")
			})
		})
	}
}

func TestProgressiveServiceMapsFileStreamIndexesWithSidecarSubtitles(t *testing.T) {
	for _, sidecars := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(sidecars)+" sidecars", func(t *testing.T) {
			source := &progressiveFakeSource{source: sidecarFirstSource("/media/feature.mkv", sidecars)}
			opener := &progressiveFakeOpener{body: io.NopCloser(strings.NewReader("remux"))}
			service := playback.NewProgressiveService(playback.ProgressiveServiceConfig{Sources: source, TranscodeDir: "/cache", Opener: opener})
			request := progressiveCopyRequest()
			request.AudioStreamIndex = hlsInt(sidecars + 1)
			response, err := service.OpenProgressive(t.Context(), request)
			if err != nil {
				t.Fatalf("OpenProgressive(): %v", err)
			}
			_ = response.Body.Close()
			requireMaps(t, opener.request.Args, "0:0", "0:1", "-0:s")
		})
	}
}

func TestSubtitleServiceConvertsEmbeddedStreamByFileIndexWithSidecars(t *testing.T) {
	container := filepath.Join(t.TempDir(), "feature.mkv")
	if err := os.WriteFile(container, []byte("container bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: sidecarFirstSource(container, 2)}
	sources.source.FileFacts = playback.SourceFileFacts{}
	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nembedded\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})

	if _, err := service.Stream(t.Context(), subtitleStreamRequest(5, "vtt")); err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if converter.calls != 1 || converter.jobs[0].StreamIndex != 3 {
		t.Fatalf("ConvertJob = %#v, want file stream 3 for MediaStream 5", converter.jobs)
	}
}
