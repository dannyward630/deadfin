// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

type countingStatisticsStores struct {
	store.Stores
	requests int
}

func (s *countingStatisticsStores) RequestStatisticsRefresh() { s.requests++ }

func TestRequestStatisticsRefreshOnlyAfterLargeScans(t *testing.T) {
	t.Parallel()
	stores := &countingStatisticsStores{}
	requestStatisticsRefresh(stores, library.RefreshResult{Inserted: 500, Updated: 400, Deleted: 99})
	if stores.requests != 0 {
		t.Fatalf("requests after 999 changed items = %d, want 0", stores.requests)
	}
	requestStatisticsRefresh(stores, library.RefreshResult{Inserted: 500, Updated: 400, Deleted: 100})
	if stores.requests != 1 {
		t.Fatalf("requests after 1000 changed items = %d, want 1", stores.requests)
	}
}
