// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"context"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/keyframes"
)

const hlsAllocationChildMode = "DEADFIN_HLS_ALLOCATION_CHILD"

const wantPlaylistOutputLimitError = "hls: playlist exceeds output limit"

// TestCreateMainPlaylistRejectsOutputLimitsBeforeKeyframeLookup keeps a
// request-controlled segment plan from allocating or rendering an unbounded
// media playlist. The same policy caps both segment count and final bytes.
func TestCreateMainPlaylistRejectsOutputLimitsBeforeKeyframeLookup(t *testing.T) {
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}

	for _, tc := range []struct {
		name         string
		runtimeTicks int64
		query        string
	}{
		{
			name:         "more than ten thousand segments",
			runtimeTicks: 100_010_000,
			query:        "?session=segment-count",
		},
		{
			name:         "ten thousand segments with one kibibyte query exceeds eight mebibytes",
			runtimeTicks: 100_000_000,
			query:        "?token=" + strings.Repeat("q", 1<<10),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &allocationCountingKeyframeSource{}
			playlist, err := CreateMainPlaylist(t.Context(), MainPlaylistRequest{
				ItemID:                    itemID,
				MediaSourceID:             "primary",
				FilePath:                  "/media/feature.mkv",
				SegmentLengthMilliseconds: 1,
				RunTimeTicks:              tc.runtimeTicks,
				SegmentContainer:          "ts",
				EndpointPrefix:            "hls1/main/",
				Query:                     tc.query,
				IsRemuxVideo:              true,
				Keyframes:                 source,
			})
			if err == nil || err.Error() != wantPlaylistOutputLimitError {
				t.Errorf("CreateMainPlaylist() error = %v, want %q", err, wantPlaylistOutputLimitError)
			}
			if playlist != "" {
				t.Errorf("CreateMainPlaylist() rendered %d bytes, want empty output-limit rejection", len(playlist))
			}
			if calls := source.calls.Load(); calls != 0 {
				t.Errorf("KeyframeSource.Keyframes calls = %d, want preflight rejection before lookup", calls)
			}
		})
	}
}

// TestSegmentLengthsRejectsCLRArrayLengthOverflow keeps a source-incompatible
// allocation failure isolated: MaxInt64 ticks at one second requires more than
// CLR's largest array length, so planning must fail before make can panic.
func TestSegmentLengthsRejectsCLRArrayLengthOverflow(t *testing.T) {
	if os.Getenv(hlsAllocationChildMode) == "segment-lengths" {
		for attempt := range 2 {
			segments, panicValue, err := safeSegmentLengths(math.MaxInt64, 1)
			if panicValue != nil {
				t.Fatalf("SegmentLengths(%d, 1) panicked on attempt %d: %v", int64(math.MaxInt64), attempt, panicValue)
			}
			if err == nil {
				t.Fatalf("SegmentLengths(%d, 1) error = nil, want CLR array-length rejection", int64(math.MaxInt64))
			}
			if segments != nil {
				t.Fatalf("SegmentLengths(%d, 1) segments = %#v, want nil on rejection", int64(math.MaxInt64), segments)
			}
		}
		return
	}

	runHLSAllocationChild(t, "segment-lengths", "^TestSegmentLengthsRejectsCLRArrayLengthOverflow$")
}

// TestCheckedSegmentCountRespectsCLRArrayLengthBoundary exercises the source
// array-length arithmetic without constructing an array at that boundary.
func TestCheckedSegmentCountRespectsCLRArrayLengthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		runtimeTicks, segmentTicks int64
		wantCount                  int
		wantError                  bool
	}{
		{
			name:         "exact CLR maximum whole count",
			runtimeTicks: maxCLRSegmentCount * 10,
			segmentTicks: 10,
			wantCount:    int(maxCLRSegmentCount),
		},
		{
			name:         "remainder beyond CLR maximum whole count",
			runtimeTicks: maxCLRSegmentCount*10 + 1,
			segmentTicks: 10,
			wantError:    true,
		},
		{
			name:         "one whole segment beyond CLR maximum",
			runtimeTicks: maxCLRSegmentCount + 1,
			segmentTicks: 1,
			wantError:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, err := checkedSegmentCount(tc.runtimeTicks, tc.segmentTicks)
			if tc.wantError {
				if err == nil {
					t.Fatalf("checkedSegmentCount(%d, %d) error = nil, want CLR limit rejection", tc.runtimeTicks, tc.segmentTicks)
				}
				if count != 0 {
					t.Fatalf("checkedSegmentCount(%d, %d) count = %d, want 0 on rejection", tc.runtimeTicks, tc.segmentTicks, count)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkedSegmentCount(%d, %d): %v", tc.runtimeTicks, tc.segmentTicks, err)
			}
			if count != tc.wantCount {
				t.Errorf("checkedSegmentCount(%d, %d) = %d, want %d", tc.runtimeTicks, tc.segmentTicks, count, tc.wantCount)
			}
		})
	}
}

// TestCreateMainPlaylistRejectsCLRArrayLengthOverflowWithoutKeyframeLookup
// preserves the fixed-plan rejection path without invoking a keyframe source.
func TestCreateMainPlaylistRejectsCLRArrayLengthOverflowWithoutKeyframeLookup(t *testing.T) {
	if os.Getenv(hlsAllocationChildMode) == "main-playlist" {
		itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
		if err != nil {
			t.Fatalf("ParseUUID(): %v", err)
		}
		source := &allocationCountingKeyframeSource{}
		request := MainPlaylistRequest{
			ItemID:                    itemID,
			MediaSourceID:             "primary",
			FilePath:                  "/media/feature.mp4",
			SegmentLengthMilliseconds: 1,
			RunTimeTicks:              math.MaxInt64,
			SegmentContainer:          "ts",
			EndpointPrefix:            "hls1/main/",
			Query:                     "?session=allocation",
			IsRemuxVideo:              true,
			Keyframes:                 source,
		}

		for attempt := range 2 {
			playlist, panicValue, err := safeMainPlaylist(request)
			if panicValue != nil {
				t.Fatalf("CreateMainPlaylist(MaxInt64, 1) panicked on attempt %d: %v", attempt, panicValue)
			}
			if err == nil {
				t.Fatal("CreateMainPlaylist(MaxInt64, 1) error = nil, want CLR array-length rejection")
			}
			if playlist != "" {
				t.Fatalf("CreateMainPlaylist(MaxInt64, 1) playlist = %q, want empty rejection", playlist)
			}
		}
		if calls := source.calls.Load(); calls != 0 {
			t.Fatalf("KeyframeSource.Keyframes calls = %d, want 0 before allocation rejection", calls)
		}
		return
	}

	runHLSAllocationChild(t, "main-playlist", "^TestCreateMainPlaylistRejectsCLRArrayLengthOverflowWithoutKeyframeLookup$")
}

// TestCreateMainPlaylistRejectsCLRArrayLengthOverflowAfterMissingKeyframes
// ensures an eligible MKV can inspect absent metadata, then reject its
// equal-segment fallback without allocating or panicking.
func TestCreateMainPlaylistRejectsCLRArrayLengthOverflowAfterMissingKeyframes(t *testing.T) {
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}
	source := &allocationCountingKeyframeSource{}
	playlist, panicValue, err := safeMainPlaylist(MainPlaylistRequest{
		ItemID:                    itemID,
		MediaSourceID:             "primary",
		FilePath:                  "/media/feature.mkv",
		SegmentLengthMilliseconds: 1,
		RunTimeTicks:              math.MaxInt64,
		SegmentContainer:          "ts",
		EndpointPrefix:            "hls1/main/",
		Query:                     "?session=missing-keyframes",
		IsRemuxVideo:              true,
		Keyframes:                 source,
	})
	if panicValue != nil {
		t.Fatalf("CreateMainPlaylist(MaxInt64, 1) panicked: %v", panicValue)
	}
	if err == nil {
		t.Fatal("CreateMainPlaylist(MaxInt64, 1) error = nil, want CLR array-length rejection")
	}
	if playlist != "" {
		t.Fatalf("CreateMainPlaylist(MaxInt64, 1) playlist = %q, want empty rejection", playlist)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Errorf("KeyframeSource.Keyframes calls = %d, want 1 before equal-segment fallback rejection", calls)
	}
}

// TestCreateMainPlaylistAllowsSparseEligibleKeyframesPastEqualSegmentCLRLimit
// keeps keyframe planning independent from the much larger equal-segment plan.
func TestCreateMainPlaylistAllowsSparseEligibleKeyframesPastEqualSegmentCLRLimit(t *testing.T) {
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}
	source := &allocationCountingKeyframeSource{
		data: keyframes.Data{
			TotalDurationTicks: math.MaxInt64,
			KeyframeTicks:      []int64{0, math.MaxInt64},
		},
		found: true,
	}

	got, err := CreateMainPlaylist(t.Context(), MainPlaylistRequest{
		ItemID:                    itemID,
		MediaSourceID:             "primary",
		FilePath:                  "/media/feature.mkv",
		SegmentLengthMilliseconds: 1,
		RunTimeTicks:              math.MaxInt64,
		SegmentContainer:          "ts",
		EndpointPrefix:            "hls1/main/",
		Query:                     "?session=keyframe-boundary",
		IsRemuxVideo:              true,
		Keyframes:                 source,
	})
	if err != nil {
		t.Fatalf("CreateMainPlaylist() error = %v", err)
	}
	const want = "#EXTM3U\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:922337203686\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:922337203685.477581, nodesc\n" +
		"hls1/main/0.ts?session=keyframe-boundary&runtimeTicks=0&actualSegmentLengthTicks=9223372036854775807\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("sparse-keyframe playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Errorf("KeyframeSource.Keyframes calls = %d, want 1", calls)
	}
}

// TestCreateMainPlaylistPreservesPinnedOutOfOrderKeyframeDeltas retains the
// source-order deltas of qualifying keyframes, including a negative delta.
func TestCreateMainPlaylistPreservesPinnedOutOfOrderKeyframeDeltas(t *testing.T) {
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}
	source := &allocationCountingKeyframeSource{
		data: keyframes.Data{
			TotalDurationTicks: 300_000_000,
			KeyframeTicks:      []int64{200_000_000, 130_000_000},
		},
		found: true,
	}

	got, err := CreateMainPlaylist(t.Context(), MainPlaylistRequest{
		ItemID:                    itemID,
		MediaSourceID:             "primary",
		FilePath:                  "/media/feature.mkv",
		SegmentLengthMilliseconds: 6_000,
		RunTimeTicks:              300_000_000,
		SegmentContainer:          "ts",
		EndpointPrefix:            "hls1/main/",
		Query:                     "?session=out-of-order",
		IsRemuxVideo:              true,
		Keyframes:                 source,
	})
	if err != nil {
		t.Fatalf("CreateMainPlaylist() error = %v", err)
	}
	const want = "#EXTM3U\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:20\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:20.000000, nodesc\n" +
		"hls1/main/0.ts?session=out-of-order&runtimeTicks=0&actualSegmentLengthTicks=200000000\n" +
		"#EXTINF:-7.000000, nodesc\n" +
		"hls1/main/1.ts?session=out-of-order&runtimeTicks=200000000&actualSegmentLengthTicks=-70000000\n" +
		"#EXTINF:17.000000, nodesc\n" +
		"hls1/main/2.ts?session=out-of-order&runtimeTicks=130000000&actualSegmentLengthTicks=170000000\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("out-of-order keyframe playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Errorf("KeyframeSource.Keyframes calls = %d, want 1", calls)
	}
}

// TestCreateMainPlaylistFormatsFractionalNegativePinnedOutOfOrderKeyframeDelta
// preserves the pinned source-order delta and its valid fractional EXTINF form.
func TestCreateMainPlaylistFormatsFractionalNegativePinnedOutOfOrderKeyframeDelta(t *testing.T) {
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}
	source := &allocationCountingKeyframeSource{
		data: keyframes.Data{
			TotalDurationTicks: 300_000_000,
			KeyframeTicks:      []int64{200_000_000, 135_000_000},
		},
		found: true,
	}

	got, err := CreateMainPlaylist(t.Context(), MainPlaylistRequest{
		ItemID:                    itemID,
		MediaSourceID:             "primary",
		FilePath:                  "/media/feature.mkv",
		SegmentLengthMilliseconds: 6_000,
		RunTimeTicks:              300_000_000,
		SegmentContainer:          "ts",
		EndpointPrefix:            "hls1/main/",
		Query:                     "?session=fractional-out-of-order",
		IsRemuxVideo:              true,
		Keyframes:                 source,
	})
	if err != nil {
		t.Fatalf("CreateMainPlaylist() error = %v", err)
	}
	const want = "#EXTM3U\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:20\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:20.000000, nodesc\n" +
		"hls1/main/0.ts?session=fractional-out-of-order&runtimeTicks=0&actualSegmentLengthTicks=200000000\n" +
		"#EXTINF:-6.500000, nodesc\n" +
		"hls1/main/1.ts?session=fractional-out-of-order&runtimeTicks=200000000&actualSegmentLengthTicks=-65000000\n" +
		"#EXTINF:16.500000, nodesc\n" +
		"hls1/main/2.ts?session=fractional-out-of-order&runtimeTicks=135000000&actualSegmentLengthTicks=165000000\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("fractional out-of-order keyframe playlist bytes differ:\n got: %q\nwant: %q", got, want)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Errorf("KeyframeSource.Keyframes calls = %d, want 1", calls)
	}
}

// TestAppendDurationFormatsNegativeTicks uses the formatter directly for the
// signed magnitudes that cannot all be reached by valid source timestamps.
func TestAppendDurationFormatsNegativeTicks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ticks int64
		want  string
	}{
		{name: "integral negative", ticks: -70_000_000, want: "-7.000000"},
		{name: "fractional negative", ticks: -65_000_000, want: "-6.500000"},
		{name: "minimum int64", ticks: math.MinInt64, want: "-922337203685.477581"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var builder strings.Builder
			appendDuration(&builder, tc.ticks)
			if got := builder.String(); got != tc.want {
				t.Errorf("appendDuration(%d) = %q, want %q", tc.ticks, got, tc.want)
			}
		})
	}
}

func runHLSAllocationChild(t *testing.T, mode, testName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, os.Args[0], "-test.run="+testName, "-test.count=1", "-test.timeout=1s")
	command.Env = append(os.Environ(), hlsAllocationChildMode+"="+mode)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("isolated HLS allocation regression %q timed out: %v\n%s", mode, ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("isolated HLS allocation regression %q failed: %v\n%s", mode, err, output)
	}
}

func safeSegmentLengths(runtimeTicks int64, segmentLengthSeconds int) (segments []float64, panicValue any, err error) {
	defer func() {
		panicValue = recover()
	}()
	segments, err = SegmentLengths(runtimeTicks, segmentLengthSeconds)
	return segments, nil, err
}

func safeMainPlaylist(request MainPlaylistRequest) (playlist string, panicValue any, err error) {
	defer func() {
		panicValue = recover()
	}()
	playlist, err = CreateMainPlaylist(context.Background(), request)
	return playlist, nil, err
}

type allocationCountingKeyframeSource struct {
	calls atomic.Int32
	data  keyframes.Data
	found bool
	err   error
}

func (s *allocationCountingKeyframeSource) Keyframes(context.Context, dto.UUID, string) (keyframes.Data, bool, error) {
	s.calls.Add(1)
	return s.data, s.found, s.err
}
