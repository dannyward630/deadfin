// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"sync/atomic"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestRunUsesInjectedStoresFactory(t *testing.T) {
	var calls atomic.Int32

	storetest.Run(t, func(t *testing.T) store.Stores {
		calls.Add(1)
		stores, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("opening factory SQLite stores: %v", err)
		}
		t.Cleanup(func() {
			if err := stores.Close(); err != nil {
				t.Errorf("closing factory SQLite stores: %v", err)
			}
		})
		return stores
	})

	if calls.Load() == 0 {
		t.Fatal("storetest.Run did not call the injected Stores factory")
	}
}
