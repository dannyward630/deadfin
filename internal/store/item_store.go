// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// ItemStore persists the normalized item universe. All writes use the store's
// single writer handle; reads use its reader pool.
type ItemStore struct{ stores Stores }

// NewItemStore constructs the typed item persistence adapter for stores.
func NewItemStore(stores Stores) *ItemStore { return &ItemStore{stores: stores} }

// IsNotFound reports whether a store read found no matching row. It keeps
// database driver error details within the store package while preserving the
// underlying error for existing callers that use errors.Is.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// UserData is the persisted per-user (or shared custom-key) playback state.
type UserData struct {
	UserID              dto.UUID
	CustomKey           string
	ItemID              dto.UUID
	Played              bool
	PositionTicks       int64
	PlayCount           int
	IsFavorite          bool
	Rating              *float32
	Likes               *int
	LastPlayed          *time.Time
	AudioStreamIndex    *int
	SubtitleStreamIndex *int
}

// ItemShare is a Playlist visibility grant. It is intentionally distinct from
// the generic item OwnerID and playlist owner/open-access facts.
type ItemShare struct {
	UserID  dto.UUID
	CanEdit bool
}

// Chapter is a durable chapter satellite.
type Chapter struct {
	Index              int
	StartPositionTicks int64
	Name               *string
	ImagePath          *string
	Extra              json.RawMessage
}

var itemColumns = []string{
	"id", "kind", "parent_id", "top_parent_id", "series_id", "season_id", "channel_id", "primary_version_id", "owner_id", "playlist_owner_user_id",
	"path", "name", "sort_name", "clean_name", "forced_sort_name", "original_title", "original_language", "overview", "tagline", "official_rating", "custom_rating", "presentation_unique_key", "series_presentation_unique_key", "album", "external_id", "external_series_id", "external_service_id", "preferred_metadata_language", "preferred_metadata_country_code", "series_name", "season_name", "episode_title", "show_id", "container", "media_source_container",
	"start_date", "end_date", "premiere_date", "date_created", "date_modified", "date_last_refreshed", "date_last_saved", "date_last_media_added",
	"production_year", "index_number", "parent_index_number", "width", "height", "total_bitrate", "inherited_parental_rating_value", "inherited_parental_rating_subvalue", "airs_before_season_number", "airs_after_season_number", "airs_before_episode_number", "run_time_ticks", "size", "community_rating", "critic_rating", "lufs", "normalization_gain",
	"unrated_type", "source_type", "media_type", "extra_type", "video_type", "iso_type", "video_3d_format", "series_status", "program_audio",
	"is_folder", "is_virtual_item", "is_in_mixed_folder", "is_locked", "is_movie", "is_series", "is_repeat", "is_placeholder", "open_access", "extra", "file_device", "file_inode", "file_changed_at",
}

func (s *ItemStore) Get(ctx context.Context, id dto.UUID) (result item.Item, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return item.Item{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id = "+s.ph(1), s.uuidArg(id))
	it, err := scanItem(row, s.stores.Dialect())
	if err != nil {
		return item.Item{}, err
	}
	if err := s.loadSatellites(ctx, tx, &it); err != nil {
		return item.Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return item.Item{}, err
	}
	return it, nil
}

func (s *ItemStore) GetBatch(ctx context.Context, ids []dto.UUID) (result []item.Item, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i := range ids {
		args[i], marks[i] = s.uuidArg(ids[i]), s.ph(i+1)
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id IN ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return nil, err
	}
	byID := make(map[dto.UUID]item.Item, len(ids))
	for rows.Next() {
		it, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		byID[it.ID] = it
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	for id, it := range byID {
		if err := s.loadSatellites(ctx, tx, &it); err != nil {
			return nil, err
		}
		byID[id] = it
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]item.Item, 0, len(ids))
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (s *ItemStore) Upsert(ctx context.Context, it item.Item) (err error) {
	if it.ID.IsNil() {
		return errors.New("upsert item: id is required")
	}
	if it.Kind.String() == "" {
		return errors.New("upsert item: known kind is required")
	}
	if it.Kind != item.KindPlaylist && !it.PlaylistOwnerUserID.IsNil() {
		return errors.New("upsert item: playlist owner is only valid for Playlist")
	}
	if it.Kind != item.KindPlaylist && it.OpenAccess {
		return errors.New("upsert item: open access is only valid for Playlist")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedWrite(ctx, tx, it); err != nil {
		return err
	}
	if err := s.validateUpsert(ctx, tx, it); err != nil {
		return err
	}
	beforeRevision, found, err := s.currentSourceRevisionTx(ctx, tx, it.ID)
	if err != nil {
		return err
	}
	if err := s.upsertItem(ctx, tx, it); err != nil {
		return err
	}
	if err := s.replaceSatellites(ctx, tx, it); err != nil {
		return err
	}
	invalidated, err := s.sourceRevisionChangedTx(ctx, tx, it.ID, beforeRevision, found)
	if err != nil {
		return err
	}
	if invalidated {
		if err := s.deleteDerivedMetadataFor(ctx, tx, it.ID); err != nil {
			return err
		}
	}
	if err := s.repairDerivedForTx(ctx, tx, []dto.UUID{it.ID}, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ItemStore) Delete(ctx context.Context, id dto.UUID) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedScope(ctx, tx, func(ctx context.Context, tx *sql.Tx) (derivedLockScope, error) {
		subtree, err := s.subtreeIDs(ctx, tx, id)
		return derivedLockScope{items: []dto.UUID{id}, deleted: subtree}, err
	}); err != nil {
		return err
	}
	// A library delete removes the complete subtree. Its closure rows define the
	// deleted set before the source item rows are removed.
	subtree, err := s.subtreeIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	tables := []string{"item_images", "provider_ids", "locked_fields", "trailer_types", "item_value_map", "item_shares", "boxset_library_folders", "people_map", "media_streams", "media_attachments", "chapters", "item_additional_parts", "keyframes", "trickplay_infos", "user_data"}
	if err := s.deleteItemRows(ctx, tx, subtree, tables); err != nil {
		return err
	}
	if err := s.repairDerivedForTx(ctx, tx, nil, subtree); err != nil {
		return err
	}
	return tx.Commit()
}

// subtreeIDs returns id and its descendants from the closure rows.
func (s *ItemStore) subtreeIDs(ctx context.Context, tx *sql.Tx, id dto.UUID) ([]dto.UUID, error) {
	rows, err := tx.QueryContext(ctx, "SELECT item_id FROM ancestors WHERE ancestor_id = "+s.ph(1)+" UNION SELECT id FROM items WHERE id = "+s.ph(2), s.uuidArg(id), s.uuidArg(id))
	if err != nil {
		return nil, err
	}
	var ids []dto.UUID
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, closeRows(rows, err)
		}
		value, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		ids = append(ids, value)
	}
	return ids, closeRows(rows, rows.Err())
}

// deleteItemRows deletes the items in ids with the rows of tables that name
// them, their linked-children rows, and the item values that only they used.
func (s *ItemStore) deleteItemRows(ctx context.Context, tx *sql.Tx, ids []dto.UUID, tables []string) error {
	values, err := s.mappedValueIDs(ctx, tx, ids)
	if err != nil {
		return err
	}
	err = s.forUUIDChunks(ids, func(_ []dto.UUID, marks string, args []any) error {
		for _, table := range tables {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE item_id IN ("+marks+")", args...); err != nil {
				return err
			}
		}
		for _, column := range []string{"parent_item_id", "child_item_id"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM linked_children WHERE "+column+" IN ("+marks+")", args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.forUUIDChunks(ids, func(_ []dto.UUID, marks string, args []any) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM items WHERE id IN ("+marks+")", args...)
		return err
	}); err != nil {
		return err
	}
	return s.deleteOrphanValues(ctx, tx, values)
}

// mappedValueIDs returns the item_values IDs that ids map.
func (s *ItemStore) mappedValueIDs(ctx context.Context, tx *sql.Tx, ids []dto.UUID) ([]int64, error) {
	var values []int64
	err := s.forUUIDChunks(ids, func(_ []dto.UUID, marks string, args []any) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT value_id FROM item_value_map WHERE item_id IN ("+marks+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var value int64
			if err := rows.Scan(&value); err != nil {
				return closeRows(rows, err)
			}
			values = append(values, value)
		}
		return closeRows(rows, rows.Err())
	})
	return values, err
}

// deleteOrphanValues deletes the listed item_values rows that no item maps.
// A writer passes the values it unmapped, so the delete does not scan the
// whole table.
func (s *ItemStore) deleteOrphanValues(ctx context.Context, tx *sql.Tx, values []int64) error {
	for start := 0; start < len(values); start += derivedChunkSize {
		chunk := values[start:min(start+derivedChunkSize, len(values))]
		args := make([]any, len(chunk))
		for index, value := range chunk {
			args[index] = value
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM item_values WHERE id IN ("+s.placeholders(len(chunk))+") AND NOT EXISTS (SELECT 1 FROM item_value_map WHERE item_value_map.value_id = item_values.id)", args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *ItemStore) MoveSubtree(ctx context.Context, id dto.UUID, parentID *dto.UUID) (err error) {
	if parentID != nil && *parentID == id {
		return errors.New("move subtree: item cannot be its own parent")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedScope(ctx, tx, func(context.Context, *sql.Tx) (derivedLockScope, error) {
		scope := derivedLockScope{items: []dto.UUID{id}}
		if parentID != nil {
			scope.parents = []dto.UUID{*parentID}
		}
		return scope, nil
	}); err != nil {
		return err
	}
	if parentID != nil {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id = "+s.ph(1), s.uuidArg(*parentID)).Scan(&exists); err != nil {
			return fmt.Errorf("move subtree parent: %w", err)
		}
		var cycle int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM ancestors WHERE item_id = "+s.ph(1)+" AND ancestor_id = "+s.ph(2), s.uuidArg(*parentID), s.uuidArg(id)).Scan(&cycle)
		if err == nil {
			return errors.New("move subtree: cycle")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var arg any
	if parentID != nil {
		arg = s.uuidArg(*parentID)
	}
	result, err := tx.ExecContext(ctx, "UPDATE items SET parent_id = "+s.ph(1)+" WHERE id = "+s.ph(2), arg, s.uuidArg(id))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if err := s.repairDerivedForTx(ctx, tx, []dto.UUID{id}, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ItemStore) Ancestors(ctx context.Context, id dto.UUID) ([]dto.UUID, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT ancestor_id FROM ancestors WHERE item_id = "+s.ph(1)+" ORDER BY ancestor_id", s.uuidArg(id))
	if err != nil {
		return nil, err
	}
	var out []dto.UUID
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, closeRows(rows, err)
		}
		u, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, u)
	}
	return out, closeRows(rows, rows.Err())
}

// RepairDerived recomputes all hierarchy-derived columns and closure encodings.
func (s *ItemStore) RepairDerived(ctx context.Context) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedGlobal(ctx, tx); err != nil {
		return err
	}
	if err := s.repairDerivedTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ItemStore) Values(ctx context.Context, kind string) ([]string, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT value FROM item_values WHERE kind = "+s.ph(1)+" ORDER BY clean_value, value", kind)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, v)
	}
	return out, closeRows(rows, rows.Err())
}

func (s *ItemStore) LinkedChildren(ctx context.Context, id dto.UUID) ([]item.LinkedChildRef, error) {
	return s.linkedChildren(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) Images(ctx context.Context, id dto.UUID) ([]item.ImageInfo, error) {
	return s.images(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) People(ctx context.Context, id dto.UUID) ([]item.PersonCredit, error) {
	return s.people(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) Shares(ctx context.Context, id dto.UUID) ([]ItemShare, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT user_id, can_edit FROM item_shares WHERE item_id = "+s.ph(1)+" ORDER BY user_id", s.uuidArg(id))
	if err != nil {
		return nil, err
	}
	var out []ItemShare
	for rows.Next() {
		var raw any
		var can any
		if err := rows.Scan(&raw, &can); err != nil {
			return nil, closeRows(rows, err)
		}
		u, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, ItemShare{UserID: u, CanEdit: asBool(can)})
	}
	return out, closeRows(rows, rows.Err())
}

func (s *ItemStore) ReplaceShares(ctx context.Context, id dto.UUID, shares []ItemShare) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireKind(ctx, tx, id, item.KindPlaylist, "replace shares"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM item_shares WHERE item_id = "+s.ph(1), s.uuidArg(id)); err != nil {
		return err
	}
	for _, share := range shares {
		if share.UserID.IsNil() {
			return errors.New("replace shares: user id is required")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO item_shares(item_id,user_id,can_edit) VALUES ("+s.placeholders(3)+")", s.uuidArg(id), s.uuidArg(share.UserID), boolArg(s.stores.Dialect(), share.CanEdit)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ItemStore) BoxSetLibraryFolders(ctx context.Context, id dto.UUID) ([]dto.UUID, error) {
	rows, err := s.stores.Reader().QueryContext(ctx, "SELECT library_folder_id FROM boxset_library_folders WHERE item_id = "+s.ph(1)+" ORDER BY library_folder_id", s.uuidArg(id))
	if err != nil {
		return nil, err
	}
	var out []dto.UUID
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, closeRows(rows, err)
		}
		u, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, u)
	}
	return out, closeRows(rows, rows.Err())
}

func (s *ItemStore) ReplaceBoxSetLibraryFolders(ctx context.Context, id dto.UUID, folders []dto.UUID) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireKind(ctx, tx, id, item.KindBoxSet, "replace boxset library folders"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM boxset_library_folders WHERE item_id = "+s.ph(1), s.uuidArg(id)); err != nil {
		return err
	}
	for _, folder := range folders {
		if folder.IsNil() {
			return errors.New("replace boxset library folders: folder id is required")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO boxset_library_folders(item_id,library_folder_id) VALUES ("+s.placeholders(2)+")", s.uuidArg(id), s.uuidArg(folder)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MutateUserDataForItem applies one playback-state change on the persisted key
// selected for itemID. The callback runs while the selected row is locked, so
// concurrent mutations cannot lose changes made from the loaded value.
func (s *ItemStore) MutateUserDataForItem(ctx context.Context, userID, itemID dto.UUID, mutate func(*UserData) error) (UserData, error) {
	result, _, err := s.mutateUserDataForItem(ctx, userID, itemID, mutate, nil)
	return result, err
}

// MutateUserDataForItemGuarded applies a playback-state mutation only when
// guard accepts it immediately before the transaction commits. A rejected
// guard rolls the transaction back and reports committed false.
func (s *ItemStore) MutateUserDataForItemGuarded(ctx context.Context, userID, itemID dto.UUID, mutate func(*UserData) error, guard func() bool) (UserData, bool, error) {
	if guard == nil {
		return UserData{}, false, errors.New("mutate user data for item: commit guard is required")
	}
	return s.mutateUserDataForItem(ctx, userID, itemID, mutate, guard)
}

func (s *ItemStore) mutateUserDataForItem(ctx context.Context, userID, itemID dto.UUID, mutate func(*UserData) error, guard func() bool) (result UserData, committed bool, err error) {
	if userID.IsNil() || itemID.IsNil() {
		return UserData{}, false, errors.New("mutate user data for item: user and item are required")
	}
	if mutate == nil {
		return UserData{}, false, errors.New("mutate user data for item: callback is required")
	}

	tx, err := s.userDataMutationTx(ctx, userID)
	if err != nil {
		return UserData{}, false, err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockUserDataMutationItem(ctx, tx, itemID); err != nil {
		return UserData{}, false, err
	}

	data, err := s.userDataForItemMutation(ctx, tx, userID, itemID)
	if err != nil {
		return UserData{}, false, err
	}
	if err := mutate(&data); err != nil {
		return UserData{}, false, err
	}
	if data.UserID != userID || data.CustomKey == "" || data.ItemID != itemID {
		return UserData{}, false, errors.New("mutate user data for item: callback must not change user, custom key, or item")
	}
	if err := s.upsertUserData(ctx, tx, data); err != nil {
		return UserData{}, false, err
	}
	if guard != nil && !guard() {
		return data, false, nil
	}
	if err := tx.Commit(); err != nil {
		return UserData{}, false, err
	}
	return data, true, nil
}

// MutateUserDataForItemAndVersions applies a selected-item mutation and, when
// complete is true, marks every distinct user-data key associated with the
// supplied version graph played in the same transaction.
func (s *ItemStore) MutateUserDataForItemAndVersions(ctx context.Context, userID, itemID dto.UUID, versionIDs []dto.UUID, mutate func(*UserData) (complete bool, err error)) (UserData, error) {
	result, _, err := s.mutateUserDataForItemAndVersions(ctx, userID, itemID, versionIDs, mutate, nil)
	return result, err
}

// MutateUserDataForItemAndVersionsGuarded applies a selected-item mutation
// and its completion updates only when guard accepts it immediately before
// commit. A rejected guard rolls the whole transaction back.
func (s *ItemStore) MutateUserDataForItemAndVersionsGuarded(ctx context.Context, userID, itemID dto.UUID, versionIDs []dto.UUID, mutate func(*UserData) (complete bool, err error), guard func() bool) (UserData, bool, error) {
	if guard == nil {
		return UserData{}, false, errors.New("mutate user data for item and versions: commit guard is required")
	}
	return s.mutateUserDataForItemAndVersions(ctx, userID, itemID, versionIDs, mutate, guard)
}

func (s *ItemStore) mutateUserDataForItemAndVersions(ctx context.Context, userID, itemID dto.UUID, versionIDs []dto.UUID, mutate func(*UserData) (complete bool, err error), guard func() bool) (result UserData, committed bool, err error) {
	if userID.IsNil() || itemID.IsNil() {
		return UserData{}, false, errors.New("mutate user data for item and versions: user and item are required")
	}
	if mutate == nil {
		return UserData{}, false, errors.New("mutate user data for item and versions: callback is required")
	}

	tx, err := s.userDataMutationTx(ctx, userID)
	if err != nil {
		return UserData{}, false, err
	}
	defer rollbackOnReturn(tx, &err)

	ids := make([]dto.UUID, 0, 1+len(versionIDs))
	seen := make(map[dto.UUID]struct{}, 1+len(versionIDs))
	for _, id := range append([]dto.UUID{itemID}, versionIDs...) {
		if id.IsNil() {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if err := s.lockUserDataMutationItem(ctx, tx, id); err != nil {
			return UserData{}, false, err
		}
	}

	data, err := s.userDataForItemMutation(ctx, tx, userID, itemID)
	if err != nil {
		return UserData{}, false, err
	}
	complete, err := mutate(&data)
	if err != nil {
		return UserData{}, false, err
	}
	if data.UserID != userID || data.CustomKey == "" || data.ItemID != itemID {
		return UserData{}, false, errors.New("mutate user data for item and versions: callback must not change user, custom key, or item")
	}
	if err := s.upsertUserData(ctx, tx, data); err != nil {
		return UserData{}, false, err
	}
	if complete {
		if err := s.markUserDataVersionsPlayed(ctx, tx, userID, ids); err != nil {
			return UserData{}, false, err
		}
	}
	if guard != nil && !guard() {
		return data, false, nil
	}
	if err := tx.Commit(); err != nil {
		return UserData{}, false, err
	}
	return data, true, nil
}

func (s *ItemStore) markUserDataVersionsPlayed(ctx context.Context, tx *sql.Tx, userID dto.UUID, itemIDs []dto.UUID) error {
	if len(itemIDs) == 0 {
		return nil
	}

	marks := make([]string, len(itemIDs))
	args := make([]any, len(itemIDs))
	itemOrder := make(map[dto.UUID]int, len(itemIDs))
	for index, id := range itemIDs {
		marks[index] = s.ph(index + 1)
		args[index] = s.uuidArg(id)
		itemOrder[id] = index
	}
	rows, err := tx.QueryContext(ctx, "SELECT item_id,custom_key FROM item_user_data_keys WHERE item_id IN ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return err
	}
	keyItems := make(map[string]dto.UUID, len(itemIDs))
	for rows.Next() {
		var rawItemID any
		var key string
		if err := rows.Scan(&rawItemID, &key); err != nil {
			return closeRows(rows, err)
		}
		itemID, err := scanUUID(rawItemID)
		if err != nil {
			return closeRows(rows, err)
		}
		previous, exists := keyItems[key]
		if !exists || itemOrder[itemID] < itemOrder[previous] {
			keyItems[key] = itemID
		}
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return err
	}
	if len(keyItems) == 0 {
		return nil
	}

	values := make([]string, 0, len(keyItems))
	args = make([]any, 0, len(keyItems)*5)
	for key, itemID := range keyItems {
		placeholder := len(args) + 1
		values = append(values, "("+s.ph(placeholder)+", "+s.ph(placeholder+1)+", "+s.ph(placeholder+2)+", "+s.ph(placeholder+3)+", "+s.ph(placeholder+4)+")")
		args = append(args, s.uuidArg(userID), key, s.uuidArg(itemID), boolArg(s.stores.Dialect(), true), int64(0))
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO user_data(user_id,custom_key,item_id,played,position_ticks) VALUES "+strings.Join(values, ", ")+" ON CONFLICT(user_id,custom_key) DO UPDATE SET played=excluded.played, position_ticks=excluded.position_ticks", args...)
	return err
}

// userDataMutationTx serializes PostgreSQL mutations for one user. SQLite's
// one-connection writer already provides that serialization without a lock.
// PostgreSQL uses read committed so a statement after a waited advisory lock
// observes the winner's commit rather than a transaction-start snapshot.
func (s *ItemStore) userDataMutationTx(ctx context.Context, userID dto.UUID) (*sql.Tx, error) {
	postgres := s.ph(1) != "?"
	options := &sql.TxOptions{}
	if postgres {
		options.Isolation = sql.LevelReadCommitted
	}
	tx, err := s.stores.Writer().BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if !postgres {
		return tx, nil
	}
	first := int32(binary.BigEndian.Uint32(userID[:4]))
	second := int32(binary.BigEndian.Uint32(userID[4:8]))
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock("+s.ph(1)+", "+s.ph(2)+")", first, second); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("mutate user data for item: lock user: %w", err)
	}
	return tx, nil
}

// lockUserDataMutationItem keeps the materialized-key relation stable through
// the mutation and maps a missing target to the package's existing item error.
func (s *ItemStore) lockUserDataMutationItem(ctx context.Context, tx *sql.Tx, itemID dto.UUID) error {
	query := "SELECT 1 FROM items WHERE id = " + s.ph(1)
	if s.ph(1) != "?" {
		query += " FOR UPDATE"
	}
	var found int
	if err := tx.QueryRowContext(ctx, query, s.uuidArg(itemID)).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("mutate user data for item: item does not exist: %w", sql.ErrNoRows)
		}
		return fmt.Errorf("mutate user data for item: read item: %w", err)
	}
	return nil
}

// userDataForItemMutation writes to the first persisted canonical key. When
// newly added metadata introduces that key, it seeds the new canonical row from
// the first existing lower-priority canonical candidate without changing the
// legacy row.
func (s *ItemStore) userDataForItemMutation(ctx context.Context, tx *sql.Tx, userID, itemID dto.UUID) (data UserData, err error) {
	keys, err := s.canonicalUserDataKeysForItemMutation(ctx, tx, itemID)
	if err != nil {
		return UserData{}, err
	}
	data = UserData{UserID: userID, CustomKey: keys[0], ItemID: itemID}

	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	marks := make([]string, len(keys))
	args := make([]any, 1, len(keys)+1)
	args[0] = s.uuidArg(userID)
	for index, key := range keys {
		marks[index] = s.ph(index + 2)
		args = append(args, key)
	}
	rows, err := tx.QueryContext(ctx, "SELECT custom_key FROM user_data WHERE user_id = "+s.ph(1)+" AND custom_key IN ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return UserData{}, err
	}
	existing := make(map[string]struct{}, len(keys))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return UserData{}, closeRows(rows, err)
		}
		if _, ok := keySet[key]; ok {
			existing[key] = struct{}{}
		}
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return UserData{}, err
	}
	for _, key := range keys {
		if _, ok := existing[key]; !ok {
			continue
		}
		data, err = s.lockUserDataForItemMutation(ctx, tx, userID, key)
		if err != nil {
			return UserData{}, err
		}
		data.CustomKey = keys[0]
		data.ItemID = itemID
		return data, nil
	}
	return data, nil
}

// lockUserDataForItemMutation locks exactly the row supplying mutation state.
func (s *ItemStore) lockUserDataForItemMutation(ctx context.Context, tx *sql.Tx, userID dto.UUID, customKey string) (data UserData, err error) {
	query := "SELECT item_id,played,position_ticks,play_count,is_favorite,rating,likes,last_played,audio_stream_index,subtitle_stream_index FROM user_data WHERE user_id = " + s.ph(1) + " AND custom_key = " + s.ph(2)
	if s.ph(1) != "?" {
		query += " FOR UPDATE"
	}
	data.UserID, data.CustomKey = userID, customKey
	var itemRaw, played, rating, likes, last, audio, subtitle any
	if err := tx.QueryRowContext(ctx, query, s.uuidArg(userID), customKey).Scan(&itemRaw, &played, &data.PositionTicks, &data.PlayCount, &data.IsFavorite, &rating, &likes, &last, &audio, &subtitle); err != nil {
		return UserData{}, err
	}
	if data.ItemID, err = scanUUID(itemRaw); err != nil {
		return UserData{}, err
	}
	data.Played = asBool(played)
	data.Rating, data.Likes = optionalFloat32(rating), optionalInt(likes)
	data.LastPlayed, data.AudioStreamIndex, data.SubtitleStreamIndex = optionalTime(last, s.stores.Dialect()), optionalInt(audio), optionalInt(subtitle)
	return data, nil
}

// canonicalUserDataKeysForItemMutation computes canonical order from the
// persisted item graph, then filters it through materialized key rows. Keeping
// that order in Go avoids making SQL's lexical ordering part of the contract.
func (s *ItemStore) canonicalUserDataKeysForItemMutation(ctx context.Context, tx *sql.Tx, itemID dto.UUID) ([]string, error) {
	it, err := scanItem(tx.QueryRowContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id = "+s.ph(1), s.uuidArg(itemID)), s.stores.Dialect())
	if err != nil {
		return nil, err
	}
	if err := s.loadSatellites(ctx, tx, &it); err != nil {
		return nil, err
	}
	candidates, err := s.userDataKeyRowsForPage(ctx, tx, []item.Item{it})
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, "SELECT custom_key FROM item_user_data_keys WHERE item_id = "+s.ph(1), s.uuidArg(itemID))
	if err != nil {
		return nil, err
	}
	materialized := make(map[string]struct{}, len(candidates))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, closeRows(rows, err)
		}
		materialized[key] = struct{}{}
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := materialized[candidate.key]; ok {
			keys = append(keys, candidate.key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("mutate user data for item: item has no user data key")
	}
	return keys, nil
}

func (s *ItemStore) UpsertUserData(ctx context.Context, data UserData) error {
	if err := validateUserData(data); err != nil {
		return err
	}
	return s.upsertUserData(ctx, s.stores.Writer(), data)
}

type userDataExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *ItemStore) upsertUserData(ctx context.Context, db userDataExecer, data UserData) error {
	q := "INSERT INTO user_data(user_id,custom_key,item_id,played,position_ticks,play_count,is_favorite,rating,likes,last_played,audio_stream_index,subtitle_stream_index) VALUES (" + s.placeholders(12) + ") ON CONFLICT(user_id,custom_key) DO UPDATE SET item_id=excluded.item_id, played=excluded.played, position_ticks=excluded.position_ticks, play_count=excluded.play_count, is_favorite=excluded.is_favorite, rating=excluded.rating, likes=excluded.likes, last_played=excluded.last_played, audio_stream_index=excluded.audio_stream_index, subtitle_stream_index=excluded.subtitle_stream_index"
	_, err := db.ExecContext(ctx, q, s.uuidArg(data.UserID), data.CustomKey, s.uuidArg(data.ItemID), boolArg(s.stores.Dialect(), data.Played), data.PositionTicks, data.PlayCount, boolArg(s.stores.Dialect(), data.IsFavorite), data.Rating, data.Likes, timeArg(s.stores.Dialect(), data.LastPlayed), data.AudioStreamIndex, data.SubtitleStreamIndex)
	return err
}

func validateUserData(data UserData) error {
	if data.UserID.IsNil() || data.CustomKey == "" || data.ItemID.IsNil() {
		return errors.New("upsert user data: user, custom key, and item are required")
	}
	return nil
}

func (s *ItemStore) GetUserData(ctx context.Context, userID dto.UUID, customKey string) (UserData, error) {
	var d UserData
	d.UserID = userID
	d.CustomKey = customKey
	var itemRaw, played, rating, likes, last, audio, sub any
	err := s.stores.Reader().QueryRowContext(ctx, "SELECT item_id,played,position_ticks,play_count,is_favorite,rating,likes,last_played,audio_stream_index,subtitle_stream_index FROM user_data WHERE user_id = "+s.ph(1)+" AND custom_key = "+s.ph(2), s.uuidArg(userID), customKey).Scan(&itemRaw, &played, &d.PositionTicks, &d.PlayCount, &d.IsFavorite, &rating, &likes, &last, &audio, &sub)
	if err != nil {
		return UserData{}, err
	}
	d.ItemID, err = scanUUID(itemRaw)
	if err != nil {
		return UserData{}, err
	}
	d.Played = asBool(played)
	d.Rating = optionalFloat32(rating)
	d.Likes = optionalInt(likes)
	d.LastPlayed = optionalTime(last, s.stores.Dialect())
	d.AudioStreamIndex = optionalInt(audio)
	d.SubtitleStreamIndex = optionalInt(sub)
	return d, nil
}

func (s *ItemStore) Streams(ctx context.Context, id dto.UUID) ([]playback.MediaStream, error) {
	return s.streams(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) ReplaceStreams(ctx context.Context, id dto.UUID, streams []playback.MediaStream) error {
	return s.replaceStreams(ctx, id, streams)
}

func (s *ItemStore) Chapters(ctx context.Context, id dto.UUID) ([]Chapter, error) {
	return s.chapters(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) ReplaceChapters(ctx context.Context, id dto.UUID, chapters []Chapter) error {
	return s.replaceChapters(ctx, id, chapters)
}

func (s *ItemStore) Attachments(ctx context.Context, id dto.UUID) ([]playback.MediaAttachment, error) {
	return s.attachments(ctx, s.stores.Reader(), id)
}

func (s *ItemStore) ReplaceAttachments(ctx context.Context, id dto.UUID, attachments []playback.MediaAttachment) error {
	return s.replaceAttachments(ctx, id, attachments)
}

func (s *ItemStore) ph(n int) string { return s.stores.Dialect().Placeholder(n) }
func (s *ItemStore) placeholders(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = s.ph(i + 1)
	}
	return strings.Join(p, ", ")
}
func (s *ItemStore) uuidArg(id dto.UUID) any { return uuidArgDialect(s.stores.Dialect(), id) }
func rollbackOnReturn(tx *sql.Tx, result *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*result = errors.Join(*result, err)
	}
}

func closeRows(rows *sql.Rows, primary error) error {
	return errors.Join(primary, rows.Close())
}

func uuidArgDialect(d Dialect, id dto.UUID) any {
	if d.Placeholder(1) == "?" {
		return id[:]
	}
	return id.Dashed()
}

func boolArg(d Dialect, value bool) any {
	if d.Boolean(value) == "1" {
		if value {
			return 1
		}
		return 0
	}
	return value
}

func timeArg(d Dialect, value *time.Time) any {
	if value == nil {
		return nil
	}
	return d.TimeValue(*value)
}
func enum(v any) string { return strconv.FormatUint(uint64(reflectEnum(v)), 10) }
func reflectEnum(v any) uint8 {
	switch x := v.(type) {
	case item.UnratedItem:
		return uint8(x)
	case item.SourceType:
		return uint8(x)
	case item.MediaType:
		return uint8(x)
	case item.ExtraType:
		return uint8(x)
	case item.VideoType:
		return uint8(x)
	case item.IsoType:
		return uint8(x)
	case item.Video3DFormat:
		return uint8(x)
	case item.SeriesStatus:
		return uint8(x)
	case item.ProgramAudio:
		return uint8(x)
	default:
		panic("unsupported item enum")
	}
}
