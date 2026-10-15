// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"errors"
	"fmt"
	"math/bits"

	"github.com/deadfin-org/deadfin/internal/dto"
)

const ticksPerMillisecond int64 = 10_000

// Metadata is the persisted description of one trickplay resolution.
type Metadata struct {
	ItemID          dto.UUID
	Width           int
	Height          int
	TileWidth       int
	TileHeight      int
	ThumbnailCount  int
	Interval        int64
	Bandwidth       int64
	SourceRevision  string
	GenerationToken string
}

// GenerationRequest contains the facts produced while generating a resolution.
type GenerationRequest struct {
	ItemID               dto.UUID
	RequestedWidth       int
	VideoWidth           *int
	GeneratedHeight      int
	TileWidth            int
	TileHeight           int
	IntervalMilliseconds int64
	ThumbnailPaths       []string
	TileByteSizes        []int64
}

// Tile is one row-major batch of thumbnail input paths.
type Tile struct {
	Index          int
	ThumbnailPaths []string
}

// GenerationPlan is the metadata and ordered tiles derived from a generation request.
type GenerationPlan struct {
	Metadata Metadata
	Tiles    []Tile
}

// ImportedTile is the information read from one legacy trickplay tile.
type ImportedTile struct {
	ByteSize int64
	Height   int
}

// ImportRequest contains the facts available when importing existing tiles.
type ImportRequest struct {
	ItemID               dto.UUID
	Width                int
	TileWidth            int
	TileHeight           int
	IntervalMilliseconds int64
	Tiles                []ImportedTile
}

// Manager derives trickplay metadata without performing I/O.
type Manager struct{}

// Plan normalizes a requested resolution, batches thumbnails, and derives metadata.
func (Manager) Plan(request GenerationRequest) (GenerationPlan, error) {
	if len(request.ThumbnailPaths) == 0 {
		return GenerationPlan{}, errors.New("cannot create trickplay from zero thumbnails")
	}
	if request.RequestedWidth <= 0 {
		return GenerationPlan{}, errors.New("trickplay width must be positive")
	}
	if request.GeneratedHeight <= 0 {
		return GenerationPlan{}, errors.New("trickplay height must be positive")
	}
	if request.IntervalMilliseconds <= 0 {
		return GenerationPlan{}, errors.New("trickplay interval must be positive")
	}

	capacity, err := tileCapacity(request.TileWidth, request.TileHeight)
	if err != nil {
		return GenerationPlan{}, err
	}

	tileCount := ceilingDivide(len(request.ThumbnailPaths), capacity)
	if len(request.TileByteSizes) != tileCount {
		return GenerationPlan{}, fmt.Errorf("trickplay tile byte sizes = %d, want %d", len(request.TileByteSizes), tileCount)
	}

	width := evenWidth(request.RequestedWidth)
	if request.VideoWidth != nil && *request.VideoWidth < request.RequestedWidth {
		if *request.VideoWidth <= 0 {
			return GenerationPlan{}, errors.New("source video width must be positive")
		}
		width = evenWidth(*request.VideoWidth)
	}
	if width <= 0 {
		return GenerationPlan{}, errors.New("normalized trickplay width must be positive")
	}

	tiles := make([]Tile, 0, tileCount)
	var bandwidth int64
	for index, first := 0, 0; first < len(request.ThumbnailPaths); index, first = index+1, first+capacity {
		last := min(first+capacity, len(request.ThumbnailPaths))
		tiles = append(tiles, Tile{Index: index, ThumbnailPaths: request.ThumbnailPaths[first:last]})

		bitrate, err := generatedBandwidth(request.TileByteSizes[index], capacity, request.IntervalMilliseconds)
		if err != nil {
			return GenerationPlan{}, fmt.Errorf("tile %d bandwidth: %w", index, err)
		}
		bandwidth = max(bandwidth, bitrate)
	}

	return GenerationPlan{
		Metadata: Metadata{
			ItemID:         request.ItemID,
			Width:          width,
			Height:         request.GeneratedHeight,
			TileWidth:      request.TileWidth,
			TileHeight:     request.TileHeight,
			ThumbnailCount: len(request.ThumbnailPaths),
			Interval:       request.IntervalMilliseconds,
			Bandwidth:      bandwidth,
		},
		Tiles: tiles,
	}, nil
}

// Import derives persisted metadata from an existing set of legacy tiles.
func (Manager) Import(request ImportRequest) (Metadata, error) {
	if request.IntervalMilliseconds <= 0 {
		return Metadata{}, errors.New("trickplay interval must be positive")
	}
	capacity, err := tileCapacity(request.TileWidth, request.TileHeight)
	if err != nil {
		return Metadata{}, err
	}

	// Legacy import divides milliseconds to whole seconds before computing bitrate.
	seconds := request.IntervalMilliseconds / 1_000
	if seconds <= 0 {
		return Metadata{}, errors.New("legacy trickplay interval must contain a whole second")
	}

	metadata := Metadata{
		ItemID:         request.ItemID,
		Width:          request.Width,
		TileWidth:      request.TileWidth,
		TileHeight:     request.TileHeight,
		ThumbnailCount: len(request.Tiles),
		Interval:       request.IntervalMilliseconds,
	}
	for index, tile := range request.Tiles {
		if tile.Height < 0 {
			return Metadata{}, fmt.Errorf("tile %d height must not be negative", index)
		}
		metadata.Height = max(metadata.Height, ceilingDivide(tile.Height, request.TileHeight))

		bitrate, err := legacyImportedBandwidth(tile.ByteSize, capacity, seconds)
		if err != nil {
			return Metadata{}, fmt.Errorf("tile %d bandwidth: %w", index, err)
		}
		metadata.Bandwidth = max(metadata.Bandwidth, bitrate)
	}
	return metadata, nil
}

// CanGenerate reports whether a known runtime is at least one sampling interval.
func (Manager) CanGenerate(runtimeTicks *int64, intervalMilliseconds int64) bool {
	if runtimeTicks == nil || *runtimeTicks < 0 || intervalMilliseconds <= 0 {
		return false
	}
	if intervalMilliseconds > int64(^uint64(0)>>1)/ticksPerMillisecond {
		return false
	}
	return *runtimeTicks >= intervalMilliseconds*ticksPerMillisecond
}

func evenWidth(width int) int { return 2 * (width / 2) }

func tileCapacity(width, height int) (int, error) {
	if width <= 0 || height <= 0 {
		return 0, errors.New("trickplay tile dimensions must be positive")
	}
	maxInt := int(^uint(0) >> 1)
	if width > maxInt/height {
		return 0, errors.New("trickplay tile capacity overflows int")
	}
	return width * height, nil
}

func ceilingDivide(value, divisor int) int {
	return value/divisor + min(value%divisor, 1)
}

func generatedBandwidth(byteSize int64, capacity int, intervalMilliseconds int64) (int64, error) {
	return ceilingScaled(byteSize, 8_000, uint64(capacity), uint64(intervalMilliseconds))
}

func legacyImportedBandwidth(byteSize int64, capacity int, seconds int64) (int64, error) {
	return ceilingScaled(byteSize, 8, uint64(capacity), uint64(seconds))
}

// ceilingScaled returns ceil(value*scale/(firstDenominator*secondDenominator)).
func ceilingScaled(value int64, scale, firstDenominator, secondDenominator uint64) (int64, error) {
	if value < 0 {
		return 0, errors.New("tile byte size must not be negative")
	}
	denominatorHigh, denominator := bits.Mul64(firstDenominator, secondDenominator)
	if denominatorHigh != 0 || denominator == 0 {
		return 0, errors.New("trickplay bandwidth denominator overflows")
	}
	numeratorHigh, numerator := bits.Mul64(uint64(value), scale)
	if numeratorHigh >= denominator {
		return 0, errors.New("trickplay bandwidth overflows int64")
	}
	quotient, remainder := bits.Div64(numeratorHigh, numerator, denominator)
	if remainder != 0 {
		if quotient >= uint64(^uint64(0)>>1) {
			return 0, errors.New("trickplay bandwidth overflows int64")
		}
		quotient++
	}
	if quotient > uint64(^uint64(0)>>1) {
		return 0, errors.New("trickplay bandwidth overflows int64")
	}
	return int64(quotient), nil
}
