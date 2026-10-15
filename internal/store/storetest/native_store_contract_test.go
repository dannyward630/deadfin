// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/native"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// openNativeStoreSQLite opens an in-memory SQLite store with native
// migrations applied and returns the NativeStore plus the underlying Stores.
func openNativeStoreSQLite(t *testing.T) (*store.NativeStore, store.Stores) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite native store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite native store: %v", err)
		}
	})
	return store.NewNativeStore(stores, clock.NewFake(nativeTestTime())), stores
}

// mustUUID generates a deterministic UUID for testing.
func mustUUID(t *testing.T, hex string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(hex)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", hex, err)
	}
	return id
}

func nativeTestTime() time.Time {
	return time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
}

func TestNativeStoreSQLite(t *testing.T) {
	runNativeStoreConformance(t, openNativeStoreSQLite)
}

// openNativeStorePostgres opens a PostgreSQL store from
// DEADFIN_TEST_POSTGRES_DSN. It skips when the opt-in live-test DSN is absent.
func openNativeStorePostgres(t *testing.T) (*store.NativeStore, store.Stores) {
	t.Helper()
	stores := storetest.OpenPostgres(t)
	return store.NewNativeStore(stores, clock.NewFake(nativeTestTime())), stores
}

func TestNativeStorePostgres(t *testing.T) {
	runNativeStoreConformance(t, openNativeStorePostgres)
}

func runNativeStoreConformance(t *testing.T, open func(*testing.T) (*store.NativeStore, store.Stores)) {
	t.Helper()
	ctx := t.Context()

	ns, _ := open(t)

	// --- Each aggregate round-trips through its typed store ---
	workID := mustUUID(t, "11111111111111111111111111111111")
	editionID := mustUUID(t, "22222222222222222222222222222222")
	releaseID := mustUUID(t, "33333333333333333333333333333333")
	assetID := mustUUID(t, "44444444444444444444444444444444")
	inventoryID := mustUUID(t, "55555555555555555555555555555555")
	requestID := mustUUID(t, "66666666666666666666666666666666")
	itemID := mustUUID(t, "77777777777777777777777777777777")
	fulfillmentID := mustUUID(t, "88888888888888888888888888888888")
	now := nativeTestTime()

	// Create a Work with identity mappings.
	work := native.Work{
		ID:    workID,
		State: native.CatalogUnidentified,
		Title: "Test Work",
		ProviderIDs: []native.IdentityMapping{
			{Catalog: native.CatalogTMDB, ExternalID: "tt00001", LastVerified: now},
			{Catalog: native.CatalogIMDb, ExternalID: "tt00002", LastVerified: now},
		},
		DateCreated:  now,
		DateModified: now,
	}
	if err := ns.CreateWork(ctx, work); err != nil {
		t.Fatalf("create work: %v", err)
	}

	// Read it back and compare every persisted field.
	gotWork, err := ns.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	assertWorkEqual(t, gotWork, work)

	// --- Each Work state round-trips unchanged ---
	for _, state := range []native.CatalogState{
		native.CatalogUnidentified,
		native.CatalogIdentified,
		native.CatalogMatched,
		native.CatalogLocked,
	} {
		work.State = state
		work.DateModified = now.Add(time.Hour)
		if err := ns.UpdateWork(ctx, work); err != nil {
			t.Fatalf("update work state %s: %v", state, err)
		}
		got, err := ns.GetWork(ctx, workID)
		if err != nil {
			t.Fatalf("get work after state %s: %v", state, err)
		}
		assertWorkEqual(t, got, work)
	}

	// --- Each named external catalog round-trips with last-verified time ---
	catalogs := []native.IdentityMapping{
		{Catalog: native.CatalogTMDB, ExternalID: "tmdb-123", LastVerified: now},
		{Catalog: native.CatalogIMDb, ExternalID: "imdb-456", LastVerified: now.Add(time.Minute)},
		{Catalog: native.CatalogTVDB, ExternalID: "tvdb-789", LastVerified: now.Add(2 * time.Minute)},
		{Catalog: native.CatalogAniDB, ExternalID: "anidb-012", LastVerified: now.Add(3 * time.Minute)},
		{Catalog: native.CatalogAniList, ExternalID: "anilist-345", LastVerified: now.Add(4 * time.Minute)},
		{Catalog: native.CatalogMAL, ExternalID: "mal-678", LastVerified: now.Add(5 * time.Minute)},
	}
	work.ProviderIDs = catalogs
	if err := ns.UpdateWork(ctx, work); err != nil {
		t.Fatalf("update work with all catalogs: %v", err)
	}
	gotWork, err = ns.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("get work after all catalogs: %v", err)
	}
	assertWorkEqual(t, gotWork, work)

	// --- Repeated identity import preserves the original native UUID ---
	// Two import calls for one catalog and external ID each propose a distinct
	// UUID. Both calls return the first UUID. The import creates an aggregate
	// only when the import returns the proposed UUID. After both imports,
	// exactly one aggregate row and one mapping row exist for that identity.
	importCatalog := native.CatalogTVDB
	importExternalID := "tvdb-import-test"
	firstUUID := mustUUID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	secondUUID := mustUUID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	// First import: no mapping exists. The method records the mapping and
	// returns the proposed UUID.
	resolvedFirst, err := ns.ImportIdentity(ctx, importCatalog, importExternalID, firstUUID)
	if err != nil {
		t.Fatalf("first identity import: %v", err)
	}
	if resolvedFirst != firstUUID {
		t.Fatalf("first import returned %v, want %v", resolvedFirst, firstUUID)
	}
	// The import wrote the clock's fixed time as last_verified. Read the
	// mapping before CreateWork replaces it, so this assertion detects a
	// direct wall-clock read.
	importedMappings, err := ns.IdentityMappings(ctx, firstUUID)
	if err != nil {
		t.Fatalf("read import mappings: %v", err)
	}
	if len(importedMappings) != 1 {
		t.Fatalf("import mapping count = %d, want 1", len(importedMappings))
	}
	assertIdentityMapping(t, importedMappings[0], importCatalog, importExternalID, now)
	// The first import returned the proposed UUID, so create the aggregate.
	// The Work carries the import mapping in its ProviderIDs so that
	// CreateWork preserves the mapping through its replace step.
	importWork := native.Work{
		ID:    firstUUID,
		State: native.CatalogIdentified,
		Title: "Import Test Work",
		ProviderIDs: []native.IdentityMapping{
			{Catalog: importCatalog, ExternalID: importExternalID, LastVerified: now},
		},
		DateCreated:  now,
		DateModified: now,
	}
	if err := ns.CreateWork(ctx, importWork); err != nil {
		t.Fatalf("create import work: %v", err)
	}
	// Second import: a mapping exists. The method returns the mapped UUID and
	// writes nothing, even though the proposed UUID differs. The second import
	// does not create a second aggregate.
	resolvedSecond, err := ns.ImportIdentity(ctx, importCatalog, importExternalID, secondUUID)
	if err != nil {
		t.Fatalf("second identity import: %v", err)
	}
	if resolvedSecond != firstUUID {
		t.Fatalf("second import returned %v, want %v", resolvedSecond, firstUUID)
	}
	// The lookup confirms the mapped UUID.
	lookupID, err := ns.ResolveAggregateByIDentity(ctx, importCatalog, importExternalID)
	if err != nil {
		t.Fatalf("resolve identity after import: %v", err)
	}
	if lookupID != firstUUID {
		t.Fatalf("identity lookup returned %v, want %v", lookupID, firstUUID)
	}
	// Exactly one aggregate row exists for the imported UUID.
	aggregateCount, err := ns.WorkCountByID(ctx, firstUUID)
	if err != nil {
		t.Fatalf("aggregate count: %v", err)
	}
	if aggregateCount != 1 {
		t.Fatalf("aggregate count = %d, want 1", aggregateCount)
	}
	// Exactly one mapping row exists for that identity.
	importCount, err := ns.IdentityMappingCount(ctx, importCatalog, importExternalID)
	if err != nil {
		t.Fatalf("identity mapping count: %v", err)
	}
	if importCount != 1 {
		t.Fatalf("identity mapping count = %d, want 1", importCount)
	}

	// --- Edition round-trip ---
	edition := native.Edition{
		ID:     editionID,
		WorkID: workID,
		Title:  "Theatrical Cut",
		ProviderIDs: []native.IdentityMapping{
			{Catalog: native.CatalogTMDB, ExternalID: "ed-tmdb-1", LastVerified: now},
		},
		DateCreated:  now,
		DateModified: now,
	}
	if err := ns.CreateEdition(ctx, edition); err != nil {
		t.Fatalf("create edition: %v", err)
	}
	gotEdition, err := ns.GetEdition(ctx, editionID)
	if err != nil {
		t.Fatalf("get edition: %v", err)
	}
	assertEditionEqual(t, gotEdition, edition)

	// --- Release round-trip ---
	release := native.Release{
		ID:               releaseID,
		EditionID:        editionID,
		Title:            "BluRay 1080p",
		AcquisitionState: native.AcquisitionMissing,
		DateCreated:      now,
		DateModified:     now,
	}
	if err := ns.CreateRelease(ctx, release); err != nil {
		t.Fatalf("create release: %v", err)
	}
	gotRelease, err := ns.GetRelease(ctx, releaseID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	assertReleaseEqual(t, gotRelease, release)

	// --- Asset round-trip: device, inode, and source revision ---
	asset := native.Asset{
		ID:             assetID,
		ReleaseID:      releaseID,
		Path:           "/media/movie.mkv",
		Device:         0x1000,
		Inode:          0x2000,
		SourceRevision: "abc123rev",
		DateCreated:    now,
		DateModified:   now,
	}
	if err := ns.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	gotAsset, err := ns.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	assertAssetEqual(t, gotAsset, asset)

	// --- Each Inventory state round-trips unchanged ---
	// The Inventory owns physical bytes, hardlink facts, and seed obligations.
	// Each required fact round-trips unchanged.
	for _, state := range []native.InventoryState{
		native.InventoryAbsent,
		native.InventoryDownloading,
		native.InventoryPresent,
		native.InventorySeeding,
		native.InventoryEvicted,
	} {
		inv := native.Inventory{
			ID:                inventoryID,
			AssetID:           assetID,
			State:             state,
			PhysicalBytes:     int64(4096),
			Hardlinked:        true,
			HardlinkTarget:    "/media/.hardlinks/movie.lnk",
			SeedRatio:         2.5,
			SeedTime:          3600,
			SeedObligationMet: true,
			DateCreated:       now,
			DateModified:      now,
		}
		if state == native.InventoryAbsent {
			if err := ns.CreateInventory(ctx, inv); err != nil {
				t.Fatalf("create inventory: %v", err)
			}
		} else {
			if err := ns.UpdateInventory(ctx, inv); err != nil {
				t.Fatalf("update inventory state %s: %v", state, err)
			}
		}
		got, err := ns.GetInventory(ctx, inventoryID)
		if err != nil {
			t.Fatalf("get inventory state %s: %v", state, err)
		}
		assertInventoryEqual(t, got, inv)
	}

	// --- Request round-trip ---
	// The Request owns request state, approval state, and the fulfillment
	// link. Each required fact round-trips unchanged.
	req := native.Request{
		ID:            requestID,
		WorkID:        &workID,
		State:         native.RequestSubmitted,
		ApprovalState: native.ApprovalPending,
		DateCreated:   now,
		DateModified:  now,
	}
	if err := ns.CreateRequest(ctx, req); err != nil {
		t.Fatalf("create request: %v", err)
	}
	gotReq, err := ns.GetRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	assertRequestEqual(t, gotReq, req)

	// Update request with approval state and fulfillment link.
	req.State = native.RequestApproved
	req.ApprovalState = native.ApprovalGranted
	req.FulfillmentID = &fulfillmentID
	req.DateModified = now.Add(2 * time.Hour)
	if err := ns.UpdateRequest(ctx, req); err != nil {
		t.Fatalf("update request with approval and fulfillment: %v", err)
	}
	gotReq, err = ns.GetRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("get request after update: %v", err)
	}
	assertRequestEqual(t, gotReq, req)

	// --- A Request rejects zero or two targets ---
	// A Request must reference exactly one of WorkID or EditionID. A Request
	// with zero targets or two targets is rejected on both backends.
	zeroTargetReq := native.Request{
		ID:            mustUUID(t, "cccccccccccccccccccccccccccccccc"),
		State:         native.RequestSubmitted,
		ApprovalState: native.ApprovalPending,
		DateCreated:   now,
		DateModified:  now,
	}
	if err := ns.CreateRequest(ctx, zeroTargetReq); err == nil {
		t.Fatalf("create request with zero targets succeeded, want error")
	}
	twoTargetReq := native.Request{
		ID:            mustUUID(t, "dddddddddddddddddddddddddddddddd"),
		WorkID:        &workID,
		EditionID:     &editionID,
		State:         native.RequestSubmitted,
		ApprovalState: native.ApprovalPending,
		DateCreated:   now,
		DateModified:  now,
	}
	if err := ns.CreateRequest(ctx, twoTargetReq); err == nil {
		t.Fatalf("create request with two targets succeeded, want error")
	}

	// --- A native UUID resolves to the expected item.Item.ID ---
	// A Work carries no source file, so the projection write carries no
	// revision guard. The sourceRevision argument is not written to the
	// projection table; the Asset owns the source revision.
	sourceRev := "src-rev-v1"
	if err := ns.SetItemProjection(ctx, workID, itemID, sourceRev); err != nil {
		t.Fatalf("set item projection: %v", err)
	}
	resolved, err := ns.ResolveItemID(ctx, workID)
	if err != nil {
		t.Fatalf("resolve item ID: %v", err)
	}
	if resolved != itemID {
		t.Fatalf("resolved item ID = %v, want %v", resolved, itemID)
	}

	// --- A repeated projection write leaves one mapping ---
	// The acceptance test proves that the mapping row count is exactly one.
	if err := ns.SetItemProjection(ctx, workID, itemID, sourceRev); err != nil {
		t.Fatalf("repeat item projection: %v", err)
	}
	if err := ns.SetItemProjection(ctx, workID, itemID, sourceRev); err != nil {
		t.Fatalf("third item projection: %v", err)
	}
	resolved2, err := ns.ResolveItemID(ctx, workID)
	if err != nil {
		t.Fatalf("resolve item ID after repeat: %v", err)
	}
	if resolved2 != itemID {
		t.Fatalf("resolved item ID after repeat = %v, want %v", resolved2, itemID)
	}
	count, err := ns.ProjectionMappingCount(ctx, workID)
	if err != nil {
		t.Fatalf("projection mapping count: %v", err)
	}
	if count != 1 {
		t.Fatalf("projection mapping count = %d, want 1", count)
	}

	// --- A stale Asset projection write returns ErrStaleSourceRevision ---
	// The Asset owns the source revision. A projection write for an Asset
	// compares the supplied source revision with native_assets.source_revision
	// for that Asset. When the values differ, the write returns
	// ErrStaleSourceRevision and leaves the mapping unchanged. When the values
	// match, the write replaces the mapping.
	assetItemA := mustUUID(t, "99999999999999999999999999999999")
	assetItemB := mustUUID(t, "8888888888888888888888888888bbbb")
	// Project item A at rev-1. The Asset source revision is "abc123rev".
	if err := ns.SetItemProjection(ctx, assetID, assetItemA, "abc123rev"); err != nil {
		t.Fatalf("set asset projection item A rev-1: %v", err)
	}
	// Update the Asset to rev-2.
	asset.SourceRevision = "abc123rev2"
	asset.DateModified = now.Add(3 * time.Hour)
	if err := ns.UpdateAsset(ctx, asset); err != nil {
		t.Fatalf("update asset to rev-2: %v", err)
	}
	// A projection write with item B at rev-1 returns ErrStaleSourceRevision
	// and leaves the mapping unchanged. The mapping still points to item A.
	err = ns.SetItemProjection(ctx, assetID, assetItemB, "abc123rev")
	if !errors.Is(err, store.ErrStaleSourceRevision) {
		t.Fatalf("stale asset projection item B rev-1 = %v, want ErrStaleSourceRevision", err)
	}
	resolvedAsset, err := ns.ResolveItemID(ctx, assetID)
	if err != nil {
		t.Fatalf("resolve asset item ID after stale write: %v", err)
	}
	if resolvedAsset != assetItemA {
		t.Fatalf("asset item ID after stale write = %v, want %v (item A)", resolvedAsset, assetItemA)
	}
	// A projection write with item B at rev-2 succeeds and updates the mapping.
	// The mapping now points to item B.
	if err := ns.SetItemProjection(ctx, assetID, assetItemB, "abc123rev2"); err != nil {
		t.Fatalf("set asset projection item B rev-2: %v", err)
	}
	resolvedAsset2, err := ns.ResolveItemID(ctx, assetID)
	if err != nil {
		t.Fatalf("resolve asset item ID after rev-2: %v", err)
	}
	if resolvedAsset2 != assetItemB {
		t.Fatalf("asset item ID after rev-2 = %v, want %v (item B)", resolvedAsset2, assetItemB)
	}

	// --- Deterministic listing ---
	works, err := ns.ListWorks(ctx)
	if err != nil {
		t.Fatalf("list works: %v", err)
	}
	if len(works) != 2 {
		t.Fatalf("list works = %d items, want 2", len(works))
	}
	// The first work is the main work; the second is the import test work.
	assertWorkEqual(t, works[0], work)
	assertWorkEqual(t, works[1], importWork)

	editions, err := ns.ListEditions(ctx)
	if err != nil {
		t.Fatalf("list editions: %v", err)
	}
	if len(editions) != 1 {
		t.Fatalf("list editions = %d items, want 1", len(editions))
	}
	assertEditionEqual(t, editions[0], edition)

	releases, err := ns.ListReleases(ctx)
	if err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("list releases = %d items, want 1", len(releases))
	}
	assertReleaseEqual(t, releases[0], release)

	assets, err := ns.ListAssets(ctx)
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("list assets = %d items, want 1", len(assets))
	}
	assertAssetEqual(t, assets[0], asset)

	inventories, err := ns.ListInventories(ctx)
	if err != nil {
		t.Fatalf("list inventories: %v", err)
	}
	if len(inventories) != 1 {
		t.Fatalf("list inventories = %d items, want 1", len(inventories))
	}
	// List returns the last-updated state (InventoryEvicted).
	lastInv := native.Inventory{
		ID:                inventoryID,
		AssetID:           assetID,
		State:             native.InventoryEvicted,
		PhysicalBytes:     int64(4096),
		Hardlinked:        true,
		HardlinkTarget:    "/media/.hardlinks/movie.lnk",
		SeedRatio:         2.5,
		SeedTime:          3600,
		SeedObligationMet: true,
		DateCreated:       now,
		DateModified:      now,
	}
	assertInventoryEqual(t, inventories[0], lastInv)

	requests, err := ns.ListRequests(ctx)
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("list requests = %d items, want 1", len(requests))
	}
	assertRequestEqual(t, requests[0], req)
}

// assertWorkEqual compares every persisted field of a Work.
func assertWorkEqual(t *testing.T, got, want native.Work) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("work ID = %v, want %v", got.ID, want.ID)
	}
	if got.State != want.State {
		t.Fatalf("work state = %s, want %s", got.State, want.State)
	}
	if got.Title != want.Title {
		t.Fatalf("work title = %q, want %q", got.Title, want.Title)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("work date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("work date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
	if len(got.ProviderIDs) != len(want.ProviderIDs) {
		t.Fatalf("work provider IDs = %d, want %d", len(got.ProviderIDs), len(want.ProviderIDs))
	}
	for _, m := range want.ProviderIDs {
		assertIdentityMapping(t, findMapping(got.ProviderIDs, m.Catalog), m.Catalog, m.ExternalID, m.LastVerified)
	}
}

// assertEditionEqual compares every persisted field of an Edition.
func assertEditionEqual(t *testing.T, got, want native.Edition) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("edition ID = %v, want %v", got.ID, want.ID)
	}
	if got.WorkID != want.WorkID {
		t.Fatalf("edition work ID = %v, want %v", got.WorkID, want.WorkID)
	}
	if got.Title != want.Title {
		t.Fatalf("edition title = %q, want %q", got.Title, want.Title)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("edition date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("edition date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
	if len(got.ProviderIDs) != len(want.ProviderIDs) {
		t.Fatalf("edition provider IDs = %d, want %d", len(got.ProviderIDs), len(want.ProviderIDs))
	}
	for _, m := range want.ProviderIDs {
		assertIdentityMapping(t, findMapping(got.ProviderIDs, m.Catalog), m.Catalog, m.ExternalID, m.LastVerified)
	}
}

// assertReleaseEqual compares every persisted field of a Release.
func assertReleaseEqual(t *testing.T, got, want native.Release) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("release ID = %v, want %v", got.ID, want.ID)
	}
	if got.EditionID != want.EditionID {
		t.Fatalf("release edition ID = %v, want %v", got.EditionID, want.EditionID)
	}
	if got.Title != want.Title {
		t.Fatalf("release title = %q, want %q", got.Title, want.Title)
	}
	if got.AcquisitionState != want.AcquisitionState {
		t.Fatalf("release acquisition state = %s, want %s", got.AcquisitionState, want.AcquisitionState)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("release date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("release date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
}

// assertAssetEqual compares every persisted field of an Asset.
func assertAssetEqual(t *testing.T, got, want native.Asset) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("asset ID = %v, want %v", got.ID, want.ID)
	}
	if got.ReleaseID != want.ReleaseID {
		t.Fatalf("asset release ID = %v, want %v", got.ReleaseID, want.ReleaseID)
	}
	if got.Path != want.Path {
		t.Fatalf("asset path = %q, want %q", got.Path, want.Path)
	}
	if got.Device != want.Device {
		t.Fatalf("asset device = %d, want %d", got.Device, want.Device)
	}
	if got.Inode != want.Inode {
		t.Fatalf("asset inode = %d, want %d", got.Inode, want.Inode)
	}
	if got.SourceRevision != want.SourceRevision {
		t.Fatalf("asset source revision = %q, want %q", got.SourceRevision, want.SourceRevision)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("asset date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("asset date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
}

// assertInventoryEqual compares every persisted field of an Inventory,
// including the hardlink facts and seed obligations.
func assertInventoryEqual(t *testing.T, got, want native.Inventory) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("inventory ID = %v, want %v", got.ID, want.ID)
	}
	if got.AssetID != want.AssetID {
		t.Fatalf("inventory asset ID = %v, want %v", got.AssetID, want.AssetID)
	}
	if got.State != want.State {
		t.Fatalf("inventory state = %s, want %s", got.State, want.State)
	}
	if got.PhysicalBytes != want.PhysicalBytes {
		t.Fatalf("inventory physical bytes = %d, want %d", got.PhysicalBytes, want.PhysicalBytes)
	}
	if got.Hardlinked != want.Hardlinked {
		t.Fatalf("inventory hardlinked = %v, want %v", got.Hardlinked, want.Hardlinked)
	}
	if got.HardlinkTarget != want.HardlinkTarget {
		t.Fatalf("inventory hardlink target = %q, want %q", got.HardlinkTarget, want.HardlinkTarget)
	}
	if got.SeedRatio != want.SeedRatio {
		t.Fatalf("inventory seed ratio = %v, want %v", got.SeedRatio, want.SeedRatio)
	}
	if got.SeedTime != want.SeedTime {
		t.Fatalf("inventory seed time = %d, want %d", got.SeedTime, want.SeedTime)
	}
	if got.SeedObligationMet != want.SeedObligationMet {
		t.Fatalf("inventory seed obligation met = %v, want %v", got.SeedObligationMet, want.SeedObligationMet)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("inventory date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("inventory date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
}

// assertRequestEqual compares every persisted field of a Request, including
// the approval state and the fulfillment link.
func assertRequestEqual(t *testing.T, got, want native.Request) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("request ID = %v, want %v", got.ID, want.ID)
	}
	if !uuidPtrEqual(got.WorkID, want.WorkID) {
		t.Fatalf("request work ID = %v, want %v", got.WorkID, want.WorkID)
	}
	if !uuidPtrEqual(got.EditionID, want.EditionID) {
		t.Fatalf("request edition ID = %v, want %v", got.EditionID, want.EditionID)
	}
	if got.State != want.State {
		t.Fatalf("request state = %s, want %s", got.State, want.State)
	}
	if got.ApprovalState != want.ApprovalState {
		t.Fatalf("request approval state = %s, want %s", got.ApprovalState, want.ApprovalState)
	}
	if !uuidPtrEqual(got.FulfillmentID, want.FulfillmentID) {
		t.Fatalf("request fulfillment ID = %v, want %v", got.FulfillmentID, want.FulfillmentID)
	}
	if got.DateCreated != want.DateCreated {
		t.Fatalf("request date_created = %v, want %v", got.DateCreated, want.DateCreated)
	}
	if got.DateModified != want.DateModified {
		t.Fatalf("request date_modified = %v, want %v", got.DateModified, want.DateModified)
	}
}

func uuidPtrEqual(got, want *dto.UUID) bool {
	if got == nil && want == nil {
		return true
	}
	if got == nil || want == nil {
		return false
	}
	return *got == *want
}

func assertIdentityMapping(t *testing.T, m native.IdentityMapping, catalog native.Catalog, externalID string, lastVerified time.Time) {
	t.Helper()
	if m.Catalog != catalog {
		t.Fatalf("identity catalog = %s, want %s", m.Catalog, catalog)
	}
	if m.ExternalID != externalID {
		t.Fatalf("identity external ID = %s, want %s", m.ExternalID, externalID)
	}
	if !m.LastVerified.Equal(lastVerified) {
		t.Fatalf("identity last-verified = %v, want %v", m.LastVerified, lastVerified)
	}
}

func findMapping(mappings []native.IdentityMapping, catalog native.Catalog) native.IdentityMapping {
	for _, m := range mappings {
		if m.Catalog == catalog {
			return m
		}
	}
	return native.IdentityMapping{}
}
