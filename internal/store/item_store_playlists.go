// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// ErrNotPlaylist reports that a playlist operation addressed an item that does
// not exist or is not a Playlist.
var ErrNotPlaylist = fmt.Errorf("%w: playlist not found", sql.ErrNoRows)

// PlaylistState is the mutable part of a Playlist: the facts PlaylistManager
// changes through UpdateToRepositoryAsync. Children is the ordered Manual
// linked-children list; an entry's position is its sort order.
type PlaylistState struct {
	ID                 dto.UUID
	Name               string
	SortName           *string
	OwnerUserID        dto.UUID
	OpenAccess         bool
	Shares             []ItemShare
	Children           []dto.UUID
	DateLastMediaAdded *time.Time
}

// CreatePlaylist writes a new Playlist item, its shares, and its ordered
// children in one transaction. The item must carry Kind Playlist and an owner.
func (s *ItemStore) CreatePlaylist(ctx context.Context, playlist item.Item, shares []ItemShare) (err error) {
	if playlist.Kind != item.KindPlaylist {
		return errors.New("create playlist: kind must be Playlist")
	}
	if playlist.ID.IsNil() {
		return errors.New("create playlist: id is required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedWrite(ctx, tx, playlist); err != nil {
		return err
	}
	if err := s.validateUpsert(ctx, tx, playlist); err != nil {
		return err
	}
	if err := s.upsertItem(ctx, tx, playlist); err != nil {
		return err
	}
	if err := s.replaceSatellites(ctx, tx, playlist); err != nil {
		return err
	}
	if err := s.writePlaylistShares(ctx, tx, playlist.ID, shares); err != nil {
		return err
	}
	if err := s.repairDerivedForTx(ctx, tx, []dto.UUID{playlist.ID}, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// Playlist reads the mutable state of the Playlist id. It returns
// ErrNotPlaylist when id is absent or another kind.
func (s *ItemStore) Playlist(ctx context.Context, id dto.UUID) (result PlaylistState, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PlaylistState{}, err
	}
	defer rollbackOnReturn(tx, &err)
	result, err = s.playlistState(ctx, tx, id, false)
	if err != nil {
		return PlaylistState{}, err
	}
	return result, tx.Commit()
}

// MutatePlaylist applies mutate to the current state of the Playlist id and
// writes the result in the same transaction, so concurrent edits of one
// playlist serialize instead of losing entries. The callback must not change
// ID. It returns the written state.
func (s *ItemStore) MutatePlaylist(ctx context.Context, id dto.UUID, mutate func(*PlaylistState) error) (result PlaylistState, err error) {
	if mutate == nil {
		return PlaylistState{}, errors.New("mutate playlist: callback is required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return PlaylistState{}, err
	}
	defer rollbackOnReturn(tx, &err)
	state, err := s.playlistState(ctx, tx, id, true)
	if err != nil {
		return PlaylistState{}, err
	}
	if err := mutate(&state); err != nil {
		return PlaylistState{}, err
	}
	if state.ID != id {
		return PlaylistState{}, errors.New("mutate playlist: callback must not change the id")
	}
	if state.OwnerUserID.IsNil() {
		return PlaylistState{}, errors.New("mutate playlist: owner is required")
	}
	clean := item.CleanName(state.Name)
	d := s.stores.Dialect()
	if _, err := tx.ExecContext(ctx, "UPDATE items SET name = "+s.ph(1)+", sort_name = "+s.ph(2)+", clean_name = "+s.ph(3)+
		", playlist_owner_user_id = "+s.ph(4)+", open_access = "+s.ph(5)+", date_last_media_added = "+s.ph(6)+" WHERE id = "+s.ph(7),
		state.Name, state.SortName, clean, s.uuidArg(state.OwnerUserID), boolArg(d, state.OpenAccess), timeArg(d, state.DateLastMediaAdded), s.uuidArg(id),
	); err != nil {
		return PlaylistState{}, fmt.Errorf("mutate playlist: update item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM item_shares WHERE item_id = "+s.ph(1), s.uuidArg(id)); err != nil {
		return PlaylistState{}, fmt.Errorf("mutate playlist: delete shares: %w", err)
	}
	if err := s.writePlaylistShares(ctx, tx, id, state.Shares); err != nil {
		return PlaylistState{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM linked_children WHERE parent_item_id = "+s.ph(1), s.uuidArg(id)); err != nil {
		return PlaylistState{}, fmt.Errorf("mutate playlist: delete children: %w", err)
	}
	seen := make(map[dto.UUID]bool, len(state.Children))
	order := 0
	for _, child := range state.Children {
		if child.IsNil() || seen[child] {
			continue
		}
		seen[child] = true
		if _, err := tx.ExecContext(ctx, "INSERT INTO linked_children(parent_item_id,child_item_id,type,sort_order) VALUES ("+s.placeholders(4)+")",
			s.uuidArg(id), s.uuidArg(child), item.LinkedChildTypeManual.String(), order); err != nil {
			return PlaylistState{}, fmt.Errorf("mutate playlist: insert child: %w", err)
		}
		order++
	}
	if err := tx.Commit(); err != nil {
		return PlaylistState{}, err
	}
	return s.Playlist(ctx, id)
}

// playlistState loads one playlist inside tx. forUpdate locks the item row on
// PostgreSQL so read-modify-write callers serialize.
func (s *ItemStore) playlistState(ctx context.Context, tx *sql.Tx, id dto.UUID, forUpdate bool) (PlaylistState, error) {
	query := "SELECT kind, name, sort_name, playlist_owner_user_id, open_access, date_last_media_added FROM items WHERE id = " + s.ph(1)
	if forUpdate && s.ph(1) != "?" {
		query += " FOR UPDATE"
	}
	var kind string
	var name, sortName sql.NullString
	var owner, openAccess, lastAdded any
	if err := tx.QueryRowContext(ctx, query, s.uuidArg(id)).Scan(&kind, &name, &sortName, &owner, &openAccess, &lastAdded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlaylistState{}, ErrNotPlaylist
		}
		return PlaylistState{}, fmt.Errorf("read playlist: %w", err)
	}
	if kind != item.KindPlaylist.String() {
		return PlaylistState{}, ErrNotPlaylist
	}
	state := PlaylistState{ID: id, Name: name.String, OpenAccess: asBool(openAccess)}
	if sortName.Valid {
		value := sortName.String
		state.SortName = &value
	}
	if !isNil(owner) {
		ownerID, err := scanUUID(owner)
		if err != nil {
			return PlaylistState{}, err
		}
		state.OwnerUserID = ownerID
	}
	state.DateLastMediaAdded = optionalTime(lastAdded, s.stores.Dialect())
	shares, err := s.playlistShares(ctx, tx, id)
	if err != nil {
		return PlaylistState{}, err
	}
	state.Shares = shares
	children, err := s.linkedChildren(ctx, tx, id)
	if err != nil {
		return PlaylistState{}, err
	}
	for _, child := range children {
		if child.Type == item.LinkedChildTypeManual {
			state.Children = append(state.Children, child.ItemID)
		}
	}
	return state, nil
}

func (s *ItemStore) playlistShares(ctx context.Context, db queryer, id dto.UUID) ([]ItemShare, error) {
	rows, err := db.QueryContext(ctx, "SELECT user_id, can_edit FROM item_shares WHERE item_id = "+s.ph(1)+" ORDER BY user_id", s.uuidArg(id))
	if err != nil {
		return nil, err
	}
	var out []ItemShare
	for rows.Next() {
		var raw, can any
		if err := rows.Scan(&raw, &can); err != nil {
			return nil, closeRows(rows, err)
		}
		userID, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, ItemShare{UserID: userID, CanEdit: asBool(can)})
	}
	return out, closeRows(rows, rows.Err())
}

// writePlaylistShares inserts shares. A user named twice keeps the last
// grant, matching AddUserToShares, which replaces an existing user entry.
func (s *ItemStore) writePlaylistShares(ctx context.Context, tx *sql.Tx, id dto.UUID, shares []ItemShare) error {
	last := make(map[dto.UUID]bool, len(shares))
	for _, share := range shares {
		if share.UserID.IsNil() {
			return errors.New("playlist shares: user id is required")
		}
		last[share.UserID] = share.CanEdit
	}
	written := make(map[dto.UUID]bool, len(shares))
	for _, share := range shares {
		if written[share.UserID] {
			continue
		}
		written[share.UserID] = true
		if _, err := tx.ExecContext(ctx, "INSERT INTO item_shares(item_id,user_id,can_edit) VALUES ("+s.placeholders(3)+")",
			s.uuidArg(id), s.uuidArg(share.UserID), boolArg(s.stores.Dialect(), last[share.UserID])); err != nil {
			return fmt.Errorf("playlist shares: insert: %w", err)
		}
	}
	return nil
}
