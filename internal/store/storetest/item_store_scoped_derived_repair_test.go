// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// Each item write repairs derived state only in the subtrees it changed.
// These tests prove that the scoped repair leaves the same ancestors closure,
// clean names, top/series/season columns, and user data keys as the full
// RepairDerived after each kind of mutation.
func TestItemStoreScopedDerivedRepairMatchesFullRepairSQLite(t *testing.T) {
	runScopedDerivedRepairMatchesFullRepair(t, func(t *testing.T) store.Stores {
		t.Helper()
		var stores store.Stores
		storetest.Run(t, func(t *testing.T) store.Stores {
			opened, err := sqlite.Open(t.Context(), ":memory:")
			if err != nil {
				t.Fatalf("open SQLite fixture: %v", err)
			}
			stores = opened
			return opened
		})
		return stores
	})
}

func TestItemStoreScopedDerivedRepairMatchesFullRepairPostgres(t *testing.T) {
	runScopedDerivedRepairMatchesFullRepair(t, storetest.OpenPostgres)
}

func runScopedDerivedRepairMatchesFullRepair(t *testing.T, open func(*testing.T) store.Stores) {
	stores := open(t)
	items := store.NewItemStore(stores)
	ctx := t.Context()
	saved := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	// assertScoped compares the state the mutation left with the state of a
	// full repair of the same rows.
	assertScoped := func(step string) {
		t.Helper()
		scoped := derivedStateDigest(t, stores)
		if err := items.RepairDerived(ctx); err != nil {
			t.Fatalf("%s: full repair: %v", step, err)
		}
		if full := derivedStateDigest(t, stores); scoped != full {
			t.Fatalf("%s: scoped derived state %s differs from full repair %s", step, scoped, full)
		}
	}
	id := func(value int) dto.UUID { return contractUUID(t, value) }
	ptr := func(value dto.UUID) *dto.UUID { return &value }
	node := func(value int, kind item.Kind, parent int, name string) item.Item {
		it := item.Item{ID: id(value), Kind: kind, Name: stringPtr(name), IsFolder: kind != item.KindEpisode && kind != item.KindMovie}
		if parent != 0 {
			it.ParentID = ptr(id(parent))
		}
		return it
	}
	reconcile := func(step string, parent int, children ...item.Item) {
		t.Helper()
		if _, err := items.ReconcileScannedPlanAtWithOptions(ctx, id(parent), children, nil, nil, saved, store.ReconcileOptions{}); err != nil {
			t.Fatalf("%s: reconcile: %v", step, err)
		}
		assertScoped(step)
	}

	root := node(1, item.KindAggregateFolder, 0, "root")
	if err := items.Upsert(ctx, root); err != nil {
		t.Fatalf("upsert root: %v", err)
	}
	assertScoped("upsert root")
	reconcile("insert libraries", 1, node(2, item.KindCollectionFolder, 1, "Shows"), node(3, item.KindCollectionFolder, 1, "Movies"))
	series := node(10, item.KindSeries, 2, "The Show")
	series.ProviderIDs = providerIDs("Tvdb", "100")
	reconcile("insert series", 2, series)
	reconcile("insert seasons", 10, node(11, item.KindSeason, 10, "Season 1"), node(14, item.KindSeason, 10, "Season 2"))
	episode := func(value, parent, index int) item.Item {
		it := node(value, item.KindEpisode, parent, fmt.Sprintf("Episode %d", value))
		it.IndexNumber = &index
		season := 1
		it.ParentIndexNumber = &season
		return it
	}
	reconcile("insert episodes", 11, episode(12, 11, 1), episode(13, 11, 2))
	reconcile("insert later episode", 14, episode(15, 14, 1))
	reconcile("insert movies", 3, node(20, item.KindMovie, 3, "Älpha—Béta!"), node(21, item.KindMovie, 3, "Second"))

	// A changed Series changes its episodes' user data keys.
	renamed := series
	renamed.Name = stringPtr("The Renamed Show")
	renamed.ProviderIDs = providerIDs("Tvdb", "200")
	if _, err := items.ReconcileScannedPlanAtWithOptions(ctx, id(2), []item.Item{renamed}, nil, []item.Item{{ID: id(90), Kind: item.KindPerson, Name: stringPtr("A Person")}}, saved, store.ReconcileOptions{ForceRefresh: true}); err != nil {
		t.Fatalf("rename series: %v", err)
	}
	assertScoped("rename series with a virtual")

	// A new Series and a moved Season change the season's subtree hierarchy.
	if err := items.Upsert(ctx, node(30, item.KindSeries, 2, "Other Show")); err != nil {
		t.Fatalf("upsert second series: %v", err)
	}
	assertScoped("upsert second series")
	if err := items.MoveSubtree(ctx, id(14), ptr(id(30))); err != nil {
		t.Fatalf("move season: %v", err)
	}
	assertScoped("move season")

	// A stale Season goes away while its first episode becomes a direct
	// child of the Series.
	reconcile("remove season and promote an episode", 10, episode(12, 10, 1))

	if err := items.Delete(ctx, id(20)); err != nil {
		t.Fatalf("delete movie: %v", err)
	}
	assertScoped("delete movie")
	moved := node(21, item.KindMovie, 2, "Second, moved")
	if err := items.Upsert(ctx, moved); err != nil {
		t.Fatalf("upsert moved movie: %v", err)
	}
	assertScoped("upsert reparented movie")
	if err := items.Delete(ctx, id(30)); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	assertScoped("delete series subtree")
}

// derivedStateDigest hashes every derived row in a stable order. Values are
// formatted as scanned, so a digest compares states of one database only.
func derivedStateDigest(t *testing.T, stores store.Stores) string {
	t.Helper()
	hash := sha256.New()
	for _, query := range []string{
		"SELECT item_id, ancestor_id FROM ancestors ORDER BY item_id, ancestor_id",
		"SELECT id, clean_name, top_parent_id, series_id, season_id FROM items ORDER BY id",
		"SELECT item_id, custom_key, is_live_tv_channel_name FROM item_user_data_keys ORDER BY item_id, custom_key",
	} {
		rows, err := stores.Reader().QueryContext(t.Context(), query)
		if err != nil {
			t.Fatalf("read derived state: %v", err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatalf("read derived columns: %v", err)
		}
		count := 0
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for index := range values {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatalf("scan derived state: %v", err)
			}
			for _, value := range values {
				fmt.Fprintf(hash, "%x|", value)
			}
			fmt.Fprintln(hash)
			count++
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close derived rows: %v", err)
		}
		fmt.Fprintf(hash, "%d rows\n", count)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
