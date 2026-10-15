// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// nvencServiceCapabilities is jellyfin-ffmpeg 8.1 with the NVENC encoders
// that the device probe kept.
func nvencServiceCapabilities(encoders ...string) encoding.Capabilities {
	return encoding.Capabilities{
		H264Encoder:         "libx264",
		EncoderMajorVersion: 8, EncoderMinorVersion: 1,
		AACEncoders:      []string{"aac"},
		SupportsTonemapX: true,
		Encoders:         append([]string{"libx264"}, encoders...),
		Decoders:         []string{"h264_cuvid", "hevc_cuvid"},
		HWAccels:         []string{"cuda"},
		Filters:          []string{"alphasrc", "hwupload_cuda", "overlay_cuda", "scale_cuda", "tonemap_cuda", "tonemapx", "yadif_cuda"},
	}
}

// The transcode codec follows the client's VideoCodec order after
// ShiftVideoCodecsIfNeeded, and only codecs with a probed encoder qualify.
func TestHLSServiceMasterSelectsTranscodeCodecByProbedEncoder(t *testing.T) {
	ada := nvencServiceCapabilities("av1_nvenc", "h264_nvenc", "hevc_nvenc")
	ampere := nvencServiceCapabilities("h264_nvenc", "hevc_nvenc")
	for _, test := range []struct {
		name       string
		caps       encoding.Capabilities
		allowHevc  bool
		allowAv1   bool
		codecs     []string
		wantCodecs string
		wantArg    string
	}{
		{"Ada AV1 allowed", ada, true, true, []string{"av1", "hevc", "h264"}, "av01.0.15M.08", "av1_nvenc"}, // NormalizeTranscodingLevel caps AV1 at 15.
		{"Ampere skips AV1 for HEVC", ampere, true, true, []string{"av1", "hevc", "h264"}, "hvc1.1.4.L120.B0", "hevc_nvenc"},
		{"AV1 and HEVC not allowed", ada, false, false, []string{"av1", "hevc", "h264"}, "avc1.640029", "h264_nvenc"},
		{"only disallowed codecs keep their order", ada, false, false, []string{"hevc", "av1"}, "hvc1.1.4.L120.B0", "hevc_nvenc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &progressiveFakeSource{source: progressiveCopySource("/media/anime.mkv")}
			manager := &hlsFakeManager{playlist: "#EXTM3U\n"}
			config := hlsServiceConfig(source, manager)
			config.Capabilities = test.caps
			config.Hardware = func() encoding.HardwareOptions {
				return encoding.HardwareOptions{
					Type: encoding.HardwareAccelerationNVENC, EnableHardwareEncoding: true,
					HardwareDecodingCodecs: []string{"h264", "hevc"}, EnableEnhancedNvdecDecoder: true,
					AllowHevcEncoding: test.allowHevc, AllowAv1Encoding: test.allowAv1,
				}
			}
			service := playback.NewHLSService(config)
			request := hlsTranscodeRequest()
			request.SegmentContainer = "mp4"
			request.VideoCodec = strings.Join(test.codecs, ",")
			request.VideoCodecs = test.codecs

			response, err := service.Master(t.Context(), request)
			if err != nil {
				t.Fatalf("Master(): %v", err)
			}
			if playlist := readHLSResponse(t, response); !strings.Contains(playlist, "CODECS=\""+test.wantCodecs+",mp4a") {
				t.Fatalf("Master() playlist = %q, want CODECS %s", playlist, test.wantCodecs)
			}
			// A live request starts the job, which exposes the argv.
			request.Live = true
			request.MinSegments = 1
			if _, err := service.Live(t.Context(), request); err != nil {
				t.Fatalf("Live(): %v", err)
			}
			if args := strings.Join(manager.lastStart.Args, " "); !strings.Contains(args, "-codec:v:0 "+test.wantArg+" ") {
				t.Fatalf("Live() args = %q, want %s", args, test.wantArg)
			}
		})
	}
}

// An AV1-only request on a GPU without an AV1 encoder cannot transcode.
func TestHLSServiceRejectsAV1OnlyRequestWithoutEncoder(t *testing.T) {
	source := &progressiveFakeSource{source: progressiveCopySource("/media/anime.mkv")}
	config := hlsServiceConfig(source, &hlsFakeManager{})
	config.Capabilities = nvencServiceCapabilities("h264_nvenc", "hevc_nvenc")
	config.Hardware = func() encoding.HardwareOptions {
		return encoding.HardwareOptions{Type: encoding.HardwareAccelerationNVENC, EnableHardwareEncoding: true, AllowAv1Encoding: true}
	}
	request := hlsTranscodeRequest()
	request.SegmentContainer = "mp4"
	request.VideoCodec = "av1"
	request.VideoCodecs = []string{"av1"}
	if _, err := playback.NewHLSService(config).Master(t.Context(), request); !errors.Is(err, playback.ErrProgressiveUnsupported) {
		t.Fatalf("Master() error = %v, want ErrProgressiveUnsupported", err)
	}
}
