// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/native"
)

// Queryer is the exported alias for the queryer interface. It is satisfied by
// both *sql.DB and *sql.Tx. The backfill package uses it to define the store
// adapter interfaces without referencing the unexported type.
type Queryer = queryer

// BackfillTx is the read-snapshot transaction used by the native backfill.
// It keeps database/sql transaction types inside the store package.
type BackfillTx interface {
	Queryer
	Commit() error
	Rollback() error
}

// BackfillUnit is one compatibility item resolved into the native aggregates
// the backfill derives from it. One item maps to exactly one unit. A unit that
// carries a Release also carries the Asset and Inventory that belong to that
// Release.
type BackfillUnit struct {
	// Item is the compatibility item the unit derives from.
	Item item.Item

	// Target is the finest-grained native aggregate the projection row names
	// for the item: Asset for file-bearing items (Movie, Episode, Audio), Work
	// for non-file aggregate items (Series, MusicAlbum, BoxSet).
	Target BackfillTarget

	// WorkID is the resolved or created native Work UUID for the item. For a
	// file-bearing item, the Work is reached through the Asset chain.
	WorkID dto.UUID

	// Identity is the resolved or imported identity mapping set for the Work.
	// First-import wins: when a mapping exists for the catalog and external ID,
	// the unit reuses the existing Work UUID. LastVerified is the backfill run
	// start time.
	Identity []native.IdentityMapping

	// WorkOwnedByParent is true for Episode and Audio items that attach to a
	// parent Work (Series or MusicAlbum). The backfill does not update the
	// Work title or state for child items; the parent owns the Work.
	WorkOwnedByParent bool

	// SourceRevision is the captured compatibility source revision for a
	// file-bearing item with a complete inode and change-time identity. The
	// writer transaction verifies it before it writes native rows. An empty
	// value marks a non-file unit.
	SourceRevision string
}

// BackfillTarget names the finest-grained native aggregate a projection row
// points at for one item.
type BackfillTarget string

const (
	// BackfillTargetAsset names an Asset projection. Movie, Episode, and Audio
	// items produce an Asset projection.
	BackfillTargetAsset BackfillTarget = "asset"
	// BackfillTargetWork names a Work projection. Series, MusicAlbum, and
	// BoxSet items produce a Work projection.
	BackfillTargetWork BackfillTarget = "work"
)

// BackfillKindGroup classifies a compatibility item into one native kind
// group for the identity-merge and collision rules.
type BackfillKindGroup string

const (
	// BackfillGroupMovie is the movie kind group.
	BackfillGroupMovie BackfillKindGroup = "movie"
	// BackfillGroupSeries is the series kind group (Series and Episode).
	BackfillGroupSeries BackfillKindGroup = "series"
	// BackfillGroupMusic is the music kind group (MusicAlbum and Audio).
	BackfillGroupMusic BackfillKindGroup = "music"
	// BackfillGroupCollection is the collection kind group (BoxSet).
	BackfillGroupCollection BackfillKindGroup = "collection"
)

// BackfillUnitResult records the write outcome for one unit.
type BackfillUnitResult struct {
	Created bool
	Updated bool
	// IdentityMerge reports that the unit reused an existing Work through a
	// same-kind-group identity collision.
	IdentityMerge bool
	// IdentityCollisionSkipped reports that the unit detected a cross-kind-
	// group identity collision and created a fresh Work instead of reusing
	// the existing one.
	IdentityCollisionSkipped bool
	// RegroupSkipped reports that a child item changed parent across Works.
	// The Asset facts update in place, but the native chain is not
	// re-parented.
	RegroupSkipped bool
}

// BackfillKindGroupForItem returns the native kind group for one item, or the
// zero value and false when the item kind is not mapped to a native aggregate.
func BackfillKindGroupForItem(it item.Item) (BackfillKindGroup, bool) {
	switch it.Kind {
	case item.KindMovie:
		return BackfillGroupMovie, true
	case item.KindSeries, item.KindEpisode:
		return BackfillGroupSeries, true
	case item.KindMusicAlbum, item.KindAudio:
		return BackfillGroupMusic, true
	case item.KindBoxSet:
		return BackfillGroupCollection, true
	default:
		return "", false
	}
}

// compareUUID compares two UUIDs by their canonical 16-byte representation in
// unsigned byte order. It returns -1, 0, or +1 when a is less than, equal to,
// or greater than b. The repository UUID wire type is plain big-endian text
// order, so a byte compare is the canonical ordering.
func compareUUID(a, b dto.UUID) int {
	for i := 0; i < 16; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// BackfillEnumerateItems is a read-only, keyset-paginated enumeration of
// compatibility items ordered by ID. It returns the next page of items with
// their ProviderIDs satellite loaded. afterID is the exclusive lower bound
// (use dto.NilUUID for the first page). limit caps the page size. The
// enumeration reads from the reader pool and loads satellites in a repeatable
// read snapshot so the caller gets a consistent item plus its provider IDs.
//
// This is read access only. It edits no existing compatibility store file.
func (s *ItemStore) BackfillEnumerateItems(ctx context.Context, afterID dto.UUID, limit int) ([]item.Item, error) {
	if limit <= 0 {
		limit = 256
	}
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE id > "+s.ph(1)+" ORDER BY id "+s.stores.Dialect().NullsLast()+" LIMIT "+s.ph(2),
		s.uuidArg(afterID), limit)
	if err != nil {
		return nil, err
	}
	page := make([]item.Item, 0, limit)
	for rows.Next() {
		it, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		page = append(page, it)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	for i := range page {
		if err := s.loadSatellites(ctx, tx, &page[i]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return page, nil
}

// BackfillProjectionsForItem enumerates every projection row for one
// compatibility item_id, ordered by native UUID bytes, inside the supplied
// read snapshot. It returns the native UUIDs that point at the item. The
// backfill applies the target-class filter and the smallest-native-UUID rule
// to the result. The method never deletes or repoints a row.
func (s *NativeStore) BackfillProjectionsForItem(ctx context.Context, db queryer, itemID dto.UUID) ([]dto.UUID, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT native_id FROM native_item_projections WHERE item_id = "+s.ph(1)+" ORDER BY native_id "+s.stores.Dialect().NullsLast(),
		s.uuidArg(itemID))
	if err != nil {
		return nil, err
	}
	var ids []dto.UUID
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		ids = append(ids, id)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return ids, nil
}

// BackfillReaderTx starts a read-only, repeatable-read transaction for the
// backfill projection enumeration.
func (s *NativeStore) BackfillReaderTx(ctx context.Context) (BackfillTx, error) {
	return s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

// BackfillFreshUUIDForPass generates a fresh server-assigned UUID for a new
// native aggregate during a backfill pass.
func (s *NativeStore) BackfillFreshUUIDForPass() dto.UUID { return backfillFreshUUID() }

// BackfillFilterProjectionsByTargetClass filters a set of projection native
// IDs to the expected target class: file-bearing items use Asset projections,
// non-file aggregate items use Work projections. It returns the filtered IDs
// and whether more than one candidate remained after filtering (the multiple-
// projection anomaly).
func (s *NativeStore) BackfillFilterProjectionsByTargetClass(ctx context.Context, db queryer, ids []dto.UUID, target BackfillTarget) ([]dto.UUID, bool, error) {
	if len(ids) <= 1 {
		return ids, false, nil
	}
	var filtered []dto.UUID
	for _, id := range ids {
		matches, err := s.projectionMatchesTargetClass(ctx, db, id, target)
		if err != nil {
			return nil, false, err
		}
		if matches {
			filtered = append(filtered, id)
		}
	}
	return filtered, len(filtered) > 1, nil
}

// projectionMatchesTargetClass reports whether a native UUID is the expected
// target class (Asset or Work) for the projection row.
func (s *NativeStore) projectionMatchesTargetClass(ctx context.Context, db queryer, nativeID dto.UUID, target BackfillTarget) (bool, error) {
	switch target {
	case BackfillTargetAsset:
		var dummy string
		err := db.QueryRowContext(ctx,
			"SELECT 1 FROM native_assets WHERE id = "+s.ph(1),
			s.uuidArg(nativeID)).Scan(&dummy)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	case BackfillTargetWork:
		var dummy string
		err := db.QueryRowContext(ctx,
			"SELECT 1 FROM native_works WHERE id = "+s.ph(1),
			s.uuidArg(nativeID)).Scan(&dummy)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	default:
		return false, nil
	}
}

// BackfillSelectedProjection returns the lexicographically smallest native UUID
// from a set of projection rows for one item. When the set is empty, it
// returns the nil UUID. The caller applies the target-class filter before it
// calls this method, so the result is the deterministic selection for the
// target class.
func BackfillSelectedProjection(ids []dto.UUID) dto.UUID {
	if len(ids) == 0 {
		return dto.NilUUID
	}
	best := ids[0]
	for _, id := range ids[1:] {
		if compareUUID(id, best) < 0 {
			best = id
		}
	}
	return best
}

// BackfillResolveWorkForItem reads the Work UUID for one parent item through
// its projection. It resolves the parent's projection rows, filters to the
// Work target class, and returns the smallest Work UUID. When no projection
// exists, it returns the nil UUID. The caller uses this to attach Episode and
// Audio items to the parent's existing Work.
func (s *NativeStore) BackfillResolveWorkForItem(ctx context.Context, db queryer, parentItemID dto.UUID) (dto.UUID, error) {
	ids, err := s.BackfillProjectionsForItem(ctx, db, parentItemID)
	if err != nil {
		return dto.UUID{}, err
	}
	if len(ids) == 0 {
		return dto.NilUUID, nil
	}
	// Filter to Work projections: a non-file aggregate parent (Series or
	// MusicAlbum) produces a Work projection.
	var workIDs []dto.UUID
	for _, id := range ids {
		var dummy string
		err := db.QueryRowContext(ctx,
			"SELECT 1 FROM native_works WHERE id = "+s.ph(1),
			s.uuidArg(id)).Scan(&dummy)
		if err == nil {
			workIDs = append(workIDs, id)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return dto.UUID{}, err
		}
	}
	return BackfillSelectedProjection(workIDs), nil
}

// BackfillWorkKindGroup reads the native kind group of the Work that a native
// UUID projects from. It resolves the native_id through the projection table
// to the compatibility item, then maps the item kind to the native kind group.
// When the native UUID has no projection row, or the projected item kind is not
// mapped, it returns the zero group and false.
//
// When nativeID is a Work UUID whose projection points at an Asset (a Movie),
// the method resolves the kind group through the Work's Edition, Release, and
// Asset chain to the projected item.
func (s *NativeStore) BackfillWorkKindGroup(ctx context.Context, db queryer, nativeID dto.UUID) (BackfillKindGroup, bool, error) {
	// First try a direct projection for the native UUID. This works for
	// Work projections (Series, MusicAlbum, BoxSet) and Asset projections.
	itemID, ok, err := s.backfillItemIDForNative(ctx, db, nativeID)
	if err != nil {
		return "", false, err
	}
	if !ok {
		// No direct projection. When nativeID is a Work UUID, try to
		// resolve through the Work's edition, release, and asset chain
		// to the projected item.
		itemID, ok, err = s.backfillItemIDForWork(ctx, db, nativeID)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, nil
		}
	}
	kindRow := db.QueryRowContext(ctx,
		"SELECT kind FROM items WHERE id = "+s.ph(1),
		s.uuidArg(itemID))
	var rawKind any
	if err := kindRow.Scan(&rawKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	kind, err := item.ParseKind(asString(rawKind))
	if err != nil {
		return "", false, err
	}
	group, gOk := BackfillKindGroupForItem(item.Item{Kind: kind})
	return group, gOk, nil
}

// backfillItemIDForNative reads the item_id for a native UUID from the
// projection table. It returns the item ID and true when a projection exists.
func (s *NativeStore) backfillItemIDForNative(ctx context.Context, db queryer, nativeID dto.UUID) (dto.UUID, bool, error) {
	row := db.QueryRowContext(ctx,
		"SELECT item_id FROM native_item_projections WHERE native_id = "+s.ph(1),
		s.uuidArg(nativeID))
	var rawItem any
	if err := row.Scan(&rawItem); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dto.NilUUID, false, nil
		}
		return dto.NilUUID, false, err
	}
	itemID, err := scanUUID(rawItem)
	if err != nil {
		return dto.NilUUID, false, err
	}
	return itemID, true, nil
}

// backfillItemIDForWork resolves the item ID for a Work UUID by tracing
// through the Work's default Edition, its first Release, and the first Asset's
// projection. This handles the Movie case where the projection points at the
// Asset, not the Work.
func (s *NativeStore) backfillItemIDForWork(ctx context.Context, db queryer, workID dto.UUID) (dto.UUID, bool, error) {
	// Find the default Edition for the Work.
	edition, err := s.BackfillDefaultEdition(ctx, db, workID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dto.NilUUID, false, nil
		}
		return dto.NilUUID, false, err
	}
	// Find the first Release for the Edition.
	release, err := s.BackfillReleaseForEdition(ctx, db, edition.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dto.NilUUID, false, nil
		}
		return dto.NilUUID, false, err
	}
	// Find the first Asset for the Release.
	asset, err := s.backfillAssetForRelease(ctx, db, release.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dto.NilUUID, false, nil
		}
		return dto.NilUUID, false, err
	}
	// Find the projection for the Asset.
	return s.backfillItemIDForNative(ctx, db, asset.ID)
}

// backfillAssetForRelease reads the first Asset for a Release, ordered by ID.
func (s *NativeStore) backfillAssetForRelease(ctx context.Context, db queryer, releaseID dto.UUID) (native.Asset, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, release_id, path, device, inode, source_revision, date_created, date_modified FROM native_assets WHERE release_id = "+s.ph(1)+" ORDER BY id LIMIT 1",
		s.uuidArg(releaseID))
	return scanAsset(row, s.stores.Dialect())
}

// BackfillAssetFacts reads the stored Asset for one native UUID. It returns
// sql.ErrNoRows when the native UUID is not an Asset. The caller compares these
// facts with the current item facts to decide whether a unit is unchanged.
func (s *NativeStore) BackfillAssetFacts(ctx context.Context, db queryer, nativeID dto.UUID) (native.Asset, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, release_id, path, device, inode, source_revision, date_created, date_modified FROM native_assets WHERE id = "+s.ph(1),
		s.uuidArg(nativeID))
	return scanAsset(row, s.stores.Dialect())
}

// BackfillWorkFacts reads the stored Work for one native UUID. It returns
// sql.ErrNoRows when the native UUID is not a Work. The caller compares these
// facts with the derived values to decide whether a Work unit is unchanged.
func (s *NativeStore) BackfillWorkFacts(ctx context.Context, db queryer, nativeID dto.UUID) (native.Work, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, state, title, date_created, date_modified FROM native_works WHERE id = "+s.ph(1),
		s.uuidArg(nativeID))
	w, err := scanWork(row, s.stores.Dialect())
	if err != nil {
		return native.Work{}, err
	}
	w.ProviderIDs, err = s.identityMappingsTx(ctx, db, w.ID)
	if err != nil {
		return native.Work{}, err
	}
	return w, nil
}

// BackfillDefaultEdition reads the default Edition (the unique Edition with an
// empty title) for one Work. It returns sql.ErrNoRows when no default Edition
// exists.
func (s *NativeStore) BackfillDefaultEdition(ctx context.Context, db queryer, workID dto.UUID) (native.Edition, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, work_id, title, date_created, date_modified FROM native_editions WHERE work_id = "+s.ph(1)+" AND title = '' ORDER BY id "+s.stores.Dialect().NullsLast()+" LIMIT 1",
		s.uuidArg(workID))
	e, err := scanEdition(row, s.stores.Dialect())
	if err != nil {
		return native.Edition{}, err
	}
	e.ProviderIDs, err = s.identityMappingsTx(ctx, db, e.ID)
	if err != nil {
		return native.Edition{}, err
	}
	return e, nil
}

// BackfillReleaseForEdition reads the first stored Release for an Edition,
// ordered by ID. A file-bearing unit attaches its Release to the default
// Edition. It returns sql.ErrNoRows when the Edition has no Release.
func (s *NativeStore) BackfillReleaseForEdition(ctx context.Context, db queryer, editionID dto.UUID) (native.Release, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, edition_id, title, acquisition_state, date_created, date_modified FROM native_releases WHERE edition_id = "+s.ph(1)+" ORDER BY id "+s.stores.Dialect().NullsLast()+" LIMIT 1",
		s.uuidArg(editionID))
	return scanRelease(row, s.stores.Dialect())
}

// BackfillInventoryForAsset reads the first stored Inventory for an Asset,
// ordered by ID. A file-bearing unit attaches one Inventory to its Asset. It
// returns sql.ErrNoRows when the Asset has no Inventory.
func (s *NativeStore) BackfillInventoryForAsset(ctx context.Context, db queryer, assetID dto.UUID) (native.Inventory, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, asset_id, state, physical_bytes, hardlinked, hardlink_target, seed_ratio, seed_time, seed_obligation_met, date_created, date_modified FROM native_inventories WHERE asset_id = "+s.ph(1)+" ORDER BY id "+s.stores.Dialect().NullsLast()+" LIMIT 1",
		s.uuidArg(assetID))
	return scanInventory(row, s.stores.Dialect())
}

// BackfillItemRevision reads the current compatibility source revision for one
// item under the writer transaction. It returns the revision and whether the
// source has a complete inode and change-time identity. When the item is
// deleted, it returns an empty revision and false.
func (s *ItemStore) BackfillItemRevision(ctx context.Context, tx *sql.Tx, itemID dto.UUID) (string, bool, error) {
	return s.currentSourceRevisionTx(ctx, tx, itemID)
}

// BackfillRequireCurrentSourceRevision wraps the compatibility source
// compare-and-swap under the native writer transaction. It locks the
// reconciliation boundary, reads the current source under the writer, and
// compares the revision. When the revision differs, the item is deleted, or
// the current source lacks complete identity, it returns
// ErrStaleSourceRevision. The native pass discards the transaction, refreshes
// the snapshot, and retries the unit once.
func (s *ItemStore) BackfillRequireCurrentSourceRevision(ctx context.Context, tx *sql.Tx, itemID dto.UUID, expected string) error {
	return s.requireCurrentSourceRevision(ctx, tx, itemID, expected)
}

// BackfillUpsertUnit performs one atomic unit upsert in one writer
// transaction. It resolves or creates the Work (and identity mappings), the
// default Edition, the Release, Asset, Inventory, and the projection row in
// foreign-key order. A failure rolls back the whole unit; there is no
// partial-unit state.
//
// The method writes only native_* tables. The compatibility source
// compare-and-swap happens before any native insert or update through
// requireCurrentSourceRevision, invoked inside this transaction before the
// native writes.
//
// selectedID names the existing native row to reuse for the unit, or the nil
// UUID when no row exists. now is the run start time for DateCreated,
// DateModified, and LastVerified. When the unit is unchanged (the stored facts
// equal the derived facts and the projection row already names the target),
// the method performs zero writes and returns Created=false, Updated=false.
func (s *NativeStore) BackfillUpsertUnit(ctx context.Context, unit BackfillUnit, selectedID dto.UUID, now time.Time) (BackfillUnitResult, error) {
	res := BackfillUnitResult{}
	err := s.execTx(ctx, func(tx *sql.Tx) error {
		// A file-bearing unit verifies the compatibility source under the
		// writer before any native write. The caller captured the revision
		// from a reader snapshot; this transaction locks and re-reads the
		// current source and compares. A mismatch returns
		// ErrStaleSourceRevision and rolls back the unit.
		if unit.Target == BackfillTargetAsset && unit.SourceRevision != "" {
			items := NewItemStore(s.stores)
			if err := items.BackfillRequireCurrentSourceRevision(ctx, tx, unit.Item.ID, unit.SourceRevision); err != nil {
				return err
			}
		}
		r, err := s.backfillUpsertUnitTx(ctx, tx, unit, selectedID, now)
		if err != nil {
			return err
		}
		res = r
		return nil
	})
	return res, err
}

// backfillUpsertUnitTx writes one unit inside the supplied writer transaction.
// It resolves or creates each aggregate in foreign-key order, then writes the
// projection row through the package-private transactional helper.
func (s *NativeStore) backfillUpsertUnitTx(ctx context.Context, tx *sql.Tx, unit BackfillUnit, selectedID dto.UUID, now time.Time) (BackfillUnitResult, error) {
	res := BackfillUnitResult{}
	created := false
	updated := false
	workID := unit.WorkID

	// Resolve the Work for the selected row. For a Work target the selected
	// row is the Work. For an Asset target the Work is reached through
	// Asset -> Release -> Edition -> Work.
	var existingWork native.Work
	hasExistingWork := false
	if !selectedID.IsNil() {
		if unit.Target == BackfillTargetWork {
			w, err := s.getWorkTx(ctx, tx, selectedID)
			if err == nil {
				existingWork = w
				hasExistingWork = true
				workID = w.ID
			} else if !errors.Is(err, sql.ErrNoRows) {
				return BackfillUnitResult{}, err
			}
		} else {
			asset, err := s.BackfillAssetFacts(ctx, tx, selectedID)
			if err == nil {
				release, rErr := s.getReleaseTx(ctx, tx, asset.ReleaseID)
				if rErr != nil {
					return BackfillUnitResult{}, rErr
				}
				edition, eErr := s.getEditionTx(ctx, tx, release.EditionID)
				if eErr != nil {
					return BackfillUnitResult{}, eErr
				}
				w, wErr := s.getWorkTx(ctx, tx, edition.WorkID)
				if wErr != nil {
					return BackfillUnitResult{}, wErr
				}
				existingWork = w
				hasExistingWork = true
				workID = w.ID
				// A child item (Episode or Audio) that moved to a
				// different parent keeps its existing native chain. The
				// Asset facts update in place, but the Release, Edition,
				// and Work chain is not re-parented. Count regroup-skipped.
				if unit.WorkOwnedByParent && !unit.WorkID.IsNil() && w.ID != unit.WorkID {
					res.RegroupSkipped = true
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return BackfillUnitResult{}, err
			}
		}
	}

	// Resolve identity to a Work UUID. First-import wins per catalog. A
	// cross-kind-group collision (the existing mapping points to a Work whose
	// projected item has a different kind group) does not reuse that Work.
	// The unit creates a fresh Work without the colliding identity, and the
	// caller counts the collision.
	//
	// Skip identity import when the unit already has a valid selected Work.
	// A re-run unit reuses its existing Work (selected by projection), so
	// identity resolution is complete. Importing again would re-detect a
	// cross-kind collision and create a fresh Work on every rerun, which
	// breaks idempotency. Identity import runs only when the unit creates a
	// new Work or merges into an unresolved Work.
	if len(unit.Identity) > 0 && !hasExistingWork {
		expectedGroup, _ := BackfillKindGroupForItem(unit.Item)
		for _, m := range unit.Identity {
			resolved, err := s.importIdentityTx(ctx, tx, m.Catalog, m.ExternalID, workID, now)
			if err != nil {
				return BackfillUnitResult{}, err
			}
			if !resolved.IsNil() && resolved != workID {
				// The identity maps to an existing Work. Verify the
				// Work's kind group matches this unit's kind group
				// before reuse. A cross-kind collision creates a fresh
				// Work without the identity mapping.
				grp, ok, gErr := s.BackfillWorkKindGroup(ctx, tx, resolved)
				if gErr != nil {
					return BackfillUnitResult{}, gErr
				}
				if ok && grp != expectedGroup {
					res.IdentityCollisionSkipped = true
					workID = backfillFreshUUID()
					unit.Identity = nil
					existingWork = native.Work{}
					hasExistingWork = false
					break
				}
				// Same-kind-group collision: reuse the existing Work.
				// This is the identity-merge the run report counts.
				res.IdentityMerge = true
				workID = resolved
				if !hasExistingWork {
					w, gErr := s.getWorkTx(ctx, tx, workID)
					if gErr != nil {
						return BackfillUnitResult{}, gErr
					}
					existingWork = w
					hasExistingWork = true
				}
			}
		}
	}
	// When identity import was skipped because the unit reuses an existing
	// selected Work, the unit still carries its derived identity. A cross-
	// kind collision Work has no identity by design: clear the unit's
	// derived identity so the update comparison does not try to add
	// identity to that Work. A Work that already has an identity keeps the
	// unit's derived identity: a stale-source retry may have changed the
	// provider identity, and the Work must update so no stale identity
	// remains in the native graph. On a normal rerun the identity matches,
	// so the comparison passes and no write occurs.
	if hasExistingWork {
		if len(existingWork.ProviderIDs) == 0 {
			unit.Identity = nil
		}
	}
	// When the unit carries a resolved parent Work (Episode or Audio), the
	// Work may already exist without a projection row for this item. Check
	// whether the Work exists before the create-or-update decision.
	if !hasExistingWork && !workID.IsNil() {
		w, err := s.getWorkTx(ctx, tx, workID)
		if err == nil {
			existingWork = w
			hasExistingWork = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return BackfillUnitResult{}, err
		}
	}

	// Create or update the Work.
	if hasExistingWork {
		if workNeedsUpdate(existingWork, unit, now) {
			existingWork.State = workStateForIdentity(unit.Identity)
			existingWork.ProviderIDs = identityWithVerified(unit.Identity, now)
			// A file-bearing unit never updates the Work title. The
			// title is set once at creation. A non-file Work unit
			// (Series, MusicAlbum, BoxSet) updates the title.
			if unit.Target != BackfillTargetAsset {
				existingWork.Title = workTitle(unit.Item)
			}
			existingWork.DateModified = now
			if err := s.updateWorkTx(ctx, tx, existingWork); err != nil {
				return BackfillUnitResult{}, err
			}
			updated = true
		}
	} else {
		w := native.Work{
			ID:           workID,
			State:        workStateForIdentity(unit.Identity),
			Title:        workTitle(unit.Item),
			ProviderIDs:  identityWithVerified(unit.Identity, now),
			DateCreated:  now,
			DateModified: now,
		}
		if err := s.createWorkTx(ctx, tx, w); err != nil {
			return BackfillUnitResult{}, err
		}
		created = true
	}

	// Work-only units (Series, MusicAlbum, BoxSet) produce no Edition, Release,
	// Asset, or Inventory. Write the Work projection with no source revision
	// guard.
	if unit.Target == BackfillTargetWork {
		changed, pErr := s.backfillWriteProjectionTx(ctx, tx, unit, workID, selectedID)
		if pErr != nil {
			return BackfillUnitResult{}, pErr
		}
		if changed && !created {
			updated = true
		}
		res.Created = created
		res.Updated = updated
		return res, nil
	}

	// File-bearing unit: resolve or create the default Edition (the unique
	// Edition with an empty title) for the Work, then the Release, Asset, and
	// Inventory.
	editionID, editionCreated, err := s.backfillResolveDefaultEditionTx(ctx, tx, workID, now)
	if err != nil {
		return BackfillUnitResult{}, err
	}
	if editionCreated {
		created = true
	}

	// File-bearing unit: resolve or create the Release, Asset, and Inventory.
	releaseID, releaseCreated, err := s.backfillResolveReleaseTx(ctx, tx, editionID, unit, selectedID, now)
	if err != nil {
		return BackfillUnitResult{}, err
	}
	if releaseCreated {
		created = true
	}

	assetID, assetCreated, assetChanged, err := s.backfillResolveAssetTx(ctx, tx, releaseID, unit, selectedID, now)
	if err != nil {
		return BackfillUnitResult{}, err
	}
	if assetCreated {
		created = true
	} else if assetChanged {
		updated = true
	}

	inventoryCreated, err := s.backfillResolveInventoryTx(ctx, tx, assetID, selectedID, now)
	if err != nil {
		return BackfillUnitResult{}, err
	}
	if inventoryCreated {
		created = true
	}

	changed, pErr := s.backfillWriteProjectionTx(ctx, tx, unit, assetID, selectedID)
	if pErr != nil {
		return BackfillUnitResult{}, pErr
	}
	if changed && !created {
		updated = true
	}
	res.Created = created
	res.Updated = updated
	return res, nil
}

// importIdentityTx records one external identity mapping for a proposed Work
// UUID inside the unit transaction. When a mapping exists for the catalog and
// external ID, the method returns the mapped UUID and writes nothing. When no
// mapping exists, the method records the mapping and returns the proposed
// UUID.
func (s *NativeStore) importIdentityTx(ctx context.Context, tx *sql.Tx, catalog native.Catalog, externalID string, proposedID dto.UUID, now time.Time) (dto.UUID, error) {
	var raw any
	lookupErr := tx.QueryRowContext(ctx,
		"SELECT aggregate_id FROM native_identity_mappings WHERE catalog = "+s.ph(1)+" AND external_id = "+s.ph(2),
		string(catalog), externalID).Scan(&raw)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return dto.UUID{}, lookupErr
	}
	if lookupErr == nil {
		return scanUUID(raw)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO native_identity_mappings (aggregate_id, catalog, external_id, last_verified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+")",
		s.uuidArg(proposedID), string(catalog), externalID, s.dialectTime(now)); err != nil {
		return dto.UUID{}, err
	}
	return proposedID, nil
}

// backfillResolveDefaultEditionTx resolves or creates the default Edition for
// one Work. It returns the Edition ID and whether it created the Edition.
func (s *NativeStore) backfillResolveDefaultEditionTx(ctx context.Context, tx *sql.Tx, workID dto.UUID, now time.Time) (dto.UUID, bool, error) {
	edition, err := s.BackfillDefaultEdition(ctx, tx, workID)
	if err == nil {
		return edition.ID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dto.UUID{}, false, err
	}
	editionID := backfillFreshUUID()
	e := native.Edition{
		ID:           editionID,
		WorkID:       workID,
		Title:        "",
		DateCreated:  now,
		DateModified: now,
	}
	if err := s.createEditionTx(ctx, tx, e); err != nil {
		return dto.UUID{}, false, err
	}
	return editionID, true, nil
}

// backfillResolveReleaseTx resolves or creates the Release under the default
// Edition for a file-bearing unit.
func (s *NativeStore) backfillResolveReleaseTx(ctx context.Context, tx *sql.Tx, editionID dto.UUID, unit BackfillUnit, selectedID dto.UUID, now time.Time) (dto.UUID, bool, error) {
	if !selectedID.IsNil() && unit.Target == BackfillTargetAsset {
		asset, err := s.BackfillAssetFacts(ctx, tx, selectedID)
		if err == nil {
			release, rErr := s.getReleaseTx(ctx, tx, asset.ReleaseID)
			if rErr != nil {
				return dto.UUID{}, false, rErr
			}
			return release.ID, false, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return dto.UUID{}, false, err
		}
	}
	releaseID := backfillFreshUUID()
	r := native.Release{
		ID:               releaseID,
		EditionID:        editionID,
		Title:            releaseTitle(unit.Item),
		AcquisitionState: native.AcquisitionAcquired,
		DateCreated:      now,
		DateModified:     now,
	}
	if err := s.createReleaseTx(ctx, tx, r); err != nil {
		return dto.UUID{}, false, err
	}
	return releaseID, true, nil
}

// backfillResolveAssetTx resolves or creates the Asset under the Release. It
// returns the Asset ID, whether it created the Asset, and whether it updated
// an existing Asset. An unchanged Asset performs zero writes.
func (s *NativeStore) backfillResolveAssetTx(ctx context.Context, tx *sql.Tx, releaseID dto.UUID, unit BackfillUnit, selectedID dto.UUID, now time.Time) (dto.UUID, bool, bool, error) {
	path := itemPath(unit.Item)
	device, inode := itemDeviceInode(unit.Item)
	if !selectedID.IsNil() {
		asset, err := s.BackfillAssetFacts(ctx, tx, selectedID)
		if err == nil {
			if asset.Path == path && asset.Device == device && asset.Inode == inode && asset.SourceRevision == unit.SourceRevision {
				return asset.ID, false, false, nil
			}
			asset.Path = path
			asset.Device = device
			asset.Inode = inode
			asset.SourceRevision = unit.SourceRevision
			asset.DateModified = now
			if err := s.updateAssetTx(ctx, tx, asset); err != nil {
				return dto.UUID{}, false, false, err
			}
			return asset.ID, false, true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return dto.UUID{}, false, false, err
		}
	}
	assetID := backfillFreshUUID()
	a := native.Asset{
		ID:             assetID,
		ReleaseID:      releaseID,
		Path:           path,
		Device:         device,
		Inode:          inode,
		SourceRevision: unit.SourceRevision,
		DateCreated:    now,
		DateModified:   now,
	}
	if err := s.createAssetTx(ctx, tx, a); err != nil {
		return dto.UUID{}, false, false, err
	}
	return assetID, true, false, nil
}

// backfillResolveInventoryTx resolves or creates the Inventory under the
// Asset. It returns whether it created the Inventory.
func (s *NativeStore) backfillResolveInventoryTx(ctx context.Context, tx *sql.Tx, assetID dto.UUID, selectedID dto.UUID, now time.Time) (bool, error) {
	if !selectedID.IsNil() {
		_, err := s.BackfillInventoryForAsset(ctx, tx, assetID)
		if err == nil {
			return false, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	inventoryID := backfillFreshUUID()
	inv := native.Inventory{
		ID:           inventoryID,
		AssetID:      assetID,
		State:        native.InventoryPresent,
		DateCreated:  now,
		DateModified: now,
	}
	if err := s.createInventoryTx(ctx, tx, inv); err != nil {
		return false, err
	}
	return true, nil
}

// backfillWriteProjectionTx writes the projection row pointing at the target
// native ID through the package-private transactional helper. It returns
// whether the projection changed. An unchanged projection performs zero
// writes.
func (s *NativeStore) backfillWriteProjectionTx(ctx context.Context, tx *sql.Tx, unit BackfillUnit, nativeID, selectedID dto.UUID) (bool, error) {
	if !selectedID.IsNil() && selectedID == nativeID {
		var existingItem any
		err := tx.QueryRowContext(ctx,
			"SELECT item_id FROM native_item_projections WHERE native_id = "+s.ph(1),
			s.uuidArg(nativeID)).Scan(&existingItem)
		if err == nil {
			itemID, sErr := scanUUID(existingItem)
			if sErr != nil {
				return false, sErr
			}
			if itemID == unit.Item.ID {
				return false, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	if err := s.setItemProjectionTx(ctx, tx, nativeID, unit.Item.ID, unit.SourceRevision); err != nil {
		return false, err
	}
	return true, nil
}

// workNeedsUpdate reports whether the stored Work differs from the derived
// values and must be updated.
func workNeedsUpdate(existing native.Work, unit BackfillUnit, now time.Time) bool {
	// A child item (Episode or Audio) does not own the Work title, state, or
	// identity. The parent Work owns these facts. A child unit performs no
	// Work update; the child attaches its Release, Asset, and Inventory to
	// the existing parent Work without changing the Work row.
	if unit.WorkOwnedByParent {
		return false
	}
	// A file-bearing unit (Movie) never updates the Work title or state.
	// Those facts are set once at creation. When two Movies share one
	// identity, the second movie merges into the first movie's Work. The
	// merge must not overwrite the Work title with the second movie's
	// title. Without this guard, each rerun toggles the shared Work title
	// between the two movie names, which breaks the zero-writes
	// idempotency contract.
	//
	// Identity is different: a stale-source retry can change the item's
	// provider identity between the original enumeration and the retry.
	// The retry must update the Work identity so no stale identity remains
	// in the native graph. On a normal rerun the identity does not change,
	// so the comparison passes and no write occurs.
	if unit.Target == BackfillTargetAsset {
		return !identityMappingsEqual(existing.ProviderIDs, identityWithVerified(unit.Identity, now))
	}
	if existing.State != workStateForIdentity(unit.Identity) {
		return true
	}
	if existing.Title != workTitle(unit.Item) {
		return true
	}
	if !identityMappingsEqual(existing.ProviderIDs, identityWithVerified(unit.Identity, now)) {
		return true
	}
	return false
}

// workStateForIdentity returns Identified when the identity set has at least
// one catalog, else Unidentified. The backfill never writes Matched or Locked.
func workStateForIdentity(identity []native.IdentityMapping) native.CatalogState {
	if len(identity) > 0 {
		return native.CatalogIdentified
	}
	return native.CatalogUnidentified
}

// workTitle returns the native Work title for an item. The Work title is the
// item name when present, else the empty string.
func workTitle(it item.Item) string {
	if it.Name != nil {
		return *it.Name
	}
	return ""
}

// releaseTitle returns the Release title for an item.
func releaseTitle(it item.Item) string {
	if it.Name != nil {
		return *it.Name
	}
	return ""
}

// itemPath returns the item path or the empty string.
func itemPath(it item.Item) string {
	if it.Path != nil {
		return *it.Path
	}
	return ""
}

// itemDeviceInode returns the item device and inode, mapping nil to 0.
func itemDeviceInode(it item.Item) (uint64, uint64) {
	var device, inode uint64
	if it.FileDevice != nil {
		device = uint64(*it.FileDevice)
	}
	if it.FileInode != nil {
		inode = uint64(*it.FileInode)
	}
	return device, inode
}

// identityWithVerified returns a copy of the identity set with LastVerified
// set to the run time.
func identityWithVerified(identity []native.IdentityMapping, now time.Time) []native.IdentityMapping {
	if len(identity) == 0 {
		return nil
	}
	out := make([]native.IdentityMapping, len(identity))
	for i, m := range identity {
		out[i] = m
		out[i].LastVerified = now
	}
	return out
}

// identityMappingsEqual reports whether two identity mapping sets are equal by
// catalog and external ID, ignoring order and LastVerified.
func identityMappingsEqual(a, b []native.IdentityMapping) bool {
	if len(a) != len(b) {
		return false
	}
	bset := make(map[native.Catalog]string, len(b))
	for _, m := range b {
		bset[m.Catalog] = m.ExternalID
	}
	for _, m := range a {
		id, ok := bset[m.Catalog]
		if !ok || id != m.ExternalID {
			return false
		}
	}
	return true
}

// backfillFreshUUID generates a fresh server-assigned UUID for a new native
// aggregate. The UUID is a version-4 (random) variant. The backfill never
// derives a native UUID from a compatibility ID; that changes server-assigned
// identity semantics.
func backfillFreshUUID() dto.UUID {
	var u dto.UUID
	if _, err := rand.Read(u[:]); err != nil {
		// crypto/rand.Read never returns an error on a blocking source. A
		// failure is a fatal platform condition.
		panic(fmt.Sprintf("backfill: generate UUID: %v", err))
	}
	// RFC 4122 version 4 and variant bits.
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return u
}

// createReleaseTx inserts a new Release inside the supplied writer
// transaction. It mirrors NativeStore.CreateRelease but stays on the unit
// transaction so the whole unit commits or rolls back atomically.
func (s *NativeStore) createReleaseTx(ctx context.Context, tx *sql.Tx, r native.Release) error {
	if r.ID.IsNil() {
		return errors.New("create release: id is required")
	}
	if r.EditionID.IsNil() {
		return errors.New("create release: edition id is required")
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO native_releases (id, edition_id, title, acquisition_state, date_created, date_modified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+", "+s.ph(6)+")",
		s.uuidArg(r.ID), s.uuidArg(r.EditionID), r.Title, string(r.AcquisitionState),
		s.dialectTime(r.DateCreated), s.dialectTime(r.DateModified))
	return err
}

// createAssetTx inserts a new Asset inside the supplied writer transaction.
func (s *NativeStore) createAssetTx(ctx context.Context, tx *sql.Tx, a native.Asset) error {
	if a.ID.IsNil() {
		return errors.New("create asset: id is required")
	}
	if a.ReleaseID.IsNil() {
		return errors.New("create asset: release id is required")
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO native_assets (id, release_id, path, device, inode, source_revision, date_created, date_modified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+", "+s.ph(6)+", "+s.ph(7)+", "+s.ph(8)+")",
		s.uuidArg(a.ID), s.uuidArg(a.ReleaseID), a.Path, int64(a.Device), int64(a.Inode),
		a.SourceRevision, s.dialectTime(a.DateCreated), s.dialectTime(a.DateModified))
	return err
}

// updateAssetTx updates the path, device, inode, and source revision of an
// Asset inside the supplied writer transaction.
func (s *NativeStore) updateAssetTx(ctx context.Context, tx *sql.Tx, a native.Asset) error {
	if a.ID.IsNil() {
		return errors.New("update asset: id is required")
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE native_assets SET path = "+s.ph(1)+", device = "+s.ph(2)+", inode = "+s.ph(3)+", source_revision = "+s.ph(4)+", date_modified = "+s.ph(5)+" WHERE id = "+s.ph(6),
		a.Path, int64(a.Device), int64(a.Inode), a.SourceRevision, s.dialectTime(a.DateModified), s.uuidArg(a.ID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// createInventoryTx inserts a new Inventory inside the supplied writer
// transaction.
func (s *NativeStore) createInventoryTx(ctx context.Context, tx *sql.Tx, inv native.Inventory) error {
	if inv.ID.IsNil() {
		return errors.New("create inventory: id is required")
	}
	if inv.AssetID.IsNil() {
		return errors.New("create inventory: asset id is required")
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO native_inventories (id, asset_id, state, physical_bytes, hardlinked, hardlink_target, seed_ratio, seed_time, seed_obligation_met, date_created, date_modified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+", "+s.ph(6)+", "+s.ph(7)+", "+s.ph(8)+", "+s.ph(9)+", "+s.ph(10)+", "+s.ph(11)+")",
		s.uuidArg(inv.ID), s.uuidArg(inv.AssetID), string(inv.State), inv.PhysicalBytes,
		boolArg(s.stores.Dialect(), inv.Hardlinked), inv.HardlinkTarget,
		inv.SeedRatio, inv.SeedTime, boolArg(s.stores.Dialect(), inv.SeedObligationMet),
		s.dialectTime(inv.DateCreated), s.dialectTime(inv.DateModified))
	return err
}
