// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"fmt"
	"math"
)

const (
	ticksPerSecond int64 = 10_000_000

	// maxCLRSegmentCount matches System.Array.MaxLength (0X7FFFFFC7), the
	// largest single-dimensional array the pinned source runtime can allocate.
	maxCLRSegmentCount int64 = 0x7fffffc7
)

// checkedSegmentCount returns the number of fixed-length segments only when it
// can be represented by the source runtime's array length ceiling.
func checkedSegmentCount(runtimeTicks, segmentTicks int64) (int, error) {
	if runtimeTicks <= 0 || segmentTicks <= 0 {
		return 0, fmt.Errorf("invalid runtime ticks (%d) or segment ticks (%d)", runtimeTicks, segmentTicks)
	}
	whole := runtimeTicks / segmentTicks
	remainder := runtimeTicks % segmentTicks
	if whole > maxCLRSegmentCount || (remainder != 0 && whole == maxCLRSegmentCount) {
		return 0, fmt.Errorf("segment count exceeds CLR maximum array length (%d)", maxCLRSegmentCount)
	}

	count := whole
	if remainder != 0 {
		count++
	}
	return int(count), nil
}

// SegmentLengths divides runtimeTicks into fixed-length HLS segments, retaining
// the final source-timestamp remainder exactly.
func SegmentLengths(runtimeTicks int64, segmentLengthSeconds int) ([]float64, error) {
	if runtimeTicks <= 0 || segmentLengthSeconds <= 0 {
		return nil, fmt.Errorf("invalid segment length (%d) or runtime ticks (%d)", segmentLengthSeconds, runtimeTicks)
	}
	if int64(segmentLengthSeconds) > math.MaxInt64/ticksPerSecond {
		return nil, fmt.Errorf("invalid segment length (%d) or runtime ticks (%d)", segmentLengthSeconds, runtimeTicks)
	}

	segmentTicks := int64(segmentLengthSeconds) * ticksPerSecond
	count, err := checkedSegmentCount(runtimeTicks, segmentTicks)
	if err != nil {
		return nil, err
	}
	whole := runtimeTicks / segmentTicks
	remainder := runtimeTicks % segmentTicks

	segments := make([]float64, count)
	for i := range segments[:int(whole)] {
		segments[i] = float64(segmentLengthSeconds)
	}
	if remainder != 0 {
		segments[len(segments)-1] = float64(remainder) / float64(ticksPerSecond)
	}
	return segments, nil
}
