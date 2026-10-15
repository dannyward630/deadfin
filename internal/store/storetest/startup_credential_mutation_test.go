// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// The startup credential write is a compare-and-set, not a normal user update:
// after any contender sets a nonblank credential, a second contender must not
// replace either half of the winning name/hash pair.
func TestIdentityConfigureFirstUserAtomicallyChoosesOneSQLiteCandidate(t *testing.T) {
	assertConfigureFirstUserChoosesOneCandidate(t, openIdentity(t))
}

// Independent SQLite store instances can be opened by separate processes.
// SQLite's busy handling and the blank-password compare-and-set must make the
// loser observe the winner rather than surfacing a transient BUSY failure.
func TestIdentityConfigureFirstUserAtomicallyChoosesOneCrossStoreSQLiteCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	firstStores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open first SQLite identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := firstStores.Close(); err != nil {
			t.Errorf("close first SQLite identity store: %v", err)
		}
	})
	secondStores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open second SQLite identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := secondStores.Close(); err != nil {
			t.Errorf("close second SQLite identity store: %v", err)
		}
	})
	assertConfigureFirstUserChoosesOneCandidate(t, store.NewIdentity(firstStores), store.NewIdentity(secondStores))
}

// Separate PostgreSQL pools model independent application instances. A local
// mutex or a transaction scoped only to one connection pool would let both
// requests pass the blank-password check and mix a winner's name with another
// request's credential.
func TestIdentityConfigureFirstUserAtomicallyChoosesOnePostgreSQLCandidate(t *testing.T) {
	first, second := openStartupPostgresIdentityPair(t)
	assertConfigureFirstUserChoosesOneCandidate(t, first, second)
}

func TestIdentityConfigureFirstUserRejectsAbsentAndConfiguredUsersWithoutMutation(t *testing.T) {
	identity := openIdentity(t)
	update := store.FirstUserUpdate{
		Name:           "attempted-name",
		NormalizedName: "ATTEMPTED-NAME",
		Rename:         true,
		PasswordHash:   "attempted-hash",
	}

	if err := identity.ConfigureFirstUser(t.Context(), update); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("configure absent first user error = %v, want ErrNotFound", err)
	}

	seedFirstUser(t, identity, "Configured", "CONFIGURED", "persisted-hash")
	if err := identity.ConfigureFirstUser(t.Context(), update); !errors.Is(err, store.ErrStartupUserConfigured) {
		t.Fatalf("configure established first user error = %v, want ErrStartupUserConfigured", err)
	}
	got, err := identity.FirstUser(t.Context())
	if err != nil {
		t.Fatalf("read configured first user: %v", err)
	}
	if got.Name != "Configured" || got.NormalizedName != "CONFIGURED" || got.PasswordHash != "persisted-hash" {
		t.Fatalf("configured-user rejection mutated first user = %#v, want persisted name and password hash", got)
	}
}

func assertConfigureFirstUserChoosesOneCandidate(t *testing.T, identities ...*store.Identity) {
	t.Helper()
	seedFirstUser(t, identities[0], "Initial", "INITIAL", "")

	candidates := []store.FirstUserUpdate{
		{Name: "First Candidate", NormalizedName: "FIRST CANDIDATE", Rename: true, PasswordHash: "first-hash"},
		{Name: "Second Candidate", NormalizedName: "SECOND CANDIDATE", Rename: true, PasswordHash: "second-hash"},
	}
	start := make(chan struct{})
	ready := make(chan struct{}, len(candidates))
	errorsByCandidate := make(chan error, len(candidates))
	var group sync.WaitGroup
	for index, candidate := range candidates {
		group.Add(1)
		go func(index int, candidate store.FirstUserUpdate) {
			defer group.Done()
			ready <- struct{}{}
			<-start
			errorsByCandidate <- identities[index%len(identities)].ConfigureFirstUser(t.Context(), candidate)
		}(index, candidate)
	}
	for range candidates {
		<-ready
	}
	close(start)
	group.Wait()
	close(errorsByCandidate)

	succeeded := 0
	for err := range errorsByCandidate {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrStartupUserConfigured):
		default:
			t.Fatalf("concurrent ConfigureFirstUser error = %v, want success or ErrStartupUserConfigured", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent ConfigureFirstUser successes = %d, want exactly one", succeeded)
	}

	got, err := identities[0].FirstUser(t.Context())
	if err != nil {
		t.Fatalf("read configured first user: %v", err)
	}
	for _, candidate := range candidates {
		if got.Name == candidate.Name && got.NormalizedName == candidate.NormalizedName && got.PasswordHash == candidate.PasswordHash {
			return
		}
	}
	t.Fatalf("concurrent ConfigureFirstUser persisted mixed or unexpected winner %#v", got)
}

func seedFirstUser(t *testing.T, identity *store.Identity, name, normalizedName, passwordHash string) {
	t.Helper()
	if _, err := identity.CreateUser(t.Context(), store.User{
		ID:             startupStoreUUID(t, 101),
		Name:           name,
		NormalizedName: normalizedName,
		PasswordHash:   passwordHash,
		Policy:         []byte(`{"IsAdministrator":true}`),
		Configuration:  []byte(`{}`),
	}); err != nil {
		t.Fatalf("seed first user: %v", err)
	}
}
