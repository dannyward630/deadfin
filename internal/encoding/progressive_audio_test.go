// SPDX-License-Identifier: GPL-2.0-only

package encoding_test

import (
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/encoding"
)

// The expected argv strings follow
// EncodingHelper.GetProgressiveAudioFullCommandLine @ b04614e
// (EncodingHelper.cs:7836-7911) for a local file without probe or analyze
// options: "<seek> -i file:<path> -threads 0 -vn <audio params>
// -id3v2_version 3 -write_id3v1 1 -y <output>".
func TestBuildProgressiveAudioArgsPortsUpstreamCommandLine(t *testing.T) {
	caps := encoding.Capabilities{
		Encoders:    []string{"aac", "libmp3lame", "libopus", "libvorbis", "flac", "alac"},
		AACEncoders: []string{"aac"},
	}
	base := encoding.AudioJob{InputPath: "/media/song.flac", OutputPath: "/transcodes/out.mp3", OutputContainer: "mp3", OutputAudioCodec: "mp3"}
	for _, tc := range []struct {
		name string
		job  func(encoding.AudioJob) encoding.AudioJob
		caps *encoding.Capabilities
		want string
	}{
		{
			name: "mp3 constant bitrate without VBR",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputAudioBitrate, job.OutputAudioChannels = 256000, 2
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -ab 256000 -ac 2 -acodec libmp3lame -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.mp3",
		},
		{
			name: "mp3 VBR quality inside the lame range",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputAudioBitrate, job.OutputAudioChannels, job.EnableAudioVbr = 192000, 2, true
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -qscale:a 2 -ac 2 -acodec libmp3lame -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.mp3",
		},
		{
			name: "mp3 VBR uses ABR below the lame range",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputAudioBitrate, job.OutputAudioChannels, job.EnableAudioVbr = 64000, 2, true
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -abr:a 1 -b:a 64000 -ac 2 -acodec libmp3lame -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.mp3",
		},
		{
			name: "aac m4a sample rate and movflags",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputPath, job.OutputContainer, job.OutputAudioCodec = "/transcodes/out.m4a", "m4a", "aac"
				job.OutputAudioBitrate, job.OutputAudioChannels, job.OutputAudioSampleRate = 128000, 2, 44100
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -ab 128000 -ac 2 -acodec aac -ar 44100 -movflags empty_moov+delay_moov -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.m4a",
		},
		{
			name: "aac prefers aac_at",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputPath, job.OutputContainer, job.OutputAudioCodec = "/transcodes/out.aac", "aac", "aac"
				job.OutputAudioBitrate, job.EnableAudioVbr = 128000, true
				return job
			},
			caps: &encoding.Capabilities{Encoders: []string{"aac_at", "aac"}, AACEncoders: []string{"aac_at", "aac"}},
			want: "-i file:/media/song.flac -threads 0 -vn -aac_at_mode:a 2 -b:a 128000 -acodec aac_at -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.aac",
		},
		{
			name: "opus rounds the sample rate",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputPath, job.OutputContainer, job.OutputAudioCodec = "/transcodes/out.ogg", "ogg", "opus"
				job.OutputAudioBitrate, job.OutputAudioSampleRate = 96000, 44100
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -ab 96000 -acodec libopus -ar 48000 -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.ogg",
		},
		{
			name: "lossless output omits the bitrate",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.OutputPath, job.OutputContainer, job.OutputAudioCodec = "/transcodes/out.flac", "flac", "flac"
				job.OutputAudioBitrate, job.OutputAudioChannels = 900000, 2
				return job
			},
			want: "-i file:/media/song.flac -threads 0 -vn -ac 2 -acodec flac -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.flac",
		},
		{
			name: "seek is clamped five seconds before the end",
			job: func(job encoding.AudioJob) encoding.AudioJob {
				job.StartOffsetTicks, job.RunTimeTicks = 1_000_000_000, 600_000_000
				return job
			},
			want: "-ss 00:00:55.000 -i file:/media/song.flac -threads 0 -vn -acodec libmp3lame -id3v2_version 3 -write_id3v1 1 -y /transcodes/out.mp3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobCaps := caps
			if tc.caps != nil {
				jobCaps = *tc.caps
			}
			args, err := encoding.BuildProgressiveAudioArgs(tc.job(base), jobCaps)
			if err != nil {
				t.Fatalf("BuildProgressiveAudioArgs() error = %v", err)
			}
			if got := strings.Join(args, " "); got != tc.want {
				t.Errorf("BuildProgressiveAudioArgs() =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestBuildProgressiveAudioArgsRejectsUnsafeJobs(t *testing.T) {
	caps := encoding.Capabilities{Encoders: []string{"libmp3lame", "pcm_s16le"}}
	base := encoding.AudioJob{InputPath: "/media/song.flac", OutputPath: "/transcodes/out.mp3", OutputContainer: "mp3", OutputAudioCodec: "mp3"}
	for _, tc := range []struct {
		name string
		job  func(encoding.AudioJob) encoding.AudioJob
	}{
		{"input path looks like an option", func(job encoding.AudioJob) encoding.AudioJob { job.InputPath = "-i"; return job }},
		{"container with a path separator", func(job encoding.AudioJob) encoding.AudioJob { job.OutputContainer = "mp3/x"; return job }},
		{"encoder that ffmpeg lacks", func(job encoding.AudioJob) encoding.AudioJob { job.OutputAudioCodec = "vorbis"; return job }},
		{"PCM output (D-63)", func(job encoding.AudioJob) encoding.AudioJob { job.OutputAudioCodec = "pcm_s16le"; return job }},
		{"no codec", func(job encoding.AudioJob) encoding.AudioJob { job.OutputAudioCodec = ""; return job }},
		{"negative bitrate", func(job encoding.AudioJob) encoding.AudioJob { job.OutputAudioBitrate = -1; return job }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if args, err := encoding.BuildProgressiveAudioArgs(tc.job(base), caps); err == nil {
				t.Errorf("BuildProgressiveAudioArgs() = %q, want an error", args)
			}
		})
	}
}

// EncodingHelper.GetAudioBitrateParam @ b04614e (EncodingHelper.cs:2807-2855).
func TestAudioBitratePortsGetAudioBitrateParam(t *testing.T) {
	limit := func(value int) *int { return &value }
	for _, tc := range []struct {
		name                  string
		requested             *int
		codec                 string
		input, output, wanted int
	}{
		{"stereo mp3 without a limit", nil, "mp3", 2, 2, 256000},
		{"stereo mp3 below the request", limit(140_000_000), "mp3", 2, 2, 256000},
		{"stereo mp3 at the request", limit(128000), "mp3", 2, 2, 128000},
		{"5.1 aac to 5.1", nil, "aac", 6, 6, 640000},
		{"5.1 aac downmix", nil, "aac", 6, 2, 256000},
		{"unknown output uses input", nil, "opus", 1, 0, 128000},
		{"unknown channels", nil, "aac", 0, 0, 384000},
		{"dts 5.1", nil, "dts", 6, 0, 768000},
		{"other codec per channel", nil, "flac", 2, 0, 256000},
		{"other codec ignores the request", limit(64000), "alac", 0, 0, 256000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := encoding.AudioBitrate(tc.requested, tc.codec, tc.input, tc.output); got != tc.wanted {
				t.Errorf("AudioBitrate() = %d, want %d", got, tc.wanted)
			}
		})
	}
}

// EncodingHelper.GetAudioEncoder @ b04614e (EncodingHelper.cs:762-822).
func TestAudioEncoderPortsGetAudioEncoder(t *testing.T) {
	caps := encoding.Capabilities{AACEncoders: []string{"libfdk_aac", "aac"}}
	for codec, want := range map[string]string{
		"aac": "libfdk_aac", "AAC": "libfdk_aac", "mp3": "libmp3lame", "vorbis": "libvorbis", "opus": "libopus",
		"flac": "flac", "dts": "dca", "alac": "alac", "ac3": "ac3", "copy": "copy", "a/b": "libfdk_aac",
	} {
		if got := encoding.AudioEncoder(codec, caps); got != want {
			t.Errorf("AudioEncoder(%q) = %q, want %q", codec, got, want)
		}
	}
	if got := encoding.AudioEncoderChannelLimit("libmp3lame"); got != 2 {
		t.Errorf("AudioEncoderChannelLimit(libmp3lame) = %d, want 2", got)
	}
	if got := encoding.AudioEncoderChannelLimit("libopus"); got != 8 {
		t.Errorf("AudioEncoderChannelLimit(libopus) = %d, want 8", got)
	}
}
