// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerDeduplicatesSharedVirtualMetadataAcrossMultiRootUnion covers the
// multi-root virtual collision: two accessible media roots that share virtual
// metadata, such as a genre named "Drama", produce the same virtual UUID from
// each root. planSources must merge those virtuals into one so that
// reconcileScannedVirtuals does not reject the plan as a duplicate.
func TestScannerDeduplicatesSharedVirtualMetadataAcrossMultiRootUnion(t *testing.T) {
	resolver := &multiRootGenreResolver{}
	harness := newScannerHarness(t, &resolver.scannerFixtureResolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = library.NewChain(resolver)
	secondSource := scannerPath(t, t.TempDir(), "second-media")
	if err := os.Mkdir(secondSource, 0o755); err != nil {
		t.Fatalf("create second source: %v", err)
	}
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, secondSource); err != nil {
		t.Fatalf("add second media root: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Kind != item.KindMovie {
				return nil
			}
			candidate.Values.Genres = []string{"Drama"}
			return nil
		}),
	})

	alphaMovie := scannerPath(t, harness.source, "alpha.mkv")
	betaMovie := scannerPath(t, secondSource, "beta.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	writeScannerFile(t, betaMovie, "beta")

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan multi-root shared genre: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan reported folder errors: %#v", result.Errors)
	}

	collection := scannerCollectionFolder(t, harness)
	alpha := scannerItemAtPath(t, harness.items, alphaMovie)
	beta := scannerItemAtPath(t, harness.items, betaMovie)
	// A media item's direct parent is the physical root Folder, not the
	// CollectionFolder. The store projection flattens the root Folder from
	// a non-recursive browse, so the browse contract is unchanged.
	if alpha.ParentID == nil {
		t.Fatalf("alpha movie has no parent")
	}
	alphaRoot, err := harness.items.Get(t.Context(), *alpha.ParentID)
	if err != nil {
		t.Fatalf("load alpha root parent: %v", err)
	}
	if alphaRoot.Kind != item.KindFolder || alphaRoot.ParentID == nil || *alphaRoot.ParentID != collection.ID {
		t.Fatalf("alpha movie parent chain = %s -> %s, want Folder -> CollectionFolder %s", alphaRoot.Kind, alphaRoot.ParentID, collection.ID)
	}
	if beta.ParentID == nil {
		t.Fatalf("beta movie has no parent")
	}
	betaRoot, err := harness.items.Get(t.Context(), *beta.ParentID)
	if err != nil {
		t.Fatalf("load beta root parent: %v", err)
	}
	if betaRoot.Kind != item.KindFolder || betaRoot.ParentID == nil || *betaRoot.ParentID != collection.ID {
		t.Fatalf("beta movie parent chain = %s -> %s, want Folder -> CollectionFolder %s", betaRoot.Kind, betaRoot.ParentID, collection.ID)
	}
	if !equalStrings(alpha.Values.Genres, []string{"Drama"}) {
		t.Fatalf("alpha genres = %#v, want [Drama]", alpha.Values.Genres)
	}
	if !equalStrings(beta.Values.Genres, []string{"Drama"}) {
		t.Fatalf("beta genres = %#v, want [Drama]", beta.Values.Genres)
	}

	genres, err := harness.items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindGenre},
		Recursive:        true,
	})
	if err != nil {
		t.Fatalf("query persisted genres: %v", err)
	}
	if len(genres.Items) != 1 {
		names := make([]string, 0, len(genres.Items))
		for _, genre := range genres.Items {
			if genre.Name != nil {
				names = append(names, *genre.Name)
			}
		}
		t.Fatalf("persisted Genre count = %d (%#v), want exactly one Drama", len(genres.Items), names)
	}
	genre := genres.Items[0]
	if genre.Name == nil || *genre.Name != "Drama" {
		t.Fatalf("persisted genre = %#v, want Drama", genre)
	}
}

// TestScannerDeduplicatesSharedPersonAcrossMultiRootUnion covers the same
// collision for nfo-derived people: two roots that share an actor name produce
// the same Person virtual UUID from each root.
func TestScannerDeduplicatesSharedPersonAcrossMultiRootUnion(t *testing.T) {
	resolver := &multiRootGenreResolver{}
	harness := newScannerHarness(t, &resolver.scannerFixtureResolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = library.NewChain(resolver)
	secondSource := scannerPath(t, t.TempDir(), "second-media")
	if err := os.Mkdir(secondSource, 0o755); err != nil {
		t.Fatalf("create second source: %v", err)
	}
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, secondSource); err != nil {
		t.Fatalf("add second media root: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Kind != item.KindMovie {
				return nil
			}
			candidate.People = []item.PersonCredit{{Name: "Shared Actor", Type: item.PersonKindActor}}
			return nil
		}),
	})

	alphaMovie := scannerPath(t, harness.source, "alpha.mkv")
	betaMovie := scannerPath(t, secondSource, "beta.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	writeScannerFile(t, betaMovie, "beta")

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan multi-root shared person: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan reported folder errors: %#v", result.Errors)
	}

	people, err := harness.items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindPerson},
		Recursive:        true,
	})
	if err != nil {
		t.Fatalf("query persisted people: %v", err)
	}
	if len(people.Items) != 1 {
		names := make([]string, 0, len(people.Items))
		for _, person := range people.Items {
			if person.Name != nil {
				names = append(names, *person.Name)
			}
		}
		t.Fatalf("persisted Person count = %d (%#v), want exactly one Shared Actor", len(people.Items), names)
	}
	person := people.Items[0]
	if person.Name == nil || *person.Name != "Shared Actor" {
		t.Fatalf("persisted person = %#v, want Shared Actor", person)
	}
}

// TestScannerDeduplicatesSharedVirtualMetadataAcrossRescan covers the
// collision on a second scan: the deduplication must remain stable so
// a rescan does not regress.
func TestScannerDeduplicatesSharedVirtualMetadataAcrossRescan(t *testing.T) {
	resolver := &multiRootGenreResolver{}
	harness := newScannerHarness(t, &resolver.scannerFixtureResolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = library.NewChain(resolver)
	secondSource := scannerPath(t, t.TempDir(), "second-media")
	if err := os.Mkdir(secondSource, 0o755); err != nil {
		t.Fatalf("create second source: %v", err)
	}
	if err := harness.topology.AddMediaPath(t.Context(), harness.virtualID, secondSource); err != nil {
		t.Fatalf("add second media root: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Kind != item.KindMovie {
				return nil
			}
			candidate.Values.Genres = []string{"Drama"}
			return nil
		}),
	})

	alphaMovie := scannerPath(t, harness.source, "alpha.mkv")
	betaMovie := scannerPath(t, secondSource, "beta.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	writeScannerFile(t, betaMovie, "beta")

	scannerScan(t, harness.scanner, "initial multi-root scan")
	scannerScan(t, harness.scanner, "rescan multi-root")

	genres, err := harness.items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindGenre},
		Recursive:        true,
	})
	if err != nil {
		t.Fatalf("query persisted genres after rescan: %v", err)
	}
	if len(genres.Items) != 1 {
		names := make([]string, 0, len(genres.Items))
		for _, genre := range genres.Items {
			if genre.Name != nil {
				names = append(names, *genre.Name)
			}
		}
		t.Fatalf("persisted Genre count after rescan = %d (%#v), want exactly one Drama", len(genres.Items), names)
	}
}

type multiRootGenreResolver struct{ scannerFixtureResolver }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
