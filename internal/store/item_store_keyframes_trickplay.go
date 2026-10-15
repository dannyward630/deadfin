// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// KeyframeInfo is the item-scoped keyframe seek cache. Duration and positions
// are 100ns ticks. A nil KeyframeTicks slice is distinct from an empty slice.
type KeyframeInfo struct {
	ItemID dto.UUID
	// SourceRevision is the required write-side source snapshot token. It is
	// deliberately not persisted or returned by reads.
	SourceRevision     string
	TotalDurationTicks int64
	KeyframeTicks      []int64
}

// TrickplayInfo is one tiled-thumbnail rendition. IntervalMS is milliseconds;
// Bandwidth is bytes per second.
type TrickplayInfo struct {
	ItemID dto.UUID
	// SourceRevision is the required write-side source snapshot token. It is
	// deliberately not persisted or returned by reads.
	SourceRevision  string
	Width           int
	Height          int
	TileWidth       int
	TileHeight      int
	ThumbnailCount  int
	IntervalMS      int64
	Bandwidth       int64
	GenerationToken string
}

// Keyframes returns the keyframe seek cache for itemID.
func (s *ItemStore) Keyframes(ctx context.Context, itemID dto.UUID) (KeyframeInfo, error) {
	if itemID.IsNil() {
		return KeyframeInfo{}, errors.New("keyframes: item id is required")
	}

	var duration int64
	var raw any
	if err := s.stores.Reader().QueryRowContext(ctx, "SELECT total_duration_ticks,keyframe_ticks FROM keyframes WHERE item_id = "+s.ph(1), s.uuidArg(itemID)).Scan(&duration, &raw); err != nil {
		return KeyframeInfo{}, err
	}

	result := KeyframeInfo{ItemID: itemID, TotalDurationTicks: duration}
	if isNil(raw) {
		return result, nil
	}
	if err := json.Unmarshal([]byte(asString(raw)), &result.KeyframeTicks); err != nil {
		return KeyframeInfo{}, fmt.Errorf("decode keyframe ticks: %w", err)
	}
	return result, nil
}

// ReplaceKeyframes atomically replaces the complete keyframe seek cache.
func (s *ItemStore) ReplaceKeyframes(ctx context.Context, value KeyframeInfo) (err error) {
	if err := validateKeyframeInfo(value); err != nil {
		return err
	}

	var ticks any
	if value.KeyframeTicks != nil {
		encoded, err := json.Marshal(value.KeyframeTicks)
		if err != nil {
			return fmt.Errorf("encode keyframe ticks: %w", err)
		}
		ticks = string(encoded)
	}

	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireCurrentSourceRevision(ctx, tx, value.ItemID, value.SourceRevision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO keyframes(item_id,total_duration_ticks,keyframe_ticks) VALUES ("+s.placeholders(3)+") ON CONFLICT(item_id) DO UPDATE SET total_duration_ticks=excluded.total_duration_ticks, keyframe_ticks=excluded.keyframe_ticks", s.uuidArg(value.ItemID), value.TotalDurationTicks, ticks); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteKeyframes removes the item-scoped keyframe seek cache.
func (s *ItemStore) DeleteKeyframes(ctx context.Context, itemID dto.UUID) (err error) {
	if itemID.IsNil() {
		return errors.New("delete keyframes: item id is required")
	}

	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if _, err := tx.ExecContext(ctx, "DELETE FROM keyframes WHERE item_id = "+s.ph(1), s.uuidArg(itemID)); err != nil {
		return err
	}
	return tx.Commit()
}

// Trickplays returns all trickplay renditions for itemID in width order.
func (s *ItemStore) Trickplays(ctx context.Context, itemID dto.UUID) ([]TrickplayInfo, error) {
	if itemID.IsNil() {
		return nil, errors.New("trickplays: item id is required")
	}

	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT width,height,tile_width,tile_height,thumbnail_count,interval_ms,bandwidth,generation_token FROM trickplay_infos WHERE item_id = "+s.ph(1)+" ORDER BY width", s.uuidArg(itemID))
	if err != nil {
		return nil, err
	}
	var result []TrickplayInfo
	for rows.Next() {
		value := TrickplayInfo{ItemID: itemID}
		if err := rows.Scan(&value.Width, &value.Height, &value.TileWidth, &value.TileHeight, &value.ThumbnailCount, &value.IntervalMS, &value.Bandwidth, &value.GenerationToken); err != nil {
			return nil, closeRows(rows, err)
		}
		result = append(result, value)
	}
	return result, closeRows(rows, rows.Err())
}

// Trickplay returns the rendition for itemID and width.
func (s *ItemStore) Trickplay(ctx context.Context, itemID dto.UUID, width int) (TrickplayInfo, error) {
	if itemID.IsNil() {
		return TrickplayInfo{}, errors.New("trickplay: item id is required")
	}
	if err := validateTrickplayWidth("trickplay", width); err != nil {
		return TrickplayInfo{}, err
	}

	result := TrickplayInfo{ItemID: itemID, Width: width}
	err := s.stores.Reader().QueryRowContext(ctx, "SELECT height,tile_width,tile_height,thumbnail_count,interval_ms,bandwidth,generation_token FROM trickplay_infos WHERE item_id = "+s.ph(1)+" AND width = "+s.ph(2), s.uuidArg(itemID), width).Scan(&result.Height, &result.TileWidth, &result.TileHeight, &result.ThumbnailCount, &result.IntervalMS, &result.Bandwidth, &result.GenerationToken)
	if err != nil {
		return TrickplayInfo{}, err
	}
	return result, nil
}

// ReplaceTrickplay atomically replaces one item/width trickplay rendition.
func (s *ItemStore) ReplaceTrickplay(ctx context.Context, value TrickplayInfo) (err error) {
	if err := validateTrickplayInfo(value); err != nil {
		return err
	}

	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireCurrentSourceRevision(ctx, tx, value.ItemID, value.SourceRevision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO trickplay_infos(item_id,width,height,tile_width,tile_height,thumbnail_count,interval_ms,bandwidth,generation_token) VALUES ("+s.placeholders(9)+") ON CONFLICT(item_id,width) DO UPDATE SET height=excluded.height, tile_width=excluded.tile_width, tile_height=excluded.tile_height, thumbnail_count=excluded.thumbnail_count, interval_ms=excluded.interval_ms, bandwidth=excluded.bandwidth, generation_token=excluded.generation_token", s.uuidArg(value.ItemID), value.Width, value.Height, value.TileWidth, value.TileHeight, value.ThumbnailCount, value.IntervalMS, value.Bandwidth, value.GenerationToken); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteTrickplay removes one item/width trickplay rendition.
func (s *ItemStore) DeleteTrickplay(ctx context.Context, itemID dto.UUID, width int) (err error) {
	if itemID.IsNil() {
		return errors.New("delete trickplay: item id is required")
	}
	if err := validateTrickplayWidth("delete trickplay", width); err != nil {
		return err
	}

	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if _, err := tx.ExecContext(ctx, "DELETE FROM trickplay_infos WHERE item_id = "+s.ph(1)+" AND width = "+s.ph(2), s.uuidArg(itemID), width); err != nil {
		return err
	}
	return tx.Commit()
}

func validateKeyframeInfo(value KeyframeInfo) error {
	if value.ItemID.IsNil() {
		return errors.New("replace keyframes: item id is required")
	}
	if strings.TrimSpace(value.SourceRevision) == "" {
		return errors.New("replace keyframes: source revision is required")
	}
	if value.TotalDurationTicks < 0 {
		return errors.New("replace keyframes: total duration ticks must not be negative")
	}
	for _, tick := range value.KeyframeTicks {
		if tick < 0 {
			return errors.New("replace keyframes: keyframe ticks must not be negative")
		}
	}
	return nil
}

func validateTrickplayInfo(value TrickplayInfo) error {
	if value.ItemID.IsNil() {
		return errors.New("replace trickplay: item id is required")
	}
	if strings.TrimSpace(value.SourceRevision) == "" {
		return errors.New("replace trickplay: source revision is required")
	}
	if err := validateTrickplayWidth("replace trickplay", value.Width); err != nil {
		return err
	}
	if value.Height < 0 {
		return errors.New("replace trickplay: height must not be negative")
	}
	if value.TileWidth < 0 {
		return errors.New("replace trickplay: tile width must not be negative")
	}
	if value.TileHeight < 0 {
		return errors.New("replace trickplay: tile height must not be negative")
	}
	if value.ThumbnailCount < 0 {
		return errors.New("replace trickplay: thumbnail count must not be negative")
	}
	if value.IntervalMS <= 0 {
		return errors.New("replace trickplay: interval milliseconds must be positive")
	}
	if value.Bandwidth < 0 {
		return errors.New("replace trickplay: bandwidth must not be negative")
	}
	if strings.TrimSpace(value.GenerationToken) == "" {
		return errors.New("replace trickplay: generation token is required")
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"height", int64(value.Height)},
		{"tile width", int64(value.TileWidth)},
		{"tile height", int64(value.TileHeight)},
		{"thumbnail count", int64(value.ThumbnailCount)},
		{"interval milliseconds", value.IntervalMS},
		{"bandwidth", value.Bandwidth},
	} {
		if err := validateTrickplayInt32("replace trickplay", field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func validateTrickplayWidth(operation string, width int) error {
	if width <= 0 {
		return fmt.Errorf("%s: width must be positive", operation)
	}
	return validateTrickplayInt32(operation, "width", int64(width))
}

func validateTrickplayInt32(operation, field string, value int64) error {
	if value > 1<<31-1 {
		return fmt.Errorf("%s: %s must fit a signed 32-bit integer", operation, field)
	}
	return nil
}
