// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// TestIntraItemDuplicateNormalization proves the plan-normalization pass
// collapses intra-item duplicates that the NFO adapter and probe produce:
// the same studio twice with different casing, and one actor credited under
// one (PersonID, PersonKind) where the probe then supplies the same actor
// with a different role. The stub Refresher writes the item exactly as
// mergeNFOResult does (adapters.go:232-242): studios copied verbatim and
// people collapsed on (PersonID, PersonKind) by nfoPeople. The stub
// MediaProbe then appends the same actor with a different role, exactly as
// appendProbePeople does (scanner_probe.go:430-446), keyed by
// (Type, name, Role) so the duplicate survives. The normalization pass
// collapses both. The scan reports no errors. The stored item has one
// studio with the first occurrence's casing and position, and one credit
// with the first role. A rescan is an exact no-op.
func TestIntraItemDuplicateNormalization(t *testing.T) {
	actor := library.PersonEntity("Alice Actor")
	actorRole := "Lead"

	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = library.NewChain(resolver)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		// Model mergeNFOResult (adapters.go:232-242): the NFO adapter copies
		// studios verbatim and collapses people on (PersonID, PersonKind).
		// The stub writes the same studio twice with different casing, and
		// one actor credited under one (PersonID, PersonKind).
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Kind != item.KindMovie {
				return nil
			}
			candidate.Values.Studios = []string{"Warner Bros", "warner bros"}
			order := 0
			candidate.People = []item.PersonCredit{{
				PersonID:  actor.ID,
				Name:      "Alice Actor",
				Role:      &actorRole,
				Type:      item.PersonKindActor,
				SortOrder: &order,
			}}
			return nil
		}),
		// Model appendProbePeople (scanner_probe.go:430-446): the probe
		// supplies the same actor with a different role. hasProbePerson
		// keys on (Type, name, Role), so the different role means the
		// duplicate survives the probe guard.
		MediaProbe: intraItemDuplicateProbe{},
	})

	movie := scannerPath(t, harness.source, "Duplicate Studio (2024).mkv")
	writeScannerFile(t, movie, "movie")

	first, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan intra-item duplicates: %v", err)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("scan reported errors: %#v", first.Errors)
	}

	stored := scannerItemAtPath(t, harness.items, movie)
	if len(stored.Values.Studios) != 1 {
		t.Fatalf("stored studios = %#v, want exactly one", stored.Values.Studios)
	}
	if stored.Values.Studios[0] != "Warner Bros" {
		t.Fatalf("stored studio casing = %q, want first occurrence %q", stored.Values.Studios[0], "Warner Bros")
	}
	if len(stored.People) != 1 {
		t.Fatalf("stored people = %#v, want exactly one credit", stored.People)
	}
	if stored.People[0].PersonID != actor.ID {
		t.Fatalf("stored person ID = %s, want %s", stored.People[0].PersonID, actor.ID)
	}
	if stored.People[0].Role == nil || *stored.People[0].Role != actorRole {
		t.Fatalf("stored person role = %v, want first role %q", stored.People[0].Role, actorRole)
	}

	second, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("rescan intra-item duplicates: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Deleted != 0 || len(second.Errors) != 0 {
		t.Fatalf("unchanged rescan = %#v, want no mutations", second)
	}
	rescanned := scannerItemAtPath(t, harness.items, movie)
	if len(rescanned.Values.Studios) != 1 || rescanned.Values.Studios[0] != "Warner Bros" {
		t.Fatalf("rescan studios = %#v, want [Warner Bros]", rescanned.Values.Studios)
	}
	if len(rescanned.People) != 1 || rescanned.People[0].PersonID != actor.ID {
		t.Fatalf("rescan people = %#v, want one credit for %s", rescanned.People, actor.ID)
	}
}

type intraItemDuplicateProbe struct{}

func (intraItemDuplicateProbe) Probe(_ context.Context, _ library.MediaProbeInput) (library.MediaProbeResult, error) {
	role := "Producer"
	return library.MediaProbeResult{
		People: []library.MediaProbePerson{
			{Name: "Alice Actor", Role: &role, Type: item.PersonKindActor},
		},
	}, nil
}
