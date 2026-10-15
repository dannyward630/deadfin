// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerSelfContainedStackedMovieMaterializesOwnedParts proves a
// self-contained directory's resolver-owned stack is a complete durable source
// graph. Secondary paths are deliberately Items rather than merely strings so
// Video.GetAdditionalParts can resolve them through the normal access policy.
func TestScannerSelfContainedStackedMovieMaterializesOwnedParts(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	probe := &scannerStackPartProbe{}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, MediaProbe: probe})

	directory := scannerPath(t, harness.source, "Stacked Directory Feature (2024)")
	primaryPath := scannerPath(t, directory, "Stacked Directory Feature (2024) part1.mkv")
	parts := []string{
		scannerPath(t, directory, "Stacked Directory Feature (2024) part2.mkv"),
		scannerPath(t, directory, "Stacked Directory Feature (2024) part3.mkv"),
	}
	for index, path := range append([]string{primaryPath}, parts...) {
		writeScannerFile(t, path, fmt.Sprintf("physical part %d", index+1))
	}
	writeScannerFile(t, scannerPath(t, directory, "movie.nfo"), `<movie><title>Stacked Directory Feature</title></movie>`)

	scannerScan(t, harness.scanner, "scan self-contained stacked movie")
	primary := scannerStoredMovieAtPath(t, harness.items, primaryPath)
	if !reflect.DeepEqual(primary.AdditionalParts, parts) {
		t.Fatalf("primary AdditionalParts = %#v, want %#v", primary.AdditionalParts, parts)
	}
	assertScannerAdditionalPartsCount(t, harness.stores, primary.ID, len(parts))
	if links := localAlternateChildren(primary.LinkedChildren); len(links) != 0 || primary.PrimaryVersionID != nil {
		t.Fatalf("stack primary alternate topology = primary=%v links=%#v, want none", primary.PrimaryVersionID, links)
	}

	firstIDs := make(map[string]dto.UUID, len(parts))
	for _, path := range parts {
		ordinary, err := harness.items.Query(t.Context(), store.ItemQuery{Path: path, IncludeItemTypes: []item.Kind{item.KindMovie}, EnableTotalRecordCount: true})
		if err != nil {
			t.Fatalf("ordinary path query %q: %v", path, err)
		}
		if len(ordinary.Items) != 0 || ordinary.TotalRecordCount != 0 {
			t.Fatalf("ordinary path query %q = %#v (total %d), want owned part hidden", path, ordinary.Items, ordinary.TotalRecordCount)
		}

		part := scannerStoredMovieAtPath(t, harness.items, path)
		if part.OwnerID == nil || *part.OwnerID != primary.ID || part.Kind != item.KindMovie {
			t.Fatalf("owned stack part %q = %#v, want Movie owned by %s", path, part, primary.ID)
		}
		if part.PrimaryVersionID != nil || len(localAlternateChildren(part.LinkedChildren)) != 0 {
			t.Fatalf("owned stack part %q alternate topology = primary=%v links=%#v, want none", path, part.PrimaryVersionID, part.LinkedChildren)
		}
		firstIDs[path] = part.ID
	}
	for _, candidate := range scannerItems(t, harness.items) {
		if candidate.Path == nil {
			continue
		}
		if _, found := firstIDs[*candidate.Path]; found {
			t.Fatalf("ordinary inventory leaked owned stack part %#v", candidate)
		}
	}
	probe.assertExactlyOnce(t, append([]string{primaryPath}, parts...))

	scannerScan(t, harness.scanner, "rescan self-contained stacked movie")
	for path, wantID := range firstIDs {
		if got := scannerStoredMovieAtPath(t, harness.items, path); got.ID != wantID {
			t.Fatalf("owned stack part %q ID after rescan = %s, want deterministic %s", path, got.ID, wantID)
		}
	}
	probe.assertExactlyOnce(t, append([]string{primaryPath}, parts...))

	if err := os.RemoveAll(directory); err != nil {
		t.Fatalf("remove self-contained stacked movie: %v", err)
	}
	scannerScan(t, harness.scanner, "rescan removed self-contained stacked movie")
	for _, path := range append([]string{primaryPath}, parts...) {
		result, err := harness.items.Query(t.Context(), store.ItemQuery{Path: path, IncludeOwnedItems: true, EnableTotalRecordCount: true})
		if err != nil {
			t.Fatalf("query removed stack path %q: %v", path, err)
		}
		if len(result.Items) != 0 || result.TotalRecordCount != 0 {
			t.Fatalf("removed stack path %q = %#v (total %d), want no orphan item", path, result.Items, result.TotalRecordCount)
		}
	}
	assertScannerNoAdditionalPartOrphans(t, harness.stores)
}

// TestScannerLockedStackSecondaryPromotionReconcilesOwnership proves that a
// globally locked physical stack part follows the newly resolved topology
// rather than restoring the deleted primary's ownership. User metadata and a
// user-created alternate relation remain persisted facts.
func TestScannerLockedStackSecondaryPromotionReconcilesOwnership(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, scannerRefreshFunc(func(context.Context, library.RefreshContext, *item.Item) error { return nil }))

	directory := scannerPath(t, harness.source, "Locked Stack Promotion (2024)")
	formerPrimaryPath := scannerPath(t, directory, "Locked Stack Promotion (2024) part1.mkv")
	survivorPath := scannerPath(t, directory, "Locked Stack Promotion (2024) part2.mkv")
	writeScannerFile(t, formerPrimaryPath, "former primary")
	writeScannerFile(t, survivorPath, "locked secondary")
	scannerScan(t, harness.scanner, "seed locked stack secondary")

	formerPrimary := scannerStoredMovieAtPath(t, harness.items, formerPrimaryPath)
	secondary := scannerStoredMovieAtPath(t, harness.items, survivorPath)
	if secondary.OwnerID == nil || *secondary.OwnerID != formerPrimary.ID {
		t.Fatalf("seed stack secondary OwnerID = %v, want former primary %s", secondary.OwnerID, formerPrimary.ID)
	}
	userAlternate := item.Item{ID: scannerID("locked-stack-promotion-user-alternate"), Kind: item.KindMovie, Name: scannerString("User Alternate"), Path: scannerString("/locked-stack-promotion-user-alternate.mkv")}
	if err := harness.items.Upsert(t.Context(), userAlternate); err != nil {
		t.Fatalf("seed user-created alternate target: %v", err)
	}
	secondary.IsLocked = true
	secondary.Name = scannerString("Locked Secondary Metadata")
	secondary.Values.Genres = []string{"Locked Secondary Genre"}
	secondary.LinkedChildren = append(secondary.LinkedChildren, item.LinkedChildRef{ItemID: userAlternate.ID, Type: item.LinkedChildTypeLinkedAlternateVersion})
	if err := harness.items.Upsert(t.Context(), secondary); err != nil {
		t.Fatalf("persist locked secondary metadata and user alternate relation: %v", err)
	}
	if err := os.Remove(formerPrimaryPath); err != nil {
		t.Fatalf("remove former stack primary: %v", err)
	}

	scannerScan(t, harness.scanner, "rescan locked promoted stack secondary")

	assertScannerNoItemAtPath(t, harness.items, formerPrimaryPath)
	survivor := scannerStoredMovieAtPath(t, harness.items, survivorPath)
	if survivor.OwnerID != nil {
		t.Fatalf("promoted locked stack secondary OwnerID = %s, want nil", survivor.OwnerID)
	}
	if !survivor.IsLocked || survivor.Name == nil || *survivor.Name != "Locked Secondary Metadata" || !reflect.DeepEqual(survivor.Values.Genres, []string{"Locked Secondary Genre"}) {
		t.Fatalf("promoted locked stack secondary metadata = %#v, want preserved global lock, name, and genres", survivor)
	}
	if !hasLinkedAlternateChild(survivor.LinkedChildren, userAlternate.ID) {
		t.Fatalf("promoted locked stack secondary links = %#v, want preserved user LinkedAlternateVersion child %s", survivor.LinkedChildren, userAlternate.ID)
	}
	assertScannerMovieVisible(t, harness, survivorPath, survivor.ID)
	all, err := harness.items.Query(t.Context(), store.ItemQuery{IncludeOwnedItems: true})
	if err != nil {
		t.Fatalf("query reconciled stack inventory: %v", err)
	}
	for _, candidate := range all.Items {
		if candidate.OwnerID != nil && *candidate.OwnerID == formerPrimary.ID {
			t.Fatalf("reconciled stack inventory retained deleted owner %s on %#v", formerPrimary.ID, candidate)
		}
	}
}

// TestScannerLockedOrdinaryMovieDemotionReconcilesOwnership proves that a
// globally locked movie that later becomes a resolver-owned stack part receives
// the current primary's OwnerID while preserving user metadata and links.
func TestScannerLockedOrdinaryMovieDemotionReconcilesOwnership(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	scannerUseDefaultMovieChain(t, harness, scannerRefreshFunc(func(context.Context, library.RefreshContext, *item.Item) error { return nil }))

	directory := scannerPath(t, harness.source, "Locked Stack Demotion (2024)")
	secondaryPath := scannerPath(t, directory, "Locked Stack Demotion (2024) part2.mkv")
	writeScannerFile(t, secondaryPath, "initial ordinary movie")
	scannerScan(t, harness.scanner, "seed ordinary locked stack candidate")

	formerOrdinary := scannerStoredMovieAtPath(t, harness.items, secondaryPath)
	if formerOrdinary.OwnerID != nil {
		t.Fatalf("seed ordinary movie OwnerID = %s, want nil", formerOrdinary.OwnerID)
	}
	userAlternate := item.Item{ID: scannerID("locked-stack-demotion-user-alternate"), Kind: item.KindMovie, Name: scannerString("User Alternate"), Path: scannerString("/locked-stack-demotion-user-alternate.mkv")}
	if err := harness.items.Upsert(t.Context(), userAlternate); err != nil {
		t.Fatalf("seed user-created alternate target: %v", err)
	}
	formerOrdinary.IsLocked = true
	formerOrdinary.Name = scannerString("Locked Ordinary Metadata")
	formerOrdinary.Values.Genres = []string{"Locked Ordinary Genre"}
	formerOrdinary.LinkedChildren = append(formerOrdinary.LinkedChildren, item.LinkedChildRef{ItemID: userAlternate.ID, Type: item.LinkedChildTypeLinkedAlternateVersion})
	if err := harness.items.Upsert(t.Context(), formerOrdinary); err != nil {
		t.Fatalf("persist locked ordinary metadata and user alternate relation: %v", err)
	}
	primaryPath := scannerPath(t, directory, "Locked Stack Demotion (2024) part1.mkv")
	writeScannerFile(t, primaryPath, "new stack primary")

	scannerScan(t, harness.scanner, "rescan locked ordinary movie as stack secondary")

	primary := scannerStoredMovieAtPath(t, harness.items, primaryPath)
	secondary := scannerStoredMovieAtPath(t, harness.items, secondaryPath)
	if secondary.OwnerID == nil || *secondary.OwnerID != primary.ID {
		t.Fatalf("demoted locked stack secondary OwnerID = %v, want current primary %s", secondary.OwnerID, primary.ID)
	}
	if !secondary.IsLocked || secondary.Name == nil || *secondary.Name != "Locked Ordinary Metadata" || !reflect.DeepEqual(secondary.Values.Genres, []string{"Locked Ordinary Genre"}) {
		t.Fatalf("demoted locked stack secondary metadata = %#v, want preserved global lock, name, and genres", secondary)
	}
	if !hasLinkedAlternateChild(secondary.LinkedChildren, userAlternate.ID) {
		t.Fatalf("demoted locked stack secondary links = %#v, want preserved user LinkedAlternateVersion child %s", secondary.LinkedChildren, userAlternate.ID)
	}
	ordinary, err := harness.items.Query(t.Context(), store.ItemQuery{Path: secondaryPath, IncludeItemTypes: []item.Kind{item.KindMovie}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query ordinary demoted stack secondary: %v", err)
	}
	if len(ordinary.Items) != 0 || ordinary.TotalRecordCount != 0 {
		t.Fatalf("ordinary inventory for demoted locked stack secondary = %#v (total %d), want hidden", ordinary.Items, ordinary.TotalRecordCount)
	}
	owned, err := harness.items.Query(t.Context(), store.ItemQuery{Path: secondaryPath, IncludeItemTypes: []item.Kind{item.KindMovie}, IncludeOwnedItems: true, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query owned demoted stack secondary: %v", err)
	}
	if len(owned.Items) != 1 || owned.Items[0].ID != secondary.ID || owned.TotalRecordCount != 1 {
		t.Fatalf("owned inventory for demoted locked stack secondary = %#v (total %d), want only %s", owned.Items, owned.TotalRecordCount, secondary.ID)
	}
}

// TestScannerFlattenedAlternateRefreshUsesPhysicalDirectoryListing guards the
// distinct source provenance of an alternate flattened from a self-contained
// directory. It probes the alternate file but must refresh local NFO/artwork
// from that file's directory, not from the directory's outer parent listing.
func TestScannerFlattenedAlternateRefreshUsesPhysicalDirectoryListing(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}

	directory := scannerPath(t, harness.source, "Directory Alternate Feature (2024)")
	primaryPath := scannerPath(t, directory, "Directory Alternate Feature (2024).mkv")
	alternatePath := scannerPath(t, directory, "Directory Alternate Feature (2024) - 1080p.mkv")
	posterPath := scannerPath(t, directory, "poster.jpg")
	writeScannerFile(t, primaryPath, "primary")
	writeScannerFile(t, alternatePath, "alternate")
	writeScannerFile(t, scannerPath(t, directory, "movie.nfo"), `<movie><title>Directory-local alternate metadata</title></movie>`)
	writeScannerFile(t, posterPath, "poster")

	registry := providers.NewRegistry(
		providers.NewLocalImageProvider(),
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }}),
	)
	var observed bool
	refresher := scannerRefreshFunc(func(ctx context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if candidate.Path != nil && *candidate.Path == alternatePath {
			observed = true
			if refresh.Directory.Path != directory || !scannerStackHasPaths(refresh.Entries, alternatePath, scannerPath(t, directory, "movie.nfo"), posterPath) {
				return fmt.Errorf("flattened alternate refresh = directory %q entries %v, want physical directory %q with local NFO/image", refresh.Directory.Path, scannerStackPaths(refresh.Entries), directory)
			}
		}
		return registry.Refresh(ctx, refresh, candidate)
	})
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: refresher})

	scannerScan(t, harness.scanner, "scan flattened alternate with physical refresh listing")
	if !observed {
		t.Fatal("refresher never observed flattened alternate")
	}
	alternate := scannerStoredMovieAtPath(t, harness.items, alternatePath)
	if alternate.Name == nil || *alternate.Name != "Directory-local alternate metadata" {
		t.Fatalf("flattened alternate metadata = %#v, want directory-local NFO title", alternate)
	}
	if !adapterHasScannerImage(alternate.ImageInfos, item.ImageTypePrimary, posterPath) {
		t.Fatalf("flattened alternate images = %#v, want directory-local poster %q", alternate.ImageInfos, posterPath)
	}
}

type scannerStackPartProbe struct {
	mu    sync.Mutex
	calls map[string]int
}

func (probe *scannerStackPartProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.calls == nil {
		probe.calls = make(map[string]int)
	}
	probe.calls[input.Entry.Path]++
	return library.MediaProbeResult{Container: "mkv"}, nil
}

func (probe *scannerStackPartProbe) assertExactlyOnce(t *testing.T, paths []string) {
	t.Helper()
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, path := range paths {
		if got := probe.calls[path]; got != 1 {
			t.Fatalf("MediaProbe(%q) calls = %d, want exactly one physical probe", path, got)
		}
	}
	if len(probe.calls) != len(paths) {
		t.Fatalf("MediaProbe paths = %#v, want only %#v", probe.calls, paths)
	}
}

func scannerStackHasPaths(entries []library.Entry, paths ...string) bool {
	actual := scannerStackPaths(entries)
	for _, path := range paths {
		if _, found := actual[path]; !found {
			return false
		}
	}
	return true
}

func scannerStackPaths(entries []library.Entry) map[string]struct{} {
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		result[filepath.Clean(entry.Path)] = struct{}{}
	}
	return result
}
