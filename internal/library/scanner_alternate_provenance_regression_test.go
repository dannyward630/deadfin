// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerAlternateProvenanceDoesNotGroupIndependentSameNamedMovies proves
// scanner alternate links come from the naming resolver's explicit group, not
// from a reconstructed display-name key. The two years deliberately resolve to
// the same display title but are distinct movies and therefore both visible.
func TestScannerAlternateProvenanceDoesNotGroupIndependentSameNamedMovies(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, nil)

	older := scannerPath(t, harness.source, "Same Title (2020).mkv")
	newer := scannerPath(t, harness.source, "Same Title (2021).mkv")
	writeScannerFile(t, older, "2020")
	writeScannerFile(t, newer, "2021")
	scannerScan(t, harness.scanner, "scan independently resolved same-title movies")
	collection := scannerCollectionFolder(t, harness)
	visible, err := harness.items.Query(t.Context(), store.ItemQuery{ParentID: &collection.ID, IncludeItemTypes: []item.Kind{item.KindMovie}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query visible same-title movies: %v", err)
	}
	if len(visible.Items) != 2 || visible.TotalRecordCount != 2 {
		t.Fatalf("visible same-title movies = %#v (total %d), want two independent movies", visible.Items, visible.TotalRecordCount)
	}
	for _, path := range []string{older, newer} {
		value := scannerStoredMovieAtPath(t, harness.items, path)
		if value.PrimaryVersionID != nil {
			t.Errorf("independent movie %q PrimaryVersionID = %s, want nil", path, value.PrimaryVersionID)
		}
		if localAlternateChildren(value.LinkedChildren) != nil {
			t.Errorf("independent movie %q local alternate links = %#v, want none", path, value.LinkedChildren)
		}
	}
}

// TestScannerAlternateProvenanceRetainsResolverPrimary demonstrates that a
// resolver-selected exact folder-name primary is not reselected from a probe or
// a filename-quality suffix by the scanner.
func TestScannerAlternateProvenanceRetainsResolverPrimary(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, nil)

	directory := scannerPath(t, harness.source, "Resolver Feature (2024)")
	exact := scannerPath(t, directory, "Resolver Feature (2024).mkv")
	highDefinition := scannerPath(t, directory, "Resolver Feature (2024) - 1080p.mkv")
	writeScannerFile(t, exact, "exact primary")
	writeScannerFile(t, highDefinition, "1080p alternate")
	scannerScan(t, harness.scanner, "scan resolver-selected alternate group")

	primary := scannerStoredMovieAtPath(t, harness.items, exact)
	alternate := scannerStoredMovieAtPath(t, harness.items, highDefinition)
	assertScannerAlternateGroup(t, primary, alternate)
}

// TestScannerAlternateProvenanceRescanClearsRemovedPrimaryRelations exercises
// the remove-and-promote path. The previous primary is deleted, and the only
// survivor must become an ordinary visible movie rather than retain a dangling
// primary-version relation.
func TestScannerAlternateProvenanceRescanClearsRemovedPrimaryRelations(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, nil)

	directory := scannerPath(t, harness.source, "Rescanned Feature (2024)")
	exact := scannerPath(t, directory, "Rescanned Feature (2024).mkv")
	alternatePath := scannerPath(t, directory, "Rescanned Feature (2024) - 1080p.mkv")
	writeScannerFile(t, exact, "exact primary")
	writeScannerFile(t, alternatePath, "alternate")
	scannerScan(t, harness.scanner, "seed alternate group")

	primary := scannerStoredMovieAtPath(t, harness.items, exact)
	alternate := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	assertScannerAlternateGroup(t, primary, alternate)
	if err := os.Remove(exact); err != nil {
		t.Fatalf("remove former primary: %v", err)
	}
	scannerScan(t, harness.scanner, "rescan after former primary removal")

	assertScannerNoItemAtPath(t, harness.items, exact)
	survivor := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	if survivor.PrimaryVersionID != nil {
		t.Fatalf("surviving alternate PrimaryVersionID = %s, want nil after primary removal", survivor.PrimaryVersionID)
	}
	if links := localAlternateChildren(survivor.LinkedChildren); links != nil {
		t.Fatalf("surviving alternate local links = %#v, want none after primary removal", links)
	}
	assertScannerMovieVisible(t, harness, alternatePath, survivor.ID)
}

// TestScannerAlternateProvenanceRescanClearsLockedSurvivorWithoutDeletingUserLinks
// covers the rehydration path that normally restores a globally locked item's
// persisted metadata. Alternate topology is scanner-owned and must still be
// cleared, while a user's LinkedAlternateVersion relationship is not scanner
// topology and must survive the rescan.
func TestScannerAlternateProvenanceRescanClearsLockedSurvivorWithoutDeletingUserLinks(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, scannerRefreshFunc(func(context.Context, library.RefreshContext, *item.Item) error { return nil }))

	directory := scannerPath(t, harness.source, "Locked Rescan Feature (2024)")
	exact := scannerPath(t, directory, "Locked Rescan Feature (2024).mkv")
	alternatePath := scannerPath(t, directory, "Locked Rescan Feature (2024) - 1080p.mkv")
	writeScannerFile(t, exact, "exact primary")
	writeScannerFile(t, alternatePath, "alternate")
	scannerScan(t, harness.scanner, "seed locked rescan alternate group")

	primary := scannerStoredMovieAtPath(t, harness.items, exact)
	alternate := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	assertScannerAlternateGroup(t, primary, alternate)

	userAlternate := item.Item{ID: scannerID("user-created-linked-alternate"), Kind: item.KindMovie, Name: scannerString("User Created Alternate"), Path: scannerString("/user-created-linked-alternate.mkv")}
	if err := harness.items.Upsert(t.Context(), userAlternate); err != nil {
		t.Fatalf("seed user-created alternate target: %v", err)
	}
	alternate.IsLocked = true
	alternate.LinkedChildren = append(alternate.LinkedChildren, item.LinkedChildRef{ItemID: userAlternate.ID, Type: item.LinkedChildTypeLinkedAlternateVersion})
	if err := harness.items.Upsert(t.Context(), alternate); err != nil {
		t.Fatalf("mark alternate globally locked with user link: %v", err)
	}
	if err := os.Remove(exact); err != nil {
		t.Fatalf("remove former primary: %v", err)
	}
	scannerScan(t, harness.scanner, "rescan globally locked survivor")

	survivor := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	if !survivor.IsLocked {
		t.Fatalf("surviving item lost global lock: %#v", survivor)
	}
	if survivor.PrimaryVersionID != nil {
		t.Fatalf("globally locked survivor PrimaryVersionID = %s, want nil after primary removal", survivor.PrimaryVersionID)
	}
	if links := localAlternateChildren(survivor.LinkedChildren); links != nil {
		t.Fatalf("globally locked survivor local alternate links = %#v, want none", links)
	}
	if !hasLinkedAlternateChild(survivor.LinkedChildren, userAlternate.ID) {
		t.Fatalf("globally locked survivor links = %#v, want preserved user LinkedAlternateVersion child %s", survivor.LinkedChildren, userAlternate.ID)
	}
	assertScannerMovieVisible(t, harness, alternatePath, survivor.ID)
}

// TestScannerDefaultResolverConsumesStackedPartsOnceAcrossConcurrentFolders
// covers the scanner boundary that turns resolver-owned stack paths into
// consumed entries. Each folder is scanned by the default resolver chain, and
// a later scan must not resurrect a separately persisted child for a part.
func TestScannerDefaultResolverConsumesStackedPartsOnceAcrossConcurrentFolders(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, nil)

	type stack struct {
		primary string
		parts   []string
	}
	stacks := make([]stack, 0, 2)
	for _, name := range []string{"Concurrent Stack A (2024)", "Concurrent Stack B (2024)"} {
		directory := scannerPath(t, harness.source, name)
		primary := scannerPath(t, directory, name+" part1.mkv")
		parts := []string{
			scannerPath(t, directory, name+" part2.mkv"),
			scannerPath(t, directory, name+" part3.mkv"),
		}
		writeScannerFile(t, primary, "part one")
		writeScannerFile(t, parts[0], "part two")
		writeScannerFile(t, parts[1], "part three")
		stacks = append(stacks, stack{primary: primary, parts: parts})
	}
	scannerScan(t, harness.scanner, "scan concurrent stacked-video folders")

	for _, want := range stacks {
		primary := scannerStoredMovieAtPath(t, harness.items, want.primary)
		if !reflect.DeepEqual(primary.AdditionalParts, want.parts) {
			t.Fatalf("stack primary %q AdditionalParts = %#v, want %#v", want.primary, primary.AdditionalParts, want.parts)
		}
		assertScannerAdditionalPartsCount(t, harness.stores, primary.ID, len(want.parts))
		for _, partPath := range want.parts {
			part := scannerStoredMovieAtPath(t, harness.items, partPath)
			if part.OwnerID == nil || *part.OwnerID != primary.ID || part.PrimaryVersionID != nil || len(localAlternateChildren(part.LinkedChildren)) != 0 {
				t.Fatalf("stack secondary %q = %#v, want hidden Movie owned by %s without alternate topology", partPath, part, primary.ID)
			}
		}
	}

	before := scannerItems(t, harness.items)
	scannerScan(t, harness.scanner, "rescan concurrent stacked-video folders")
	if after := scannerItems(t, harness.items); !reflect.DeepEqual(after, before) {
		t.Fatalf("stacked-video rescan changed item inventory\nbefore: %#v\nafter: %#v", before, after)
	}
}

// TestScannerDefaultResolverLinksEpisodeAlternateVersions verifies that TV
// multi-version provenance follows the same resolver-selected source graph as
// movies. The canonical inventory exposes only the primary episode, while both
// backing episode identities remain playable media sources.
func TestScannerDefaultResolverLinksEpisodeAlternateVersions(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, harness.topology, "movies", "tvshows")
	scannerUseDefaultMovieChain(t, harness, nil)

	seriesPath := scannerPath(t, harness.source, "Provenance Show (2024)")
	seasonPath := scannerPath(t, seriesPath, "Season 01")
	primaryPath := scannerPath(t, seasonPath, "Provenance Show - S01E01 - 1080p.mkv")
	alternatePath := scannerPath(t, seasonPath, "Provenance Show - S01E01 - 720p.mkv")
	writeScannerFile(t, primaryPath, "1080p episode")
	writeScannerFile(t, alternatePath, "720p episode")
	scannerScan(t, harness.scanner, "scan episode alternate-version group")

	series := scannerItemAtPath(t, harness.items, seriesPath)
	season := scannerItemAtPath(t, harness.items, seasonPath)
	primary := scannerStoredEpisodeAtPath(t, harness.items, primaryPath)
	alternate := scannerStoredEpisodeAtPath(t, harness.items, alternatePath)
	if primary.Kind != item.KindEpisode || alternate.Kind != item.KindEpisode {
		t.Fatalf("episode alternate kinds = (%s, %s), want Episode", primary.Kind, alternate.Kind)
	}
	if primary.SeriesID == nil || *primary.SeriesID != series.ID || primary.SeasonID == nil || *primary.SeasonID != season.ID {
		t.Fatalf("primary episode hierarchy = %#v, want series %s and season %s", primary, series.ID, season.ID)
	}
	if alternate.PrimaryVersionID == nil || *alternate.PrimaryVersionID != primary.ID {
		t.Fatalf("alternate episode PrimaryVersionID = %v, want %s", alternate.PrimaryVersionID, primary.ID)
	}
	if links := localAlternateChildren(primary.LinkedChildren); len(links) != 1 || links[0].ItemID != alternate.ID {
		t.Fatalf("primary episode local alternate links = %#v, want child %s", links, alternate.ID)
	}

	visible, err := harness.items.Query(t.Context(), store.ItemQuery{ParentID: &season.ID, IncludeItemTypes: []item.Kind{item.KindEpisode}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query canonical episode inventory: %v", err)
	}
	if len(visible.Items) != 1 || visible.TotalRecordCount != 1 || visible.Items[0].ID != primary.ID {
		t.Fatalf("canonical episode inventory = %#v (total %d), want only primary %s", visible.Items, visible.TotalRecordCount, primary.ID)
	}

	expected := []versionSourceIdentity{{id: primary.ID, path: primaryPath}, {id: alternate.ID, path: alternatePath}}
	for _, requested := range []dto.UUID{primary.ID, alternate.ID} {
		records, err := harness.items.MediaSourceRecords(t.Context(), requested)
		if err != nil {
			t.Fatalf("load episode source graph for %s: %v", requested, err)
		}
		versionSourceAssertRecords(t, records, expected)
	}
}

// TestScannerRescanKeepsUserAlternateLinkWhenItOverlapsResolverLocalChild
// ensures a user-created relationship is not duplicated by a scanner-owned
// local relation for the same child. The persisted user link wins as the sole
// parent-to-child relation, while the child still carries resolver provenance.
func TestScannerRescanKeepsUserAlternateLinkWhenItOverlapsResolverLocalChild(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, nil)

	directory := scannerPath(t, harness.source, "Overlapping Link Feature (2024)")
	primaryPath := scannerPath(t, directory, "Overlapping Link Feature (2024) - 1080p.mkv")
	alternatePath := scannerPath(t, directory, "Overlapping Link Feature (2024) - 720p.mkv")
	writeScannerFile(t, primaryPath, "primary")
	writeScannerFile(t, alternatePath, "alternate")
	scannerScan(t, harness.scanner, "seed resolver-local alternate group")

	primary := scannerStoredMovieAtPath(t, harness.items, primaryPath)
	alternate := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	assertScannerAlternateGroup(t, primary, alternate)
	// linked_children identifies a relationship by parent and child, so this is
	// the persisted user replacement of the scanner-owned relation that a real
	// edit leaves before the next scan.
	primary.LinkedChildren = []item.LinkedChildRef{{ItemID: alternate.ID, Type: item.LinkedChildTypeLinkedAlternateVersion}}
	if err := harness.items.Upsert(t.Context(), primary); err != nil {
		t.Fatalf("persist user alternate link overlapping local child: %v", err)
	}

	scannerScan(t, harness.scanner, "rescan overlapping user and local alternate link")
	primary = scannerStoredMovieAtPath(t, harness.items, primaryPath)
	alternate = scannerStoredMovieAtPath(t, harness.items, alternatePath)
	if alternate.PrimaryVersionID == nil || *alternate.PrimaryVersionID != primary.ID {
		t.Fatalf("rescanned alternate PrimaryVersionID = %v, want %s", alternate.PrimaryVersionID, primary.ID)
	}
	var overlapping []item.LinkedChildRef
	for _, linked := range primary.LinkedChildren {
		if linked.ItemID == alternate.ID {
			overlapping = append(overlapping, linked)
		}
	}
	if len(overlapping) != 1 || overlapping[0].Type != item.LinkedChildTypeLinkedAlternateVersion {
		t.Fatalf("rescanned overlapping links = %#v, want exactly one user LinkedAlternateVersion child", overlapping)
	}
}

func scannerUseDefaultMovieChain(t *testing.T, harness *scannerHarness, refresher library.Refresher) {
	t.Helper()
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 2, Refresher: refresher})
}

func assertScannerAlternateGroup(t *testing.T, primary, alternate item.Item) {
	t.Helper()
	if primary.PrimaryVersionID != nil {
		t.Fatalf("resolver-selected primary PrimaryVersionID = %s, want nil", primary.PrimaryVersionID)
	}
	if alternate.PrimaryVersionID == nil || *alternate.PrimaryVersionID != primary.ID {
		t.Fatalf("resolver-selected alternate PrimaryVersionID = %v, want %s", alternate.PrimaryVersionID, primary.ID)
	}
	links := localAlternateChildren(primary.LinkedChildren)
	if len(links) != 1 || links[0].ItemID != alternate.ID {
		t.Fatalf("resolver-selected primary local alternate links = %#v, want child %s", links, alternate.ID)
	}
}

func scannerStoredMovieAtPath(t *testing.T, items *store.ItemStore, path string) item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{Path: path, IncludeItemTypes: []item.Kind{item.KindMovie}, IncludeOwnedItems: true, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query stored scanner movie at %q: %v", path, err)
	}
	if len(result.Items) != 1 || result.TotalRecordCount != 1 {
		t.Fatalf("stored scanner movie at %q = %#v (total %d), want one", path, result.Items, result.TotalRecordCount)
	}
	return result.Items[0]
}

func scannerStoredEpisodeAtPath(t *testing.T, items *store.ItemStore, path string) item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{Path: path, IncludeItemTypes: []item.Kind{item.KindEpisode}, IncludeOwnedItems: true, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query stored scanner episode at %q: %v", path, err)
	}
	if len(result.Items) != 1 || result.TotalRecordCount != 1 {
		t.Fatalf("stored scanner episode at %q = %#v (total %d), want one", path, result.Items, result.TotalRecordCount)
	}
	return result.Items[0]
}

func assertScannerMovieVisible(t *testing.T, harness *scannerHarness, path string, wantID dto.UUID) {
	t.Helper()
	visible, err := harness.items.Query(t.Context(), store.ItemQuery{Path: path, IncludeItemTypes: []item.Kind{item.KindMovie}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query visible scanner movie: %v", err)
	}
	if len(visible.Items) != 1 || visible.Items[0].ID != wantID || visible.TotalRecordCount != 1 {
		t.Fatalf("visible scanner movie at %q = %#v (total %d), want only %s", path, visible.Items, visible.TotalRecordCount, wantID)
	}
}

func localAlternateChildren(values []item.LinkedChildRef) []item.LinkedChildRef {
	var result []item.LinkedChildRef
	for _, value := range values {
		if value.Type == item.LinkedChildTypeLocalAlternateVersion {
			result = append(result, value)
		}
	}
	return result
}

func hasLinkedAlternateChild(values []item.LinkedChildRef, target dto.UUID) bool {
	for _, value := range values {
		if value.ItemID == target && value.Type == item.LinkedChildTypeLinkedAlternateVersion {
			return true
		}
	}
	return false
}
