// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
)

// fakeJellyfinFFmpeg returns an ffmpeg that prints the jellyfin-ffmpeg 8.1.1
// probe output from testdata. The list probes call "ffmpeg -nostdin -<list>".
// The runtime-key probe calls "ffmpeg -hide_banner -f lavfi ..." and gets the
// key help that jellyfin-ffmpeg prints for "?".
func fakeJellyfinFFmpeg(t *testing.T) string {
	t.Helper()
	data, err := filepath.Abs("testdata/jellyfin-ffmpeg-8.1.1")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n[ \"$2\" = -f ] && exec cat \"" + data + "/keys.txt\"\nexec cat \"" + data + "/${2#-}.txt\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestJellyfinFFmpegProbeSelectsFullQSVPipeline runs the startup probe
// against jellyfin-ffmpeg 8.1.1 output and builds the HLS argv for the
// encoding.xml of a QSV deployment. The full hardware pipeline needs the
// OpenCL, VAAPI, and alphasrc filters from the probe.
func TestJellyfinFFmpegProbeSelectsFullQSVPipeline(t *testing.T) {
	probed, err := ffmpeg.ProbeCapabilities(context.Background(), fakeJellyfinFFmpeg(t))
	if err != nil {
		t.Fatal(err)
	}
	caps := encodingCapabilities(probed, ffmpeg.NvidiaCapabilities{})
	if !caps.SupportsTonemapX || !caps.SupportsSubtitleExtraction {
		t.Fatalf("probe lost tonemapx or subtitles: %#v", caps.Filters)
	}
	// jellyfin-ffmpeg has the "p" pause key. Without it the throttler sends
	// "c", which does not pause the threaded FFmpeg 8 pipeline (#523).
	if !caps.SupportsPKeyPause {
		t.Fatal("probe lost the jellyfin-ffmpeg p-key pause")
	}

	options := config.DefaultEncodingOptions()
	options.HardwareAccelerationType = encoding.HardwareAccelerationQSV
	options.HardwareDecodingCodecs = []string{"h264", "hevc", "vp9", "vc1"}
	options.EnableTonemapping = true
	options.EnableVppTonemapping = true
	hardware := hardwareOptions(options, func(string) bool { return true })

	job := func(width, height int) encoding.HLSJob {
		return encoding.HLSJob{
			EncodingJob: encoding.EncodingJob{
				InputPath: "/media/show/episode.mkv", InputContainer: "mkv",
				OutputPath: "/cache/transcodes/abc.m3u8", OutputContainer: "ts",
				VideoStream: encoding.VideoStream{
					Index: 0, Codec: "hevc", ColorTransfer: "smpte2084", BitDepth: 10, NalLengthSize: "4",
					Width: width, Height: height, PixelFormat: "yuv420p10le", Profile: "Main 10", RangeType: "HDR10", RealFrameRate: 23.976,
				},
				AudioStream:      encoding.AudioStream{Index: 1, Codec: "aac", Channels: 2},
				OutputVideoCodec: "h264", OutputAudioCodec: "copy", OutputVideoProfile: "high", OutputVideoLevel: 41,
				OutputWidth: 1920, OutputHeight: 1080, OutputVideoBitrate: 8_000_000,
				VideoQuality: encoding.VideoQuality{Preset: "veryfast", CRF: 23, ForcedKeyFrameSeconds: 3, X264Options: "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0"},
				Hardware:     hardware,
			},
			SegmentLengthSeconds: 3,
		}
	}

	for _, test := range []struct {
		name string
		job  encoding.HLSJob
		want []string
	}{
		{
			name: "1080p HEVC 10-bit HDR10 with PGS burn-in",
			job: func() encoding.HLSJob {
				job := job(1920, 1080)
				job.SubtitleStream = &encoding.SubtitleStream{Index: 4, Codec: "hdmv_pgs_subtitle", BurnIn: true}
				return job
			}(),
			want: []string{
				"-hwaccel vaapi -hwaccel_output_format vaapi",
				"-filter_hw_device qs",
				"[0:4]scale,scale=-1:1080:fast_bilinear",
				"hwupload=derive_device=qsv:extra_hw_frames=64[sub]",
				// Same-size VPP tone mapping needs no scale_vaapi.
				"[0:0]setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,procamp_vaapi=b=16,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709:extra_hw_frames=32,hwmap=derive_device=qsv,format=qsv[main]",
				"[main][sub]overlay_qsv=eof_action=pass:repeatlast=0:w=1920:h=1080",
				"-codec:v:0 h264_qsv",
			},
		},
		{
			name: "1080p HEVC 10-bit SDR with PGS burn-in",
			job: func() encoding.HLSJob {
				job := job(1920, 1080)
				job.VideoStream.ColorTransfer, job.VideoStream.RangeType = "bt709", "SDR"
				job.SubtitleStream = &encoding.SubtitleStream{Index: 4, Codec: "hdmv_pgs_subtitle", BurnIn: true}
				return job
			}(),
			want: []string{
				"-hwaccel vaapi -hwaccel_output_format vaapi",
				"scale_vaapi=format=nv12",
				"hwmap=derive_device=qsv,format=qsv[main]",
				"[main][sub]overlay_qsv=eof_action=pass:repeatlast=0:w=1920:h=1080",
			},
		},
		{
			name: "4K HDR10 to 1080p SDR",
			job:  job(3840, 2160),
			want: []string{
				"-init_hw_device opencl=ocl@va",
				"-hwaccel vaapi -hwaccel_output_format vaapi",
				"-vf setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,scale_vaapi=w=1920:h=1080:extra_hw_frames=24,procamp_vaapi=b=16,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709:extra_hw_frames=32,hwmap=derive_device=qsv,format=qsv",
				"-codec:v:0 h264_qsv",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := encoding.BuildHLSArgs(test.job, caps)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ")
			for _, want := range test.want {
				if !strings.Contains(joined, want) {
					t.Errorf("argv lacks %q\nargv: %s", want, joined)
				}
			}
			// The legacy copy-back chain ends the main graph in system
			// memory (format=nv12) or tone maps in software.
			for _, software := range []string{"format=nv12[main]", "tonemapx"} {
				if strings.Contains(joined, software) {
					t.Errorf("argv has software filter %q\nargv: %s", software, joined)
				}
			}
		})
	}
}
