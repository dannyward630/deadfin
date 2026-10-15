// SPDX-License-Identifier: GPL-2.0-only

// Package backfill runs the native-model backfill pass that derives native
// Work, Edition, Release, Asset, and Inventory rows from the existing
// compatibility items. The pass is re-runnable, idempotent, and
// single-threaded. Any failure leaves the Jellyfin-visible library and its
// data unchanged (INV-5, docs/08-control-plane.md:357-359).
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/native"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ItemEnumerator is the read interface the pass uses on the compatibility
// item store.
type ItemEnumerator interface {
	BackfillEnumerateItems(ctx context.Context, afterID dto.UUID, limit int) ([]item.Item, error)
	Get(ctx context.Context, id dto.UUID) (item.Item, error)
	DerivedSourceSnapshot(ctx context.Context, id dto.UUID) (store.DerivedSource, error)
}

// NativeWriter is the read and write interface the pass uses on the native
// store.
type NativeWriter interface {
	BackfillReaderTx(ctx context.Context) (store.BackfillTx, error)
	BackfillFreshUUIDForPass() dto.UUID
	BackfillProjectionsForItem(ctx context.Context, db store.Queryer, itemID dto.UUID) ([]dto.UUID, error)
	BackfillFilterProjectionsByTargetClass(ctx context.Context, db store.Queryer, ids []dto.UUID, target store.BackfillTarget) ([]dto.UUID, bool, error)
	BackfillWorkKindGroup(ctx context.Context, db store.Queryer, nativeID dto.UUID) (store.BackfillKindGroup, bool, error)
	BackfillResolveWorkForItem(ctx context.Context, db store.Queryer, parentItemID dto.UUID) (dto.UUID, error)
	BackfillUpsertUnit(ctx context.Context, unit store.BackfillUnit, selectedID dto.UUID, now time.Time) (store.BackfillUnitResult, error)
}

// Deps holds the store adapters the pass reads from and writes to.
type Deps struct {
	Items  ItemEnumerator
	Native NativeWriter
}

// Pass runs one backfill pass. It enumerates compatibility items in keyset
// order, derives a native unit for each mapped item, and upserts the unit in
// one transaction. The pass is single-threaded. A unit failure does not stop
// the pass; the pass counts it and continues.
type Pass struct {
	deps Deps
	clk  clock.Clock
	now  time.Time
}

// New constructs a Pass that runs against the supplied store adapters. The
// clock supplies the run start time for DateCreated, DateModified, and
// LastVerified.
func New(deps Deps, clk clock.Clock) *Pass {
	return &Pass{deps: deps, clk: clk, now: clk.Now()}
}

// providerCatalogOrder is the fixed priority order for the six identity
// catalogs. First present catalog wins for identity resolution.
var providerCatalogOrder = []providerCatalogEntry{
	{key: "Tmdb", catalog: native.CatalogTMDB},
	{key: "Imdb", catalog: native.CatalogIMDb},
	{key: "Tvdb", catalog: native.CatalogTVDB},
	{key: "AniDB", catalog: native.CatalogAniDB},
	{key: "AniList", catalog: native.CatalogAniList},
	{key: "MyAnimeList", catalog: native.CatalogMAL},
}

type providerCatalogEntry struct {
	key     string
	catalog native.Catalog
}

// Run executes one pass and writes the JSON run report to out. It returns an
// error when the pass cannot enumerate items. A unit failure increments a
// counter and does not stop the pass. When any unit fails, Run returns a
// non-nil error so the CLI exits non-zero.
func (p *Pass) Run(ctx context.Context, out io.Writer) error {
	report := p.run(ctx)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("backfill: encode report: %w", err)
	}
	if report.Failed > 0 {
		return fmt.Errorf("backfill: %d unit(s) failed", report.Failed)
	}
	if report.EnumerationError != "" {
		return fmt.Errorf("backfill: enumeration failed: %s", report.EnumerationError)
	}
	return nil
}

func (p *Pass) run(ctx context.Context) Report {
	report := NewReport(p.now)
	var afterID dto.UUID
	for {
		if ctx.Err() != nil {
			report.Cancelled = true
			break
		}
		page, err := p.deps.Items.BackfillEnumerateItems(ctx, afterID, 256)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				report.Cancelled = true
				break
			}
			report.EnumerationError = err.Error()
			break
		}
		if len(page) == 0 {
			break
		}
		for _, it := range page {
			p.processItem(ctx, &report, it)
			afterID = it.ID
		}
		if len(page) < 256 {
			break
		}
	}
	report.EndTime = p.clk.Now()
	return report
}

func (p *Pass) processItem(ctx context.Context, report *Report, it item.Item) {
	group, mapped := store.BackfillKindGroupForItem(it)
	if !mapped {
		report.SkippedUnsupported++
		report.IncSkipByKind(it.Kind.String())
		return
	}
	if it.IsVirtualItem || it.IsPlaceHolder {
		report.SkippedVirtual++
		report.IncSkipByKind(it.Kind.String())
		return
	}

	switch it.Kind {
	case item.KindMovie:
		p.processFileItem(ctx, report, it, store.BackfillTargetAsset, group)
	case item.KindEpisode:
		p.processEpisode(ctx, report, it)
	case item.KindAudio:
		p.processAudio(ctx, report, it)
	case item.KindSeries, item.KindMusicAlbum, item.KindBoxSet:
		p.processWorkItem(ctx, report, it, group)
	}
}

// processFileItem handles a Movie, which is self-grouping: its unit creates the
// Work, default Edition, Release, Asset, and Inventory together.
func (p *Pass) processFileItem(ctx context.Context, report *Report, it item.Item, target store.BackfillTarget, group store.BackfillKindGroup) {
	_, complete := p.sourceRevision(ctx, it)
	if !complete {
		report.IncompleteSourceIdentity++
		return
	}
	derive := func(fresh item.Item) store.BackfillUnit {
		freshRev, _ := p.sourceRevision(ctx, fresh)
		return store.BackfillUnit{
			Item:           fresh,
			Target:         target,
			WorkID:         p.deps.Native.BackfillFreshUUIDForPass(),
			Identity:       p.identityForItem(fresh, group),
			SourceRevision: freshRev,
		}
	}
	unit := derive(it)
	p.upsertUnit(ctx, report, unit, derive)
}

// processEpisode groups an Episode to its Series Work through Item.SeriesID.
// When the Series Work does not exist yet (the child UUID sorts before the
// parent UUID), the pass creates the parent Work first, then attaches the
// child.
func (p *Pass) processEpisode(ctx context.Context, report *Report, it item.Item) {
	if it.SeriesID == nil || it.SeriesID.IsNil() {
		report.SkippedOrphanEpisode++
		return
	}
	series, err := p.deps.Items.Get(ctx, *it.SeriesID)
	if err != nil {
		report.SkippedOrphanEpisode++
		return
	}
	if series.Kind != item.KindSeries {
		report.SkippedOrphanEpisode++
		return
	}
	_, complete := p.sourceRevision(ctx, it)
	if !complete {
		report.IncompleteSourceIdentity++
		return
	}
	workID := p.resolveParentWorkID(ctx, report, *it.SeriesID)
	if workID.IsNil() {
		// The Series Work does not exist yet. The keyset enumeration orders
		// by UUID, and random UUIDs do not guarantee parent before child.
		// Create the parent Work now, then attach the child.
		p.processWorkItem(ctx, report, series, store.BackfillGroupSeries)
		workID = p.resolveParentWorkID(ctx, report, *it.SeriesID)
	}
	if workID.IsNil() {
		report.SkippedOrphanEpisode++
		return
	}
	derive := func(fresh item.Item) store.BackfillUnit {
		freshWorkID := p.resolveParentWorkID(ctx, report, *fresh.SeriesID)
		freshRev, _ := p.sourceRevision(ctx, fresh)
		return store.BackfillUnit{
			Item:              fresh,
			Target:            store.BackfillTargetAsset,
			WorkID:            freshWorkID,
			WorkOwnedByParent: true,
			SourceRevision:    freshRev,
		}
	}
	unit := derive(it)
	p.upsertUnit(ctx, report, unit, derive)
}

// processAudio groups an Audio item to its MusicAlbum Work through
// Item.ParentID. When the Album Work does not exist yet (the child UUID sorts
// before the parent UUID), the pass creates the parent Work first, then
// attaches the child.
func (p *Pass) processAudio(ctx context.Context, report *Report, it item.Item) {
	if it.ParentID == nil || it.ParentID.IsNil() {
		report.SkippedOrphanAudio++
		return
	}
	parent, err := p.deps.Items.Get(ctx, *it.ParentID)
	if err != nil {
		report.SkippedOrphanAudio++
		return
	}
	if parent.Kind != item.KindMusicAlbum {
		report.SkippedOrphanAudio++
		return
	}
	_, complete := p.sourceRevision(ctx, it)
	if !complete {
		report.IncompleteSourceIdentity++
		return
	}
	workID := p.resolveParentWorkID(ctx, report, *it.ParentID)
	if workID.IsNil() {
		// The Album Work does not exist yet. The keyset enumeration orders
		// by UUID, and random UUIDs do not guarantee parent before child.
		// Create the parent Work now, then attach the child.
		p.processWorkItem(ctx, report, parent, store.BackfillGroupMusic)
		workID = p.resolveParentWorkID(ctx, report, *it.ParentID)
	}
	if workID.IsNil() {
		report.SkippedOrphanAudio++
		return
	}
	derive := func(fresh item.Item) store.BackfillUnit {
		freshWorkID := p.resolveParentWorkID(ctx, report, *fresh.ParentID)
		freshRev, _ := p.sourceRevision(ctx, fresh)
		return store.BackfillUnit{
			Item:              fresh,
			Target:            store.BackfillTargetAsset,
			WorkID:            freshWorkID,
			WorkOwnedByParent: true,
			SourceRevision:    freshRev,
		}
	}
	unit := derive(it)
	p.upsertUnit(ctx, report, unit, derive)
}

// resolveParentWorkID reads the Work UUID for a parent item through its
// projection. It returns the nil UUID when no Work projection exists.
func (p *Pass) resolveParentWorkID(ctx context.Context, report *Report, parentID dto.UUID) dto.UUID {
	tx, err := p.deps.Native.BackfillReaderTx(ctx)
	if err != nil {
		report.Failed++
		return dto.NilUUID
	}
	defer func() { _ = tx.Rollback() }()
	workID, err := p.deps.Native.BackfillResolveWorkForItem(ctx, tx, parentID)
	if err != nil {
		report.Failed++
		return dto.NilUUID
	}
	if err := tx.Commit(); err != nil {
		report.Failed++
		return dto.NilUUID
	}
	return workID
}

// processWorkItem handles Series, MusicAlbum, and BoxSet, which produce a Work
// projection only.
func (p *Pass) processWorkItem(ctx context.Context, report *Report, it item.Item, group store.BackfillKindGroup) {
	derive := func(fresh item.Item) store.BackfillUnit {
		return store.BackfillUnit{
			Item:     fresh,
			Target:   store.BackfillTargetWork,
			WorkID:   p.deps.Native.BackfillFreshUUIDForPass(),
			Identity: p.identityForItem(fresh, group),
		}
	}
	unit := derive(it)
	p.upsertUnit(ctx, report, unit, derive)
}

// sourceRevision captures the compatibility source revision for a file-bearing
// item. It returns the revision and whether the source has a complete inode and
// change-time identity.
func (p *Pass) sourceRevision(ctx context.Context, it item.Item) (string, bool) {
	if !hasCompleteSourceIdentity(it) {
		return "", false
	}
	snap, err := p.deps.Items.DerivedSourceSnapshot(ctx, it.ID)
	if err != nil {
		return "", false
	}
	return snap.SourceRevision, snap.SourceRevision != ""
}

func hasCompleteSourceIdentity(it item.Item) bool {
	return it.FileInode != nil && it.FileChangedAt != nil
}

// identityForItem resolves the identity mappings for an item from its
// ProviderIDs, restricted to the six catalogs in the fixed priority order.
func (p *Pass) identityForItem(it item.Item, group store.BackfillKindGroup) []native.IdentityMapping {
	var out []native.IdentityMapping
	for _, e := range providerCatalogOrder {
		if v, ok := it.ProviderIDs.Get(e.key); ok && v != "" {
			out = append(out, native.IdentityMapping{
				Catalog:      e.catalog,
				ExternalID:   v,
				LastVerified: p.now,
			})
		}
	}
	return out
}

// upsertUnit selects the existing native row for the item, verifies it belongs
// to the expected kind group, and upserts the unit. It handles the stale-source
// retry once. The pass counts a unit as upserted only when it created or
// updated a native row.
//
// derive re-builds the full unit from a fresh item. On a stale-source retry
// the pass re-reads the item, then calls derive to re-derive identity, target,
// WorkID, parent relation, WorkOwnedByParent, and source revision from the
// fresh facts. This prevents stale values from the original enumeration from
// persisting into the native graph.
func (p *Pass) upsertUnit(ctx context.Context, report *Report, unit store.BackfillUnit, derive func(item.Item) store.BackfillUnit) {
	selectedID, res, retry, err := p.selectAndUpsert(ctx, report, unit)
	if err != nil {
		if retry && errors.Is(err, store.ErrStaleSourceRevision) {
			if unit.Target == store.BackfillTargetAsset && unit.SourceRevision != "" {
				// Re-read the item, then re-derive the complete unit from
				// the fresh facts. The item may have changed path, device,
				// inode, provider identity, aggregate target, WorkID, or
				// parent ownership between the enumeration snapshot and the
				// retry. derive re-builds all derived fields from the fresh
				// item so no stale value from the original enumeration
				// persists.
				freshItem, iErr := p.deps.Items.Get(ctx, unit.Item.ID)
				if iErr != nil {
					report.Failed++
					return
				}
				unit = derive(freshItem)
				if unit.SourceRevision == "" {
					report.StaleRetryExhausted++
					return
				}
			}
			retryRes, rErr := p.deps.Native.BackfillUpsertUnit(ctx, unit, selectedID, p.now)
			if rErr != nil {
				if errors.Is(rErr, store.ErrStaleSourceRevision) {
					report.StaleRetryExhausted++
					return
				}
				report.Failed++
				return
			}
			if retryRes.Created || retryRes.Updated {
				report.UnitsUpserted++
			}
			if retryRes.IdentityMerge {
				report.IdentityMerge++
			}
			if retryRes.IdentityCollisionSkipped {
				report.IdentityCollisionSkipped++
			}
			if retryRes.RegroupSkipped {
				report.RegroupSkipped++
			}
			report.StaleRetried++
			return
		}
		report.Failed++
		return
	}
	if res.Created || res.Updated {
		report.UnitsUpserted++
	}
	if res.IdentityMerge {
		report.IdentityMerge++
	}
	if res.IdentityCollisionSkipped {
		report.IdentityCollisionSkipped++
	}
	if res.RegroupSkipped {
		report.RegroupSkipped++
	}
}

// selectAndUpsert reads the projection rows for the item inside a reader
// snapshot, applies the target-class filter and the smallest-native-UUID rule,
// verifies the selected row belongs to the expected kind group, and upserts the
// unit. It returns the selected native ID, whether a stale-source retry is
// allowed, and the error.
func (p *Pass) selectAndUpsert(ctx context.Context, report *Report, unit store.BackfillUnit) (dto.UUID, store.BackfillUnitResult, bool, error) {
	tx, err := p.deps.Native.BackfillReaderTx(ctx)
	if err != nil {
		return dto.NilUUID, store.BackfillUnitResult{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := p.deps.Native.BackfillProjectionsForItem(ctx, tx, unit.Item.ID)
	if err != nil {
		return dto.NilUUID, store.BackfillUnitResult{}, false, err
	}
	filtered, multiple, err := p.deps.Native.BackfillFilterProjectionsByTargetClass(ctx, tx, ids, unit.Target)
	if err != nil {
		return dto.NilUUID, store.BackfillUnitResult{}, false, err
	}
	if multiple {
		report.MultipleProjectionAnomaly++
	}
	selected := store.BackfillSelectedProjection(filtered)

	if !selected.IsNil() {
		grp, ok, gErr := p.deps.Native.BackfillWorkKindGroup(ctx, tx, selected)
		if gErr != nil {
			return dto.NilUUID, store.BackfillUnitResult{}, false, gErr
		}
		expectedGroup, _ := store.BackfillKindGroupForItem(unit.Item)
		if ok && grp != expectedGroup {
			report.IdentityCollisionSkipped++
			unit.WorkID = p.deps.Native.BackfillFreshUUIDForPass()
			unit.Identity = nil
			selected = dto.NilUUID
		}
	}

	if err := tx.Commit(); err != nil {
		return dto.NilUUID, store.BackfillUnitResult{}, false, err
	}

	res, err := p.deps.Native.BackfillUpsertUnit(ctx, unit, selected, p.now)
	if err != nil {
		return selected, store.BackfillUnitResult{}, true, err
	}
	return selected, res, false, nil
}
