// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// TestScannerCaseFoldsNFOPersonIdentity verifies that credits differing only by
// case share Jellyfin's one durable Person entity and its canonical spelling.
// Source: Jellyfin Emby.Server.Implementations/Library/LibraryManager.cs,
// GetNewItemId's case-insensitive metadata identity path.
func TestScannerCaseFoldsNFOPersonIdentity(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	movie := scannerPath(t, harness.source, "case-folded-person.mkv")
	writeScannerFile(t, movie, "movie")
	writeScannerFile(t, scannerPath(t, harness.source, "case-folded-person.nfo"), `<movie><title>Case Folded Person</title><actor><name>Alice</name></actor><director>alice</director></movie>`)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewChain(scannerProviderContextResolver{base: resolver})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: providers.NewRegistry(providers.NewNFOProvider(providers.NFOProviderOptions{
			Open: func(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) },
		})),
	})

	first, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan case-folded NFO people: %v", err)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("case-folded NFO people scan errors = %v, want none", first.Errors)
	}
	assertScannerCaseFoldedPerson(t, harness, movie)

	second, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("rescan case-folded NFO people: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Deleted != 0 || len(second.Errors) != 0 {
		t.Fatalf("unchanged case-folded NFO people rescan = %#v, want no mutations", second)
	}
	assertScannerCaseFoldedPerson(t, harness, movie)
}

func assertScannerCaseFoldedPerson(t *testing.T, harness *scannerHarness, moviePath string) {
	t.Helper()
	want := library.PersonEntity("Alice")
	if got := library.PersonEntity("alice"); got.ID != want.ID {
		t.Fatalf("case-folded PersonEntity IDs differ: Alice=%s alice=%s", want.ID, got.ID)
	}

	movie := scannerItemAtPath(t, harness.items, moviePath)
	credits := make(map[item.PersonKind]item.PersonCredit, len(movie.People))
	for _, credit := range movie.People {
		credits[credit.Type] = credit
	}
	if len(credits) != 2 || credits[item.PersonKindActor].PersonID != want.ID || credits[item.PersonKindDirector].PersonID != want.ID || credits[item.PersonKindActor].Name != "Alice" || credits[item.PersonKindDirector].Name != "Alice" {
		t.Fatalf("movie person credits = %#v, want Actor and Director credits for one Person %s named Alice", movie.People, want.ID)
	}

	people := 0
	for _, candidate := range scannerItems(t, harness.items) {
		if candidate.Kind == item.KindPerson {
			people++
			if candidate.ID != want.ID || candidate.Name == nil || *candidate.Name != "Alice" {
				t.Fatalf("persisted Person entity = %#v, want only %s named Alice", candidate, want.ID)
			}
		}
	}
	if people != 1 {
		t.Fatalf("persisted Person entity count = %d, want one case-folded Alice", people)
	}
}
