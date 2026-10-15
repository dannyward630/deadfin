// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// SQLiteSnapshot provides read-only access to a stock Jellyfin SQLite database.
type SQLiteSnapshot struct {
	db *sql.DB
}

// SQLiteMedia is the aggregate and stream data used when comparing a media item
// to a stock Jellyfin SQLite snapshot.
type SQLiteMedia struct {
	Container    string
	Size         any
	RunTimeTicks any
	TotalBitrate any
	Streams      []map[string]any
}

var sqliteSnapshotStreamColumns = [...]string{
	"StreamIndex", "AspectRatio", "AverageFrameRate", "BitDepth", "BitRate", "BlPresentFlag", "ChannelLayout", "Channels", "Codec", "CodecTag", "CodecTimeBase", "ColorPrimaries", "ColorSpace", "ColorTransfer", "Comment", "DvBlSignalCompatibilityId", "DvLevel", "DvProfile", "DvVersionMajor", "DvVersionMinor", "ElPresentFlag", "Height", "IsAnamorphic", "IsAvc", "IsDefault", "IsExternal", "IsForced", "IsHearingImpaired", "IsInterlaced", "Language", "Level", "NalLengthSize", "PixelFormat", "Profile", "RealFrameRate", "RefFrames", "Rotation", "RpuPresentFlag", "SampleRate", "StreamType", "TimeBase", "Title", "Width", "Hdr10PlusPresentFlag", "IsOriginal",
}

// OpenSQLiteSnapshot opens path as a read-only SQLite database and verifies that
// the stock snapshot is reachable before returning it.
func OpenSQLiteSnapshot(ctx context.Context, path string) (*SQLiteSnapshot, error) {
	dsn, err := readOnlySQLiteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open stock SQLite snapshot: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect stock SQLite snapshot: %w", err)
	}
	return &SQLiteSnapshot{db: db}, nil
}

// Close releases the read-only SQLite snapshot connection.
func (s *SQLiteSnapshot) Close() error {
	return s.db.Close()
}

// Media reads the aggregate BaseItems fields and ordered MediaStreamInfos rows
// required to compare path against a stock Jellyfin SQLite snapshot.
func (s *SQLiteSnapshot) Media(ctx context.Context, path string) (SQLiteMedia, error) {
	var itemID, data string
	var media SQLiteMedia
	if err := s.db.QueryRowContext(ctx, `SELECT Id, Size, RunTimeTicks, TotalBitrate, Data FROM BaseItems WHERE Path = ?`, path).Scan(&itemID, &media.Size, &media.RunTimeTicks, &media.TotalBitrate, &data); err != nil {
		return SQLiteMedia{}, fmt.Errorf("stock BaseItems row for %q: %w", path, err)
	}
	var payload struct {
		Container string `json:"Container"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return SQLiteMedia{}, fmt.Errorf("decode stock BaseItems.Data for %q: %w", path, err)
	}
	media.Container = payload.Container

	rows, err := s.db.QueryContext(ctx, `SELECT StreamIndex, AspectRatio, AverageFrameRate, BitDepth, BitRate, BlPresentFlag, ChannelLayout, Channels, Codec, CodecTag, CodecTimeBase, ColorPrimaries, ColorSpace, ColorTransfer, Comment, DvBlSignalCompatibilityId, DvLevel, DvProfile, DvVersionMajor, DvVersionMinor, ElPresentFlag, Height, IsAnamorphic, IsAvc, IsDefault, IsExternal, IsForced, IsHearingImpaired, IsInterlaced, Language, Level, NalLengthSize, PixelFormat, Profile, RealFrameRate, RefFrames, Rotation, RpuPresentFlag, SampleRate, StreamType, TimeBase, Title, Width, Hdr10PlusPresentFlag, IsOriginal FROM MediaStreamInfos WHERE ItemId = ? ORDER BY StreamIndex`, itemID)
	if err != nil {
		return SQLiteMedia{}, fmt.Errorf("stock MediaStreamInfos rows for %q: %w", path, err)
	}

	for rows.Next() {
		values := make([]any, len(sqliteSnapshotStreamColumns))
		destinations := make([]any, len(sqliteSnapshotStreamColumns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return SQLiteMedia{}, fmt.Errorf("scan stock MediaStreamInfos row for %q: %w", path, closeRows(rows, err))
		}
		stream := make(map[string]any, len(sqliteSnapshotStreamColumns))
		for index, column := range sqliteSnapshotStreamColumns {
			stream[column] = values[index]
		}
		media.Streams = append(media.Streams, stream)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return SQLiteMedia{}, fmt.Errorf("read stock MediaStreamInfos rows for %q: %w", path, err)
	}
	return media, nil
}

func closeRows(rows *sql.Rows, primary error) error {
	return errors.Join(primary, rows.Close())
}

func readOnlySQLiteDSN(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("stock SQLite snapshot path is required")
	}

	var dsn *url.URL
	var err error
	if strings.HasPrefix(path, "file:") {
		dsn, err = url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("parse stock SQLite snapshot DSN: %w", err)
		}
	} else {
		dsn = &url.URL{Scheme: "file", Path: path}
	}
	query := dsn.Query()
	query.Set("mode", "ro")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}
