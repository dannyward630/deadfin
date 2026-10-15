// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"slices"
	"testing"
)

// gopJob is an h264 HLS transcode with 6 s segments whose output frame rate
// can differ from the source RealFrameRate.
func gopJob(encoder string, sourceRate, outputRate float64) (HLSJob, Capabilities) {
	job := h264SDRJob()
	job.SegmentLengthSeconds = 6
	job.VideoStream.RealFrameRate = sourceRate
	job.OutputFrameRate = outputRate
	caps := jellyfinFFmpegCapabilities()
	switch encoder {
	case "h264_qsv":
		job.Hardware = qsvOptions()
	case "h264_vaapi":
		job.Hardware = qsvOptions()
		job.Hardware.Type = HardwareAccelerationVAAPI
		caps.VaapiDriver = "iHD"
	case "h264_nvenc":
		job.Hardware = nvencOptions()
		caps = nvidiaCapabilities()
	}
	return job, caps
}

func argValue(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

// TestHLSGOPFollowsOutputFrameRate checks that the encoders that cannot force
// key frames get a GOP of one segment counted at the -r output rate (D-62). Upstream counts it at the source RealFrameRate
// (EncodingHelper.cs:2016-2030), so a capped rate made segments longer than
// the EXTINF values.
func TestHLSGOPFollowsOutputFrameRate(t *testing.T) {
	for _, test := range []struct {
		name                   string
		encoder                string
		sourceRate, outputRate float64
		wantRate, wantGOP      string
	}{
		// ATV-V22: VC-1 RealFrameRate 47.952 capped by MaxFramerate.
		{"qsv 47.952 capped to 23.976", "h264_qsv", 47.952, 23.976025, "23.976025", "144"},
		{"qsv 29.97i capped to 23.976", "h264_qsv", 29.97003, 23.976025, "23.976025", "144"},
		{"qsv 59.94 capped to 30", "h264_qsv", 59.94006, 30, "30", "180"},
		{"qsv uncapped keeps RealFrameRate", "h264_qsv", 23.976025, 0, "", "144"},
		{"qsv explicit Framerate above source", "h264_qsv", 25, 50, "50", "300"},
		{"nvenc 47.952 capped to 23.976", "h264_nvenc", 47.952, 23.976025, "23.976025", "144"},
		{"nvenc 59.94 capped to 30", "h264_nvenc", 59.94006, 30, "30", "180"},
		{"nvenc uncapped keeps RealFrameRate", "h264_nvenc", 59.94006, 0, "", "360"},
		{"qsv capped without RealFrameRate", "h264_qsv", 0, 30, "30", "180"},
		{"qsv without any rate has no GOP", "h264_qsv", 0, 0, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			job, caps := gopJob(test.encoder, test.sourceRate, test.outputRate)
			args, err := BuildHLSArgs(job, caps)
			if err != nil {
				t.Fatal(err)
			}
			if codec, _ := argValue(args, "-codec:v:0"); codec != test.encoder {
				t.Fatalf("encoder = %q, want %q: %q", codec, test.encoder, args)
			}
			rate, _ := argValue(args, "-r")
			if rate != test.wantRate {
				t.Errorf("-r = %q, want %q", rate, test.wantRate)
			}
			gop, _ := argValue(args, "-g:v:0")
			keyintMin, _ := argValue(args, "-keyint_min:v:0")
			if gop != test.wantGOP || keyintMin != test.wantGOP {
				t.Errorf("-g:v:0 %q -keyint_min:v:0 %q, want %q: %q", gop, keyintMin, test.wantGOP, args)
			}
			if slices.Contains(args, "-force_key_frames:0") {
				t.Errorf("GOP encoder %s got -force_key_frames: %q", test.encoder, args)
			}
		})
	}
}

// TestHLSGOPFollowsReferenceFrameRateWithoutOutputRate covers ATV-V22 on
// v0.2.34 (D-62). The TS VC-1 source probes RealFrameRate
// 47.952 for 23.976 frames. MaxFramerate 23.976025 is not below the average
// rate, so no -r is set and the encoder receives 23.976 frames per second.
func TestHLSGOPFollowsReferenceFrameRateWithoutOutputRate(t *testing.T) {
	for _, test := range []struct {
		name                 string
		encoder              string
		real, reference, out float64
		wantGOP              string
	}{
		{"qsv average below RealFrameRate", "h264_qsv", 47.952, 23.976025, 0, "72"},
		{"nvenc average below RealFrameRate", "h264_nvenc", 47.952, 23.976025, 0, "72"},
		{"qsv output rate wins over average", "h264_qsv", 59.94006, 59.94006, 30, "90"},
		{"qsv without average keeps RealFrameRate", "h264_qsv", 25, 0, 0, "75"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job, caps := gopJob(test.encoder, test.real, test.out)
			job.SegmentLengthSeconds = 3
			job.VideoStream.ReferenceFrameRate = test.reference
			args, err := BuildHLSArgs(job, caps)
			if err != nil {
				t.Fatal(err)
			}
			gop, _ := argValue(args, "-g:v:0")
			keyintMin, _ := argValue(args, "-keyint_min:v:0")
			if gop != test.wantGOP || keyintMin != test.wantGOP {
				t.Errorf("-g:v:0 %q -keyint_min:v:0 %q, want %q: %q", gop, keyintMin, test.wantGOP, args)
			}
		})
	}
}

// TestHLSForcedKeyFramesIgnoreFrameRate checks that libx264 and VAAPI keep
// the time-based force_key_frames expression and take no GOP, whatever the
// source and output frame rates are (EncodingHelper.cs:2045-2059).
func TestHLSForcedKeyFramesIgnoreFrameRate(t *testing.T) {
	for _, encoder := range []string{"libx264", "h264_vaapi"} {
		t.Run(encoder, func(t *testing.T) {
			job, caps := gopJob(encoder, 47.952, 23.976025)
			args, err := BuildHLSArgs(job, caps)
			if err != nil {
				t.Fatal(err)
			}
			if codec, _ := argValue(args, "-codec:v:0"); codec != encoder {
				t.Fatalf("encoder = %q, want %q: %q", codec, encoder, args)
			}
			if expr, _ := argValue(args, "-force_key_frames:0"); expr != "expr:gte(t,n_forced*6)" {
				t.Errorf("-force_key_frames:0 = %q", expr)
			}
			if slices.Contains(args, "-g:v:0") {
				t.Errorf("forced-key-frame encoder %s got a GOP: %q", encoder, args)
			}
		})
	}
}
