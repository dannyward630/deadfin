// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// TestScannerFullRefreshRerunsRemoteStageOnExistingItem is the regression for
// the full refresh: a plain validate refresh of an already-scanned library
// does not run the remote metadata stage, while a full/replace refresh re-runs
// it on the existing (unchanged-on-disk) items and backfills their provider
// ids. The fake refresher stands in for the registry's remote stage — it
// records the refresh mode threaded through the scan and enriches only under a
// full/replace intent, exactly as providers.Registry gates
// ProviderStageRemoteMetadata.
func TestScannerFullRefreshRerunsRemoteStageOnExistingItem(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, movie, "the matrix")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	var movieModes []library.RefreshMode
	refresher := scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if candidate.Kind != item.KindMovie {
			return nil
		}
		movieModes = append(movieModes, refresh.Mode)
		if refresh.ReplaceAllMetadata || refresh.Mode >= library.RefreshModeFullRefresh {
			candidate.ProviderIDs.Set("Tmdb", "603")
		}
		return nil
	})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   refresher,
	})

	// A validate-only scan seeds the library the way an initial scan with internet
	// providers off would: the item exists but never touched the remote stage.
	first, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
		Metadata: library.RefreshModeValidationOnly,
		Image:    library.RefreshModeValidationOnly,
	})
	if err != nil {
		t.Fatalf("validate-only scan: %v", err)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("validate-only scan errors = %v", first.Errors)
	}
	seeded := scannerItemAtPath(t, harness.items, movie)
	if _, hasTmdb := seeded.ProviderIDs.Get("Tmdb"); hasTmdb {
		t.Fatalf("validate-only scan fetched remote metadata for %q; want the remote stage suppressed", movie)
	}

	// A full/replace refresh of the same, unchanged library re-runs the remote
	// stage on the existing item and backfills its TMDb id.
	second, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
		Metadata:           library.RefreshModeFullRefresh,
		Image:              library.RefreshModeFullRefresh,
		ReplaceAllMetadata: true,
	})
	if err != nil {
		t.Fatalf("full refresh scan: %v", err)
	}
	if len(second.Errors) != 0 {
		t.Fatalf("full refresh scan errors = %v", second.Errors)
	}
	if second.Updated == 0 {
		t.Fatalf("full refresh updated = %d, want the existing item re-refreshed and persisted", second.Updated)
	}
	refreshed := scannerItemAtPath(t, harness.items, movie)
	if id, ok := refreshed.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
		t.Fatalf("full refresh TMDb id = %q,%t; want 603,true — remote stage did not re-run on the already-scanned item", id, ok)
	}

	// The scan threaded each invocation's mode down to the item's RefreshContext,
	// including the second pass over the already-existing item.
	wantModes := []library.RefreshMode{library.RefreshModeValidationOnly, library.RefreshModeFullRefresh}
	if !reflect.DeepEqual(movieModes, wantModes) {
		t.Fatalf("threaded refresh modes for the movie = %v, want %v", movieModes, wantModes)
	}
}

// fakeRemoteProvider is a providers.Provider whose remote stage backfills a
// fixed TMDb id and overview. It replaces the real TMDb provider. Thus, the
// test exercises the genuine registry refresh-mode gate rather than a raw
// scannerRefreshFunc.
type fakeRemoteProvider struct {
	calls int
}

func (p *fakeRemoteProvider) Name() string { return "fake-remote" }

func (p *fakeRemoteProvider) Stage() providers.ProviderStage {
	return providers.ProviderStageRemoteMetadata
}

func (p *fakeRemoteProvider) Refresh(_ context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
	p.calls++
	target.ProviderIDs.Set("Tmdb", "603")
	overview := "A provider-sourced overview"
	target.Overview = &overview
	return true, nil
}

// TestScannerFullRefreshBackfillsAndForcesRepersist exercises the live
// scanner-to-store caller with providers.Registry. It covers the transition
// from a library whose items were stored with no remote metadata to one whose
// remote provider runs.
//
//  1. Seed two unchanged movies with no remote provider. The items persist
//     with no provider metadata.
//  2. Add the remote provider. A default library refresh (the intent
//     POST /Library/Refresh threads) does not run it: the items were
//     refreshed before and did not change (MetadataService.GetProviders).
//     A FullRefresh without ReplaceAllMetadata ("search for missing
//     metadata") runs it and adds provider IDs and an overview.
//  3. A default rescan of the now-metadata-rich items is a no-op. The remote
//     stage does not run. The store skips the retained children.
//     DateLastSaved does not advance.
//  4. A full refresh without ReplaceAllMetadata runs the remote stage again.
//     If metadata is unchanged, ForceRefresh writes the items and advances
//     DateLastSaved.
func TestScannerFullRefreshBackfillsAndForcesRepersist(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	inception := scannerPath(t, harness.source, "inception.mkv")
	writeScannerFile(t, matrix, "the matrix")
	writeScannerFile(t, inception, "inception")

	remote := &fakeRemoteProvider{}
	now := time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	useRefresher := func(refresher library.Refresher) {
		t.Helper()
		if err := harness.scanner.Close(); err != nil {
			t.Fatalf("close scanner: %v", err)
		}
		harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
			Concurrency: 1,
			Now:         func() time.Time { return now },
			Refresher:   refresher,
		})
	}

	// 1. Seed the library with no remote provider. The items persist with no
	// provider metadata.
	useRefresher(providers.NewRegistry())
	seed, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions())
	if err != nil {
		t.Fatalf("seed scan without a remote provider: %v", err)
	}
	if len(seed.Errors) != 0 {
		t.Fatalf("seed scan errors = %v", seed.Errors)
	}
	for _, path := range []string{matrix, inception} {
		seeded := scannerItemAtPath(t, harness.items, path)
		if _, hasTmdb := seeded.ProviderIDs.Get("Tmdb"); hasTmdb {
			t.Fatalf("seed scan fetched remote metadata for %q; want none", path)
		}
		if seeded.Overview != nil {
			t.Fatalf("seed scan set overview for %q; want no remote enrichment", path)
		}
	}

	// 2. Add the remote provider.
	useRefresher(providers.NewRegistry(remote))

	// A default library refresh re-runs the scan. This is the intent
	// POST /Library/Refresh threads. The unchanged items were refreshed
	// before, so the remote stage does not run and nothing changes.
	plain, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions())
	if err != nil {
		t.Fatalf("plain library refresh with providers on: %v", err)
	}
	if len(plain.Errors) != 0 {
		t.Fatalf("plain library refresh errors = %v", plain.Errors)
	}
	if remote.calls != 0 || plain.Updated != 0 {
		t.Fatalf("plain library refresh made %d remote calls and updated %d items; want neither for unchanged, already refreshed items", remote.calls, plain.Updated)
	}

	// A FullRefresh without ReplaceAllMetadata runs the remote stage. The
	// stored items have no overview, so the provider value fills it.
	refresh, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
		Metadata: library.RefreshModeFullRefresh,
		Image:    library.RefreshModeFullRefresh,
	})
	if err != nil {
		t.Fatalf("search for missing metadata: %v", err)
	}
	if len(refresh.Errors) != 0 {
		t.Fatalf("search for missing metadata errors = %v", refresh.Errors)
	}
	if remote.calls == 0 {
		t.Fatalf("search for missing metadata made 0 remote calls; want the remote stage to run")
	}
	if refresh.Updated == 0 {
		t.Fatalf("search for missing metadata updated = 0, want the existing items backfilled with provider metadata")
	}
	for _, path := range []string{matrix, inception} {
		backfilled := scannerItemAtPath(t, harness.items, path)
		if id, ok := backfilled.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
			t.Fatalf("search for missing metadata TMDb id for %q = %q,%t; want 603,true — remote stage did not backfill the already-scanned item", path, id, ok)
		}
		if backfilled.Overview == nil || *backfilled.Overview != "A provider-sourced overview" {
			t.Fatalf("search for missing metadata overview for %q = %v; want provider-sourced overview — remote stage did not backfill metadata", path, backfilled.Overview)
		}
	}
	backfilledSaved := scannerItemAtPath(t, harness.items, matrix).DateLastSaved
	if backfilledSaved == nil {
		t.Fatalf("backfilled item has no DateLastSaved")
	}

	// 3. A default rescan of the now-metadata-rich items is a no-op. The
	// remote stage returns identical metadata. The store skips the retained
	// children. DateLastSaved does not advance.
	callsAfterBackfill := remote.calls
	now = now.Add(time.Hour)
	noOp, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions())
	if err != nil {
		t.Fatalf("default rescan: %v", err)
	}
	if noOp.Updated != 0 {
		t.Fatalf("default rescan updated = %d, want a no-op for unchanged items with identical remote metadata", noOp.Updated)
	}
	if remote.calls != callsAfterBackfill {
		t.Fatalf("default rescan made %d remote calls, want none for unchanged, already refreshed items", remote.calls-callsAfterBackfill)
	}
	redone := scannerItemAtPath(t, harness.items, matrix)
	if redone.DateLastSaved == nil || !redone.DateLastSaved.Equal(*backfilledSaved) {
		t.Fatalf("default rescan advanced DateLastSaved = %v, want unchanged %v — a plain rescan must not force-persist an unchanged item", redone.DateLastSaved, *backfilledSaved)
	}

	// 4. A full refresh without ReplaceAllMetadata runs the remote stage
	// again. The metadata is unchanged. ForceRefresh writes the items and
	// advances DateLastSaved.
	now = now.Add(time.Hour)
	full, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
		Metadata: library.RefreshModeFullRefresh,
		Image:    library.RefreshModeFullRefresh,
	})
	if err != nil {
		t.Fatalf("full refresh without replace-all: %v", err)
	}
	if len(full.Errors) != 0 {
		t.Fatalf("full refresh errors = %v", full.Errors)
	}
	if remote.calls == callsAfterBackfill {
		t.Fatalf("full refresh made no additional remote calls; want the remote stage to re-run under FullRefresh")
	}
	if full.Updated == 0 {
		t.Fatalf("full refresh updated = 0, want the unchanged items force-persisted even though remote metadata is identical")
	}
	wantSaved := now.UTC().Truncate(time.Microsecond)
	for _, path := range []string{matrix, inception} {
		forced := scannerItemAtPath(t, harness.items, path)
		if forced.DateLastSaved == nil || !forced.DateLastSaved.Equal(wantSaved) {
			t.Fatalf("full refresh DateLastSaved for %q = %v, want advanced lifecycle time %v — ForceRefresh did not reach the persisted path", path, forced.DateLastSaved, wantSaved)
		}
		if id, ok := forced.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
			t.Fatalf("full refresh lost TMDb id for %q = %q,%t; want 603,true", path, id, ok)
		}
	}
}

// TestScannerRefreshCompleteDispatchesOnlyRequestedTrickplayItems proves the
// scanner's existing refresh-completion seam carries every targeted regeneration
// request to a task consumer only when the item API requested it. The fake
// consumer deliberately observes the result rather than generating inline:
// generation remains owned by the existing task/runner path.
func TestScannerRefreshCompleteDispatchesOnlyRequestedTrickplayItems(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "existing-width.mkv")
	writeScannerFile(t, movie, "existing width")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	var dispatched []string
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		RefreshComplete: func(result library.RefreshResult) {
			for _, id := range result.RegenerateTrickplayItemIDs {
				dispatched = append(dispatched, id.String())
			}
		},
	})
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed existing item: %v", err)
	}
	existing := scannerItemAtPath(t, harness.items, movie)

	for _, options := range []library.RefreshOptions{
		{},
		{RegenerateTrickplayItemIDs: []dto.UUID{}},
		{RegenerateTrickplayItemIDs: []dto.UUID{existing.ID}},
	} {
		if _, err := harness.scanner.ScanWithOptions(t.Context(), options); err != nil {
			t.Fatalf("scan with refresh options %#v: %v", options, err)
		}
	}

	if got, want := dispatched, []string{existing.ID.String()}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task-consumer dispatches = %#v, want only requested item %#v", got, want)
	}
}

// backfillMovieResolver resolves a media file into a bare Movie with no
// provider ID and no image, so a counting refresher is the sole source of
// backfilled satellites. This keeps the reconcile plan free of the fixture
// resolver's local artwork, which would otherwise duplicate a remote
// Primary image and trip the duplicate-image quarantine.
type backfillMovieResolver struct{}

func (backfillMovieResolver) Priority() library.ResolverPriority {
	return library.ResolverPriorityFirst
}

func (backfillMovieResolver) Resolve(_ context.Context, input library.ResolveContext) (*item.Item, error) {
	name := filepath.Base(input.Entry.Path)
	if !input.Entry.IsDir {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	result := item.Item{
		ID:           scannerID(input.Entry.Path),
		Kind:         item.KindFolder,
		Path:         scannerString(input.Entry.Path),
		Name:         scannerString(name),
		IsFolder:     input.Entry.IsDir,
		DateCreated:  scannerTime(input.Entry.Created),
		DateModified: scannerTime(input.Entry.Modified),
	}
	if input.Parent != nil {
		parentID := input.Parent.ID
		result.ParentID = &parentID
		if input.Parent.TopParentID != nil {
			topParentID := *input.Parent.TopParentID
			result.TopParentID = &topParentID
		} else {
			topParentID := input.Parent.ID
			result.TopParentID = &topParentID
		}
	}
	if !input.Entry.IsDir {
		// A bare Movie with no provider ID and no image, so a counting
		// refresher is the sole source of backfilled satellites.
		result.Kind = item.KindMovie
		result.Size = scannerInt64(input.Entry.Size)
	}
	return &result, nil
}

// countingRefresher is a library.Refresher that counts its invocations and
// optionally backfills a fixed provider ID and a remote image. It stands in
// for the provider registry's remote stage so the test proves the scanner
// counts an invocation at the refresher boundary and a persisted provider
// ID or image after reconcile. When lockName is set, it locks the name field
// so the test can prove a locked field survives the backfill. When
// enrichLocalImage is set, it adds a local Primary image. remoteImageType
// selects the remote image type it backfills (Primary by default).
type countingRefresher struct {
	calls            int
	enrich           bool
	lockName         *string
	enrichLocalImage bool
	remoteImageType  item.ImageType
}

func (r *countingRefresher) Refresh(_ context.Context, _ library.RefreshContext, target *item.Item) error {
	r.calls++
	if r.lockName != nil {
		target.IsLocked = false
		target.LockedFields = append(target.LockedFields, item.MetadataFieldName)
		target.Name = r.lockName
	}
	if r.enrichLocalImage {
		target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: item.ImageTypePrimary, Path: *target.Path + ".jpg"})
	}
	if !r.enrich {
		return nil
	}
	target.ProviderIDs.Set("Tmdb", "603")
	imageType := r.remoteImageType
	if imageType == 0 {
		imageType = item.ImageTypePrimary
	}
	target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: imageType, Path: "https://image.tmdb.org/t/p/original/poster.jpg"})
	return nil
}

// newBackfillScanner builds a harness scanner whose resolver and refresher
// drive the remote-backfill mode without the provider registry.
func newBackfillScanner(t *testing.T, harness *scannerHarness, refresher *countingRefresher) {
	t.Helper()
	now := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
	})
}

// newBackfillScannerWithRefresher is the general form of newBackfillScanner
// for any library.Refresher, not only *countingRefresher.
func newBackfillScannerWithRefresher(t *testing.T, harness *scannerHarness, refresher library.Refresher) {
	t.Helper()
	now := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
	})
}

// newBackfillScannerWithRefresherAndReadDir is the general form of
// newBackfillScannerWithRefresher for tests that need a custom ReadDir
// function. The inaccessible-root test uses this to make one media root
// return a permission error during the backfill scan.
func newBackfillScannerWithRefresherAndReadDir(t *testing.T, harness *scannerHarness, refresher library.Refresher, readDir func(string) ([]library.Entry, error)) {
	t.Helper()
	now := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
		ReadDir:     readDir,
	})
}

// TestRemoteRefreshInvokesProviderAndPersistsProviderIDAndImage proves the
// remote-backfill scan mode invokes the counting provider at the refresher
// boundary and persists a provider ID and a remote image through the
// reconcile path.
func TestRemoteRefreshInvokesProviderAndPersistsProviderIDAndImage(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	seededTop := scannerItemAtPath(t, harness.items, matrix).TopParentID
	if seededTop == nil {
		t.Fatal("seed scan stored no TopParentID")
	}

	refresher := &countingRefresher{enrich: true}
	newBackfillScanner(t, harness, refresher)

	result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if err != nil {
		t.Fatalf("RefreshExistingMetadata: %v", err)
	}
	if result.ProviderInvocations == 0 {
		t.Fatalf("ProviderInvocations = 0, want the counting provider invoked at least once")
	}
	if result.PersistedProviderIDs == 0 {
		t.Fatalf("PersistedProviderIDs = 0, want a provider ID persisted after reconcile")
	}
	if result.PersistedImages == 0 {
		t.Fatalf("PersistedImages = 0, want a remote image persisted after reconcile")
	}

	scanned := scannerItemAtPath(t, harness.items, matrix)
	if id, ok := scanned.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
		t.Fatalf("backfilled TMDb id = %q,%t, want 603,true", id, ok)
	}
	// The backfill write keeps the derived library link (it once wrote it as NULL).
	if scanned.TopParentID == nil || *scanned.TopParentID != *seededTop {
		t.Fatalf("backfilled TopParentID = %v, want the seeded %v", scanned.TopParentID, *seededTop)
	}
	hasRemotePrimary := false
	for _, image := range scanned.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == "https://image.tmdb.org/t/p/original/poster.jpg" {
			hasRemotePrimary = true
			break
		}
	}
	if !hasRemotePrimary {
		t.Fatalf("backfilled movie has no remote Primary image with the exact path; ImageInfos = %#v", scanned.ImageInfos)
	}
}

// TestRemoteRefreshPreservesLockedMetadataAndLocalImage proves the
// remote-backfill mode preserves a locked metadata field and an existing
// local image. The refresher locks the name and adds a local Primary image on
// the first pass, then a second backfill adds a remote Backdrop image. The
// locked name and the local Primary image remain unchanged.
func TestRemoteRefreshPreservesLockedMetadataAndLocalImage(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")

	lockedName := "Locked Original Title"
	// Seed the library with a default scan that locks the name and adds a
	// local Primary image. A default scan does not enforce the zero-work
	// error, so it persists the locked field and local artwork without
	// provider backfill.
	seed := &countingRefresher{enrich: false, lockName: &lockedName, enrichLocalImage: true}
	newBackfillScanner(t, harness, seed)
	if _, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	seeded := scannerItemAtPath(t, harness.items, matrix)
	if seeded.Name == nil || *seeded.Name != lockedName {
		t.Fatalf("seed scan name = %v, want %q", seeded.Name, lockedName)
	}
	var hasLocalPrimary bool
	for _, image := range seeded.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == matrix+".jpg" {
			hasLocalPrimary = true
		}
	}
	if !hasLocalPrimary {
		t.Fatalf("seed scan did not persist a local Primary image; ImageInfos = %#v", seeded.ImageInfos)
	}

	// Second pass: a backfill that re-discovers the local Primary image (as
	// the local image provider does from the directory listing) and adds a
	// TMDb id plus a remote Backdrop image. The locked name and the local
	// Primary image must remain unchanged.
	second := &countingRefresher{enrich: true, enrichLocalImage: true, remoteImageType: item.ImageTypeBackdrop}
	newBackfillScanner(t, harness, second)
	if _, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	redone := scannerItemAtPath(t, harness.items, matrix)
	if redone.Name == nil || *redone.Name != lockedName {
		t.Fatalf("second backfill name = %v, want the locked value %q — a locked field must survive the backfill", redone.Name, lockedName)
	}
	if id, ok := redone.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
		t.Fatalf("second backfill TMDb id = %q,%t, want 603,true", id, ok)
	}
	var stillLocalPrimary, hasRemoteBackdrop bool
	for _, image := range redone.ImageInfos {
		if image.Type == item.ImageTypePrimary && image.Path == matrix+".jpg" {
			stillLocalPrimary = true
		}
		if image.Type == item.ImageTypeBackdrop && strings.HasPrefix(image.Path, "https://image.tmdb.org/t/p/") {
			hasRemoteBackdrop = true
		}
	}
	if !stillLocalPrimary {
		t.Fatalf("second backfill dropped the local Primary image; ImageInfos = %#v", redone.ImageInfos)
	}
	if !hasRemoteBackdrop {
		t.Fatalf("second backfill did not add the remote Backdrop image; ImageInfos = %#v", redone.ImageInfos)
	}
}

// TestRemoteRefreshNoProviderPersistenceReturnsError proves the
// remote-backfill mode returns a non-nil error when the refresher makes no
// provider or image mutation. The library is seeded first, so a second
// backfill with a no-op refresher changes no provider ID or image; the scan
// must not report a false success.
func TestRemoteRefreshNoProviderPersistenceReturnsError(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")

	seed := &countingRefresher{enrich: true}
	newBackfillScanner(t, harness, seed)
	if _, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	noOp := &countingRefresher{enrich: false}
	newBackfillScanner(t, harness, noOp)
	result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if err == nil {
		t.Fatalf("RefreshExistingMetadata with no provider work returned nil; want a non-nil error — a zero-work report must not report success")
	}
	if result.ProviderInvocations == 0 {
		t.Fatalf("ProviderInvocations = 0, want the no-op refresher invoked at least once")
	}
	if result.PersistedProviderIDs != 0 || result.PersistedImages != 0 {
		t.Fatalf("PersistedProviderIDs = %d, PersistedImages = %d, want no provider or image mutation", result.PersistedProviderIDs, result.PersistedImages)
	}

	// The all-stored plan sent the stored root Folder and the stored movie to
	// MetadataBackfill with ForceRefresh. Both count as updated because the
	// ForceRefresh flag writes the DateLastSaved timestamp. No stored item was
	// skipped. The no-op refresher still produced no provider or image
	// mutation, so the zero-work guard returns an error.
	if result.BackfillUpdated != 2 {
		t.Fatalf("BackfillUpdated = %d, want 2 — the stored root Folder and the stored movie were both ForceRefresh-updated by the backfill", result.BackfillUpdated)
	}
	if result.BackfillSkipped != 0 {
		t.Fatalf("BackfillSkipped = %d, want 0 — no stored item was skipped in the all-stored plan", result.BackfillSkipped)
	}
}

// TestRemoteRefreshNoProviderInvocationReturnsError proves the
// remote-backfill mode returns a non-nil error when the remote stage makes no
// invocation at all. An empty library resolves no items, so the refresher is
// never called.
func TestRemoteRefreshNoProviderInvocationReturnsError(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})

	refresher := &countingRefresher{enrich: true}
	newBackfillScanner(t, harness, refresher)
	result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if err == nil {
		t.Fatalf("RefreshExistingMetadata on an empty library returned nil; want a non-nil error — no provider invocation must not report success")
	}
	if result.ProviderInvocations != 0 {
		t.Fatalf("ProviderInvocations = %d, want 0 on an empty library", result.ProviderInvocations)
	}

	// The empty plan sent no candidates to the backfill partition, so both
	// backfill counters are zero.
	if result.BackfillUpdated != 0 {
		t.Fatalf("BackfillUpdated = %d, want 0 — an empty plan has no stored item to update", result.BackfillUpdated)
	}
	if result.BackfillSkipped != 0 {
		t.Fatalf("BackfillSkipped = %d, want 0 — an empty plan has no stored item to skip", result.BackfillSkipped)
	}
}

// TestRemoteRefreshFolderErrorIncludesPathAndCause proves a folder error
// during the remote-backfill scan carries the folder path and the cause. The
// source directory is removed after the virtual folder is configured, so the
// scanner cannot enumerate it.
func TestRemoteRefreshFolderErrorIncludesPathAndCause(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})

	refresher := &countingRefresher{enrich: true}
	newBackfillScanner(t, harness, refresher)

	if err := os.RemoveAll(harness.source); err != nil {
		t.Fatalf("remove library source: %v", err)
	}
	result, _ := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if len(result.Errors) == 0 {
		t.Fatalf("RefreshResult.Errors is empty, want a folder error for the missing source %q", harness.source)
	}
	var foundPath bool
	for _, folderErr := range result.Errors {
		if strings.Contains(folderErr.Error(), harness.source) {
			foundPath = true
			break
		}
	}
	if !foundPath {
		t.Fatalf("no folder error mentions the source path %q; errors = %v", harness.source, result.Errors)
	}
}

// countingMediaProbe is a library.MediaProbe that records every Probe call. A
// normal scan probes resolved media; a remote-backfill scan skips probing. The
// test uses the call count to prove the queued normal scan did not inherit the
// backfill mode.
type countingMediaProbe struct {
	mu    sync.Mutex
	calls int
}

func (p *countingMediaProbe) Probe(_ context.Context, _ library.MediaProbeInput) (library.MediaProbeResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	size := int64(0)
	return library.MediaProbeResult{Container: "mkv", Size: &size}, nil
}

func (p *countingMediaProbe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// blockingCountingRefresher is a countingRefresher whose first Refresh call
// signals that it entered Refresh and then blocks until the test releases the
// release channel. This lets the test queue a TriggerRefresh intent while the
// RefreshExistingMetadata scan holds the serialization gate.
type blockingCountingRefresher struct {
	countingRefresher
	entered chan struct{}
	release chan struct{}
}

func (r *blockingCountingRefresher) Refresh(ctx context.Context, rc library.RefreshContext, target *item.Item) error {
	if r.release != nil {
		// Signal that Refresh entered, then block until the test releases.
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		// Clear the channels so subsequent items do not block.
		r.entered = nil
		r.release = nil
	}
	return r.countingRefresher.Refresh(ctx, rc, target)
}

// TestRemoteBackfillQueuedStandardRefreshUsesNormalMode proves that a normal
// scan queued while a RefreshExistingMetadata (remote-backfill) scan is in
// flight does not inherit the backfill mode. The queued normal scan must call
// the media prober; the backfill scan must not.
func TestRemoteBackfillQueuedStandardRefreshUsesNormalMode(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")

	probe := &countingMediaProbe{}
	refresher := &blockingCountingRefresher{
		countingRefresher: countingRefresher{enrich: true},
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	now := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
		MediaProbe:  probe,
	})

	// Start the remote-backfill scan in a goroutine. The refresher blocks on
	// the release channel, so scanMu stays held while the test queues a
	// TriggerRefresh intent.
	type scanOutcome struct {
		result library.RefreshResult
		err    error
	}
	done := make(chan scanOutcome, 1)
	go func() {
		result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
		done <- scanOutcome{result: result, err: err}
	}()

	// Wait until the refresher is inside Refresh.
	<-refresher.entered

	if err := harness.scanner.TriggerRefresh(library.DefaultRefreshOptions()); err != nil {
		t.Fatalf("Trigger while backfill scan in flight: %v", err)
	}

	// Release the refresher so the backfill scan completes and the drain loop
	// runs the queued normal scan.
	close(refresher.release)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("RefreshExistingMetadata did not complete within 30s")
	}

	// The backfill scan skipped media probing. The queued normal scan must
	// have called the media prober at least once for the movie file. If the
	// remoteBackfill flag leaked into the queued scan, the probe count stays
	// at zero.
	if calls := probe.Calls(); calls == 0 {
		t.Fatalf("MediaProbe calls = 0, want at least 1 — the queued normal scan inherited the backfill mode and skipped media probing")
	}
}

// barrierCountingRefresher enriches each item with a TMDb id and a remote
// Primary image, and uses a barrier to prove that two scanFolder workers
// call the refresher at the same time. Only movie items trigger the
// barrier. Folder refresher calls in planSources run sequentially before
// the scan queue dispatches the child tasks, so they must not consume
// barrier slots. The observed counter lets the test verify that two
// concurrent refresher calls entered the barrier before release.
type barrierCountingRefresher struct {
	observed atomic.Int32
	release  chan struct{}
}

func newBarrierCountingRefresher() *barrierCountingRefresher {
	return &barrierCountingRefresher{release: make(chan struct{})}
}

func (r *barrierCountingRefresher) Refresh(ctx context.Context, _ library.RefreshContext, target *item.Item) error {
	if target.Kind == item.KindMovie {
		n := r.observed.Add(1)
		if n == 2 {
			close(r.release)
		}
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	target.ProviderIDs.Set("Tmdb", "603")
	target.ImageInfos = append(target.ImageInfos, item.ImageInfo{
		Type: item.ImageTypePrimary,
		Path: "https://image.tmdb.org/t/p/original/poster.jpg",
	})
	return nil
}

// TestRemoteRefreshConcurrentProviderCounts proves that concurrent
// scanFolder workers update the provider invocation and persistence
// counts without a data race. The test creates two sibling directory
// plans under one media root. Each directory holds one movie file, so
// planSources resolves the directories as Folder items and enqueues two
// child tasks. The scan queue dispatches both child tasks to separate
// workers at the same time. A barrier in the refresher observes two
// concurrent refresher calls before it releases them. The test asserts
// the exact provider-invocation, persisted-provider-ID, and
// persisted-image counts from both plans. Counts include the folder
// items that planSources enriched sequentially and the movie items that
// the concurrent workers enriched, so the test proves the race-free
// aggregation loses no increments.
func TestRemoteRefreshConcurrentProviderCounts(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})

	// Create two sibling directories under the media root. Each
	// directory holds one movie file. planSources resolves the
	// directories as Folder items and enqueues a child task for each.
	// The scan queue then scans both directories concurrently.
	dirA := scannerPath(t, harness.source, "movies-a")
	dirB := scannerPath(t, harness.source, "movies-b")
	writeScannerFile(t, scannerPath(t, dirA, "movie-a.mkv"), "movie a")
	writeScannerFile(t, scannerPath(t, dirB, "movie-b.mkv"), "movie b")

	refresher := newBarrierCountingRefresher()
	now := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 2,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
	})

	result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if err != nil {
		t.Fatalf("RefreshExistingMetadata: %v", err)
	}

	// The barrier observed two concurrent movie-item refresher calls.
	// If the scan queue ran the child tasks one at a time, the first
	// movie call would block until the context cancelled and the scan
	// would fail. The scan completed without error, so both workers
	// entered the refresher at the same time.
	if got := refresher.observed.Load(); got != 2 {
		t.Fatalf("barrier observed = %d concurrent movie calls, want 2", got)
	}

	// Two Folder items (the sibling directories, enriched sequentially in
	// planSources) plus two Movie items (enriched concurrently by the two
	// workers) give four provider invocations, four persisted provider
	// IDs, and four persisted images. The counts include work from both
	// concurrent plans and must not lose increments.
	const want = 4
	if result.ProviderInvocations != want {
		t.Fatalf("ProviderInvocations = %d, want %d", result.ProviderInvocations, want)
	}
	if result.PersistedProviderIDs != want {
		t.Fatalf("PersistedProviderIDs = %d, want %d", result.PersistedProviderIDs, want)
	}
	if result.PersistedImages != want {
		t.Fatalf("PersistedImages = %d, want %d", result.PersistedImages, want)
	}
}

// replacingProviderRefresher is a library.Refresher that sets a TMDb id and a
// remote Primary image to configurable values. The test uses two instances:
// one seeds stale values, the other replaces them with new values. The
// replacement keeps the same cardinality (one provider ID, one remote image)
// but changes both values, so a count-only delta would report zero.
type replacingProviderRefresher struct {
	tmdbID    string
	imagePath string
}

func (r *replacingProviderRefresher) Refresh(_ context.Context, _ library.RefreshContext, target *item.Item) error {
	target.ProviderIDs.Set("Tmdb", r.tmdbID)
	target.ImageInfos = append(target.ImageInfos, item.ImageInfo{
		Type: item.ImageTypePrimary,
		Path: r.imagePath,
	})
	return nil
}

// TestRemoteRefreshCountsSameCardinalitySatelliteUpdates proves the
// persistence delta counts a provider-ID replacement and a remote-image
// replacement that keep the same cardinality but change the values. The
// test seeds one stale TMDb id and one stale remote image, then replaces
// both with new values. The scan must report a nil error, one persisted
// provider ID, and one persisted image, and the store must hold the new
// values.
func TestRemoteRefreshCountsSameCardinalitySatelliteUpdates(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")

	// Seed the library with a stale TMDb id and a stale remote image.
	seed := &replacingProviderRefresher{
		tmdbID:    "603",
		imagePath: "https://image.tmdb.org/t/p/original/stale.jpg",
	}
	newBackfillScannerWithRefresher(t, harness, seed)
	if _, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	seeded := scannerItemAtPath(t, harness.items, matrix)
	if id, ok := seeded.ProviderIDs.Get("Tmdb"); !ok || id != "603" {
		t.Fatalf("seed TMDb id = %q,%t, want 603,true", id, ok)
	}
	if !scannerHasRemoteImage(t, seeded, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/stale.jpg") {
		t.Fatalf("seed did not persist the stale remote image; ImageInfos = %#v", seeded.ImageInfos)
	}

	// Replace both values without changing the cardinality: one provider
	// ID and one remote image, both with new values.
	replacement := &replacingProviderRefresher{
		tmdbID:    "999",
		imagePath: "https://image.tmdb.org/t/p/original/fresh.jpg",
	}
	newBackfillScannerWithRefresher(t, harness, replacement)
	result, err := harness.scanner.RefreshExistingMetadata(t.Context(), nil)
	if err != nil {
		t.Fatalf("RefreshExistingMetadata: %v", err)
	}
	if result.PersistedProviderIDs != 1 {
		t.Fatalf("PersistedProviderIDs = %d, want 1 — a same-cardinality provider-ID replacement must count as one persistence", result.PersistedProviderIDs)
	}
	if result.PersistedImages != 1 {
		t.Fatalf("PersistedImages = %d, want 1 — a same-cardinality remote-image replacement must count as one persistence", result.PersistedImages)
	}

	refreshed := scannerItemAtPath(t, harness.items, matrix)
	if id, ok := refreshed.ProviderIDs.Get("Tmdb"); !ok || id != "999" {
		t.Fatalf("replaced TMDb id = %q,%t, want 999,true — the store must hold the new value", id, ok)
	}
	if !scannerHasRemoteImage(t, refreshed, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/fresh.jpg") {
		t.Fatalf("the store does not hold the new remote image; ImageInfos = %#v", refreshed.ImageInfos)
	}
	if scannerHasRemoteImage(t, refreshed, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/stale.jpg") {
		t.Fatalf("the stale remote image survived the replacement; ImageInfos = %#v", refreshed.ImageInfos)
	}
}

// scannerHasRemoteImage reports whether an item has a remote image with the
// given type and path.
func scannerHasRemoteImage(t *testing.T, value item.Item, imageType item.ImageType, path string) bool {
	t.Helper()
	for _, image := range value.ImageInfos {
		if image.Type == imageType && image.Path == path {
			return true
		}
	}
	return false
}
