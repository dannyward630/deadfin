// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// ErrInvalidReference reports a request that names an item that does not
// exist or has the wrong kind. Upstream raises ArgumentException for it.
var ErrInvalidReference = errors.New("invalid item reference")

type invalidReference string

func (e invalidReference) Error() string { return string(e) }
func (e invalidReference) Unwrap() error { return ErrInvalidReference }

func (s *ItemStore) validateUpsert(ctx context.Context, tx *sql.Tx, it item.Item) error {
	if it.Kind == item.KindPlaylist && it.PlaylistOwnerUserID.IsNil() {
		return errors.New("upsert item: Playlist requires playlist owner")
	}
	if it.ParentID == nil {
		return nil
	}
	if *it.ParentID == it.ID {
		return errors.New("upsert item: item cannot be its own parent")
	}
	// The proposed parent must not already be in the item's subtree. The closure
	// is current before this write because every hierarchy write repairs it.
	var found int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM ancestors WHERE item_id = "+s.ph(1)+" AND ancestor_id = "+s.ph(2), s.uuidArg(*it.ParentID), s.uuidArg(it.ID)).Scan(&found)
	if err == nil {
		return errors.New("upsert item: parent is a descendant")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("upsert item parent cycle check: %w", err)
	}
	return nil
}

func (s *ItemStore) requireKind(ctx context.Context, tx *sql.Tx, id dto.UUID, want item.Kind, operation string) error {
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT kind FROM items WHERE id = "+s.ph(1), s.uuidArg(id)).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %w", operation, invalidReference("item does not exist"))
		}
		return fmt.Errorf("%s: read item kind: %w", operation, err)
	}
	if kind != want.String() {
		return fmt.Errorf("%s: %w", operation, invalidReference("item must be "+want.String()))
	}
	return nil
}

func (s *ItemStore) requireItem(ctx context.Context, tx *sql.Tx, id dto.UUID, operation string) error {
	var found int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id = "+s.ph(1), s.uuidArg(id)).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %w", operation, invalidReference("item does not exist"))
		}
		return fmt.Errorf("%s: read item: %w", operation, err)
	}
	return nil
}
