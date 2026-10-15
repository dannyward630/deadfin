// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestItemStorePersonImagesSQLite(t *testing.T) {
	runItemStorePersonImages(t, openReviewerItemStore)
}

func TestItemStorePersonImagesPostgres(t *testing.T) {
	runItemStorePersonImages(t, openPostgresReviewerItemStore)
}

func runItemStorePersonImages(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()
	const tmdb = "https://image.tmdb.org/t/p/original/alice.jpg"

	t.Run("PrimaryImages reads the first Primary image of each item", func(t *testing.T) {
		items, _ := open(t)
		alice, bob, carol := library.PersonEntity("Alice"), library.PersonEntity("Bob"), library.PersonEntity("Carol")
		hash := "LEHV6nWB2yk8"
		alice.ImageInfos = []item.ImageInfo{
			{Type: item.ImageTypeBackdrop, Path: "/people/alice-backdrop.jpg"},
			{Type: item.ImageTypePrimary, Path: tmdb, BlurHash: &hash},
		}
		bob.ImageInfos = []item.ImageInfo{{Type: item.ImageTypeBackdrop, Path: "/people/bob-backdrop.jpg"}}
		for _, value := range []item.Item{alice, bob, carol} {
			if err := items.Upsert(t.Context(), value); err != nil {
				t.Fatalf("upsert %s: %v", *value.Name, err)
			}
		}
		got, err := items.PrimaryImages(t.Context(), []dto.UUID{alice.ID, bob.ID, carol.ID, alice.ID})
		if err != nil {
			t.Fatalf("PrimaryImages: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("PrimaryImages = %#v, want only Alice", got)
		}
		image := got[alice.ID]
		if image.Type != item.ImageTypePrimary || image.Path != tmdb || image.BlurHash == nil || *image.BlurHash != hash {
			t.Fatalf("Alice Primary image = %#v, want the stored TMDb profile image", image)
		}
	})

	t.Run("a scan keeps a stored person Primary image", func(t *testing.T) {
		items, _ := open(t)
		parent, movie, person := scannedPlanPeopleFixture(t)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		withImage := person
		withImage.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: tmdb}}
		if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{withImage}); err != nil {
			t.Fatalf("first reconcile: %v", err)
		}
		// A later plan credits the person without an image, then with a
		// different one. Neither replaces the stored Primary image.
		other := person
		other.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/other.jpg"}}
		for _, candidate := range []item.Item{person, other} {
			if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{candidate}); err != nil {
				t.Fatalf("later reconcile: %v", err)
			}
			images, err := items.Images(t.Context(), person.ID)
			if err != nil {
				t.Fatalf("read person images: %v", err)
			}
			if len(images) != 1 || images[0].Path != tmdb {
				t.Fatalf("person images = %#v, want the first stored Primary image", images)
			}
		}
	})

	t.Run("a metadata backfill adds a missing person Primary image only", func(t *testing.T) {
		items, _ := open(t)
		parent, movie, person := scannedPlanPeopleFixture(t)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{person}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		withImage := person
		withImage.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: tmdb}}
		saved := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		if _, err := items.MetadataBackfill(t.Context(), parent.ID, nil, []item.Item{withImage}, saved, store.MetadataBackfillOptions{}); err != nil {
			t.Fatalf("backfill with image: %v", err)
		}
		other := person
		other.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/other.jpg"}}
		if _, err := items.MetadataBackfill(t.Context(), parent.ID, nil, []item.Item{other}, saved, store.MetadataBackfillOptions{}); err != nil {
			t.Fatalf("backfill with other image: %v", err)
		}
		images, err := items.Images(t.Context(), person.ID)
		if err != nil {
			t.Fatalf("read person images: %v", err)
		}
		if len(images) != 1 || images[0].Path != tmdb {
			t.Fatalf("person images = %#v, want the first backfilled Primary image", images)
		}
	})
}
