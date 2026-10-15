// SPDX-License-Identifier: GPL-2.0-only

package hls

import "errors"

const (
	maxPlaylistSegmentCount int64 = 10_000
	maxPlaylistOutputBytes  int64 = 8 << 20
)

var errPlaylistOutputLimit = errors.New("hls: playlist exceeds output limit")

// playlistSegmentCount returns the fixed-plan count without allocating it.
// Its inputs are validated by callers before this calculation.
func playlistSegmentCount(runtimeTicks, segmentTicks int64) int64 {
	count := runtimeTicks / segmentTicks
	if runtimeTicks%segmentTicks != 0 {
		count++
	}
	return count
}

func checkPlaylistSegmentCount(count int64) error {
	if count > maxPlaylistSegmentCount {
		return errPlaylistOutputLimit
	}
	return nil
}

// playlistSizeCounter tracks a rendered playlist size without constructing its
// contents. Once the configured limit is reached it remains saturated, so all
// size calculations are overflow-safe.
type playlistSizeCounter struct {
	bytes    int64
	exceeded bool
}

func (counter *playlistSizeCounter) add(count int) {
	if counter.exceeded {
		return
	}
	if int64(count) > maxPlaylistOutputBytes-counter.bytes {
		counter.exceeded = true
		return
	}
	counter.bytes += int64(count)
}

func (counter *playlistSizeCounter) addString(value string) {
	counter.add(len(value))
}

func (counter *playlistSizeCounter) result() (int, error) {
	if counter.exceeded {
		return 0, errPlaylistOutputLimit
	}
	return int(counter.bytes), nil
}

func decimalUint64Length(value uint64) int {
	length := 1
	for value >= 10 {
		value /= 10
		length++
	}
	return length
}

func decimalInt64Length(value int64) int {
	if value < 0 {
		return 1 + decimalUint64Length(uint64(-(value+1))+1)
	}
	return decimalUint64Length(uint64(value))
}
