// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestPostgresConcurrentOpenMigratesOnce opens several stores at once on one
// empty schema, as servers that start together do. The migration advisory
// lock lets one open apply the migrations; the others wait and find them
// applied.
func TestPostgresConcurrentOpenMigratesOnce(t *testing.T) {
	dsn := storetest.PostgresSchemaURL(t)
	const opens = 8
	var wg sync.WaitGroup
	opened := make([]store.Stores, opens)
	errs := make([]error, opens)
	for i := range opens {
		wg.Go(func() {
			opened[i], errs[i] = postgres.Open(t.Context(), postgres.Config{DSN: dsn})
		})
	}
	wg.Wait()
	for i := range opens {
		if errs[i] != nil {
			t.Errorf("open %d: %v", i, errs[i])
			continue
		}
		users, err := store.NewIdentity(opened[i]).ListUsers(t.Context())
		if err != nil || len(users) != 0 {
			t.Errorf("open %d: users = %v, %v", i, users, err)
		}
		if err := opened[i].Close(); err != nil {
			t.Errorf("close %d: %v", i, err)
		}
	}
}
