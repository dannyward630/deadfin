// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestHLSServiceSegmentProjectsExactTimingOnlyWhenPresent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provided bool
		want     *playback.HLSSegmentTiming
	}{
		{
			name:     "present exact facts",
			provided: true,
			want: &playback.HLSSegmentTiming{
				RuntimeTicks:             312_400_000,
				ActualSegmentLengthTicks: 37_600_000,
			},
		},
		{name: "absent exact facts preserves nominal fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &hlsSegmentTimingProjectionManager{
				hlsFakeManager: &hlsFakeManager{segment: "segment"},
			}
			service := newHLSService(&progressiveFakeSource{source: progressiveCopySource("/media/timing.mkv")}, manager)
			request := hlsTranscodeRequest()
			request.SegmentIndex = 3
			request.RuntimeTicks = 312_400_000
			request.ActualSegmentLengthTicks = 37_600_000
			request.ActualSegmentLengthTicksSet = tc.provided

			response, err := service.Segment(t.Context(), request)
			if err != nil {
				t.Fatalf("Segment: %v", err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("close segment response: %v", err)
			}
			if tc.want == nil {
				if manager.timing != nil {
					t.Fatalf("WaitSegment timing = %#v, want nil for absent facts", manager.timing)
				}
				return
			}
			if manager.timing == nil || *manager.timing != *tc.want {
				t.Fatalf("WaitSegment timing = %#v, want %#v", manager.timing, tc.want)
			}
		})
	}
}

type hlsSegmentTimingProjectionManager struct {
	*hlsFakeManager
	timing *playback.HLSSegmentTiming
}

func (manager *hlsSegmentTimingProjectionManager) WaitSegment(_ context.Context, _ string, _ int, timing *playback.HLSSegmentTiming) (io.ReadCloser, error) {
	manager.timing = timing
	if manager.waitErr != nil {
		return nil, manager.waitErr
	}
	return io.NopCloser(strings.NewReader(manager.segment)), nil
}
