// SPDX-License-Identifier: GPL-2.0-only

package trickplay_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/trickplay"
)

func TestManagerPlanNormalizesConfiguredAndCappedWidths(t *testing.T) {
	itemID := mustUUID(t, "00112233445566778899aabbccddeeff")
	for _, test := range []struct {
		name  string
		input trickplay.GenerationRequest
		want  int
	}{
		{
			name: "rounds an odd configured width down to an even encoder width",
			input: trickplay.GenerationRequest{
				ItemID:               itemID,
				RequestedWidth:       321,
				GeneratedHeight:      180,
				TileWidth:            2,
				TileHeight:           2,
				IntervalMilliseconds: 1_500,
				ThumbnailPaths:       []string{"frame-000.jpg"},
				TileByteSizes:        []int64{1},
			},
			want: 320,
		},
		{
			name: "caps to the smaller source width before enforcing evenness",
			input: trickplay.GenerationRequest{
				ItemID:               itemID,
				RequestedWidth:       640,
				VideoWidth:           intPointer(319),
				GeneratedHeight:      180,
				TileWidth:            2,
				TileHeight:           2,
				IntervalMilliseconds: 1_500,
				ThumbnailPaths:       []string{"frame-000.jpg"},
				TileByteSizes:        []int64{1},
			},
			want: 318,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := (trickplay.Manager{}).Plan(test.input)
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			if got := plan.Metadata.Width; got != test.want {
				t.Fatalf("planned width = %d, want %d", got, test.want)
			}
		})
	}
}

func TestManagerPlanBatchesThumbnailsInRowMajorTilesAndKeepsPartialFinalTile(t *testing.T) {
	input := trickplay.GenerationRequest{
		ItemID:               mustUUID(t, "b6d1efb179084e4e92ce8fa5d40f8b4f"),
		RequestedWidth:       321,
		VideoWidth:           intPointer(319),
		GeneratedHeight:      180,
		TileWidth:            3,
		TileHeight:           2,
		IntervalMilliseconds: 1_500,
		ThumbnailPaths: []string{
			"frame-000.jpg", "frame-001.jpg", "frame-002.jpg", "frame-003.jpg",
			"frame-004.jpg", "frame-005.jpg", "frame-006.jpg",
		},
		TileByteSizes: []int64{1_000, 1_001},
	}

	plan, err := (trickplay.Manager{}).Plan(input)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	wantMetadata := trickplay.Metadata{
		ItemID:         mustUUID(t, "b6d1efb179084e4e92ce8fa5d40f8b4f"),
		Width:          318,
		Height:         180,
		TileWidth:      3,
		TileHeight:     2,
		ThumbnailCount: 7,
		Interval:       1_500,
		Bandwidth:      890,
	}
	if got := plan.Metadata; got != wantMetadata {
		t.Fatalf("planned metadata = %#v, want %#v", got, wantMetadata)
	}

	wantTiles := []trickplay.Tile{
		{Index: 0, ThumbnailPaths: []string{"frame-000.jpg", "frame-001.jpg", "frame-002.jpg", "frame-003.jpg", "frame-004.jpg", "frame-005.jpg"}},
		{Index: 1, ThumbnailPaths: []string{"frame-006.jpg"}},
	}
	if got := plan.Tiles; !reflect.DeepEqual(got, wantTiles) {
		t.Fatalf("planned tile batches = %#v, want %#v", got, wantTiles)
	}
}

func TestManagerPlanRoundsBandwidthUpUsingFullTileCapacity(t *testing.T) {
	for _, test := range []struct {
		name     string
		tileGrid [2]int
		interval int64
		bytes    int64
		want     int64
	}{
		{
			name:     "partial tile keeps the configured two by two denominator",
			tileGrid: [2]int{2, 2},
			interval: 1_500,
			bytes:    1_001,
			want:     1_335,
		},
		{
			name:     "fractional bitrate is rounded toward positive infinity",
			tileGrid: [2]int{3, 2},
			interval: 3_000,
			bytes:    2_501,
			want:     1_112,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := (trickplay.Manager{}).Plan(trickplay.GenerationRequest{
				ItemID:               mustUUID(t, "00112233445566778899aabbccddeeff"),
				RequestedWidth:       320,
				GeneratedHeight:      180,
				TileWidth:            test.tileGrid[0],
				TileHeight:           test.tileGrid[1],
				IntervalMilliseconds: test.interval,
				ThumbnailPaths:       []string{"frame-000.jpg"},
				TileByteSizes:        []int64{test.bytes},
			})
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			if got := plan.Metadata.Bandwidth; got != test.want {
				t.Fatalf("bandwidth = %d, want %d", got, test.want)
			}
		})
	}
}

func TestManagerImportUsesLegacyWholeSecondBandwidthArithmetic(t *testing.T) {
	itemID := mustUUID(t, "00112233445566778899aabbccddeeff")
	metadata, err := (trickplay.Manager{}).Import(trickplay.ImportRequest{
		ItemID:               itemID,
		Width:                320,
		TileWidth:            2,
		TileHeight:           2,
		IntervalMilliseconds: 1_500,
		Tiles: []trickplay.ImportedTile{
			{ByteSize: 1, Height: 359},
			{ByteSize: 1_001, Height: 361},
		},
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	want := trickplay.Metadata{
		ItemID:         itemID,
		Width:          320,
		Height:         181,
		TileWidth:      2,
		TileHeight:     2,
		ThumbnailCount: 2,
		Interval:       1_500,
		Bandwidth:      2_002,
	}
	if metadata != want {
		t.Fatalf("imported metadata = %#v, want %#v", metadata, want)
	}
}

func TestManagerCanGenerateHonorsRuntimeTickBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		intervalMS int64
		runtime    *int64
		want       bool
	}{
		{name: "missing runtime is ineligible", intervalMS: 1_500, want: false},
		{name: "one 100ns tick before interval is ineligible", intervalMS: 1_500, runtime: int64Pointer(14_999_999), want: false},
		{name: "runtime exactly equal to interval is eligible", intervalMS: 1_500, runtime: int64Pointer(15_000_000), want: true},
		{name: "subsecond interval uses 100ns ticks without truncation", intervalMS: 999, runtime: int64Pointer(9_990_000), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := (trickplay.Manager{}).CanGenerate(test.runtime, test.intervalMS); got != test.want {
				t.Fatalf("CanGenerate(runtime=%v, interval=%dms) = %t, want %t", test.runtime, test.intervalMS, got, test.want)
			}
		})
	}
}

func intPointer(value int) *int {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

func mustUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", text, err)
	}
	return id
}
