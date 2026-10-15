// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestValidateReconcileChildParity proves that ValidateReconcileChild
// rejects any child that ReconcileScannedPlanAtWithOptions rejects for a
// per-child rule. Each case mutates one child shape that the store
// validates inside the reconcile loop body, then asserts both methods
// reject it. The cross-child rules (duplicate child ID, conflicting person
// names) stay plan-level and are not tested here.
func TestValidateReconcileChildParity(t *testing.T) {
	t.Run("SQLite", func(t *testing.T) {
		runValidateReconcileChildParity(t, openItemStore)
	})
}

func runValidateReconcileChildParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	items := open(t)
	parent := hierarchyItem(t, 19100, item.KindFolder, nil)
	if err := items.Upsert(t.Context(), parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	linkedTarget := reconciliationChild(t, 19101, item.KindMovie, parent.ID, "linked target")
	linkedTarget.Path = stringPtr("/library/linked.mkv")
	if err := items.Upsert(t.Context(), linkedTarget); err != nil {
		t.Fatalf("seed linked target: %v", err)
	}

	baseline := reconciliationChild(t, 19102, item.KindMovie, parent.ID, "baseline child")
	baseline.Path = stringPtr("/library/baseline.mkv")
	baseline.ProviderIDs = providerIDs("Tmdb", "baseline")

	cases := []struct {
		name string
		edit func(*item.Item)
	}{
		{name: "duplicate image type and index", edit: func(c *item.Item) {
			c.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "one.jpg"}, {Type: item.ImageTypePrimary, Index: 0, Path: "two.jpg"}}
		}},
		{name: "duplicate metadata field", edit: func(c *item.Item) {
			c.LockedFields = []item.MetadataField{item.MetadataFieldName, item.MetadataFieldName}
		}},
		{name: "duplicate trailer type", edit: func(c *item.Item) {
			c.TrailerTypes = []item.TrailerType{item.TrailerTypeLocalTrailer, item.TrailerTypeLocalTrailer}
		}},
		{name: "duplicate person identity and kind", edit: func(c *item.Item) {
			c.People = []item.PersonCredit{{PersonID: contractUUID(t, 19103), Name: "dup", Type: item.PersonKindActor}, {PersonID: contractUUID(t, 19103), Name: "dup", Type: item.PersonKindActor}}
		}},
		{name: "duplicate linked child", edit: func(c *item.Item) {
			c.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedTarget.ID, Type: item.LinkedChildTypeManual}, {ItemID: linkedTarget.ID, Type: item.LinkedChildTypeManual}}
		}},
		{name: "duplicate item value", edit: func(c *item.Item) {
			c.Values = item.ItemValues{Genres: []string{"Drama", "Drama"}}
		}},
		{name: "nil child id", edit: func(c *item.Item) {
			c.ID = dto.UUID{}
		}},
		{name: "empty kind", edit: func(c *item.Item) {
			c.Kind = item.Kind(255)
		}},
		{name: "nil parent", edit: func(c *item.Item) {
			c.ParentID = nil
		}},
		{name: "foreign parent", edit: func(c *item.Item) {
			foreign := contractUUID(t, 19104)
			c.ParentID = &foreign
		}},
		{name: "playlist owner on non-playlist", edit: func(c *item.Item) {
			c.PlaylistOwnerUserID = contractUUID(t, 19105)
		}},
		{name: "open access on non-playlist", edit: func(c *item.Item) {
			c.OpenAccess = true
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child := baseline
			tc.edit(&child)

			_, reconcileErr := items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, []item.Item{child}, nil, nil, now, store.ReconcileOptions{})
			if reconcileErr == nil {
				t.Fatalf("ReconcileScannedPlanAtWithOptions accepted %s", tc.name)
			}

			validateErr := items.ValidateReconcileChild(parent.ID, child)
			if validateErr == nil {
				t.Fatalf("ValidateReconcileChild accepted %s (reconcile rejected: %v)", tc.name, reconcileErr)
			}
		})
	}

	t.Run("valid child passes", func(t *testing.T) {
		if err := items.ValidateReconcileChild(parent.ID, baseline); err != nil {
			t.Fatalf("ValidateReconcileChild rejected valid child: %v", err)
		}
	})
}
