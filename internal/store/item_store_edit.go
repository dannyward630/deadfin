// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// ErrEditItemNotFound reports that an edited item has no stored row.
var ErrEditItemNotFound = errors.New("edit item: item not found")

// EditItems persists a client metadata edit (ItemUpdateController.UpdateItem
// and the image write paths). Each edited item replaces its stored row and
// satellites wholesale: the caller loaded the item, applied the edit, and
// hands back the complete result. Entities are Person, Genre, and Studio
// items the edit references; the write inserts an absent entity and leaves a
// stored entity unchanged.
//
// The write runs in one transaction. An edit changes no hierarchy, so the
// write rejects a kind, parent, or path change and keeps the stored derived
// hierarchy columns. An edit can change a name or provider IDs, so the write
// repairs the derived state of the written items and their descendants: an
// edited Series changes the user-data keys of its episodes.
func (s *ItemStore) EditItems(ctx context.Context, edited []item.Item, entities []item.Item) (err error) {
	if len(edited) == 0 && len(entities) == 0 {
		return nil
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := s.lockDerivedWrite(ctx, tx, append(append([]item.Item(nil), edited...), entities...)...); err != nil {
		return err
	}
	var written []dto.UUID

	storedEntities, err := s.reconcileExistingVirtuals(ctx, tx, entities)
	if err != nil {
		return err
	}
	for _, entity := range entities {
		if _, found := storedEntities[entity.ID]; found {
			continue
		}
		written = append(written, entity.ID)
		if err := s.upsertItem(ctx, tx, entity); err != nil {
			return fmt.Errorf("edit item entity %s: %w", entity.ID, err)
		}
		if err := s.replaceSatellites(ctx, tx, entity); err != nil {
			return fmt.Errorf("edit item entity %s: %w", entity.ID, err)
		}
	}

	stored, err := s.reconcileExistingVirtuals(ctx, tx, edited)
	if err != nil {
		return err
	}
	for _, value := range edited {
		previous, found := stored[value.ID]
		if !found {
			return fmt.Errorf("%w: %s", ErrEditItemNotFound, value.ID)
		}
		if previous.Kind != value.Kind || !equalUUIDPtrFields(previous.ParentID, value.ParentID) || !equalStringPtrFields(previous.Path, value.Path) {
			return fmt.Errorf("edit item %s: kind, parent, and path are not editable", value.ID)
		}
		keepDerivedHierarchy(&value, previous)
		if err := s.validateUpsert(ctx, tx, value); err != nil {
			return fmt.Errorf("edit item %s: %w", value.ID, err)
		}
		written = append(written, value.ID)
		if err := s.upsertItem(ctx, tx, value); err != nil {
			return fmt.Errorf("edit item %s: %w", value.ID, err)
		}
		if err := s.replaceSatellites(ctx, tx, value); err != nil {
			return fmt.Errorf("edit item %s: %w", value.ID, err)
		}
	}
	if err := s.repairDerivedForTx(ctx, tx, written, nil); err != nil {
		return err
	}
	return tx.Commit()
}
