// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strings"
	"testing"
)

func TestBuildHLSArgsAudioOnlyMapsSelectedAudioWithoutVideoPipeline(t *testing.T) {
	for _, tc := range []struct {
		name, container, outputCodec, wantSegmentType string
	}{
		{name: "AAC transport stream", container: "ts", outputCodec: "aac", wantSegmentType: "mpegts"},
		{name: "audio copy fragmented MP4", container: "fmp4", outputCodec: "copy", wantSegmentType: "fmp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := hlsUnitJob()
			job.VideoStream = VideoStream{}
			job.OutputVideoCodec = ""
			job.OutputWidth, job.OutputHeight, job.OutputVideoBitrate = 0, 0, 0
			job.SubtitleStream = nil
			job.AudioStream = AudioStream{Index: 3, Codec: "ac3", Channels: 6}
			job.OutputAudioCodec = tc.outputCodec
			job.OutputAudioBitrate = 192_000
			job.OutputAudioChannels = 2
			job.OutputContainer = tc.container

			args, err := BuildHLSArgs(job, progressiveTestCapabilities())
			if err != nil {
				t.Fatalf("BuildHLSArgs(audio-only): %v", err)
			}
			if got := hlsAudioOnlyMapValues(args); len(got) != 1 || got[0] != "0:3" {
				t.Fatalf("audio-only map values = %q, want exactly selected audio stream 0:3; argv=%q", got, args)
			}
			wantCodec := tc.outputCodec
			if wantCodec == "aac" {
				wantCodec = aacEncoder(progressiveTestCapabilities())
			}
			if !hasArgumentValue(args, "-codec:a:0", wantCodec) {
				t.Errorf("audio-only argv = %q, want audio codec %q", args, wantCodec)
			}
			if !hasArgumentValue(args, "-hls_segment_type", tc.wantSegmentType) {
				t.Errorf("audio-only argv = %q, want HLS segment type %q", args, tc.wantSegmentType)
			}
			for _, argument := range args {
				if strings.HasPrefix(argument, "-codec:v") || strings.HasPrefix(argument, "-bsf:v") || argument == "-vf" || argument == "-filter_complex" || strings.HasPrefix(argument, "-force_key_frames") || strings.HasPrefix(argument, "-sc_threshold:v") || argument == "-start_at_zero" {
					t.Errorf("audio-only argv leaked video pipeline token %q: %q", argument, args)
				}
			}
		})
	}
}

func TestBuildHLSArgsAudioOnlyRejectsMissingAudioStream(t *testing.T) {
	job := hlsUnitJob()
	job.VideoStream = VideoStream{}
	job.OutputVideoCodec = ""
	job.OutputWidth, job.OutputHeight, job.OutputVideoBitrate = 0, 0, 0
	job.AudioStream = AudioStream{}
	job.OutputAudioCodec = ""
	job.OutputAudioBitrate, job.OutputAudioChannels, job.OutputAudioSampleRate = 0, 0, 0

	if _, err := BuildHLSArgs(job, progressiveTestCapabilities()); err == nil {
		t.Fatal("BuildHLSArgs(audio-only) accepted a job without an audio stream")
	}
}

func hlsAudioOnlyMapValues(args []string) []string {
	maps := make([]string, 0, 1)
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-map" {
			maps = append(maps, args[index+1])
		}
	}
	return maps
}
