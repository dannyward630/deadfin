// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"crypto/md5" // #nosec G501 -- deterministic fixture identities, not security.
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
	"go.uber.org/goleak"
)

func TestScannerRecursivelyReconcilesConfiguredSourceAndPreservesSatellites(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	first := scannerPath(t, harness.source, "alpha", "one.mkv")
	second := scannerPath(t, harness.source, "beta", "two.mkv")
	writeScannerFile(t, first, "one")
	writeScannerFile(t, second, "two")

	scannerScan(t, harness.scanner, "initial scan")
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("scanner virtual folder fixture = %#v, %v", folders, err)
	}
	if got := scannerItemAtPath(t, harness.items, folders[0].Path); got.Kind != item.KindFolder {
		t.Fatalf("scanned virtual folder = %#v, want Folder", got)
	}
	assertScannerPaths(t, harness.items, scannerPath(t, harness.source, "alpha"), first, scannerPath(t, harness.source, "beta"), second)
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, first), first, int64(len("one")))

	if err := os.Remove(second); err != nil {
		t.Fatalf("remove stale media: %v", err)
	}
	renamed := scannerPath(t, harness.source, "alpha", "renamed.mkv")
	if err := os.Rename(first, renamed); err != nil {
		t.Fatalf("rename media: %v", err)
	}
	writeScannerFile(t, scannerPath(t, harness.source, "beta", "new.mkv"), "new media")
	writeScannerFile(t, renamed, "renamed with changed size")

	scannerScan(t, harness.scanner, "reconcile add/update/remove/rename scan")
	assertScannerNoItemAtPath(t, harness.items, first)
	assertScannerNoItemAtPath(t, harness.items, second)
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, renamed), renamed, int64(len("renamed with changed size")))
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, scannerPath(t, harness.source, "beta", "new.mkv")), scannerPath(t, harness.source, "beta", "new.mkv"), int64(len("new media")))

	before := scannerItems(t, harness.items)
	scannerScan(t, harness.scanner, "unchanged second rescan")
	if after := scannerItems(t, harness.items); !reflect.DeepEqual(after, before) {
		t.Fatalf("unchanged rescan persisted a different item graph\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestScannerReconcilesConcurrentRemovedNestedBranchesAndSatellites(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 2})

	alphaKeep := scannerPath(t, harness.source, "alpha", "keep.mkv")
	betaKeep := scannerPath(t, harness.source, "beta", "keep.mkv")
	alphaRemovedFolder := scannerPath(t, harness.source, "alpha", "obsolete", "nested")
	betaRemovedFolder := scannerPath(t, harness.source, "beta", "obsolete", "nested")
	alphaRemoved := scannerPath(t, alphaRemovedFolder, "alpha-old.mkv")
	betaRemoved := scannerPath(t, betaRemovedFolder, "beta-old.mkv")
	writeScannerFile(t, alphaKeep, "alpha current")
	writeScannerFile(t, betaKeep, "beta current")
	writeScannerFile(t, alphaRemoved, "alpha stale")
	writeScannerFile(t, betaRemoved, "beta stale")

	scannerScan(t, harness.scanner, "seed nested branches")
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, alphaRemoved), alphaRemoved, int64(len("alpha stale")))
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, betaRemoved), betaRemoved, int64(len("beta stale")))
	alphaRemovedItem := scannerItemAtPath(t, harness.items, alphaRemoved)
	betaRemovedItem := scannerItemAtPath(t, harness.items, betaRemoved)
	alphaRemovedItem.AdditionalParts = []string{"/media/alpha-old-cd2.mkv", "/media/alpha-old-cd3.mkv"}
	betaRemovedItem.AdditionalParts = []string{"/media/beta-old-cd2.mkv"}
	for _, stale := range []item.Item{alphaRemovedItem, betaRemovedItem} {
		if err := harness.items.Upsert(t.Context(), stale); err != nil {
			t.Fatalf("seed stale scanner additional parts for %s: %v", stale.ID, err)
		}
	}
	assertScannerAdditionalPartsCount(t, harness.stores, alphaRemovedItem.ID, len(alphaRemovedItem.AdditionalParts))
	assertScannerAdditionalPartsCount(t, harness.stores, betaRemovedItem.ID, len(betaRemovedItem.AdditionalParts))

	if err := os.RemoveAll(scannerPath(t, harness.source, "alpha", "obsolete")); err != nil {
		t.Fatalf("remove alpha nested branch: %v", err)
	}
	if err := os.RemoveAll(scannerPath(t, harness.source, "beta", "obsolete")); err != nil {
		t.Fatalf("remove beta nested branch: %v", err)
	}

	arrived := make(chan struct{}, 2)
	released := make(chan struct{})
	var synchronize sync.Once
	var synchronizedPaths sync.Map
	resolver.block = func(ctx context.Context, path string) {
		if path != alphaKeep && path != betaKeep {
			return
		}
		if _, loaded := synchronizedPaths.LoadOrStore(path, struct{}{}); loaded {
			return
		}
		select {
		case arrived <- struct{}{}:
		case <-ctx.Done():
			return
		}
		synchronize.Do(func() {
			<-arrived
			<-arrived
			close(released)
		})
		select {
		case <-released:
		case <-ctx.Done():
		}
	}

	scannerScan(t, harness.scanner, "reconcile synchronized nested branch removals")
	resolver.block = nil
	assertScannerNoItemAtPath(t, harness.items, scannerPath(t, harness.source, "alpha", "obsolete"))
	assertScannerNoItemAtPath(t, harness.items, alphaRemovedFolder)
	assertScannerNoItemAtPath(t, harness.items, alphaRemoved)
	assertScannerNoItemAtPath(t, harness.items, scannerPath(t, harness.source, "beta", "obsolete"))
	assertScannerNoItemAtPath(t, harness.items, betaRemovedFolder)
	assertScannerNoItemAtPath(t, harness.items, betaRemoved)
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, alphaKeep), alphaKeep, int64(len("alpha current")))
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, betaKeep), betaKeep, int64(len("beta current")))
	assertScannerAdditionalPartsCount(t, harness.stores, alphaRemovedItem.ID, 0)
	assertScannerAdditionalPartsCount(t, harness.stores, betaRemovedItem.ID, 0)
	assertScannerNoAdditionalPartOrphans(t, harness.stores)

	before := scannerItems(t, harness.items)
	for cycle := 1; cycle <= 3; cycle++ {
		scannerScan(t, harness.scanner, "unchanged reconciliation cycle")
		assertScannerNoItemAtPath(t, harness.items, alphaRemoved)
		assertScannerNoItemAtPath(t, harness.items, betaRemoved)
		if after := scannerItems(t, harness.items); !reflect.DeepEqual(after, before) {
			t.Fatalf("unchanged reconciliation cycle %d persisted a different item graph\nbefore: %#v\nafter:  %#v", cycle, before, after)
		}
	}
}

// TestScannerPersistsSourceCompatibleSortName proves the real production scan
// path materializes the stock sort key even when its resolver supplies only a
// display name. The expected literal is deliberately independent of
// item.ComputeSortName so this detects a missing reconciliation materialization.
func TestScannerPersistsSourceCompatibleSortName(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "The Matrix 2.mkv")
	writeScannerFile(t, media, "matrix")

	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan source item: %v", err)
	}
	got := scannerItemAtPath(t, harness.items, media)
	if got.Name == nil || *got.Name != "The Matrix 2" {
		t.Fatalf("scanned display name = %v, want The Matrix 2", got.Name)
	}
	if got.SortName == nil || *got.SortName != "matrix 0000000002" {
		t.Fatalf("persisted sort name = %v, want source-compatible matrix 0000000002", got.SortName)
	}
}

func TestScannerPreservesInaccessibleSubtreeWhileReconcilingAccessibleSibling(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	accessible := scannerPath(t, harness.source, "accessible", "remove.mkv")
	inaccessible := scannerPath(t, harness.source, "inaccessible", "keep.mkv")
	writeScannerFile(t, accessible, "remove")
	writeScannerFile(t, inaccessible, "keep")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if err := os.Remove(accessible); err != nil {
		t.Fatalf("remove accessible media: %v", err)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	deniedDirectory := scannerPath(t, harness.source, "inaccessible")
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 2,
		ReadDir: func(path string) ([]library.Entry, error) {
			if path == deniedDirectory {
				return nil, fs.ErrPermission
			}
			return scannerReadDir(path)
		},
	})

	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan with inaccessible folder: %v", err)
	}
	assertScannerNoItemAtPath(t, harness.items, accessible)
	if got := scannerItemAtPath(t, harness.items, inaccessible); got.Path == nil || *got.Path != inaccessible {
		t.Fatalf("inaccessible descendant = %#v, want preserved path %q", got, inaccessible)
	}
}

func TestScannerRollsBackOnlyMalformedFolderAndContinuesSiblings(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	good := scannerPath(t, harness.source, "good", "movie.mkv")
	bad := scannerPath(t, harness.source, "bad", "movie.mkv")
	writeScannerFile(t, good, "good old")
	writeScannerFile(t, bad, "bad old")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	beforeBad := scannerItemAtPath(t, harness.items, bad)

	writeScannerFile(t, good, "good updated")
	writeScannerFile(t, bad, "bad updated")
	resolver.setMalformed(bad)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, library.NewChain(resolver), library.ScannerOptions{Concurrency: 2})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan containing malformed resolver result: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("scan containing malformed resolver result did not report a folder error")
	}
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, good), good, int64(len("good updated")))
	if afterBad := scannerItemAtPath(t, harness.items, bad); !reflect.DeepEqual(afterBad, beforeBad) {
		t.Fatalf("malformed folder changed despite its failed reconciliation\nbefore: %#v\nafter:  %#v", beforeBad, afterBad)
	}
}

func TestScannerRejectsResolvedPathThatDoesNotMatchEnumeratedEntry(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	good := scannerPath(t, harness.source, "good", "movie.mkv")
	bad := scannerPath(t, harness.source, "bad", "movie.mkv")
	writeScannerFile(t, good, "good old")
	writeScannerFile(t, bad, "bad old")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	beforeBad := scannerItemAtPath(t, harness.items, bad)

	writeScannerFile(t, good, "good updated")
	resolver.setPathOverride(bad, scannerPath(t, harness.source, "bad", "invented.mkv"))
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with mismatched entry result: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("mismatched entry path was not reported")
	}
	assertScannerMediaSatellites(t, scannerItemAtPath(t, harness.items, good), good, int64(len("good updated")))
	if afterBad := scannerItemAtPath(t, harness.items, bad); !reflect.DeepEqual(afterBad, beforeBad) {
		t.Fatalf("mismatched entry path changed its folder subtree\nbefore: %#v\nafter:  %#v", beforeBad, afterBad)
	}
}

func TestScannerRejectsConfiguredSourcePathMismatchBeforeReconciliation(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	secondSource := scannerPath(t, t.TempDir(), "second-media")
	if err := os.Mkdir(secondSource, 0o755); err != nil {
		t.Fatalf("create second configured source: %v", err)
	}
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, secondSource); err != nil {
		t.Fatalf("add second configured source: %v", err)
	}
	firstMedia := scannerPath(t, harness.source, "first.mkv")
	secondMedia := scannerPath(t, secondSource, "second.mkv")
	writeScannerFile(t, firstMedia, "first")
	writeScannerFile(t, secondMedia, "second")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed multi-source scan: %v", err)
	}
	beforeFirst := scannerItemAtPath(t, harness.items, firstMedia)
	beforeSecond := scannerItemAtPath(t, harness.items, secondMedia)

	// Override the first media entry path to cause a resolution mismatch.
	resolver.setPathOverride(firstMedia, scannerPath(t, harness.source, "invented-entry"))
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan with mismatched configured source: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("mismatched configured source path was not reported")
	}
	if afterFirst := scannerItemAtPath(t, harness.items, firstMedia); !reflect.DeepEqual(afterFirst, beforeFirst) {
		t.Fatalf("mismatched configured source changed its persisted item\nbefore: %#v\nafter:  %#v", beforeFirst, afterFirst)
	}
	if afterSecond := scannerItemAtPath(t, harness.items, secondMedia); !reflect.DeepEqual(afterSecond, beforeSecond) {
		t.Fatalf("rejected source plan changed unaffected configured sibling\nbefore: %#v\nafter:  %#v", beforeSecond, afterSecond)
	}
}

func TestScannerCanonicalizesCleanEquivalentSourceBeforeInaccessibleRescan(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "canonical source")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	beforeMedia := scannerItemAtPath(t, harness.items, media)

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 2,
		ReadDir: func(path string) ([]library.Entry, error) {
			if path == harness.source {
				return nil, fs.ErrPermission
			}
			return scannerReadDir(path)
		},
	})

	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("inaccessible source rescan: %v", err)
	}
	if afterMedia := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(afterMedia, beforeMedia) {
		t.Fatalf("inaccessible source lost persisted descendant\nbefore: %#v\nafter:  %#v", beforeMedia, afterMedia)
	}
}

func TestScannerDefaultResolversReceiveDirectoryChildren(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, h.topology, "movies", "music")
	artist := scannerPath(t, h.source, "Artist")
	album := scannerPath(t, artist, "albums")
	writeScannerFile(t, scannerPath(t, album, "Track.mp3"), "audio")
	if err := h.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	h.chain = library.NewDefaultChain(naming.DefaultOptions())
	h.scanner = newScanner(t, h.topology, h.items, h.chain, library.ScannerOptions{Concurrency: 2})
	if _, err := h.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("default resolver scan: %v", err)
	}
	if got := scannerItemAtPath(t, h.items, artist); got.Kind != item.KindMusicArtist {
		t.Fatalf("artist kind = %s, want MusicArtist", got.Kind)
	}
	if got := scannerItemAtPath(t, h.items, album); got.Kind != item.KindMusicAlbum {
		t.Fatalf("album kind = %s, want MusicAlbum", got.Kind)
	}
}

func TestScannerDefaultPhotoAlbumResolverReceivesDirectoryChildren(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, h.topology, "movies", "photos")
	album := scannerPath(t, h.source, "Holiday")
	writeScannerFile(t, scannerPath(t, album, "IMG_0001.jpg"), "photo")
	if err := h.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	h.chain = library.NewDefaultChain(naming.DefaultOptions())
	h.scanner = newScanner(t, h.topology, h.items, h.chain, library.ScannerOptions{Concurrency: 2})
	if _, err := h.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("default photo scan: %v", err)
	}
	if got := scannerItemAtPath(t, h.items, album); got.Kind != item.KindPhotoAlbum {
		t.Fatalf("photo album kind = %s, want PhotoAlbum", got.Kind)
	}
}

func TestScannerRecursiveContextsCarryAncestorsForEpisodeResolution(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, h.topology, "movies", "tvshows")
	seriesPath := scannerPath(t, h.source, "Synthetic Show (2024)")
	seasonPath := scannerPath(t, seriesPath, "Season 01")
	episodePath := scannerPath(t, seasonPath, "Synthetic Show S01E01 - Pilot.mkv")
	writeScannerFile(t, episodePath, "episode")
	if err := h.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	h.chain = library.NewDefaultChain(naming.DefaultOptions())
	h.scanner = newScanner(t, h.topology, h.items, h.chain, library.ScannerOptions{Concurrency: 2})
	if _, err := h.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("TV scan: %v", err)
	}
	series := scannerItemAtPath(t, h.items, seriesPath)
	season := scannerItemAtPath(t, h.items, seasonPath)
	episode := scannerItemAtPath(t, h.items, episodePath)
	if episode.Kind != item.KindEpisode || episode.SeriesID == nil || *episode.SeriesID != series.ID || episode.SeasonID == nil || *episode.SeasonID != season.ID || episode.SeriesName == nil || series.Name == nil || *episode.SeriesName != *series.Name {
		t.Fatalf("episode ancestor facts = %#v, series %#v season %#v", episode, series, season)
	}
}

func TestScannerMalformedSourcePlanLeavesVirtualAndSubtreeAbsent(t *testing.T) {
	r := &scannerFixtureResolver{}
	h := newScannerHarness(t, r)
	folders, err := h.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("virtual folder fixture = %#v, %v", folders, err)
	}
	// The scanner flattens the source root: resolveFolder resolves the
	// entries inside the source. A malformed entry path makes the plan fail.
	movie := scannerPath(t, h.source, "movie.mkv")
	writeScannerFile(t, movie, "movie")
	r.setPathOverride(movie, scannerPath(t, h.source, "invented-entry"))
	result, err := h.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("malformed source scan: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("malformed source plan was not reported")
	}
	assertScannerNoItemAtPath(t, h.items, folders[0].Path)
	assertScannerNoItemAtPath(t, h.items, h.source)
	if got := scannerItems(t, h.items); len(got) != 0 {
		t.Fatalf("malformed source plan persisted %#v", got)
	}
}

func TestScannerUnchangedRescanReportsNoMutations(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	writeScannerFile(t, scannerPath(t, h.source, "movie.mkv"), "same")
	if _, err := h.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	observer, err := storetest.NewSQLiteItemUpdateObserver(t.Context(), h.stores)
	if err != nil {
		t.Fatalf("observe scanner item updates: %v", err)
	}
	result, err := h.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("unchanged scan: %v", err)
	}
	if result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("unchanged mutation result = %#v", result)
	}
	writes, err := observer.Count(t.Context())
	if err != nil {
		t.Fatalf("count scanner writes: %v", err)
	}
	if writes != 0 {
		t.Fatalf("unchanged scan wrote %d item rows, including virtual folder", writes)
	}
}

func TestScannerRejectsOrDeduplicatesNormalizedSourceAlias(t *testing.T) {
	h := newScannerHarness(t, &scannerFixtureResolver{})
	alias := h.source + string(filepath.Separator) + "."
	if err := h.topology.AddMediaPath(t.Context(), h.virtualID, alias); err == nil {
		folders, listErr := h.topology.VirtualFolders(t.Context())
		if listErr != nil {
			t.Fatalf("list accepted alias: %v", listErr)
		}
		count := 0
		for _, path := range folders[0].MediaPaths {
			if filepath.Clean(path) == h.source {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("normalized media paths = %#v, want one", folders[0].MediaPaths)
		}
	}
	writeScannerFile(t, scannerPath(t, h.source, "movie.mkv"), "one")
	if _, err := h.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("alias scan: %v", err)
	}
	movies := 0
	for _, candidate := range scannerItems(t, h.items) {
		if candidate.Path != nil && *candidate.Path == scannerPath(t, h.source, "movie.mkv") {
			movies++
		}
	}
	if movies != 1 {
		t.Fatalf("movie count = %d, want one (no duplicate from alias)", movies)
	}
	assertScannerNoItemAtPath(t, h.items, alias)
}

func TestScannerNormalizesOverlappingConfiguredRoots(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	nested := scannerPath(t, harness.source, "sub")
	media := scannerPath(t, nested, "movie.mkv")
	writeScannerFile(t, media, "movie")
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, nested); err != nil {
		t.Fatalf("configure nested source root: %v", err)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	nestedRefreshes := 0
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     scannerReadDir,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Path == nil || *candidate.Path != nested {
				return nil
			}
			nestedRefreshes++
			if nestedRefreshes == 1 {
				candidate.Overview = scannerString("provider refresh from normalized root")
				return nil
			}
			candidate.Overview = scannerString("provider refreshed duplicate root")
			return nil
		}),
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan overlapping roots: %v", err)
	}
	// The scanner flattens the source root: the nested directory and its
	// movie are scanned once (no duplicate from the overlapping root).
	// The scanner creates one root Folder for the normalized outer root.
	// The nested directory and its movie are scanned once (no duplicate from
	// the overlapping root). The root Folder, nested directory, and movie
	// are three inserts.
	if len(result.Errors) != 0 || result.Inserted != 3 {
		t.Fatalf("normalized root scan result = %#v, want root Folder, nested directory, and movie inserted once", result)
	}
	collection := scannerCollectionFolder(t, harness)
	nestedItem := scannerItemAtPath(t, harness.items, nested)
	movieItem := scannerItemAtPath(t, harness.items, media)
	if nestedItem.ParentID == nil || *nestedItem.ParentID == collection.ID {
		t.Fatalf("normalized root hierarchy nested=%#v, want nested parent not the CollectionFolder (it is a child of the root Folder)", nestedItem)
	}
	if movieItem.ParentID == nil || *movieItem.ParentID != nestedItem.ID {
		t.Fatalf("normalized root hierarchy movie=%#v, want nested parent %s", movieItem, nestedItem.ID)
	}
	nestedRoot, err := harness.items.Get(t.Context(), *nestedItem.ParentID)
	if err != nil {
		t.Fatalf("load nested parent root Folder: %v", err)
	}
	if nestedRoot.Kind != item.KindFolder || nestedRoot.ParentID == nil || *nestedRoot.ParentID != collection.ID {
		t.Fatalf("nested parent root = %#v, want Folder child of CollectionFolder %s", nestedRoot, collection.ID)
	}
	if nestedItem.Overview == nil || *nestedItem.Overview != "provider refresh from normalized root" {
		t.Fatalf("nested provider side effect = %#v, want exactly one refresh through the normalized outer root", nestedItem.Overview)
	}
}

func TestScannerProgressCallbackCloseReturns(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	h := newScannerHarness(t, &scannerFixtureResolver{})
	writeScannerFile(t, scannerPath(t, h.source, "movie.mkv"), "close")
	if err := h.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	closed := make(chan error, 1)
	var once sync.Once
	var scanner *library.Scanner
	scanner = newScanner(t, h.topology, h.items, h.chain, library.ScannerOptions{Concurrency: 1, Progress: func(event library.RefreshProgress) {
		if event.Status == "started" {
			once.Do(func() { closed <- scanner.Close() })
		}
	}})
	h.scanner = scanner
	done := make(chan error, 1)
	go func() { _, err := scanner.Scan(context.Background()); done <- err }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("callback close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("progress callback Close deadlocked")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan did not return after callback Close")
	}
}

func TestScannerProgressTraversalIsDeterministicAndFanoutIsBounded(t *testing.T) {
	var progress []string
	var progressMu sync.Mutex
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	for _, directory := range []string{"delta", "alpha", "charlie", "bravo"} {
		writeScannerFile(t, scannerPath(t, harness.source, directory, "movie.mkv"), directory)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	release := make(chan struct{})
	reached := make(chan struct{})
	var callsMu sync.Mutex
	active, peak := 0, 0
	resolver.block = func(ctx context.Context, path string) {
		if filepath.Ext(path) != ".mkv" {
			return
		}
		callsMu.Lock()
		active++
		if active > peak {
			peak = active
		}
		if active == 2 {
			select {
			case <-reached:
			default:
				close(reached)
			}
		}
		callsMu.Unlock()
		defer func() {
			callsMu.Lock()
			active--
			callsMu.Unlock()
		}()
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, library.NewChain(resolver), library.ScannerOptions{
		Concurrency: 2,
		Progress: func(event library.RefreshProgress) {
			progressMu.Lock()
			progress = append(progress, event.Path)
			progressMu.Unlock()
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(t.Context())
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("scanner did not use the configured two-way fanout")
	}
	callsMu.Lock()
	gotPeak := peak
	callsMu.Unlock()
	if gotPeak > 2 {
		t.Fatalf("resolver concurrency peak = %d, want at most configured limit 2", gotPeak)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("bounded scan: %v", err)
	}

	progressMu.Lock()
	first := append([]string(nil), progress...)
	progress = nil
	progressMu.Unlock()
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("determinism rescan: %v", err)
	}
	progressMu.Lock()
	second := append([]string(nil), progress...)
	progressMu.Unlock()
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("progress traversal differs across unchanged scans\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

func TestScannerReturnsCallerCancellationWithoutLeakingWorkers(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	started := make(chan struct{})
	resolver := &scannerFixtureResolver{
		block: func(ctx context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "slow", "movie.mkv"), "slow")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan did not reach cancellable resolver")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller-canceled scan error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller-canceled scan did not return")
	}
}

func TestScannerCloseCancelsActiveScanWithoutLeakingWorkers(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	started := make(chan struct{})
	resolver := &scannerFixtureResolver{
		block: func(ctx context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "slow", "movie.mkv"), "slow")

	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(context.Background())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan did not reach cancellable resolver")
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close active scanner: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("active scan succeeded after scanner close")
		}
	case <-time.After(time.Second):
		t.Fatal("active scan did not return after scanner close")
	}
}

func TestScannerTriggerDeduplicatesCompletedRunsAndCloseCancelsOwnedWorker(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	started := make(chan int, 3)
	firstRelease := make(chan struct{})
	var mu sync.Mutex
	runs := 0
	resolver := &scannerFixtureResolver{
		block: func(ctx context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			mu.Lock()
			runs++
			run := runs
			mu.Unlock()
			started <- run
			if run == 1 {
				<-firstRelease
				return
			}
			<-ctx.Done()
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "slow", "movie.mkv"), "slow")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	completions := make(chan struct{}, 2)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 2,
		RefreshComplete: func(library.RefreshResult) {
			completions <- struct{}{}
		},
	})
	subscription := harness.scanner.Progress().Subscribe(events.SubscriptionOptions{Buffer: 16})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close scanner progress subscription: %v", err)
		}
	})

	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger initial scan: %v", err)
	}
	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger duplicate active scan: %v", err)
	}
	assertScannerTriggeredRun(t, started, 1)

	close(firstRelease)
	assertScannerCompletion(t, subscription.Events())
	assertScannerRefreshCompletion(t, completions)
	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger scan after completion: %v", err)
	}
	assertScannerTriggeredRun(t, started, 2)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close scanner with triggered run: %v", err)
	}
}

// TestScannerTriggerRefreshQueuesStrongerIntentDuringActiveScan proves that an
// item refresh accepted while a plain background scan owns the scanner is
// preserved as one follow-up scan. Its full-refresh intent must reach the
// metadata stage, and its requested trickplay item must be reported exactly
// once to the completion consumer.
func TestScannerTriggerRefreshQueuesStrongerIntentDuringActiveScan(t *testing.T) {
	firstRelease := make(chan struct{})
	started := make(chan int, 2)
	var runsMu sync.Mutex
	runs := 0
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "queued", "movie.mkv")
	writeScannerFile(t, movie, "queued refresh")
	resolver.block = func(ctx context.Context, path string) {
		if path != movie {
			return
		}
		runsMu.Lock()
		runs++
		run := runs
		runsMu.Unlock()
		started <- run
		if run == 1 {
			select {
			case <-firstRelease:
			case <-ctx.Done():
			}
		}
	}

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	refreshes := make(chan library.RefreshContext, 2)
	completions := make(chan library.RefreshResult, 2)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
			if candidate.Path != nil && *candidate.Path == movie {
				refreshes <- refresh
			}
			return nil
		}),
		RefreshComplete: func(result library.RefreshResult) {
			completions <- result
		},
	})
	subscription := harness.scanner.Progress().Subscribe(events.SubscriptionOptions{Buffer: 16})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close scanner progress subscription: %v", err)
		}
	})

	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger initial scan: %v", err)
	}
	assertScannerTriggeredRun(t, started, 1)
	firstRequested := scannerID(movie)
	secondRequested := scannerID(movie + ".second")
	if firstRequested == secondRequested {
		t.Fatal("distinct trickplay fixture paths produced the same item ID")
	}
	metadataIntent := library.RefreshOptions{
		Metadata:                   library.RefreshModeFullRefresh,
		ReplaceAllMetadata:         true,
		RegenerateTrickplayItemIDs: []dto.UUID{firstRequested},
	}
	if err := harness.scanner.TriggerRefresh(metadataIntent); err != nil {
		t.Fatalf("queue metadata refresh while initial scan is active: %v", err)
	}
	// The caller can reuse its request buffer after asynchronous acceptance; the
	// queued scan must retain its own copy of the first item ID.
	metadataIntent.RegenerateTrickplayItemIDs[0] = dto.UUID{0xff}
	imageIntent := library.RefreshOptions{
		Image:                      library.RefreshModeFullRefresh,
		ReplaceAllImages:           true,
		RegenerateTrickplayItemIDs: []dto.UUID{secondRequested, firstRequested},
	}
	if err := harness.scanner.TriggerRefresh(imageIntent); err != nil {
		t.Fatalf("merge image refresh while follow-up is pending: %v", err)
	}

	close(firstRelease)
	assertScannerTriggeredRun(t, started, 2)
	assertScannerCompletion(t, subscription.Events())
	assertScannerCompletion(t, subscription.Events())

	// The first completion belongs to the original plain scan. The one queued
	// follow-up preserves the strongest metadata/image intent and every distinct
	// item request in first-seen order, even when an item appears twice.
	receiveScannerSignal(t, refreshes, "initial scanner item-refresh intent")
	followUpRefresh := receiveScannerSignal(t, refreshes, "queued follow-up item-refresh intent")
	if followUpRefresh.Mode != metadataIntent.Metadata ||
		followUpRefresh.Image != imageIntent.Image ||
		!followUpRefresh.ReplaceAllMetadata ||
		!followUpRefresh.ReplaceAllImages {
		t.Fatalf("follow-up refresh intent = %#v, want full metadata/image with both replacement flags", followUpRefresh)
	}

	firstCompletion := receiveScannerSignal(t, completions, "initial scanner refresh completion")
	if got := firstCompletion.RegenerateTrickplayItemIDs; len(got) != 0 {
		t.Fatalf("initial completion trickplay requests = %#v, want none", got)
	}
	followUpCompletion := receiveScannerSignal(t, completions, "queued follow-up scanner refresh completion")
	wantRequested := []dto.UUID{firstRequested, secondRequested}
	if got := followUpCompletion.RegenerateTrickplayItemIDs; !reflect.DeepEqual(got, wantRequested) {
		t.Fatalf("follow-up completion trickplay requests = %#v, want %#v", got, wantRequested)
	}
	// LastResult is a snapshot: callers cannot mutate its plural dispatch list.
	latest, _, found := harness.scanner.LastResult()
	if !found {
		t.Fatal("LastResult missing queued follow-up completion")
	}
	latest.RegenerateTrickplayItemIDs[0] = dto.UUID{}
	snapshot, _, found := harness.scanner.LastResult()
	if !found {
		t.Fatal("LastResult missing after snapshot mutation")
	}
	if got := snapshot.RegenerateTrickplayItemIDs; !reflect.DeepEqual(got, wantRequested) {
		t.Fatalf("LastResult trickplay requests after caller mutation = %#v, want %#v", got, wantRequested)
	}

	runsMu.Lock()
	gotRuns := runs
	runsMu.Unlock()
	if gotRuns != 2 {
		t.Fatalf("background scan runs = %d, want initial scan plus one merged follow-up", gotRuns)
	}
}

// TestScannerTriggerRefreshSurvivesDrainHandoff forces TriggerRefresh to race
// the active worker's final pending check. Whether it wins the queued path or
// starts the next worker after that check, the explicit refresh must run once.
func TestScannerTriggerRefreshSurvivesDrainHandoff(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	firstRelease := make(chan struct{})
	started := make(chan int, 2)
	var runsMu sync.Mutex
	runs := 0
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "handoff", "movie.mkv")
	writeScannerFile(t, movie, "drain handoff")
	resolver.block = func(_ context.Context, path string) {
		if path != movie {
			return
		}
		runsMu.Lock()
		runs++
		run := runs
		runsMu.Unlock()
		started <- run
		if run == 1 {
			<-firstRelease
		}
	}

	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger initial scan: %v", err)
	}
	assertScannerTriggeredRun(t, started, 1)

	queued := make(chan error, 1)
	race := make(chan struct{})
	go func() {
		<-race
		queued <- harness.scanner.TriggerRefresh(library.RefreshOptions{
			Metadata: library.RefreshModeFullRefresh,
			Image:    library.RefreshModeFullRefresh,
		})
	}()
	go func() {
		<-race
		close(firstRelease)
	}()
	close(race)
	if err := receiveScannerSignal(t, queued, "drain-handoff TriggerRefresh result"); err != nil {
		t.Fatalf("trigger refresh at drain handoff: %v", err)
	}
	assertScannerTriggeredRun(t, started, 2)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close scanner after drain handoff: %v", err)
	}
	runsMu.Lock()
	gotRuns := runs
	runsMu.Unlock()
	if gotRuns != 2 {
		t.Fatalf("background scan runs across drain handoff = %d, want 2", gotRuns)
	}
}

// TestScannerScanWithOptionsDrainsQueuedExplicitRefresh keeps an explicit
// refresh accepted behind a synchronous scan in that scan's drain ownership.
func TestScannerScanWithOptionsDrainsQueuedExplicitRefresh(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	started := make(chan int, 2)
	var runsMu sync.Mutex
	runs := 0
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "synchronous", "movie.mkv")
	writeScannerFile(t, movie, "synchronous queued refresh")
	resolver.block = func(ctx context.Context, path string) {
		if path != movie {
			return
		}
		runsMu.Lock()
		runs++
		run := runs
		runsMu.Unlock()
		started <- run
		switch run {
		case 1:
			select {
			case <-firstRelease:
			case <-ctx.Done():
			}
		case 2:
			select {
			case <-secondRelease:
			case <-ctx.Done():
			}
		}
	}

	scanDone := make(chan error, 1)
	go func() {
		_, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions())
		scanDone <- err
	}()
	assertScannerTriggeredRun(t, started, 1)
	if err := harness.scanner.TriggerRefresh(library.RefreshOptions{
		Metadata: library.RefreshModeFullRefresh,
		Image:    library.RefreshModeFullRefresh,
	}); err != nil {
		t.Fatalf("queue explicit refresh behind synchronous scan: %v", err)
	}
	close(firstRelease)
	assertScannerTriggeredRun(t, started, 2)
	select {
	case err := <-scanDone:
		t.Fatalf("synchronous scan returned before queued refresh drained: %v", err)
	default:
	}
	close(secondRelease)
	if err := receiveScannerSignal(t, scanDone, "synchronous scan completion after queued refresh"); err != nil {
		t.Fatalf("synchronous scan with queued refresh: %v", err)
	}
	runsMu.Lock()
	gotRuns := runs
	runsMu.Unlock()
	if gotRuns != 2 {
		t.Fatalf("synchronous scan and queued refresh runs = %d, want 2", gotRuns)
	}
}

// TestScannerCloseDropsPendingRefreshWithoutLeakingWorkers proves shutdown
// cancels the active worker and discards, rather than starts, its queued work.
func TestScannerCloseDropsPendingRefreshWithoutLeakingWorkers(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	var runsMu sync.Mutex
	runs := 0
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "close", "movie.mkv")
	writeScannerFile(t, movie, "close pending refresh")
	resolver.block = func(ctx context.Context, path string) {
		if path != movie {
			return
		}
		runsMu.Lock()
		runs++
		run := runs
		runsMu.Unlock()
		if run != 1 {
			return
		}
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		<-release
	}

	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger initial scan: %v", err)
	}
	receiveScannerSignal(t, started, "initial triggered scanner start")
	if err := harness.scanner.TriggerRefresh(library.RefreshOptions{Metadata: library.RefreshModeFullRefresh}); err != nil {
		t.Fatalf("queue refresh before close: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- harness.scanner.Close() }()
	receiveScannerSignal(t, cancelled, "active scanner cancellation acknowledgement")
	close(release)
	if err := receiveScannerSignal(t, closed, "scanner Close result after pending refresh"); err != nil {
		t.Fatalf("close scanner with pending refresh: %v", err)
	}
	runsMu.Lock()
	gotRuns := runs
	runsMu.Unlock()
	if gotRuns != 1 {
		t.Fatalf("scanner runs after Close with pending refresh = %d, want 1", gotRuns)
	}
}

func TestScannerTriggerRejectsRequestsAfterCloseBegins(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	started := make(chan struct{})
	closeObserved := make(chan struct{})
	release := make(chan struct{})
	resolver := &scannerFixtureResolver{
		block: func(ctx context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			close(started)
			<-ctx.Done()
			close(closeObserved)
			<-release
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "slow", "movie.mkv"), "slow")

	if err := harness.scanner.Trigger(); err != nil {
		t.Fatalf("trigger initial scan: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("triggered scanner did not reach blocking resolver")
	}
	closed := make(chan error, 1)
	go func() { closed <- harness.scanner.Close() }()
	select {
	case <-closeObserved:
	case <-time.After(time.Second):
		t.Fatal("scanner Close did not cancel triggered scan")
	}
	if err := harness.scanner.Trigger(); !errors.Is(err, library.ErrScannerClosed) {
		t.Fatalf("trigger while Close is waiting = %v, want ErrScannerClosed", err)
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close triggered scanner: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scanner Close did not return after active worker released")
	}
}

// scannerExpectedSignalDeadline tolerates scheduler contention from the full
// library race suite while still diagnosing a missing scanner signal locally.
const scannerExpectedSignalDeadline = 10 * time.Second

func receiveScannerSignal[T any](t *testing.T, signal <-chan T, name string) T {
	t.Helper()
	timer := time.NewTimer(scannerExpectedSignalDeadline)
	defer timer.Stop()

	select {
	case value, ok := <-signal:
		if !ok {
			t.Fatalf("%s channel closed before signal", name)
		}
		return value
	case <-timer.C:
		t.Fatalf("timed out after %s waiting for %s", scannerExpectedSignalDeadline, name)
		var zero T
		return zero
	}
}

func assertScannerTriggeredRun(t *testing.T, started <-chan int, want int) {
	t.Helper()
	got := receiveScannerSignal(t, started, "triggered scanner run")
	if got != want {
		t.Fatalf("triggered scan run = %d, want %d", got, want)
	}
}

func assertScannerCompletion(t *testing.T, progress <-chan library.RefreshProgress) {
	t.Helper()
	for {
		event := receiveScannerSignal(t, progress, "triggered scanner completion progress")
		if event.Status == "completed" {
			return
		}
	}
}

func assertScannerRefreshCompletion(t *testing.T, completions <-chan struct{}) {
	t.Helper()
	receiveScannerSignal(t, completions, "triggered scanner refresh completion")
}

type scannerHarness struct {
	source    string
	virtualID dto.UUID
	stores    store.Stores
	topology  *library.Topology
	items     *store.ItemStore
	chain     *library.Chain
	scanner   *library.Scanner
}

func newScannerHarness(t *testing.T, resolver *scannerFixtureResolver) *scannerHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open scanner SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close scanner SQLite stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open scanner topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close scanner topology: %v", err)
		}
	})
	source := scannerPath(t, t.TempDir(), "media")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatalf("create scanner source: %v", err)
	}
	folder, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}})
	if err != nil {
		t.Fatalf("add scanner virtual folder: %v", err)
	}
	chain := library.NewChain(resolver)
	items := store.NewItemStore(stores)
	scanner := newScanner(t, topology, items, chain, library.ScannerOptions{Concurrency: 2})
	harness := &scannerHarness{source: source, virtualID: folder.ID, stores: stores, topology: topology, items: items, chain: chain, scanner: scanner}
	t.Cleanup(func() {
		if err := harness.scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})
	return harness
}

func newScanner(t *testing.T, topology *library.Topology, items *store.ItemStore, chain *library.Chain, options library.ScannerOptions) *library.Scanner {
	t.Helper()
	scanner, err := library.NewScanner(topology, items, chain, options)
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	return scanner
}

type scannerFixtureResolver struct {
	mu           sync.Mutex
	malformed    map[string]bool
	pathOverride map[string]string
	block        func(context.Context, string)
}

func (r *scannerFixtureResolver) Priority() library.ResolverPriority {
	return library.ResolverPriorityFirst
}

func (r *scannerFixtureResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	r.mu.Lock()
	malformed := r.malformed[input.Entry.Path]
	override := r.pathOverride[input.Entry.Path]
	block := r.block
	r.mu.Unlock()
	if block != nil {
		block(ctx, input.Entry.Path)
	}
	if malformed {
		return &item.Item{}, nil
	}
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
		result.Kind = item.KindMovie
		result.Size = scannerInt64(input.Entry.Size)
		result.ProviderIDs.Set("Fixture", input.Entry.Path)
		result.Values.Tags = []string{"fixture"}
		result.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: input.Entry.Path + ".jpg", DateModified: scannerTime(input.Entry.Modified)}}
	}
	if override != "" {
		result.ID = scannerID(override)
		result.Path = scannerString(override)
		result.Name = scannerString(strings.TrimSuffix(filepath.Base(override), filepath.Ext(override)))
	}
	return &result, nil
}

func (r *scannerFixtureResolver) setMalformed(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.malformed == nil {
		r.malformed = make(map[string]bool)
	}
	r.malformed[path] = true
}

func (r *scannerFixtureResolver) setPathOverride(entryPath, resolvedPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pathOverride == nil {
		r.pathOverride = make(map[string]string)
	}
	r.pathOverride[entryPath] = resolvedPath
}

func rewriteScannerCollection(t *testing.T, topology *library.Topology, from, to string) {
	t.Helper()
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil || len(folders) == 0 {
		t.Fatalf("list virtual folders before marker rewrite: %#v, %v", folders, err)
	}
	path := folders[0].Path
	if err := os.Remove(filepath.Join(path, from+".collection")); err != nil {
		t.Fatalf("remove %s collection marker: %v", from, err)
	}
	if err := os.WriteFile(filepath.Join(path, to+".collection"), nil, 0o600); err != nil {
		t.Fatalf("write %s collection marker: %v", to, err)
	}
}

func scannerReadDir(path string) ([]library.Entry, error) {
	directory, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]library.Entry, 0, len(directory))
	for _, value := range directory {
		info, err := value.Info()
		if err != nil {
			return nil, err
		}
		entries = append(entries, library.Entry{Path: filepath.Join(path, value.Name()), IsDir: value.IsDir(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return entries, nil
}

func writeScannerFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create scanner file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write scanner file %q: %v", path, err)
	}
}

func scannerScan(t *testing.T, scanner *library.Scanner, operation string) library.RefreshResult {
	t.Helper()
	result, err := scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("%s reported folder errors: %#v", operation, result.Errors)
	}
	return result
}

func scannerItems(t *testing.T, items *store.ItemStore) []item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query scanner items: %v", err)
	}
	return result.Items
}

// scannerCollectionFolder returns the resolved CollectionFolder item for the
// harness's single virtual folder. The scanner flattens media roots to direct
// children of the CollectionFolder, so tests query by its item ID, not the
// topology registry ID stored in harness.virtualID.
func scannerCollectionFolder(t *testing.T, harness *scannerHarness) item.Item {
	t.Helper()
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("load collection folder: folders=%+v err=%v", folders, err)
	}
	return scannerItemAtPath(t, harness.items, folders[0].Path)
}

func scannerItemAtPath(t *testing.T, items *store.ItemStore, path string) item.Item {
	t.Helper()
	for _, candidate := range scannerItems(t, items) {
		if candidate.Path != nil && *candidate.Path == path {
			return candidate
		}
	}
	t.Fatalf("scanner item at path %q not found", path)
	return item.Item{}
}

func assertScannerNoItemAtPath(t *testing.T, items *store.ItemStore, path string) {
	t.Helper()
	for _, candidate := range scannerItems(t, items) {
		if candidate.Path != nil && *candidate.Path == path {
			t.Fatalf("scanner retained stale item at path %q: %#v", path, candidate)
		}
	}
}

func assertScannerPaths(t *testing.T, items *store.ItemStore, paths ...string) {
	t.Helper()
	actual := make(map[string]struct{})
	for _, candidate := range scannerItems(t, items) {
		if candidate.Path != nil {
			actual[*candidate.Path] = struct{}{}
		}
	}
	for _, path := range paths {
		if _, found := actual[path]; !found {
			t.Fatalf("scanner item paths = %#v, missing %q", actual, path)
		}
	}
}

func assertScannerMediaSatellites(t *testing.T, got item.Item, path string, wantSize int64) {
	t.Helper()
	if got.Kind != item.KindMovie || got.Path == nil || *got.Path != path || got.Size == nil || *got.Size != wantSize {
		t.Fatalf("scanner media = %#v, want movie at %q with size %d", got, path, wantSize)
	}
	if provider, ok := got.ProviderIDs.Get("Fixture"); !ok || provider != path {
		t.Fatalf("scanner provider IDs = %#v, want Fixture=%q", got.ProviderIDs.Snapshot(), path)
	}
	if !reflect.DeepEqual(got.Values.Tags, []string{"fixture"}) {
		t.Fatalf("scanner values = %#v, want fixture tag", got.Values)
	}
	if len(got.ImageInfos) != 1 || got.ImageInfos[0].Path != path+".jpg" {
		t.Fatalf("scanner image satellites = %#v, want fixture image for %q", got.ImageInfos, path)
	}
}

func scannerID(path string) dto.UUID {
	sum := md5.Sum([]byte(path)) // #nosec G401 -- deterministic fixture identity, not security.
	return dto.UUID(sum)
}

func scannerPath(t *testing.T, elements ...string) string {
	t.Helper()
	return filepath.Join(elements...)
}

func scannerString(value string) *string { return &value }

func scannerInt64(value int64) *int64 { return &value }

func scannerTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func assertScannerAdditionalPartsCount(t *testing.T, stores store.Stores, itemID dto.UUID, want int) {
	t.Helper()
	storetest.AssertAdditionalPartsCount(t, stores, itemID, want)
}

func assertScannerNoAdditionalPartOrphans(t *testing.T, stores store.Stores) {
	t.Helper()
	storetest.AssertNoAdditionalPartOrphans(t, stores)
}
