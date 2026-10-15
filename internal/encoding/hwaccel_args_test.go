// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strings"
	"testing"
)

// The goldens in this file are the argv the pinned EncodingHelper.cs builds
// for the same media facts and EncodingOptions on Linux with jellyfin-ffmpeg
// 8.1: GetInputVideoHwaccelArgs (:1011-1243), GetIntelQsvVaapiVidFiltersPrefered
// (:4747-5007), GetIntelVaapiFullVidFiltersPrefered (:5078-5313),
// GetSwVidFilterChain (:3853-3973), GetVideoQualityParam (:2089-2397), and
// GetHlsVideoKeyFrameArguments (:2004-2087). No GPU is needed: the builder is
// pure.

func jellyfinFFmpegCapabilities() Capabilities {
	return Capabilities{
		H264Encoder:                "libx264",
		EncoderMajorVersion:        8,
		EncoderMinorVersion:        1,
		EncoderPatchVersion:        1,
		SupportsSubtitleExtraction: true,
		AACEncoders:                []string{"aac"},
		SupportsTonemapX:           true,
		Encoders:                   []string{"h264_qsv", "h264_vaapi", "hevc_qsv", "libx264"},
		Decoders:                   []string{"h264_qsv", "hevc_qsv", "vp9_qsv"},
		HWAccels:                   []string{"drm", "opencl", "qsv", "vaapi"},
		Filters: []string{
			"alphasrc", "deinterlace_vaapi", "hwupload_vaapi", "overlay_opencl", "overlay_qsv", "overlay_vaapi",
			"procamp_vaapi", "scale_opencl", "scale_vaapi", "subtitles", "tonemap_opencl", "tonemap_vaapi",
			"tonemapx", "transpose_vaapi", "vpp_qsv",
		},
	}
}

func qsvOptions() HardwareOptions {
	return HardwareOptions{
		Type:                           HardwareAccelerationQSV,
		EnableHardwareEncoding:         true,
		HardwareDecodingCodecs:         []string{"h264", "hevc", "vp9", "av1"},
		EnableDecodingColorDepth10Hevc: true,
		EnableDecodingColorDepth10Vp9:  true,
		PreferSystemNativeHwDecoder:    true,
		EnableTonemapping:              true,
		EnableVppTonemapping:           true,
		Tonemapping:                    DefaultTonemapping(),
		VppTonemappingBrightness:       16,
		VppTonemappingContrast:         1,
		VaapiDevice:                    "/dev/dri/renderD128",
		EncoderPreset:                  "auto",
	}
}

func hevcHDR10Job() HLSJob {
	return HLSJob{
		EncodingJob: EncodingJob{
			InputPath:       "/media/movies/1917.mkv",
			InputContainer:  "mkv",
			OutputPath:      "/cache/transcodes/abc.m3u8",
			OutputContainer: "ts",
			VideoStream: VideoStream{
				Index: 0, Codec: "hevc", ColorTransfer: "smpte2084", BitDepth: 10, NalLengthSize: "4",
				Width: 3840, Height: 2160, PixelFormat: "yuv420p10le", Profile: "Main 10", RangeType: "HDR10", RealFrameRate: 23.976,
			},
			AudioStream:        AudioStream{Index: 1, Codec: "aac", Channels: 2},
			OutputVideoCodec:   "h264",
			OutputAudioCodec:   "copy",
			OutputVideoProfile: "high",
			OutputVideoLevel:   41,
			OutputWidth:        1920,
			OutputHeight:       1080,
			OutputVideoBitrate: 8_000_000,
			CopyTimestamps:     true,
			VideoQuality: VideoQuality{
				Preset: "veryfast", CRF: 23, ForcedKeyFrameSeconds: 3,
				X264Options: "subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0",
			},
		},
		SegmentLengthSeconds: 3,
	}
}

func h264SDRJob() HLSJob {
	job := hevcHDR10Job()
	job.InputPath = "/media/movies/aliens.mkv"
	job.VideoStream = VideoStream{
		Index: 0, Codec: "h264", BitDepth: 8, NalLengthSize: "4",
		Width: 1920, Height: 1080, PixelFormat: "yuv420p", Profile: "High", RangeType: "SDR", RealFrameRate: 24,
	}
	job.OutputWidth, job.OutputHeight = 1280, 720
	job.OutputVideoBitrate = 4_000_000
	return job
}

const (
	hlsMapCopyAudio = "-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:s"
	hlsOutputTail   = "-codec:a:0 copy -copyts -avoid_negative_ts disabled -max_muxing_queue_size 2048 -f hls -max_delay 5000000 -hls_time 3 -hls_segment_type mpegts -start_number 0 -hls_segment_filename /cache/transcodes/abc%d.ts -hls_playlist_type vod -hls_list_size 0 -y /cache/transcodes/abc.m3u8"
	qsvDevices      = "-init_hw_device vaapi=va:,vendor_id=0x8086,driver=iHD -init_hw_device qsv=qs@va"
	qsvDevicesOCL   = qsvDevices + " -init_hw_device opencl=ocl@va"
	vaapiSurface    = "-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate"
	qsvEncode8M     = "-codec:v:0 h264_qsv -preset veryfast -mbbrc 1 -b:v 8000000 -maxrate 8000001 -rc_init_occupancy 8000000 -bufsize 16000000 -profile:v:0 high -level 41 -g:v:0 72 -keyint_min:v:0 72"
	x264Encode8M    = "-codec:v:0 libx264 -profile:v:0 high -level:v:0 4.1 -preset veryfast -crf 23 -maxrate 8000000 -bufsize 16000000 -x264opts:0 subme=0:me_range=16:rc_lookahead=10:me=hex:open_gop=0 -force_key_frames:0 expr:gte(t,n_forced*3) -sc_threshold:v:0 0"
	hdrSetparams    = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc"
	sdrSetparams    = "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709"
	vppTonemap      = "procamp_vaapi=b=16,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709:extra_hw_frames=32"
)

func argv(parts ...string) []string { return strings.Fields(strings.Join(parts, " ")) }

func TestBuildHLSArgsHardwareGoldens(t *testing.T) {
	caps := jellyfinFFmpegCapabilities()
	withoutOpenCL := jellyfinFFmpegCapabilities()
	withoutOpenCL.HWAccels = []string{"drm", "qsv", "vaapi"}
	iHD := jellyfinFFmpegCapabilities()
	iHD.VaapiDriver = "iHD"
	vaapi := qsvOptions()
	vaapi.Type = HardwareAccelerationVAAPI

	for _, test := range []struct {
		name string
		job  HLSJob
		caps Capabilities
		want []string
	}{
		{
			// GetSwVidFilterChain: tonemapx with the default bt2390 options.
			name: "software HDR10 tone mapping",
			job:  hevcHDR10Job(),
			caps: caps,
			want: argv("-i file:/media/movies/1917.mkv", hlsMapCopyAudio, x264Encode8M,
				"-vf", hdrSetparams+",scale=trunc(1920/2)*2:trunc(1080/2)*2,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p",
				hlsOutputTail),
		},
		{
			// VAAPI decode into VRAM, VPP scale, map to QSV, h264_qsv encode.
			name: "QSV SDR full hardware",
			job: func() HLSJob {
				job := h264SDRJob()
				job.Hardware = qsvOptions()
				return job
			}(),
			caps: caps,
			want: argv(qsvDevices, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/aliens.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 h264_qsv -preset veryfast -mbbrc 1 -b:v 4000000 -maxrate 4000001 -rc_init_occupancy 4000000 -bufsize 8000000 -profile:v:0 high -level 41 -g:v:0 72 -keyint_min:v:0 72",
				"-vf", sdrSetparams+",scale_vaapi=w=1280:h=720:format=nv12:extra_hw_frames=24,hwmap=derive_device=qsv,format=qsv",
				hlsOutputTail),
		},
		{
			// IsIntelVppTonemapAvailable: HDR10 uses tonemap_vaapi.
			name: "QSV HDR10 with VPP tone mapping",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = qsvOptions()
				return job
			}(),
			caps: caps,
			want: argv(qsvDevicesOCL, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio, qsvEncode8M,
				"-vf", hdrSetparams+",scale_vaapi=w=1920:h=1080:extra_hw_frames=24,"+vppTonemap+",hwmap=derive_device=qsv,format=qsv",
				hlsOutputTail),
		},
		{
			// Graphical subtitles upload to QSV and overlay in VRAM.
			name: "QSV HDR10 with PGS burn-in",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = qsvOptions()
				job.SubtitleStream = &SubtitleStream{Index: 3, Codec: "hdmv_pgs_subtitle", BurnIn: true}
				return job
			}(),
			caps: caps,
			want: argv(qsvDevicesOCL, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/1917.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0", qsvEncode8M,
				"-filter_complex", "[0:3]scale,scale=-1:1080:fast_bilinear,crop,pad=max(1920\\,iw):max(1080\\,ih):(ow-iw)/2:(oh-ih)/2:black@0,crop=1920:1080,format=bgra,hwupload=derive_device=qsv:extra_hw_frames=64[sub];"+
					"[0:0]"+hdrSetparams+",scale_vaapi=w=1920:h=1080:extra_hw_frames=24,"+vppTonemap+",hwmap=derive_device=qsv,format=qsv[main];"+
					"[main][sub]overlay_qsv=eof_action=pass:repeatlast=0:w=1920:h=1080",
				"-start_at_zero", hlsOutputTail),
		},
		{
			// Dolby Vision profile 5 is not VPP eligible; OpenCL maps it.
			name: "QSV Dolby Vision profile 5 with OpenCL tone mapping",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = qsvOptions()
				job.VideoStream.RangeType = "DOVI"
				job.VideoStream.DolbyVisionProfile = 5
				return job
			}(),
			caps: caps,
			want: argv(qsvDevicesOCL, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio, qsvEncode8M,
				"-vf", hdrSetparams+",scale_vaapi=w=1920:h=1080:extra_hw_frames=24,hwmap=derive_device=opencl:mode=read,tonemap_opencl=format=nv12:p=bt709:t=bt709:m=bt709:tonemap=bt2390:peak=100:desat=0,hwmap=derive_device=qsv:mode=write:reverse=1:extra_hw_frames=16,format=qsv",
				hlsOutputTail),
		},
		{
			// Without OpenCL the legacy copy-back pipeline decodes to memory
			// and uses the software chain before h264_qsv.
			name: "QSV legacy pipeline without OpenCL",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = qsvOptions()
				return job
			}(),
			caps: withoutOpenCL,
			want: argv(qsvDevices, "-filter_hw_device qs -hwaccel vaapi -i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio, qsvEncode8M,
				"-vf", hdrSetparams+",scale=trunc(1920/2)*2:trunc(1080/2)*2,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=nv12",
				hlsOutputTail),
		},
		{
			name: "QSV decode with software encode",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = qsvOptions()
				job.Hardware.EnableHardwareEncoding = false
				return job
			}(),
			caps: caps,
			want: argv(qsvDevicesOCL, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio, x264Encode8M,
				"-vf", hdrSetparams+",scale_vaapi=w=1920:h=1080:extra_hw_frames=24,"+vppTonemap+",hwmap=mode=read,format=nv12",
				hlsOutputTail),
		},
		{
			// Text subtitles render on an alphasrc canvas and overlay in VRAM.
			name: "QSV SDR with text subtitle burn-in",
			job: func() HLSJob {
				job := h264SDRJob()
				job.Hardware = qsvOptions()
				// File stream 2 is the first subtitle stream: si= counts subtitle streams only.
				job.SubtitleStream = &SubtitleStream{Index: 2, SubtitleIndex: 0, Codec: "subrip", BurnIn: true}
				return job
			}(),
			caps: caps,
			want: argv(qsvDevices, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/aliens.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0",
				"-codec:v:0 h264_qsv -preset veryfast -mbbrc 1 -b:v 4000000 -maxrate 4000001 -rc_init_occupancy 4000000 -bufsize 8000000 -profile:v:0 high -level 41 -g:v:0 72 -keyint_min:v:0 72",
				"-filter_complex", "alphasrc=s=1280x720:r=10:start='0',format=bgra,subtitles=f='/media/movies/aliens.mkv':si=0:alpha=1:sub2video=1,hwupload=derive_device=qsv:extra_hw_frames=64[sub];"+
					"[0:0]"+sdrSetparams+",scale_vaapi=w=1280:h=720:format=nv12:extra_hw_frames=24,hwmap=derive_device=qsv,format=qsv[main];"+
					"[main][sub]overlay_qsv=eof_action=pass:repeatlast=0:w=1280:h=720",
				"-start_at_zero", hlsOutputTail),
		},
		{
			// An embedded ASS stream burns from its cached extraction with
			// the attachment folder as fontsdir, as GetTextSubtitlesFilter
			// does with GetSubtitleFilePath and GetAttachmentFolderPath.
			name: "QSV SDR with extracted ASS burn-in and fontsdir",
			job: func() HLSJob {
				job := h264SDRJob()
				job.Hardware = qsvOptions()
				job.SubtitleStream = &SubtitleStream{
					Index: 2, SubtitleIndex: 0, Codec: "ass", BurnIn: true,
					ExtractedPath: "/data/subtitles/9a/9a1b/2.ass",
					FontsDir:      "/data/attachments/9a/9a1b",
				}
				return job
			}(),
			caps: caps,
			want: argv(qsvDevices, "-filter_hw_device qs", vaapiSurface, "-i file:/media/movies/aliens.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0",
				"-codec:v:0 h264_qsv -preset veryfast -mbbrc 1 -b:v 4000000 -maxrate 4000001 -rc_init_occupancy 4000000 -bufsize 8000000 -profile:v:0 high -level 41 -g:v:0 72 -keyint_min:v:0 72",
				"-filter_complex", "alphasrc=s=1280x720:r=24:start='0',format=bgra,subtitles=f='/data/subtitles/9a/9a1b/2.ass':alpha=1:sub2video=1:fontsdir='/data/attachments/9a/9a1b',hwupload=derive_device=qsv:extra_hw_frames=64[sub];"+
					"[0:0]"+sdrSetparams+",scale_vaapi=w=1280:h=720:format=nv12:extra_hw_frames=24,hwmap=derive_device=qsv,format=qsv[main];"+
					"[main][sub]overlay_qsv=eof_action=pass:repeatlast=0:w=1280:h=720",
				"-start_at_zero", hlsOutputTail),
		},
		{
			name: "VAAPI iHD HDR10 with VPP tone mapping",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = vaapi
				return job
			}(),
			caps: iHD,
			want: argv("-init_hw_device vaapi=va:/dev/dri/renderD128,driver=iHD", vaapiSurface, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 h264_vaapi -rc_mode VBR -b:v 8000000 -maxrate 8000000 -bufsize 16000000 -profile:v:0 high -level 41 -sei -a53_cc -force_key_frames:0 expr:gte(t,n_forced*3)",
				"-vf", hdrSetparams+",scale_vaapi=w=1920:h=1080:extra_hw_frames=24,"+vppTonemap,
				hlsOutputTail),
		},
		{
			// D-37: VAAPI without a detected iHD driver stays in software.
			name: "VAAPI without iHD falls back to software",
			job: func() HLSJob {
				job := hevcHDR10Job()
				job.Hardware = vaapi
				return job
			}(),
			caps: caps,
			want: argv("-i file:/media/movies/1917.mkv", hlsMapCopyAudio, x264Encode8M,
				"-vf", hdrSetparams+",scale=trunc(1920/2)*2:trunc(1080/2)*2,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p",
				hlsOutputTail),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildHLSArgs(test.job, test.caps)
			if err != nil {
				t.Fatalf("BuildHLSArgs: %v", err)
			}
			if difference := argvDifference(test.want, got); difference != "" {
				t.Fatalf("argv mismatch\n got: %s\n%s", strings.Join(got, " "), difference)
			}
		})
	}
}

func TestBuildHLSArgsSoftwareTonemapHonorsOptions(t *testing.T) {
	job := hevcHDR10Job()
	job.Hardware.Tonemapping = Tonemapping{Algorithm: "hable", Mode: "auto", Range: "tv", Desat: 0.5, Peak: 400, Param: 0.3}
	got, err := BuildHLSArgs(job, jellyfinFFmpegCapabilities())
	if err != nil {
		t.Fatalf("BuildHLSArgs: %v", err)
	}
	want := "tonemapx=tonemap=hable:desat=0.5:peak=400:t=bt709:m=bt709:p=bt709:format=yuv420p:param=0.3:range=tv"
	if !strings.Contains(strings.Join(got, " "), want) {
		t.Fatalf("argv %q does not contain %q", got, want)
	}
}

func TestBuildHLSArgsRejectsHDRWithoutAnyToneMapper(t *testing.T) {
	caps := jellyfinFFmpegCapabilities()
	caps.SupportsTonemapX = false
	if _, err := BuildHLSArgs(hevcHDR10Job(), caps); err == nil {
		t.Fatal("BuildHLSArgs accepted HDR without tonemapx or hardware tone mapping")
	}
	job := hevcHDR10Job()
	job.Hardware = qsvOptions()
	if _, err := BuildHLSArgs(job, caps); err != nil {
		t.Fatalf("BuildHLSArgs with QSV tone mapping: %v", err)
	}
}

func TestCanTonemap(t *testing.T) {
	hdr10 := hevcHDR10Job().VideoStream
	sdr := h264SDRJob().VideoStream
	eightBitHDR := hdr10
	eightBitHDR.BitDepth = 8
	eightBitHDR.PixelFormat = "yuv420p"
	noTonemapX := jellyfinFFmpegCapabilities()
	noTonemapX.SupportsTonemapX = false
	vppOnly := qsvOptions()
	vppOnly.EnableTonemapping = false
	disabled := qsvOptions()
	disabled.EnableTonemapping = false
	disabled.EnableVppTonemapping = false
	dv5 := hdr10
	dv5.RangeType = "DOVI"
	dv5.DolbyVisionProfile = 5

	for _, test := range []struct {
		name    string
		stream  VideoStream
		caps    Capabilities
		options HardwareOptions
		want    bool
	}{
		{"software tonemapx", hdr10, jellyfinFFmpegCapabilities(), HardwareOptions{}, true},
		{"no tone mapper", hdr10, noTonemapX, HardwareOptions{}, false},
		{"8-bit HDR", eightBitHDR, jellyfinFFmpegCapabilities(), qsvOptions(), false},
		{"SDR is not tone mapped", sdr, jellyfinFFmpegCapabilities(), qsvOptions(), false},
		{"QSV VPP", hdr10, noTonemapX, vppOnly, true},
		{"QSV VPP cannot map DV profile 5", dv5, noTonemapX, vppOnly, false},
		{"QSV OpenCL maps DV profile 5", dv5, noTonemapX, qsvOptions(), true},
		{"QSV with tone mapping disabled", hdr10, noTonemapX, disabled, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTonemap(test.stream, test.caps, test.options); got != test.want {
				t.Fatalf("CanTonemap = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHardwareSummaryReportsPipelineGates(t *testing.T) {
	caps := jellyfinFFmpegCapabilities()
	caps.VaapiDriver = "iHD"
	want := "hwaccels=[drm opencl qsv vaapi] filters=15 tonemapx=true alphasrc=true opencl_full=true vaapi_full=true cuda_full=false qsv_full=true vaapi_driver=iHD"
	if got := caps.HardwareSummary(); got != want {
		t.Fatalf("HardwareSummary = %q, want %q", got, want)
	}
	if got := (Capabilities{}).HardwareSummary(); !strings.Contains(got, "filters=0") || !strings.Contains(got, "qsv_full=false") || !strings.Contains(got, "vaapi_driver=unknown") {
		t.Fatalf("empty HardwareSummary = %q", got)
	}
}
