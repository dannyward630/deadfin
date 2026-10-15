// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestMaintainSucceedsOnOpenSQLiteStore(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer func() { _ = stores.Close() }()

	if err := store.Maintain(ctx, stores); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
}
