// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/backfill"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/native"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestBackfillSQLite runs the backfill conformance suite on SQLite.
func TestBackfillSQLite(t *testing.T) {
	runBackfillConformance(t, openBackfillSQLite)
}

// TestBackfillPostgres runs the backfill conformance suite on PostgreSQL.
func TestBackfillPostgres(t *testing.T) {
	runBackfillConformance(t, openBackfillPostgres)
}

func openBackfillSQLite(t *testing.T) nativeBackfillFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite backfill fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite backfill fixture: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	ns := store.NewNativeStore(stores, clock.NewFake(backfillTestTime()))
	return nativeBackfillFixture{items: items, native: ns, stores: stores}
}

func openBackfillPostgres(t *testing.T) nativeBackfillFixture {
	t.Helper()
	stores := storetest.OpenPostgres(t)
	items := store.NewItemStore(stores)
	ns := store.NewNativeStore(stores, clock.NewFake(backfillTestTime()))
	return nativeBackfillFixture{items: items, native: ns, stores: stores}
}

type nativeBackfillFixture struct {
	items  *store.ItemStore
	native *store.NativeStore
	stores store.Stores
}

func backfillTestTime() time.Time {
	return time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
}

func newBackfillPass(f nativeBackfillFixture) *backfill.Pass {
	return backfill.New(backfill.Deps{Items: f.items, Native: f.native}, clock.NewFake(backfillTestTime()))
}

// staleInjectingNative wraps a NativeWriter so that the first
// BackfillUpsertUnit call for the target item modifies the item's source
// facts before the writer transaction runs. This forces a stale
// compatibility CAS: the pass captured the original revision in the reader
// snapshot, but the writer reads a different revision. The pass retries
// once, re-reading the now-modified item and re-deriving the full unit.
type staleInjectingNative struct {
	backfill.NativeWriter
	t        *testing.T
	items    *store.ItemStore
	targetID dto.UUID
	mutate   func(item.Item) item.Item
	fired    bool
}

func (w *staleInjectingNative) BackfillUpsertUnit(ctx context.Context, unit store.BackfillUnit, selectedID dto.UUID, now time.Time) (store.BackfillUnitResult, error) {
	if !w.fired && unit.Item.ID == w.targetID {
		w.fired = true
		it := getItem(w.t, ctx, w.items, w.targetID)
		it = w.mutate(it)
		mustUpsert(w.t, w.items, it)
	}
	return w.NativeWriter.BackfillUpsertUnit(ctx, unit, selectedID, now)
}

func runBackfillConformance(t *testing.T, open func(*testing.T) nativeBackfillFixture) {
	t.Helper()
	ctx := t.Context()

	// Case 1: seeded library backfills to exact counts.
	t.Run("seeded_library_backfills", func(t *testing.T) {
		f := open(t)
		seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		assertCount(t, f, "native_works", 4)
		assertCount(t, f, "native_editions", 3)
		assertCount(t, f, "native_releases", 5)
		assertCount(t, f, "native_assets", 5)
		assertCount(t, f, "native_inventories", 5)
		assertCount(t, f, "native_item_projections", 8)
		assertCount(t, f, "native_identity_mappings", 1)
	})

	// Case 2: immediate rerun performs zero writes.
	t.Run("rerun_zero_writes", func(t *testing.T) {
		f := open(t)
		seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		before := snapshotNativeTimestamps(t, ctx, f)
		report := runPassReport(t, ctx, f)
		if report.UnitsUpserted != 0 {
			t.Fatalf("rerun upserted %d units, want 0", report.UnitsUpserted)
		}
		if report.Failed != 0 {
			t.Fatalf("rerun failed %d units, want 0", report.Failed)
		}
		after := snapshotNativeTimestamps(t, ctx, f)
		if !timestampsEqual(before, after) {
			t.Fatalf("rerun changed native DateModified values")
		}
	})

	// Case 3: a changed complete-source item updates only its Asset chain.
	t.Run("changed_episode_updates_only_its_asset", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		episode := getItem(t, ctx, f.items, ids.episode1)
		newInode := int64(0x9999)
		episode.FileInode = &newInode
		if err := f.items.Upsert(ctx, episode); err != nil {
			t.Fatalf("upsert changed episode: %v", err)
		}

		before := snapshotNativeTimestamps(t, ctx, f)
		advancedTime := backfillTestTime().Add(time.Hour)
		pass := backfill.New(backfill.Deps{Items: f.items, Native: f.native}, clock.NewFake(advancedTime))
		var out bytes.Buffer
		if err := pass.Run(ctx, &out); err != nil {
			t.Fatalf("backfill pass: %v\noutput: %s", err, out.String())
		}
		after := snapshotNativeTimestamps(t, ctx, f)

		// Exactly one Asset and its projection changed. No Work, Edition,
		// or Inventory DateModified changed.
		changed := timestampDiffCount(before, after)
		if changed != 1 {
			t.Fatalf("rerun changed %d native rows, want exactly 1 (the changed Asset)", changed)
		}
	})

	// Case 4: two movie items with the same TMDB ID resolve to one Work and
	// the identity-merge counter is exactly one. An immediate rerun performs
	// zero native writes and leaves every DateModified unchanged.
	t.Run("two_movies_same_tmdb_one_work", func(t *testing.T) {
		f := open(t)
		seedTwoMoviesSameTMDB(t, f)
		report := runPassReport(t, ctx, f)

		assertCount(t, f, "native_works", 1)
		count, err := f.native.IdentityMappingCount(ctx, native.CatalogTMDB, "550")
		if err != nil {
			t.Fatalf("identity mapping count: %v", err)
		}
		if count != 1 {
			t.Fatalf("identity mapping count = %d, want 1", count)
		}
		if report.IdentityMerge != 1 {
			t.Fatalf("identity_merge = %d, want 1", report.IdentityMerge)
		}
		if report.IdentityCollisionSkipped != 0 {
			t.Fatalf("identity_collision_skipped = %d, want 0", report.IdentityCollisionSkipped)
		}

		// Immediate rerun performs zero native writes. The shared Work
		// title must not toggle between the two movie names on rerun.
		before := snapshotNativeTimestamps(t, ctx, f)
		rerun := runPassReport(t, ctx, f)
		if rerun.UnitsUpserted != 0 {
			t.Fatalf("rerun upserted %d units, want 0", rerun.UnitsUpserted)
		}
		if rerun.Failed != 0 {
			t.Fatalf("rerun failed %d units, want 0", rerun.Failed)
		}
		after := snapshotNativeTimestamps(t, ctx, f)
		if !timestampsEqual(before, after) {
			t.Fatalf("rerun changed native DateModified values for merged Work")
		}
	})

	// Case 5: cancellation leaves compatibility items unchanged.
	t.Run("cancellation_leaves_compatibility_unchanged", func(t *testing.T) {
		f := open(t)
		seedBackfillLibrary(t, f)

		before := compatibilityChecksum(t, ctx, f)

		cancelCtx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancel()
		}()
		pass := backfill.New(backfill.Deps{Items: f.items, Native: f.native}, clock.NewFake(backfillTestTime()))
		var out bytes.Buffer
		_ = pass.Run(cancelCtx, &out)
		wg.Wait()

		after := compatibilityChecksum(t, ctx, f)
		if before != after {
			t.Fatalf("cancellation changed the compatibility item table")
		}

		runPass(t, ctx, f)
		assertCount(t, f, "native_works", 4)
	})

	// Case 6: a stale projection write surfaces as a retried unit. The
	// report must show StaleRetried=1.
	t.Run("stale_projection_retries_unit", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		// Inject a source change between the reader snapshot and the writer
		// transaction for episode1. The pass captures the original revision,
		// but the writer reads a different revision, so the CAS fails. The
		// pass retries once with the fresh snapshot and succeeds.
		newInode := int64(0x8888)
		newChanged := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
		wrapped := &staleInjectingNative{
			NativeWriter: f.native,
			t:            t,
			items:        f.items,
			targetID:     ids.episode1,
			mutate: func(it item.Item) item.Item {
				it.FileInode = &newInode
				it.FileChangedAt = &newChanged
				return it
			},
		}
		pass := backfill.New(backfill.Deps{Items: f.items, Native: wrapped}, clock.NewFake(backfillTestTime()))
		var out bytes.Buffer
		if err := pass.Run(ctx, &out); err != nil {
			t.Fatalf("backfill pass: %v\noutput: %s", err, out.String())
		}
		var report backfill.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("decode report: %v\noutput: %s", err, out.String())
		}
		if report.StaleRetried != 1 {
			t.Fatalf("stale_retried = %d, want 1", report.StaleRetried)
		}
		if report.Failed != 0 {
			t.Fatalf("stale test failed %d units, want 0", report.Failed)
		}
		if report.StaleRetryExhausted != 0 {
			t.Fatalf("stale retry exhausted = %d, want 0", report.StaleRetryExhausted)
		}
	})

	// P1 store/cross-kind-external-identity-merges: a Movie and a Series
	// that share one external ID create two Works and one collision. An
	// immediate rerun performs zero native writes and leaves ownership and
	// identity mappings unchanged.
	t.Run("cross_kind_external_identity", func(t *testing.T) {
		f := open(t)
		seedCrossKindIdentity(t, f)
		report := runPassReport(t, ctx, f)

		// First pass: two Works and two projections, one collision.
		assertCount(t, f, "native_works", 2)
		assertCount(t, f, "native_item_projections", 2)
		if report.IdentityCollisionSkipped != 1 {
			t.Fatalf("identity_collision_skipped = %d, want 1", report.IdentityCollisionSkipped)
		}
		if report.IdentityMerge != 0 {
			t.Fatalf("identity_merge = %d, want 0", report.IdentityMerge)
		}

		// Immediate rerun performs zero native writes. The cross-kind
		// collision must not create a fresh Work on every rerun.
		before := snapshotNativeTimestamps(t, ctx, f)
		rerun := runPassReport(t, ctx, f)
		if rerun.UnitsUpserted != 0 {
			t.Fatalf("rerun upserted %d units, want 0", rerun.UnitsUpserted)
		}
		if rerun.Failed != 0 {
			t.Fatalf("rerun failed %d units, want 0", rerun.Failed)
		}
		after := snapshotNativeTimestamps(t, ctx, f)
		if !timestampsEqual(before, after) {
			t.Fatalf("rerun changed native DateModified values")
		}
		// The rerun keeps two Works and two projections. The identity
		// mapping stays on the original-kind Work (the movie), and the
		// fresh-kind Work (the series) keeps no mapping.
		assertCount(t, f, "native_works", 2)
		assertCount(t, f, "native_item_projections", 2)
		mappingCount, err := f.native.IdentityMappingCount(ctx, native.CatalogTMDB, "42")
		if err != nil {
			t.Fatalf("identity mapping count: %v", err)
		}
		if mappingCount != 1 {
			t.Fatalf("identity mapping count = %d, want 1", mappingCount)
		}
	})

	// P1 backfill/child-unit-mutates-parent-work: an identified parent
	// Work survives a child pass with no title, state, provider ID,
	// identity mapping, or DateModified change.
	t.Run("identified_parent_survives_child", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		// Read the Series Work before the child pass: title, state,
		// provider IDs, identity mappings, and DateModified.
		workID := resolveParentWorkID(t, ctx, f, ids.series)
		beforeWork, err := f.native.GetWork(ctx, workID)
		if err != nil {
			t.Fatalf("get work before child pass: %v", err)
		}
		before := snapshotNativeTimestamps(t, ctx, f)

		// Re-run the pass. The child must not mutate the parent Work.
		runPass(t, ctx, f)

		after := snapshotNativeTimestamps(t, ctx, f)
		if !timestampsEqual(before, after) {
			t.Fatalf("child pass changed parent Work DateModified")
		}

		afterWork, err := f.native.GetWork(ctx, workID)
		if err != nil {
			t.Fatalf("get work after child pass: %v", err)
		}
		if afterWork.Title != beforeWork.Title {
			t.Fatalf("series work title changed: %q -> %q", beforeWork.Title, afterWork.Title)
		}
		if afterWork.Title != "Test Series" {
			t.Fatalf("series work title = %q, want %q", afterWork.Title, "Test Series")
		}
		if afterWork.State != beforeWork.State {
			t.Fatalf("series work state changed: %q -> %q", beforeWork.State, afterWork.State)
		}
		if afterWork.State != native.CatalogUnidentified {
			t.Fatalf("series work state = %q, want %q", afterWork.State, native.CatalogUnidentified)
		}
		if len(afterWork.ProviderIDs) != len(beforeWork.ProviderIDs) {
			t.Fatalf("series work provider IDs changed: %d -> %d", len(beforeWork.ProviderIDs), len(afterWork.ProviderIDs))
		}
		if len(afterWork.ProviderIDs) != 0 {
			t.Fatalf("series work has %d provider IDs, want 0", len(afterWork.ProviderIDs))
		}
	})

	// P1 backfill/child-order-assumes-parent-uuid: a child whose UUID
	// sorts before its parent UUID creates the complete child graph.
	t.Run("child_uuid_before_parent", func(t *testing.T) {
		f := open(t)
		seedChildBeforeParent(t, f)
		report := runPassReport(t, ctx, f)

		// One Work for the Series, one Episode with an Asset projection.
		assertCount(t, f, "native_works", 1)
		assertCount(t, f, "native_assets", 1)
		assertCount(t, f, "native_item_projections", 2)
		if report.SkippedOrphanEpisode != 0 {
			t.Fatalf("orphan episode = %d, want 0", report.SkippedOrphanEpisode)
		}
	})

	// P1 backfill/source-retry-keeps-stale-item-facts: when the source
	// changes between the reader snapshot and the writer transaction, the
	// retry re-derives the complete unit from the fresh item. The fresh
	// item changes source facts and provider identity, so the retry must
	// write only fresh identity, source revision, target, and WorkID.
	t.Run("stale_source_refreshes_all_facts", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		// The movie carries a TMDB identity. The stale-injection wrapper
		// changes the movie's source facts (inode, change-time, path) and
		// its provider identity (Tmdb 550 -> Tmdb 999) before the first
		// writer transaction for that item. The reader snapshot still has
		// the original revision, so the compatibility CAS fails. The retry
		// re-reads the now-modified item, re-derives the identity and
		// source revision, and writes only fresh facts.
		newInode := int64(0x7777)
		newChanged := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
		newPath := "/library/renamed-movie.mkv"
		wrapped := &staleInjectingNative{
			NativeWriter: f.native,
			t:            t,
			items:        f.items,
			targetID:     ids.movie,
			mutate: func(it item.Item) item.Item {
				it.FileInode = &newInode
				it.FileChangedAt = &newChanged
				it.Path = &newPath
				it.ProviderIDs = providerIDs("Tmdb", "999")
				return it
			},
		}
		pass := backfill.New(backfill.Deps{Items: f.items, Native: wrapped}, clock.NewFake(backfillTestTime()))
		var out bytes.Buffer
		if err := pass.Run(ctx, &out); err != nil {
			t.Fatalf("backfill pass: %v\noutput: %s", err, out.String())
		}
		var report backfill.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("decode report: %v\noutput: %s", err, out.String())
		}
		if report.StaleRetried != 1 {
			t.Fatalf("stale_retried = %d, want 1", report.StaleRetried)
		}
		if report.Failed != 0 {
			t.Fatalf("failed = %d, want 0", report.Failed)
		}
		if report.StaleRetryExhausted != 0 {
			t.Fatalf("stale_retry_exhausted = %d, want 0", report.StaleRetryExhausted)
		}

		// The Asset must carry the fresh path and inode, not the stale
		// ones from the original enumeration.
		assetID := resolveAssetProjection(t, ctx, f, ids.movie)
		asset := nativeAssetFacts(t, ctx, f, assetID)
		if asset.Path != newPath {
			t.Fatalf("asset path = %q, want %q (fresh)", asset.Path, newPath)
		}
		if asset.Inode != uint64(newInode) {
			t.Fatalf("asset inode = %d, want %d (fresh)", asset.Inode, newInode)
		}

		// The Work must carry the fresh identity (Tmdb 999), not the
		// stale identity (Tmdb 550). Resolve the Work through the Asset
		// chain: Asset -> Release -> Edition -> Work.
		workID := resolveWorkFromAsset(t, ctx, f, assetID)
		work, err := f.native.GetWork(ctx, workID)
		if err != nil {
			t.Fatalf("get work: %v", err)
		}
		var hasFresh, hasStale bool
		for _, m := range work.ProviderIDs {
			if m.Catalog == native.CatalogTMDB && m.ExternalID == "999" {
				hasFresh = true
			}
			if m.Catalog == native.CatalogTMDB && m.ExternalID == "550" {
				hasStale = true
			}
		}
		if !hasFresh {
			t.Fatalf("work has no fresh identity (Tmdb 999)")
		}
		if hasStale {
			t.Fatalf("work still has stale identity (Tmdb 550)")
		}
	})

	// P1 report/required-counters-never-change: the identity-merge and
	// regroup-skipped counters report exact counts.
	t.Run("identity_merge_counter", func(t *testing.T) {
		f := open(t)
		seedTwoMoviesSameTMDB(t, f)
		report := runPassReport(t, ctx, f)
		if report.IdentityMerge != 1 {
			t.Fatalf("identity_merge = %d, want 1", report.IdentityMerge)
		}
	})

	t.Run("regroup_skipped_counter", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		// Re-parent the episode to a different series.
		newSeries := item.Item{
			ID:   contractUUID(t, 2001),
			Kind: item.KindSeries,
			Name: stringPtr("New Series"),
		}
		mustUpsert(t, f.items, newSeries)
		episode := getItem(t, ctx, f.items, ids.episode1)
		episode.SeriesID = &newSeries.ID
		episode.ParentID = &newSeries.ID
		if err := f.items.Upsert(ctx, episode); err != nil {
			t.Fatalf("upsert re-parented episode: %v", err)
		}
		report := runPassReport(t, ctx, f)
		if report.RegroupSkipped != 1 {
			t.Fatalf("regroup_skipped = %d, want 1", report.RegroupSkipped)
		}
	})

	// P1 backfill/projection-cardinality: an item with multiple projection
	// rows after the target-class filter uses the deterministic smallest-
	// native-UUID selection. The selected row is reused on rerun. Unselected
	// rows remain unchanged. The report records the anomaly counter.
	t.Run("multiple_projections", func(t *testing.T) {
		f := open(t)
		ids := seedBackfillLibrary(t, f)
		runPass(t, ctx, f)

		// The movie already has one Asset projection. Insert a second
		// Asset chain (Work, Edition, Release, Asset) and point its
		// projection at the same movie item. The pass must select the
		// lexicographically smallest native UUID, retain the other row,
		// and increment the anomaly counter.
		originalAssetID := resolveAssetProjection(t, ctx, f, ids.movie)

		// Create a second Asset chain. The pass must select the
		// lexicographically smallest native UUID between the original and
		// the extra, retain the other row, and increment the anomaly
		// counter.
		extraWorkID := contractUUID(t, 9001)
		extraEditionID := contractUUID(t, 9002)
		extraReleaseID := contractUUID(t, 9003)
		extraAssetID := contractUUID(t, 9004)
		now := backfillTestTime()
		if err := f.native.CreateWork(ctx, native.Work{
			ID:           extraWorkID,
			State:        native.CatalogUnidentified,
			Title:        "Extra Work",
			DateCreated:  now,
			DateModified: now,
		}); err != nil {
			t.Fatalf("create extra work: %v", err)
		}
		if err := f.native.CreateEdition(ctx, native.Edition{
			ID:           extraEditionID,
			WorkID:       extraWorkID,
			DateCreated:  now,
			DateModified: now,
		}); err != nil {
			t.Fatalf("create extra edition: %v", err)
		}
		if err := f.native.CreateRelease(ctx, native.Release{
			ID:               extraReleaseID,
			EditionID:        extraEditionID,
			AcquisitionState: native.AcquisitionAcquired,
			DateCreated:      now,
			DateModified:     now,
		}); err != nil {
			t.Fatalf("create extra release: %v", err)
		}
		if err := f.native.CreateAsset(ctx, native.Asset{
			ID:             extraAssetID,
			ReleaseID:      extraReleaseID,
			Path:           "/library/extra.mkv",
			SourceRevision: "",
			DateCreated:    now,
			DateModified:   now,
		}); err != nil {
			t.Fatalf("create extra asset: %v", err)
		}
		if err := f.native.SetItemProjection(ctx, extraAssetID, ids.movie, ""); err != nil {
			t.Fatalf("set extra projection: %v", err)
		}

		report := runPassReport(t, ctx, f)
		if report.MultipleProjectionAnomaly != 1 {
			t.Fatalf("multiple_projection_anomaly = %d, want 1", report.MultipleProjectionAnomaly)
		}

		// Both projection rows survive: the pass never deletes an
		// unselected row.
		assertCount(t, f, "native_item_projections", 9)

		// The selected projection is the lexicographically smallest
		// native UUID. Determine which one is smaller.
		wantSelected := originalAssetID
		if compareTestUUID(extraAssetID, originalAssetID) < 0 {
			wantSelected = extraAssetID
		}
		selected := resolveAssetProjection(t, ctx, f, ids.movie)
		if selected != wantSelected {
			t.Fatalf("selected projection %s, want smallest %s (original %s, extra %s)", selected, wantSelected, originalAssetID, extraAssetID)
		}

		// Immediate rerun keeps the same selection and does not delete the
		// unselected row.
		before := snapshotNativeTimestamps(t, ctx, f)
		rerun := runPassReport(t, ctx, f)
		if rerun.UnitsUpserted != 0 {
			t.Fatalf("rerun upserted %d units, want 0", rerun.UnitsUpserted)
		}
		after := snapshotNativeTimestamps(t, ctx, f)
		if !timestampsEqual(before, after) {
			t.Fatalf("rerun changed native DateModified values")
		}
		assertCount(t, f, "native_item_projections", 9)
		selected2 := resolveAssetProjection(t, ctx, f, ids.movie)
		if selected2 != selected {
			t.Fatalf("selected projection changed on rerun: %s -> %s", selected, selected2)
		}
	})
}

// nativeBackfillSeedIDs records the UUIDs of the seeded library.
type nativeBackfillSeedIDs struct {
	movie    dto.UUID
	series   dto.UUID
	episode1 dto.UUID
	episode2 dto.UUID
	album    dto.UUID
	track1   dto.UUID
	track2   dto.UUID
	boxset   dto.UUID
	folder   dto.UUID
	virtEps  dto.UUID
}

func seedBackfillLibrary(t *testing.T, f nativeBackfillFixture) nativeBackfillSeedIDs {
	t.Helper()
	ids := nativeBackfillSeedIDs{
		movie:    contractUUID(t, 1001),
		series:   contractUUID(t, 1002),
		episode1: contractUUID(t, 1003),
		episode2: contractUUID(t, 1004),
		album:    contractUUID(t, 1005),
		track1:   contractUUID(t, 1006),
		track2:   contractUUID(t, 1007),
		boxset:   contractUUID(t, 1008),
		folder:   contractUUID(t, 1009),
		virtEps:  contractUUID(t, 1010),
	}

	device, inode := int64(0x1000), int64(0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)

	movie := item.Item{
		ID:            ids.movie,
		Kind:          item.KindMovie,
		Name:          stringPtr("Test Movie"),
		Path:          stringPtr("/library/test-movie.mkv"),
		FileDevice:    &device,
		FileInode:     &inode,
		FileChangedAt: &changed,
		ProviderIDs:   providerIDs("Tmdb", "550"),
	}
	mustUpsert(t, f.items, movie)

	series := item.Item{
		ID:   ids.series,
		Kind: item.KindSeries,
		Name: stringPtr("Test Series"),
	}
	mustUpsert(t, f.items, series)

	for i, id := range []dto.UUID{ids.episode1, ids.episode2} {
		ep := item.Item{
			ID:            id,
			Kind:          item.KindEpisode,
			ParentID:      &ids.series,
			SeriesID:      &ids.series,
			Name:          stringPtr("Test Episode"),
			Path:          stringPtr("/library/test-episode-" + dtoToHex(id) + ".mkv"),
			FileDevice:    &device,
			FileInode:     ptrInt64(inode + int64(i)),
			FileChangedAt: &changed,
		}
		mustUpsert(t, f.items, ep)
	}

	album := item.Item{
		ID:   ids.album,
		Kind: item.KindMusicAlbum,
		Name: stringPtr("Test Album"),
	}
	mustUpsert(t, f.items, album)

	for i, id := range []dto.UUID{ids.track1, ids.track2} {
		track := item.Item{
			ID:            id,
			Kind:          item.KindAudio,
			ParentID:      &ids.album,
			Name:          stringPtr("Test Track"),
			Path:          stringPtr("/library/test-track-" + dtoToHex(id) + ".flac"),
			FileDevice:    &device,
			FileInode:     ptrInt64(inode + int64(i) + 100),
			FileChangedAt: &changed,
		}
		mustUpsert(t, f.items, track)
	}

	boxset := item.Item{
		ID:   ids.boxset,
		Kind: item.KindBoxSet,
		Name: stringPtr("Test BoxSet"),
	}
	mustUpsert(t, f.items, boxset)

	folder := item.Item{
		ID:       ids.folder,
		Kind:     item.KindFolder,
		Name:     stringPtr("Test Folder"),
		IsFolder: true,
	}
	mustUpsert(t, f.items, folder)

	virtEps := item.Item{
		ID:            ids.virtEps,
		Kind:          item.KindEpisode,
		ParentID:      &ids.series,
		SeriesID:      &ids.series,
		Name:          stringPtr("Virtual Episode"),
		IsVirtualItem: true,
	}
	mustUpsert(t, f.items, virtEps)

	return ids
}

func seedTwoMoviesSameTMDB(t *testing.T, f nativeBackfillFixture) {
	t.Helper()
	device, inode := int64(0x1000), int64(0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		movie := item.Item{
			ID:            contractUUID(t, 2001+i),
			Kind:          item.KindMovie,
			Name:          stringPtr("Same TMDB Movie"),
			Path:          stringPtr("/library/same-tmdb-" + itoa(i) + ".mkv"),
			FileDevice:    &device,
			FileInode:     ptrInt64(inode + int64(i)),
			FileChangedAt: &changed,
			ProviderIDs:   providerIDs("Tmdb", "550"),
		}
		mustUpsert(t, f.items, movie)
	}
}

// seedCrossKindIdentity seeds a Movie and a Series that share the same TMDB
// external ID. The backfill must not merge them into one Work.
func seedCrossKindIdentity(t *testing.T, f nativeBackfillFixture) {
	t.Helper()
	device, inode := int64(0x1000), int64(0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)

	movie := item.Item{
		ID:            contractUUID(t, 3001),
		Kind:          item.KindMovie,
		Name:          stringPtr("Cross Kind Movie"),
		Path:          stringPtr("/library/cross-kind-movie.mkv"),
		FileDevice:    &device,
		FileInode:     &inode,
		FileChangedAt: &changed,
		ProviderIDs:   providerIDs("Tmdb", "42"),
	}
	mustUpsert(t, f.items, movie)

	series := item.Item{
		ID:          contractUUID(t, 3002),
		Kind:        item.KindSeries,
		Name:        stringPtr("Cross Kind Series"),
		ProviderIDs: providerIDs("Tmdb", "42"),
	}
	mustUpsert(t, f.items, series)
}

// seedChildBeforeParent seeds a Series and an Episode where the Episode UUID
// sorts before the Series UUID. The pass must create the parent Work when the
// child is processed first.
func seedChildBeforeParent(t *testing.T, f nativeBackfillFixture) {
	t.Helper()
	// The episode UUID sorts before the series UUID so the keyset
	// enumeration processes the child first. The pass must create the
	// parent Work and then attach the child.
	episodeID := contractUUID(t, 1)
	seriesID := contractUUID(t, 2)
	device, inode := int64(0x1000), int64(0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)

	series := item.Item{
		ID:   seriesID,
		Kind: item.KindSeries,
		Name: stringPtr("Parent Series"),
	}
	mustUpsert(t, f.items, series)

	episode := item.Item{
		ID:            episodeID,
		Kind:          item.KindEpisode,
		ParentID:      &seriesID,
		SeriesID:      &seriesID,
		Name:          stringPtr("Child Episode"),
		Path:          stringPtr("/library/child-episode.mkv"),
		FileDevice:    &device,
		FileInode:     &inode,
		FileChangedAt: &changed,
	}
	mustUpsert(t, f.items, episode)
}

func runPass(t *testing.T, ctx context.Context, f nativeBackfillFixture) {
	t.Helper()
	var out bytes.Buffer
	if err := newBackfillPass(f).Run(ctx, &out); err != nil {
		t.Fatalf("backfill pass: %v\noutput: %s", err, out.String())
	}
}

func runPassReport(t *testing.T, ctx context.Context, f nativeBackfillFixture) backfill.Report {
	t.Helper()
	var out bytes.Buffer
	_ = newBackfillPass(f).Run(ctx, &out)
	var r backfill.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("decode report: %v\noutput: %s", err, out.String())
	}
	return r
}

func mustUpsert(t *testing.T, items *store.ItemStore, it item.Item) {
	t.Helper()
	if err := items.Upsert(t.Context(), it); err != nil {
		t.Fatalf("upsert %s %s: %v", it.Kind, it.ID, err)
	}
}

func getItem(t *testing.T, ctx context.Context, items *store.ItemStore, id dto.UUID) item.Item {
	t.Helper()
	got, err := items.Get(ctx, id)
	if err != nil {
		t.Fatalf("get item %s: %v", id, err)
	}
	return got
}

func assertCount(t *testing.T, f nativeBackfillFixture, table string, want int) {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM " + table
	row := f.stores.Reader().QueryRowContext(t.Context(), query)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}

func compatibilityChecksum(t *testing.T, ctx context.Context, f nativeBackfillFixture) string {
	t.Helper()
	// Count rows in the items table and every satellite table. A
	// byte-identical comparison uses the row count plus the set of item IDs
	// and all satellite values.
	rows, err := f.stores.Reader().QueryContext(ctx,
		"SELECT id, kind, path, name, file_inode, file_changed_at, file_device FROM items ORDER BY id")
	if err != nil {
		t.Fatalf("query items: %v", err)
	}
	defer rows.Close()
	var buf bytes.Buffer
	for rows.Next() {
		var id, kind, path, name, inode, changed, device any
		if err := rows.Scan(&id, &kind, &path, &name, &inode, &changed, &device); err != nil {
			t.Fatalf("scan item: %v", err)
		}
		buf.WriteString(fmt.Sprintf("%v|%v|%v|%v|%v|%v|%v\n", id, kind, path, name, inode, changed, device))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	// Include provider-IDs satellite rows.
	prows, err := f.stores.Reader().QueryContext(ctx,
		"SELECT item_id, provider, value FROM provider_ids ORDER BY item_id, provider")
	if err != nil {
		t.Fatalf("query provider ids: %v", err)
	}
	defer prows.Close()
	for prows.Next() {
		var itemID, key, value any
		if err := prows.Scan(&itemID, &key, &value); err != nil {
			t.Fatalf("scan provider id: %v", err)
		}
		buf.WriteString(fmt.Sprintf("pid|%v|%v|%v\n", itemID, key, value))
	}
	if err := prows.Err(); err != nil {
		t.Fatalf("provider rows err: %v", err)
	}
	return buf.String()
}

func snapshotNativeTimestamps(t *testing.T, ctx context.Context, f nativeBackfillFixture) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, table := range []string{"native_works", "native_editions", "native_releases", "native_assets", "native_inventories"} {
		rows, err := f.stores.Reader().QueryContext(ctx,
			"SELECT id, date_modified FROM "+table+" ORDER BY id")
		if err != nil {
			t.Fatalf("query %s timestamps: %v", table, err)
		}
		for rows.Next() {
			var id, ts any
			if err := rows.Scan(&id, &ts); err != nil {
				rows.Close()
				t.Fatalf("scan %s timestamp: %v", table, err)
			}
			out[table+"."+fmt.Sprintf("%v", id)] = fmt.Sprintf("%v", ts)
		}
		rows.Close()
	}
	return out
}

func timestampsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func timestampDiffCount(a, b map[string]string) int {
	diff := 0
	for k, v := range a {
		if b[k] != v {
			diff++
		}
	}
	return diff
}

// resolveParentWorkID reads the Work UUID for a parent item through its
// projection. It fails the test when no Work projection exists.
func resolveParentWorkID(t *testing.T, ctx context.Context, f nativeBackfillFixture, parentID dto.UUID) dto.UUID {
	t.Helper()
	tx, err := f.native.BackfillReaderTx(ctx)
	if err != nil {
		t.Fatalf("backfill reader tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	workID, err := f.native.BackfillResolveWorkForItem(ctx, tx, parentID)
	if err != nil {
		t.Fatalf("resolve work for item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit reader tx: %v", err)
	}
	if workID.IsNil() {
		t.Fatalf("no work projection for parent %s", parentID)
	}
	return workID
}

// nativeWorkTitle reads the title of a Work by ID.
func nativeWorkTitle(t *testing.T, ctx context.Context, f nativeBackfillFixture, workID dto.UUID) string {
	t.Helper()
	w, err := f.native.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("get work %s: %v", workID, err)
	}
	return w.Title
}

// resolveAssetProjection reads the Asset native UUID for one item through its
// projection. It fails the test when no Asset projection exists.
func resolveAssetProjection(t *testing.T, ctx context.Context, f nativeBackfillFixture, itemID dto.UUID) dto.UUID {
	t.Helper()
	tx, err := f.native.BackfillReaderTx(ctx)
	if err != nil {
		t.Fatalf("backfill reader tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := f.native.BackfillProjectionsForItem(ctx, tx, itemID)
	if err != nil {
		t.Fatalf("projections for item: %v", err)
	}
	if len(ids) == 0 {
		t.Fatalf("no projection for item %s", itemID)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit reader tx: %v", err)
	}
	// Return the first projection (there should be exactly one).
	return ids[0]
}

// nativeAssetFacts reads the stored Asset for one native UUID.
func nativeAssetFacts(t *testing.T, ctx context.Context, f nativeBackfillFixture, nativeID dto.UUID) native.Asset {
	t.Helper()
	tx, err := f.native.BackfillReaderTx(ctx)
	if err != nil {
		t.Fatalf("backfill reader tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	asset, err := f.native.BackfillAssetFacts(ctx, tx, nativeID)
	if err != nil {
		t.Fatalf("asset facts for %s: %v", nativeID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit reader tx: %v", err)
	}
	return asset
}

// resolveWorkFromAsset traces the Asset -> Release -> Edition -> Work chain
// to find the Work UUID for an Asset. It fails the test when any step fails.
func resolveWorkFromAsset(t *testing.T, ctx context.Context, f nativeBackfillFixture, assetID dto.UUID) dto.UUID {
	t.Helper()
	asset := nativeAssetFacts(t, ctx, f, assetID)
	release, err := f.native.GetRelease(ctx, asset.ReleaseID)
	if err != nil {
		t.Fatalf("get release %s: %v", asset.ReleaseID, err)
	}
	edition, err := f.native.GetEdition(ctx, release.EditionID)
	if err != nil {
		t.Fatalf("get edition %s: %v", release.EditionID, err)
	}
	return edition.WorkID
}

// compareTestUUID compares two UUIDs by their canonical 16-byte representation
// in unsigned byte order. It returns -1, 0, or +1.
func compareTestUUID(a, b dto.UUID) int {
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

func ptrInt64(v int64) *int64 { return &v }

func dtoToHex(id dto.UUID) string { return id.String() }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return "1"
}
