// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// ErrStaleSourceRevision reports that a derived-metadata write was based on a
// source snapshot that is no longer current. It also covers a source deleted
// after its snapshot was read.
var ErrStaleSourceRevision = errors.New("stale source revision")

// DerivedSource is the atomically captured subset of a media source that
// derived-metadata workers need. SourceRevision is an opaque write-side
// compare-and-swap value projected only when the source has a complete inode
// and change-time identity; it is not durable metadata.
type DerivedSource struct {
	ItemID             dto.UUID
	Path               *string
	Size               *int64
	DateModified       *time.Time
	FileInode          *int64
	FileChangedAt      *time.Time
	RunTimeTicks       *int64
	Width              *int
	Height             *int
	Container          *string
	SourceRevision     string
	ReferenceFrameRate *float32
	sourceRevision     string
}

// DerivedSourceSnapshot reads the source projection and its complete ordered
// stream descriptions from one repeatable-read snapshot. SourceRevision covers
// the exact facts whose reconciliation changes invalidate derived metadata when
// the source has a complete inode and change-time identity.
func (s *ItemStore) DerivedSourceSnapshot(ctx context.Context, itemID dto.UUID) (result DerivedSource, err error) {
	if itemID.IsNil() {
		return DerivedSource{}, errors.New("derived source snapshot: item id is required")
	}

	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return DerivedSource{}, err
	}
	defer rollbackOnReturn(tx, &err)
	result, err = s.derivedSource(ctx, tx, itemID, false)
	if err != nil {
		return DerivedSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return DerivedSource{}, err
	}
	return result, nil
}

// derivedSource reads the eleven revision item facts and the full ordered stream
// descriptions. lockRows applies PostgreSQL share locks during a conditional
// writer transaction; SQLite's one-connection writer provides that serialization.
func (s *ItemStore) derivedSource(ctx context.Context, db queryer, itemID dto.UUID, lockRows bool) (DerivedSource, error) {
	query := "SELECT path,size,date_modified,file_inode,file_changed_at,run_time_ticks,container,media_source_container,width,height,total_bitrate FROM items WHERE id = " + s.ph(1)
	if lockRows && s.ph(1) != "?" {
		query += " FOR SHARE"
	}
	var path, size, modified, inode, changed, runtime, container, mediaSourceContainer, width, height, bitrate any
	if err := db.QueryRowContext(ctx, query, s.uuidArg(itemID)).Scan(&path, &size, &modified, &inode, &changed, &runtime, &container, &mediaSourceContainer, &width, &height, &bitrate); err != nil {
		return DerivedSource{}, err
	}

	facts := item.Item{
		ID:                   itemID,
		Path:                 optionalString(path),
		Size:                 optionalInt64(size),
		RunTimeTicks:         optionalInt64(runtime),
		Container:            optionalString(container),
		MediaSourceContainer: optionalString(mediaSourceContainer),
		Width:                optionalInt(width),
		Height:               optionalInt(height),
		TotalBitrate:         optionalInt(bitrate),
		FileInode:            optionalInt64(inode),
	}
	if !isNil(modified) {
		value, err := s.stores.Dialect().ScanTime(modified)
		if err != nil {
			return DerivedSource{}, fmt.Errorf("scan derived source date modified: %w", err)
		}
		facts.DateModified = &value
	}
	if !isNil(changed) {
		value, err := s.stores.Dialect().ScanTime(changed)
		if err != nil {
			return DerivedSource{}, fmt.Errorf("scan derived source file changed at: %w", err)
		}
		facts.FileChangedAt = &value
	}
	streams, err := s.streamsWithLock(ctx, db, itemID, lockRows)
	if err != nil {
		return DerivedSource{}, err
	}
	revision, err := sourceRevision(facts, streams)
	if err != nil {
		return DerivedSource{}, err
	}
	projectedRevision := ""
	if hasCompleteSourceIdentity(facts) {
		projectedRevision = revision
	}
	result := DerivedSource{
		ItemID:         itemID,
		Path:           cloneString(facts.Path),
		Size:           cloneInt64(facts.Size),
		DateModified:   cloneTime(facts.DateModified),
		FileInode:      cloneInt64(facts.FileInode),
		FileChangedAt:  cloneTime(facts.FileChangedAt),
		RunTimeTicks:   cloneInt64(facts.RunTimeTicks),
		Width:          cloneInt(facts.Width),
		Height:         cloneInt(facts.Height),
		Container:      cloneString(facts.Container),
		SourceRevision: projectedRevision,
		sourceRevision: revision,
	}
	for index := range streams {
		stream := &streams[index]
		if stream.Type != playback.StreamTypeVideo {
			continue
		}
		if rate := stream.ReferenceFrameRate(); rate != nil {
			value := *rate
			result.ReferenceFrameRate = &value
		}
		break
	}
	return result, nil
}

// currentSourceRevisionTx reads the revision from a source-fact mutation
// transaction that already holds the item's derived-state lock.
func (s *ItemStore) currentSourceRevisionTx(ctx context.Context, tx *sql.Tx, itemID dto.UUID) (string, bool, error) {
	current, err := s.derivedSource(ctx, tx, itemID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return current.sourceRevision, true, nil
}

func (s *ItemStore) sourceRevisionChangedTx(ctx context.Context, tx *sql.Tx, itemID dto.UUID, before string, found bool) (bool, error) {
	if !found {
		return false, nil
	}
	after, exists, err := s.currentSourceRevisionTx(ctx, tx, itemID)
	if err != nil {
		return false, err
	}
	return !exists || before != after, nil
}

// requireCurrentSourceRevision serializes with reconciliation, locks the
// current PostgreSQL source rows, and compares the caller's snapshot revision
// before any derived metadata is written.
func (s *ItemStore) requireCurrentSourceRevision(ctx context.Context, tx *sql.Tx, itemID dto.UUID, expected string) error {
	if expected == "" {
		return ErrStaleSourceRevision
	}
	if err := s.lockDerivedItems(ctx, tx, itemID); err != nil {
		return err
	}
	current, err := s.derivedSource(ctx, tx, itemID, true)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleSourceRevision
	}
	if err != nil {
		return err
	}
	if !hasCompleteSourceIdentity(item.Item{FileInode: current.FileInode, FileChangedAt: current.FileChangedAt}) || current.sourceRevision != expected {
		return ErrStaleSourceRevision
	}
	return nil
}

// hasCompleteSourceIdentity distinguishes authoritative source identities from
// legacy records. Device is deliberately advisory because it is unavailable or
// unstable on some supported platforms.
func hasCompleteSourceIdentity(value item.Item) bool {
	return value.FileInode != nil && value.FileChangedAt != nil
}

func optionalInt64(value any) *int64 {
	if isNil(value) {
		return nil
	}
	result := asInt64(value)
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

type sourceRevisionItemFacts struct {
	Path                 *string `json:"path"`
	Size                 *int64  `json:"size"`
	DateModified         *string `json:"dateModified"`
	FileInode            *int64  `json:"fileInode"`
	FileChangedAt        *string `json:"fileChangedAt"`
	RunTimeTicks         *int64  `json:"runTimeTicks"`
	Container            *string `json:"container"`
	MediaSourceContainer *string `json:"mediaSourceContainer"`
	Width                *int    `json:"width"`
	Height               *int    `json:"height"`
	TotalBitrate         *int    `json:"totalBitrate"`
}

// sourceRevisionItemFragment encodes precisely the non-stream source facts
// that invalidate derived metadata. JSON nulls preserve nil-versus-zero, while
// the timestamp's UTC RFC3339Nano form makes dialect storage representation
// irrelevant.
func sourceRevisionItemFragment(value item.Item) []byte {
	var modified, changedAt *string
	if value.DateModified != nil {
		utc := value.DateModified.UTC().Format(time.RFC3339Nano)
		modified = &utc
	}
	if value.FileChangedAt != nil {
		utc := value.FileChangedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		changedAt = &utc
	}
	encoded, err := json.Marshal(sourceRevisionItemFacts{
		Path:                 value.Path,
		Size:                 value.Size,
		DateModified:         modified,
		FileInode:            value.FileInode,
		FileChangedAt:        changedAt,
		RunTimeTicks:         value.RunTimeTicks,
		Container:            value.Container,
		MediaSourceContainer: value.MediaSourceContainer,
		Width:                value.Width,
		Height:               value.Height,
		TotalBitrate:         value.TotalBitrate,
	})
	if err != nil {
		panic(fmt.Sprintf("encode source revision item fragment: %v", err))
	}
	return encoded
}

func sourceRevisionChanged(stored, next item.Item) bool {
	return !bytes.Equal(sourceRevisionItemFragment(stored), sourceRevisionItemFragment(next))
}

func sourceRevision(value item.Item, streams []playback.MediaStream) (string, error) {
	streamFragment, err := json.Marshal(streams)
	if err != nil {
		return "", fmt.Errorf("encode source revision streams: %w", err)
	}
	hash := sha256.New()
	_, _ = hash.Write(sourceRevisionItemFragment(value))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(streamFragment)
	return hex.EncodeToString(hash.Sum(nil)), nil
}
