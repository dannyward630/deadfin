// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

const addCollectionOperation = "add to collection"

// AddToCollection links each item in itemIDs to collectionID as a Manual
// child of the BoxSet. The write runs in one transaction so that a failure
// rolls back every insert. The ON CONFLICT clause makes a repeated request
// a no-op: an already-linked child does not add a second relation.
func (s *ItemStore) AddToCollection(ctx context.Context, collectionID dto.UUID, itemIDs []dto.UUID) (err error) {
	if collectionID.IsNil() {
		return errors.New(addCollectionOperation + ": collection id is required")
	}
	if len(itemIDs) == 0 {
		return errors.New(addCollectionOperation + ": at least one item id is required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireKind(ctx, tx, collectionID, item.KindBoxSet, addCollectionOperation); err != nil {
		return err
	}
	for _, childID := range itemIDs {
		if err := s.requireItem(ctx, tx, childID, addCollectionOperation); err != nil {
			return err
		}
	}
	for _, childID := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO linked_children(parent_item_id, child_item_id, type, sort_order) VALUES ("+
				s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+") "+
				"ON CONFLICT(parent_item_id, child_item_id) DO NOTHING",
			s.uuidArg(collectionID), s.uuidArg(childID), item.LinkedChildTypeManual.String(), nil,
		); err != nil {
			return fmt.Errorf("%s: insert linked child: %w", addCollectionOperation, err)
		}
	}
	return tx.Commit()
}

const removeCollectionOperation = "remove from collection"

// RemoveFromCollection ports CollectionManager.RemoveFromCollectionAsync: it
// unlinks each item in itemIDs from the BoxSet. An id that is not a member
// is skipped, as upstream logs and skips it.
func (s *ItemStore) RemoveFromCollection(ctx context.Context, collectionID dto.UUID, itemIDs []dto.UUID) (err error) {
	if collectionID.IsNil() {
		return errors.New(removeCollectionOperation + ": collection id is required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.requireKind(ctx, tx, collectionID, item.KindBoxSet, removeCollectionOperation); err != nil {
		return err
	}
	for _, childID := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM linked_children WHERE parent_item_id = "+s.ph(1)+" AND child_item_id = "+s.ph(2),
			s.uuidArg(collectionID), s.uuidArg(childID),
		); err != nil {
			return fmt.Errorf("%s: delete linked child: %w", removeCollectionOperation, err)
		}
	}
	return tx.Commit()
}
