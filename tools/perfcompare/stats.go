// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"math"
	"sort"
	"time"
)

// latency summarizes a set of timed samples in milliseconds.
type latency struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50Ms"`
	P95     float64 `json:"p95Ms"`
	Max     float64 `json:"maxMs"`
	Mean    float64 `json:"meanMs"`
}

// summarize returns the nearest-rank p50 and p95, the maximum, and the mean
// of the samples. It returns the zero value for no samples.
func summarize(samples []time.Duration) latency {
	if len(samples) == 0 {
		return latency{}
	}
	ms := make([]float64, len(samples))
	var sum float64
	for i, d := range samples {
		ms[i] = float64(d) / float64(time.Millisecond)
		sum += ms[i]
	}
	sort.Float64s(ms)
	return latency{
		Samples: len(ms),
		P50:     round2(percentile(ms, 50)),
		P95:     round2(percentile(ms, 95)),
		Max:     round2(ms[len(ms)-1]),
		Mean:    round2(sum / float64(len(ms))),
	}
}

// percentile returns the nearest-rank percentile p (0 < p <= 100) of sorted.
// sorted must be in ascending order and not empty.
func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// ratio returns d/j rounded to two places, or 0 when either side has no
// value. A ratio above 1 means Deadfin is slower.
func ratio(d, j float64) float64 {
	if d <= 0 || j <= 0 {
		return 0
	}
	return round2(d / j)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
