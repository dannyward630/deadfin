// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/native"
)

// NativeStore persists the native control-plane aggregates. All writes use the
// store's single writer handle; reads use its reader pool. The native graph is
// a derived layer (docs/08-control-plane.md:35-39).
type NativeStore struct {
	stores Stores
	clock  clock.Clock
}

// NewNativeStore constructs the native control-plane persistence adapter. The
// clock supplies the last_verified timestamp for new identity mappings and
// must not be nil.
func NewNativeStore(stores Stores, clock clock.Clock) *NativeStore {
	if clock == nil {
		panic("NewNativeStore: clock must not be nil")
	}
	return &NativeStore{stores: stores, clock: clock}
}

// ---------------------------------------------------------------------------
// Work
// ---------------------------------------------------------------------------

// CreateWork inserts a new Work. The ID must be a server-assigned UUID.
func (s *NativeStore) CreateWork(ctx context.Context, w native.Work) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.createWorkTx(ctx, tx, w)
	})
}

func (s *NativeStore) createWorkTx(ctx context.Context, tx *sql.Tx, w native.Work) error {
	if w.ID.IsNil() {
		return errors.New("create work: id is required")
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO native_works (id, state, title, date_created, date_modified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+")",
		s.uuidArg(w.ID), string(w.State), w.Title,
		s.dialectTime(w.DateCreated), s.dialectTime(w.DateModified))
	if err != nil {
		return err
	}
	return s.replaceIdentityMappingsTx(ctx, tx, w.ID, w.ProviderIDs)
}

// GetWork reads one Work by ID, including its identity mappings.
func (s *NativeStore) GetWork(ctx context.Context, id dto.UUID) (native.Work, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Work{}, err
	}
	defer rollbackOnReturn(tx, &err)
	w, err := s.getWorkTx(ctx, tx, id)
	if err != nil {
		return native.Work{}, err
	}
	return w, tx.Commit()
}

func (s *NativeStore) getWorkTx(ctx context.Context, db queryer, id dto.UUID) (native.Work, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, state, title, date_created, date_modified FROM native_works WHERE id = "+s.ph(1),
		s.uuidArg(id))
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

// UpdateWork updates the state, title, and identity mappings of a Work.
func (s *NativeStore) UpdateWork(ctx context.Context, w native.Work) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.updateWorkTx(ctx, tx, w)
	})
}

func (s *NativeStore) updateWorkTx(ctx context.Context, tx *sql.Tx, w native.Work) error {
	if w.ID.IsNil() {
		return errors.New("update work: id is required")
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE native_works SET state = "+s.ph(1)+", title = "+s.ph(2)+", date_modified = "+s.ph(3)+" WHERE id = "+s.ph(4),
		string(w.State), w.Title, s.dialectTime(w.DateModified), s.uuidArg(w.ID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return s.replaceIdentityMappingsTx(ctx, tx, w.ID, w.ProviderIDs)
}

// ListWorks returns all Works ordered by ID for deterministic listing.
func (s *NativeStore) ListWorks(ctx context.Context) ([]native.Work, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, state, title, date_created, date_modified FROM native_works ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var works []native.Work
	for rows.Next() {
		w, err := scanWork(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		works = append(works, w)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	for i := range works {
		works[i].ProviderIDs, err = s.identityMappingsTx(ctx, tx, works[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return works, tx.Commit()
}

// ---------------------------------------------------------------------------
// Edition
// ---------------------------------------------------------------------------

// CreateEdition inserts a new Edition.
func (s *NativeStore) CreateEdition(ctx context.Context, e native.Edition) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.createEditionTx(ctx, tx, e)
	})
}

func (s *NativeStore) createEditionTx(ctx context.Context, tx *sql.Tx, e native.Edition) error {
	if e.ID.IsNil() {
		return errors.New("create edition: id is required")
	}
	if e.WorkID.IsNil() {
		return errors.New("create edition: work id is required")
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO native_editions (id, work_id, title, date_created, date_modified) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+")",
		s.uuidArg(e.ID), s.uuidArg(e.WorkID), e.Title,
		s.dialectTime(e.DateCreated), s.dialectTime(e.DateModified))
	if err != nil {
		return err
	}
	return s.replaceIdentityMappingsTx(ctx, tx, e.ID, e.ProviderIDs)
}

// GetEdition reads one Edition by ID, including its identity mappings.
func (s *NativeStore) GetEdition(ctx context.Context, id dto.UUID) (native.Edition, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Edition{}, err
	}
	defer rollbackOnReturn(tx, &err)
	e, err := s.getEditionTx(ctx, tx, id)
	if err != nil {
		return native.Edition{}, err
	}
	return e, tx.Commit()
}

func (s *NativeStore) getEditionTx(ctx context.Context, db queryer, id dto.UUID) (native.Edition, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, work_id, title, date_created, date_modified FROM native_editions WHERE id = "+s.ph(1),
		s.uuidArg(id))
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

// UpdateEdition updates the title and identity mappings of an Edition.
func (s *NativeStore) UpdateEdition(ctx context.Context, e native.Edition) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.updateEditionTx(ctx, tx, e)
	})
}

func (s *NativeStore) updateEditionTx(ctx context.Context, tx *sql.Tx, e native.Edition) error {
	if e.ID.IsNil() {
		return errors.New("update edition: id is required")
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE native_editions SET title = "+s.ph(1)+", date_modified = "+s.ph(2)+" WHERE id = "+s.ph(3),
		e.Title, s.dialectTime(e.DateModified), s.uuidArg(e.ID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return s.replaceIdentityMappingsTx(ctx, tx, e.ID, e.ProviderIDs)
}

func (s *NativeStore) ListEditions(ctx context.Context) ([]native.Edition, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, work_id, title, date_created, date_modified FROM native_editions ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var editions []native.Edition
	for rows.Next() {
		e, err := scanEdition(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		editions = append(editions, e)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	for i := range editions {
		editions[i].ProviderIDs, err = s.identityMappingsTx(ctx, tx, editions[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return editions, tx.Commit()
}

// ---------------------------------------------------------------------------
// Release
// ---------------------------------------------------------------------------

// CreateRelease inserts a new Release.
func (s *NativeStore) CreateRelease(ctx context.Context, r native.Release) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
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
	})
}

// GetRelease reads one Release by ID.
func (s *NativeStore) GetRelease(ctx context.Context, id dto.UUID) (native.Release, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Release{}, err
	}
	defer rollbackOnReturn(tx, &err)
	r, err := s.getReleaseTx(ctx, tx, id)
	if err != nil {
		return native.Release{}, err
	}
	return r, tx.Commit()
}

func (s *NativeStore) getReleaseTx(ctx context.Context, db queryer, id dto.UUID) (native.Release, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, edition_id, title, acquisition_state, date_created, date_modified FROM native_releases WHERE id = "+s.ph(1),
		s.uuidArg(id))
	return scanRelease(row, s.stores.Dialect())
}

// UpdateRelease updates the title and acquisition state of a Release.
func (s *NativeStore) UpdateRelease(ctx context.Context, r native.Release) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		if r.ID.IsNil() {
			return errors.New("update release: id is required")
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE native_releases SET title = "+s.ph(1)+", acquisition_state = "+s.ph(2)+", date_modified = "+s.ph(3)+" WHERE id = "+s.ph(4),
			r.Title, string(r.AcquisitionState), s.dialectTime(r.DateModified), s.uuidArg(r.ID))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// ListReleases returns all Releases ordered by ID for deterministic listing.
func (s *NativeStore) ListReleases(ctx context.Context) ([]native.Release, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, edition_id, title, acquisition_state, date_created, date_modified FROM native_releases ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var releases []native.Release
	for rows.Next() {
		r, err := scanRelease(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		releases = append(releases, r)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return releases, tx.Commit()
}

// ---------------------------------------------------------------------------
// Asset
// ---------------------------------------------------------------------------

// CreateAsset inserts a new Asset with device, inode, and source revision.
func (s *NativeStore) CreateAsset(ctx context.Context, a native.Asset) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
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
	})
}

// GetAsset reads one Asset by ID.
func (s *NativeStore) GetAsset(ctx context.Context, id dto.UUID) (native.Asset, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Asset{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT id, release_id, path, device, inode, source_revision, date_created, date_modified FROM native_assets WHERE id = "+s.ph(1),
		s.uuidArg(id))
	a, err := scanAsset(row, s.stores.Dialect())
	if err != nil {
		return native.Asset{}, err
	}
	return a, tx.Commit()
}

// UpdateAsset updates the path, device, inode, and source revision of an Asset.
func (s *NativeStore) UpdateAsset(ctx context.Context, a native.Asset) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
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
	})
}

// ListAssets returns all Assets ordered by ID for deterministic listing.
func (s *NativeStore) ListAssets(ctx context.Context) ([]native.Asset, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, release_id, path, device, inode, source_revision, date_created, date_modified FROM native_assets ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var assets []native.Asset
	for rows.Next() {
		a, err := scanAsset(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		assets = append(assets, a)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return assets, tx.Commit()
}

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------
// CreateInventory inserts a new Inventory.
func (s *NativeStore) CreateInventory(ctx context.Context, inv native.Inventory) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
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
	})
}

// GetInventory reads one Inventory by ID.
func (s *NativeStore) GetInventory(ctx context.Context, id dto.UUID) (native.Inventory, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Inventory{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT id, asset_id, state, physical_bytes, hardlinked, hardlink_target, seed_ratio, seed_time, seed_obligation_met, date_created, date_modified FROM native_inventories WHERE id = "+s.ph(1),
		s.uuidArg(id))
	inv, err := scanInventory(row, s.stores.Dialect())
	if err != nil {
		return native.Inventory{}, err
	}
	return inv, tx.Commit()
}

// UpdateInventory updates the state, physical bytes, hardlink facts, and seed
// obligations of an Inventory.
func (s *NativeStore) UpdateInventory(ctx context.Context, inv native.Inventory) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		if inv.ID.IsNil() {
			return errors.New("update inventory: id is required")
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE native_inventories SET state = "+s.ph(1)+", physical_bytes = "+s.ph(2)+", "+
				"hardlinked = "+s.ph(3)+", hardlink_target = "+s.ph(4)+", "+
				"seed_ratio = "+s.ph(5)+", seed_time = "+s.ph(6)+", seed_obligation_met = "+s.ph(7)+", "+
				"date_modified = "+s.ph(8)+" WHERE id = "+s.ph(9),
			string(inv.State), inv.PhysicalBytes,
			boolArg(s.stores.Dialect(), inv.Hardlinked), inv.HardlinkTarget,
			inv.SeedRatio, inv.SeedTime, boolArg(s.stores.Dialect(), inv.SeedObligationMet),
			s.dialectTime(inv.DateModified), s.uuidArg(inv.ID))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// ListInventories returns all Inventories ordered by ID for deterministic
func (s *NativeStore) ListInventories(ctx context.Context) ([]native.Inventory, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, asset_id, state, physical_bytes, hardlinked, hardlink_target, seed_ratio, seed_time, seed_obligation_met, date_created, date_modified FROM native_inventories ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var inventories []native.Inventory
	for rows.Next() {
		inv, err := scanInventory(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		inventories = append(inventories, inv)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return inventories, tx.Commit()
}

// ---------------------------------------------------------------------------
// Request
// ---------------------------------------------------------------------------
// CreateRequest inserts a new Request. Exactly one of WorkID or EditionID must
// be set. A Request with zero or two targets is rejected.
func (s *NativeStore) CreateRequest(ctx context.Context, r native.Request) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		if r.ID.IsNil() {
			return errors.New("create request: id is required")
		}
		if r.WorkID == nil && r.EditionID == nil {
			return errors.New("create request: exactly one of work id or edition id is required")
		}
		if r.WorkID != nil && r.EditionID != nil {
			return errors.New("create request: work id and edition id are mutually exclusive")
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO native_requests (id, work_id, edition_id, state, approval_state, fulfillment_id, date_created, date_modified) VALUES ("+
				s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+", "+s.ph(6)+", "+s.ph(7)+", "+s.ph(8)+")",
			s.uuidArg(r.ID), s.uuidArgPtr(r.WorkID), s.uuidArgPtr(r.EditionID), string(r.State),
			string(r.ApprovalState), s.uuidArgPtr(r.FulfillmentID),
			s.dialectTime(r.DateCreated), s.dialectTime(r.DateModified))
		return err
	})
}

// GetRequest reads one Request by ID.
func (s *NativeStore) GetRequest(ctx context.Context, id dto.UUID) (native.Request, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return native.Request{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT id, work_id, edition_id, state, approval_state, fulfillment_id, date_created, date_modified FROM native_requests WHERE id = "+s.ph(1),
		s.uuidArg(id))
	r, err := scanRequest(row, s.stores.Dialect())
	if err != nil {
		return native.Request{}, err
	}
	return r, tx.Commit()
}

// UpdateRequest updates the state, approval state, and fulfillment link of a
// Request.
func (s *NativeStore) UpdateRequest(ctx context.Context, r native.Request) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		if r.ID.IsNil() {
			return errors.New("update request: id is required")
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE native_requests SET state = "+s.ph(1)+", approval_state = "+s.ph(2)+", fulfillment_id = "+s.ph(3)+", date_modified = "+s.ph(4)+" WHERE id = "+s.ph(5),
			string(r.State), string(r.ApprovalState), s.uuidArgPtr(r.FulfillmentID),
			s.dialectTime(r.DateModified), s.uuidArg(r.ID))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// ListRequests returns all Requests ordered by ID for deterministic listing.
func (s *NativeStore) ListRequests(ctx context.Context) ([]native.Request, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx,
		"SELECT id, work_id, edition_id, state, approval_state, fulfillment_id, date_created, date_modified FROM native_requests ORDER BY id NULLS FIRST")
	if err != nil {
		return nil, err
	}
	var requests []native.Request
	for rows.Next() {
		r, err := scanRequest(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		requests = append(requests, r)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return requests, tx.Commit()
}

// ---------------------------------------------------------------------------
// Identity mappings
// ---------------------------------------------------------------------------

// SetIdentityMappings replaces the identity mappings for one aggregate.
func (s *NativeStore) SetIdentityMappings(ctx context.Context, aggregateID dto.UUID, mappings []native.IdentityMapping) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.replaceIdentityMappingsTx(ctx, tx, aggregateID, mappings)
	})
}

// IdentityMappings reads the identity mappings for one aggregate.
func (s *NativeStore) IdentityMappings(ctx context.Context, aggregateID dto.UUID) ([]native.IdentityMapping, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollbackOnReturn(tx, &err)
	mappings, err := s.identityMappingsTx(ctx, tx, aggregateID)
	if err != nil {
		return nil, err
	}
	return mappings, tx.Commit()
}

// ResolveAggregateByIDentity reads the aggregate UUID for one external
// catalog and external ID. It returns sql.ErrNoRows when no mapping exists.
// This is the external-identity lookup that proves a repeated import returns
// the first native UUID.
func (s *NativeStore) ResolveAggregateByIDentity(ctx context.Context, catalog native.Catalog, externalID string) (dto.UUID, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return dto.UUID{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT aggregate_id FROM native_identity_mappings WHERE catalog = "+s.ph(1)+" AND external_id = "+s.ph(2),
		string(catalog), externalID)
	var raw any
	if err := row.Scan(&raw); err != nil {
		return dto.UUID{}, err
	}
	id, err := scanUUID(raw)
	if err != nil {
		return dto.UUID{}, err
	}
	return id, tx.Commit()
}

// ImportIdentity records one external identity mapping for a proposed
// aggregate UUID. When a mapping exists for the catalog and external ID, the
// method returns the mapped UUID and writes nothing. When no mapping exists,
// the method records the mapping and returns the proposed UUID. A unique
// constraint on (catalog, external_id) prevents two aggregates from mapping
// the same external identity.
func (s *NativeStore) ImportIdentity(ctx context.Context, catalog native.Catalog, externalID string, proposedID dto.UUID) (dto.UUID, error) {
	var resolved dto.UUID
	err := s.execTx(ctx, func(tx *sql.Tx) error {
		// Check for an existing mapping by catalog and external ID.
		var raw any
		lookupErr := tx.QueryRowContext(ctx,
			"SELECT aggregate_id FROM native_identity_mappings WHERE catalog = "+s.ph(1)+" AND external_id = "+s.ph(2),
			string(catalog), externalID).Scan(&raw)
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil {
			id, err := scanUUID(raw)
			if err != nil {
				return err
			}
			resolved = id
			return nil
		}
		// No mapping exists. Record the mapping and return the proposed UUID.
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO native_identity_mappings (aggregate_id, catalog, external_id, last_verified) VALUES ("+
				s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+")",
			s.uuidArg(proposedID), string(catalog), externalID, s.dialectTime(s.clock.Now())); err != nil {
			return err
		}
		resolved = proposedID
		return nil
	})
	return resolved, err
}

func (s *NativeStore) replaceIdentityMappingsTx(ctx context.Context, tx *sql.Tx, aggregateID dto.UUID, mappings []native.IdentityMapping) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM native_identity_mappings WHERE aggregate_id = "+s.ph(1),
		s.uuidArg(aggregateID)); err != nil {
		return err
	}
	for _, m := range mappings {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO native_identity_mappings (aggregate_id, catalog, external_id, last_verified) VALUES ("+
				s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+")",
			s.uuidArg(aggregateID), string(m.Catalog), m.ExternalID, s.dialectTime(m.LastVerified)); err != nil {
			return err
		}
	}
	return nil
}

func (s *NativeStore) identityMappingsTx(ctx context.Context, db queryer, aggregateID dto.UUID) ([]native.IdentityMapping, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT catalog, external_id, last_verified FROM native_identity_mappings WHERE aggregate_id = "+s.ph(1)+" ORDER BY catalog NULLS FIRST",
		s.uuidArg(aggregateID))
	if err != nil {
		return nil, err
	}
	var mappings []native.IdentityMapping
	for rows.Next() {
		var m native.IdentityMapping
		var catalog, externalID string
		var rawTime any
		if err := rows.Scan(&catalog, &externalID, &rawTime); err != nil {
			return nil, closeRows(rows, err)
		}
		m.Catalog = native.Catalog(catalog)
		m.ExternalID = externalID
		t, err := s.stores.Dialect().ScanTime(rawTime)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		m.LastVerified = t
		mappings = append(mappings, m)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return mappings, nil
}

// ---------------------------------------------------------------------------
// Item projection
// ---------------------------------------------------------------------------
// SetItemProjection maps a native UUID to a compatibility item.Item.ID. The
// write is idempotent: a repeated write leaves one mapping. For an Asset, the
// method compares the supplied source revision with
// native_assets.source_revision for that Asset. When the values differ, the
// method returns ErrStaleSourceRevision and leaves the mapping unchanged. The
// guard does not compare against a revision stored on the projection row. A
// projection write for an aggregate without a source file carries no revision
// guard. This preserves the source-revision check order from ItemStore.Upsert
// (internal/store/item_store.go:139-185).
func (s *NativeStore) SetItemProjection(ctx context.Context, nativeID, itemID dto.UUID, sourceRevision string) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		return s.setItemProjectionTx(ctx, tx, nativeID, itemID, sourceRevision)
	})
}

func (s *NativeStore) setItemProjectionTx(ctx context.Context, tx *sql.Tx, nativeID, itemID dto.UUID, sourceRevision string) error {
	if nativeID.IsNil() {
		return errors.New("set item projection: native id is required")
	}
	if itemID.IsNil() {
		return errors.New("set item projection: item id is required")
	}
	// Look up native_assets by nativeID. When the row exists, the nativeID is
	// an Asset and the source revision must match. When no row exists, the
	// nativeID is an aggregate without a source file and no guard applies.
	var storedRevision string
	err := tx.QueryRowContext(ctx,
		"SELECT source_revision FROM native_assets WHERE id = "+s.ph(1),
		s.uuidArg(nativeID)).Scan(&storedRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		// The nativeID is an Asset. The supplied revision must match the
		// stored revision. An empty supplied revision fails the guard.
		if sourceRevision != storedRevision {
			return ErrStaleSourceRevision
		}
	}
	d := s.stores.Dialect()
	if d.Placeholder(1) == "?" {
		_, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO native_item_projections (native_id, item_id) VALUES ("+s.ph(1)+", "+s.ph(2)+")",
			s.uuidArg(nativeID), s.uuidArg(itemID))
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO native_item_projections (native_id, item_id) VALUES ("+s.ph(1)+", "+s.ph(2)+") "+
			"ON CONFLICT (native_id) DO UPDATE SET item_id = EXCLUDED.item_id",
		s.uuidArg(nativeID), s.uuidArg(itemID))
	return err
}

// ResolveItemID reads the compatibility item.Item.ID for one native UUID.
func (s *NativeStore) ResolveItemID(ctx context.Context, nativeID dto.UUID) (dto.UUID, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return dto.UUID{}, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT item_id FROM native_item_projections WHERE native_id = "+s.ph(1),
		s.uuidArg(nativeID))
	var raw any
	if err := row.Scan(&raw); err != nil {
		return dto.UUID{}, err
	}
	id, err := scanUUID(raw)
	if err != nil {
		return dto.UUID{}, err
	}
	return id, tx.Commit()
}

// ProjectionMappingCount returns the number of projection rows for one native
// UUID. The count must be exactly one after a repeated write.
func (s *NativeStore) ProjectionMappingCount(ctx context.Context, nativeID dto.UUID) (int, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM native_item_projections WHERE native_id = "+s.ph(1),
		s.uuidArg(nativeID))
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

// IdentityMappingCount returns the number of identity-mapping rows for one
// catalog and external ID. The count must be exactly one after a repeated
// import.
func (s *NativeStore) IdentityMappingCount(ctx context.Context, catalog native.Catalog, externalID string) (int, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM native_identity_mappings WHERE catalog = "+s.ph(1)+" AND external_id = "+s.ph(2),
		string(catalog), externalID)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

// WorkCountByID returns the number of native_works rows for one ID. The count
// must be exactly one after an identity import creates the aggregate.
func (s *NativeStore) WorkCountByID(ctx context.Context, id dto.UUID) (int, error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer rollbackOnReturn(tx, &err)
	row := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM native_works WHERE id = "+s.ph(1),
		s.uuidArg(id))
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func (s *NativeStore) ph(n int) string { return s.stores.Dialect().Placeholder(n) }

func (s *NativeStore) uuidArg(id dto.UUID) any { return uuidArgDialect(s.stores.Dialect(), id) }

func (s *NativeStore) uuidArgPtr(id *dto.UUID) any {
	if id == nil || id.IsNil() {
		return nil
	}
	return uuidArgDialect(s.stores.Dialect(), *id)
}

func (s *NativeStore) dialectTime(t time.Time) any { return s.stores.Dialect().TimeValue(t) }

func (s *NativeStore) execTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func scanWork(row rowScanner, d Dialect) (native.Work, error) {
	var w native.Work
	var idRaw any
	var state, title string
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &state, &title, &dcRaw, &dmRaw); err != nil {
		return native.Work{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Work{}, err
	}
	w.ID = id
	w.State = native.CatalogState(state)
	w.Title = title
	if w.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Work{}, err
	}
	if w.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Work{}, err
	}
	return w, nil
}

func scanEdition(row rowScanner, d Dialect) (native.Edition, error) {
	var e native.Edition
	var idRaw, workIDRaw any
	var title string
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &workIDRaw, &title, &dcRaw, &dmRaw); err != nil {
		return native.Edition{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Edition{}, err
	}
	workID, err := scanUUID(workIDRaw)
	if err != nil {
		return native.Edition{}, err
	}
	e.ID = id
	e.WorkID = workID
	e.Title = title
	if e.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Edition{}, err
	}
	if e.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Edition{}, err
	}
	return e, nil
}

func scanRelease(row rowScanner, d Dialect) (native.Release, error) {
	var r native.Release
	var idRaw, editionIDRaw any
	var title, acqState string
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &editionIDRaw, &title, &acqState, &dcRaw, &dmRaw); err != nil {
		return native.Release{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Release{}, err
	}
	editionID, err := scanUUID(editionIDRaw)
	if err != nil {
		return native.Release{}, err
	}
	r.ID = id
	r.EditionID = editionID
	r.Title = title
	r.AcquisitionState = native.AcquisitionState(acqState)
	if r.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Release{}, err
	}
	if r.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Release{}, err
	}
	return r, nil
}

func scanAsset(row rowScanner, d Dialect) (native.Asset, error) {
	var a native.Asset
	var idRaw, releaseIDRaw any
	var path, sourceRev string
	var device, inode int64
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &releaseIDRaw, &path, &device, &inode, &sourceRev, &dcRaw, &dmRaw); err != nil {
		return native.Asset{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Asset{}, err
	}
	releaseID, err := scanUUID(releaseIDRaw)
	if err != nil {
		return native.Asset{}, err
	}
	a.ID = id
	a.ReleaseID = releaseID
	a.Path = path
	a.Device = uint64(device)
	a.Inode = uint64(inode)
	a.SourceRevision = sourceRev
	if a.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Asset{}, err
	}
	if a.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Asset{}, err
	}
	return a, nil
}

func scanInventory(row rowScanner, d Dialect) (native.Inventory, error) {
	var inv native.Inventory
	var idRaw, assetIDRaw any
	var state string
	var physicalBytes int64
	var hardlinkedRaw any
	var hardlinkTarget string
	var seedRatio float64
	var seedTime int64
	var seedObligationMetRaw any
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &assetIDRaw, &state, &physicalBytes,
		&hardlinkedRaw, &hardlinkTarget, &seedRatio, &seedTime, &seedObligationMetRaw,
		&dcRaw, &dmRaw); err != nil {
		return native.Inventory{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Inventory{}, err
	}
	assetID, err := scanUUID(assetIDRaw)
	if err != nil {
		return native.Inventory{}, err
	}
	inv.ID = id
	inv.AssetID = assetID
	inv.State = native.InventoryState(state)
	inv.PhysicalBytes = physicalBytes
	inv.Hardlinked = asBool(hardlinkedRaw)
	inv.HardlinkTarget = hardlinkTarget
	inv.SeedRatio = seedRatio
	inv.SeedTime = seedTime
	inv.SeedObligationMet = asBool(seedObligationMetRaw)
	if inv.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Inventory{}, err
	}
	if inv.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Inventory{}, err
	}
	return inv, nil
}

func scanRequest(row rowScanner, d Dialect) (native.Request, error) {
	var r native.Request
	var idRaw, workIDRaw, editionIDRaw, fulfillmentIDRaw any
	var state, approvalState string
	var dcRaw, dmRaw any
	if err := row.Scan(&idRaw, &workIDRaw, &editionIDRaw, &state, &approvalState, &fulfillmentIDRaw, &dcRaw, &dmRaw); err != nil {
		return native.Request{}, err
	}
	id, err := scanUUID(idRaw)
	if err != nil {
		return native.Request{}, err
	}
	r.ID = id
	r.State = native.RequestState(state)
	r.ApprovalState = native.ApprovalState(approvalState)
	if workIDRaw != nil {
		wID, err := scanUUID(workIDRaw)
		if err != nil {
			return native.Request{}, err
		}
		if !wID.IsNil() {
			r.WorkID = &wID
		}
	}
	if editionIDRaw != nil {
		eID, err := scanUUID(editionIDRaw)
		if err != nil {
			return native.Request{}, err
		}
		if !eID.IsNil() {
			r.EditionID = &eID
		}
	}
	if fulfillmentIDRaw != nil {
		fID, err := scanUUID(fulfillmentIDRaw)
		if err != nil {
			return native.Request{}, err
		}
		if !fID.IsNil() {
			r.FulfillmentID = &fID
		}
	}
	if r.DateCreated, err = d.ScanTime(dcRaw); err != nil {
		return native.Request{}, err
	}
	if r.DateModified, err = d.ScanTime(dmRaw); err != nil {
		return native.Request{}, err
	}
	return r, nil
}
