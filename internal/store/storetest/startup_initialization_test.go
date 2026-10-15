// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
)

// User-service startup initialization must make the empty-store decision and
// insertion in one transaction. A ListUsers/CreateUser sequence passes under
// ordinary startup but admits duplicate first users under concurrent requests.
func TestIdentityCreateUserIfEmptyAdmitsExactlyOneConcurrentCandidate(t *testing.T) {
	assertIdentityCreateUserIfEmptyIsAtomic(t, openIdentity(t))
}

// Separate stores have separate one-connection writer pools. This verifies the
// PostgreSQL transaction/advisory lock protects the empty-store claim across
// application instances, rather than merely serializing a single local pool.
func TestIdentityPostgresCreateUserIfEmptyAdmitsExactlyOneCrossPoolCandidate(t *testing.T) {
	first, second := openStartupPostgresIdentityPair(t)
	assertIdentityCreateUserIfEmptyIsAtomic(t, first, second)
}

func assertIdentityCreateUserIfEmptyIsAtomic(t *testing.T, identities ...*store.Identity) {
	t.Helper()
	if len(identities) == 0 {
		t.Fatal("at least one identity store is required")
	}
	t.Helper()
	const workers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, workers)
	results := make(chan initialUserCreateResult, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			ready <- struct{}{}
			<-start
			identity := identities[index%len(identities)]
			created, err := identity.CreateUserIfEmpty(t.Context(), store.User{
				ID:             startupStoreUUID(t, index+1),
				Name:           fmt.Sprintf("startup-candidate-%d", index),
				NormalizedName: fmt.Sprintf("STARTUP-CANDIDATE-%d", index),
				Policy:         []byte(`{"IsAdministrator":true}`),
				Configuration:  []byte(`{}`),
			})
			results <- initialUserCreateResult{created: created, err: err}
		}(index)
	}
	for range workers {
		<-ready
	}
	close(start)
	group.Wait()
	close(results)

	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent CreateUserIfEmpty: %v", result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent CreateUserIfEmpty reported %d creators, want exactly one", createdCount)
	}
	users, err := identities[0].ListUsers(t.Context())
	if err != nil {
		t.Fatalf("list initial users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("concurrent CreateUserIfEmpty persisted %d users, want exactly one", len(users))
	}
}

type initialUserCreateResult struct {
	created bool
	err     error
}

func startupStoreUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse startup store UUID %d: %v", value, err)
	}
	return id
}

func openStartupPostgresIdentityPair(t *testing.T) (*store.Identity, *store.Identity) {
	t.Helper()
	dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEADFIN_TEST_POSTGRES_DSN is not set")
	}
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		t.Fatalf("generate PostgreSQL startup schema suffix: %v", err)
	}
	schema := "startup_storetest_" + hex.EncodeToString(suffix[:])
	first, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open first PostgreSQL startup store: %v", err)
	}
	second, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		if closeErr := first.Close(); closeErr != nil {
			t.Errorf("close first PostgreSQL startup store after second-open failure: %v", closeErr)
		}
		t.Fatalf("open second PostgreSQL startup store: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second PostgreSQL startup store: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Errorf("close first PostgreSQL startup store: %v", err)
		}
		if err := dropStartupPostgresSchema(context.Background(), dsn, schema); err != nil {
			t.Errorf("drop PostgreSQL startup schema %q: %v", schema, err)
		}
	})
	return store.NewIdentity(first), store.NewIdentity(second)
}

func dropStartupPostgresSchema(ctx context.Context, dsn, schema string) error {
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open PostgreSQL schema administrator: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		return err
	}
	return nil
}
