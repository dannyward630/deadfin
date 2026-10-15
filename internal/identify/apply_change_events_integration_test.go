// SPDX-License-Identifier: GPL-2.0-only

package identify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
)

// noOpResolver is a minimal library.Resolver the real Scanner accepts. It
// claims no entries, so the scanner reconciles an empty child plan. The
// identify Apply path does not run the scanner; the scanner exists only to own
// the real ChangeBus that Apply publishes through.
type noOpResolver struct{}

func (noOpResolver) Priority() library.ResolverPriority { return library.ResolverPriorityFirst }
func (noOpResolver) Resolve(context.Context, library.ResolveContext) (*item.Item, error) {
	return nil, nil
}

// recordingItemUpdatePublisher records every PublishItemUpdated call and
// delegates to the real library.ChangeBus. The subscription coalesces events at
// the bus (change_events.go:47-53), so the recorder is the only seam that
// observes the exact arguments the service passes before the bus batches them.
type recordingItemUpdatePublisher struct {
	calls []recordedPublish
	bus   *library.ChangeBus
}

type recordedPublish struct {
	parentID           dto.UUID
	collectionFolderID dto.UUID
	ids                []dto.UUID
}

func (r *recordingItemUpdatePublisher) PublishItemUpdated(parentID, collectionFolderID dto.UUID, ids []dto.UUID) {
	r.calls = append(r.calls, recordedPublish{
		parentID:           parentID,
		collectionFolderID: collectionFolderID,
		ids:                append([]dto.UUID(nil), ids...),
	})
	r.bus.PublishItemUpdated(parentID, collectionFolderID, ids)
}

// TestApplyPublishesExactlyOneUpdateThroughRealChangeBus proves the identify
// Apply path publishes exactly one item update through the process ChangeBus.
// The test wires the real Scanner ChangeBus, records the publisher call at the
// service boundary, and verifies the debounced bus delivers an updated-only
// payload after a fake-clock advance. It uses no wall-clock sleep.
func TestApplyPublishesExactlyOneUpdateThroughRealChangeBus(t *testing.T) {
	items, stores := setupTestItemsAndStores(t)

	fakeClock := clock.NewFake(time.Unix(0, 0).UTC())

	// A real Topology with one Movies virtual folder that owns the media root.
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("new topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	mediaRoot := t.TempDir()
	virtual, err := topology.AddVirtualFolder(context.Background(), "Movies", "movies", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: mediaRoot}},
	})
	if err != nil {
		t.Fatalf("add virtual folder: %v", err)
	}
	collectionFolderID := virtual.CollectionFolderID

	// A real Scanner owns the real ChangeBus. Apply publishes through that bus.
	chain := library.NewChain(noOpResolver{})
	scanner, err := library.NewScanner(topology, items, chain, library.ScannerOptions{
		Concurrency: 1,
		Clock:       fakeClock,
	})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})

	// Subscribe to the real bus before Apply so the delivery channel captures
	// the published event.
	subscription := scanner.Changes().Subscribe(1)
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close subscription: %v", err)
		}
	})

	// The scanned CollectionFolder item. Its ID is the deterministic path-derived
	// identity the topology reports. It is the root ancestor with no parent, so
	// the store derives its TopParentID as itself.
	collectionFolder := &item.Item{
		ID:       collectionFolderID,
		Kind:     item.KindCollectionFolder,
		Name:     ptr("Movies"),
		Path:     &mediaRoot,
		IsFolder: true,
	}
	if err := items.Upsert(context.Background(), *collectionFolder); err != nil {
		t.Fatalf("upsert collection folder: %v", err)
	}

	// An intermediate folder between the collection item and the target movie.
	// Apply maps the intermediate folder ID to the parent argument and the
	// collection-folder ID to the collection-folder argument.
	intermediatePath := filepath.Join(mediaRoot, "Intermediate Folder")
	if err := os.MkdirAll(intermediatePath, 0o755); err != nil {
		t.Fatalf("mkdir intermediate: %v", err)
	}
	intermediateID := mustUUID("00112233445566778899aabbccddeeff")
	intermediate := &item.Item{
		ID:       intermediateID,
		Kind:     item.KindFolder,
		Name:     ptr("Intermediate Folder"),
		Path:     &intermediatePath,
		IsFolder: true,
		ParentID: &collectionFolderID,
	}
	if err := items.Upsert(context.Background(), *intermediate); err != nil {
		t.Fatalf("upsert intermediate folder: %v", err)
	}

	// The target movie under the intermediate folder.
	moviePath := filepath.Join(intermediatePath, "movie.mp4")
	if err := os.WriteFile(moviePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write movie file: %v", err)
	}
	movieID := mustUUID("aabbccddeeff00112233445566778899")
	movie := &item.Item{
		ID:          movieID,
		Kind:        item.KindMovie,
		Name:        ptr("Movie"),
		Path:        &moviePath,
		ProviderIDs: newProviderIDs("Tmdb", "999"),
		ParentID:    &intermediateID,
	}
	if err := items.Upsert(context.Background(), *movie); err != nil {
		t.Fatalf("upsert movie: %v", err)
	}

	// Reload the movie and make sure the store derived its TopParentID from the
	// root ancestor (the CollectionFolder). The store overwrites whatever a
	// caller passes, so this proves the ancestor chain resolves to the
	// collection-folder ID.
	loaded, err := items.GetBatch(context.Background(), []dto.UUID{movieID})
	if err != nil {
		t.Fatalf("reload movie: %v", err)
	}
	if len(loaded) == 0 {
		t.Fatal("movie not found after upsert")
	}
	reloadedMovie := loaded[0]
	if reloadedMovie.TopParentID == nil {
		t.Fatal("reloaded movie TopParentID is nil, want the collection-folder ID")
	}
	if *reloadedMovie.TopParentID != collectionFolderID {
		t.Fatalf("reloaded movie TopParentID = %v, want collection-folder ID %v",
			*reloadedMovie.TopParentID, collectionFolderID)
	}

	// Build the service with the real ChangeBus from the scanner. New assigns
	// the bus to the private publisher interface only when the pointer is not
	// nil.
	svc := New(Options{
		Items:     items,
		Topology:  topology,
		Refresher: &fakeRefresher{},
		Clock:     fakeClock,
		Changes:   scanner.Changes(),
	})

	// Replace the private publisher with a recorder that delegates to the real
	// bus. The recorder captures the exact arguments before the bus coalesces
	// them, so the test can assert the parent, collection-folder, and item IDs.
	recorder := &recordingItemUpdatePublisher{bus: scanner.Changes()}
	svc.publisher = recorder

	// Apply once with a no-op refresher.
	result := providers.RemoteSearchResult{
		Name:           ptr("New Title"),
		ProviderIDs:    map[string]string{"Tmdb": "123"},
		ProductionYear: intptr(2024),
	}
	if err := svc.Apply(context.Background(), movieID, result, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Assert exactly one publisher call with the expected parent,
	// collection-folder, and item IDs.
	if len(recorder.calls) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.parentID != intermediateID {
		t.Errorf("publisher parentID = %v, want intermediate folder ID %v",
			call.parentID, intermediateID)
	}
	if call.collectionFolderID != collectionFolderID {
		t.Errorf("publisher collectionFolderID = %v, want collection-folder ID %v",
			call.collectionFolderID, collectionFolderID)
	}
	if len(call.ids) != 1 || call.ids[0] != movieID {
		t.Errorf("publisher ids = %v, want [%v]", call.ids, movieID)
	}

	// The bus debounces with a 30-second trailing window. Advance the fake
	// clock past the debounce so the bus delivers the coalesced event.
	fakeClock.Advance(31 * time.Second)

	// Assert one UpdateInfo whose ItemsUpdated contains only the movie ID and
	// all other arrays are empty. The coalescer goroutine delivers the event
	// after the fake-clock advance, so receive the update through a guarded
	// select. No wall-clock sleep is used.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case update, ok := <-subscription.Events():
		if !ok {
			t.Fatal("subscription closed before the update arrived")
		}
		if len(update.ItemsUpdated) != 1 || update.ItemsUpdated[0] != movieID {
			t.Errorf("ItemsUpdated = %v, want [%v]", update.ItemsUpdated, movieID)
		}
		if len(update.ItemsAdded) != 0 {
			t.Errorf("ItemsAdded = %v, want empty", update.ItemsAdded)
		}
		if len(update.ItemsRemoved) != 0 {
			t.Errorf("ItemsRemoved = %v, want empty", update.ItemsRemoved)
		}
		if len(update.FoldersAddedTo) != 0 {
			t.Errorf("FoldersAddedTo = %v, want empty", update.FoldersAddedTo)
		}
		if len(update.FoldersRemovedFrom) != 0 {
			t.Errorf("FoldersRemovedFrom = %v, want empty", update.FoldersRemovedFrom)
		}
		if len(update.CollectionFolders) != 0 {
			t.Errorf("CollectionFolders = %v, want empty for an update-only mutation",
				update.CollectionFolders)
		}
	case <-timer.C:
		t.Fatal("debounced update event was not delivered")
	}
}
