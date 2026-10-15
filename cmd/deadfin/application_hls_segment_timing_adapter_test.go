// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestHLSTranscodeManagerProjectsOptionalSegmentTiming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		timing *playback.HLSSegmentTiming
		want   *ffmpeg.HLSSegmentTiming
	}{
		{
			name: "exact timing",
			timing: &playback.HLSSegmentTiming{
				RuntimeTicks:             312_400_000,
				ActualSegmentLengthTicks: 37_600_000,
			},
			want: &ffmpeg.HLSSegmentTiming{
				RuntimeTicks:             312_400_000,
				ActualSegmentLengthTicks: 37_600_000,
			},
		},
		{name: "legacy timing absence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &p6TrackingReadCloser{Reader: &p6StringReader{value: "segment"}}
			manager := &hlsSegmentTimingAdapterManager{p6HLSManager: &p6HLSManager{segment: body}}
			adapter := hlsTranscodeManager{manager: manager}

			got, err := adapter.WaitSegment(t.Context(), "/transcodes/timing.m3u8", 3, tc.timing)
			if err != nil {
				t.Fatalf("WaitSegment: %v", err)
			}
			if got != body {
				t.Fatalf("WaitSegment body = %T %p, want original %p", got, got, body)
			}
			if tc.want == nil {
				if manager.timing != nil {
					t.Fatalf("ffmpeg timing = %#v, want nil", manager.timing)
				}
				return
			}
			if manager.timing == nil || *manager.timing != *tc.want {
				t.Fatalf("ffmpeg timing = %#v, want %#v", manager.timing, tc.want)
			}
		})
	}
}

type hlsSegmentTimingAdapterManager struct {
	*p6HLSManager
	timing *ffmpeg.HLSSegmentTiming
}

func (manager *hlsSegmentTimingAdapterManager) WaitSegment(_ context.Context, playlist string, index int, timings ...*ffmpeg.HLSSegmentTiming) (io.ReadCloser, error) {
	manager.segmentPlaylist, manager.segmentIndex = playlist, index
	if len(timings) != 1 {
		return nil, fmt.Errorf("timing arguments = %d, want one", len(timings))
	}
	manager.timing = timings[0]
	return manager.segment, nil
}
