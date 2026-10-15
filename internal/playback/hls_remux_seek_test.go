// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"testing"
)

// A remux segment restart seeks like upstream GetFastSeekCommandLineParameter
// (EncodingHelper.cs:3046-3072): 0.5 s past the segment start, at most 5 s
// before the end of the source runtime.
func TestHLSServiceRemuxSegmentSeeksPastKeyframeAndClampsToRuntime(t *testing.T) {
	for _, tc := range []struct {
		name         string
		runtimeTicks int64
		index        int
		wantSeek     string
	}{
		{name: "segment start", runtimeTicks: 130_130_000, index: 1, wantSeek: "00:00:13.513"},
		{name: "near the end", runtimeTicks: 5_990_000_000, index: 99, wantSeek: "00:09:55.000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := progressiveCopySource("/media/goldenrec-input.mp4")
			runtime := int64(6_000_000_000)
			source.RunTimeTicks = &runtime
			manager := &hlsFakeManager{segment: "segment"}
			service := newHLSService(&progressiveFakeSource{source: source}, manager)
			request := hlsTranscodeRequest()
			request.AllowVideoStreamCopy = true
			request.EnableAutoStreamCopy = true
			request.VideoBitrate = hlsInt(2_600_000)
			request.RuntimeTicks = tc.runtimeTicks
			request.SegmentIndex = tc.index

			if _, err := service.Segment(t.Context(), request); err != nil {
				t.Fatalf("Segment(%d): %v", tc.index, err)
			}
			args := manager.lastRestart.Args
			if !hlsArgPair(args, "-codec:v:0", "copy") {
				t.Fatalf("argv = %q, want a video-copy remux", args)
			}
			if !hlsArgPair(args, "-ss", tc.wantSeek) {
				t.Errorf("remux argv = %q, want seek %q", args, tc.wantSeek)
			}
			if !hlsArgPair(args, "-fflags", "+genpts") {
				t.Errorf("remux argv = %q, want -fflags +genpts", args)
			}
		})
	}
}
