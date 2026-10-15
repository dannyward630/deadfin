// SPDX-License-Identifier: GPL-2.0-only

package playgrant_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/auth/playgrant"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
)

func uuid(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGrantAuthorizesOnlyItsItemAndSources(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	store := playgrant.New(fake)
	user := uuid(t, "00000000-0000-0000-0000-0000000000a1")
	item := uuid(t, "00000000-0000-0000-0000-0000000000b1")
	other := uuid(t, "00000000-0000-0000-0000-0000000000b2")
	source := item.String()
	store.Issue("session-1", user, item, []string{source})

	for _, test := range []struct {
		name          string
		playSession   string
		item          dto.UUID
		mediaSourceID string
		want          bool
	}{
		{"granted item and source", "session-1", item, source, true},
		{"granted item, dashed source", "session-1", item, item.Dashed(), true},
		{"granted item, default source", "session-1", item, "", true},
		{"other item", "session-1", other, "", false},
		{"other source", "session-1", item, other.String(), false},
		{"unknown session", "session-2", item, source, false},
		{"empty session", "", item, source, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := store.Authorize(test.playSession, test.item, test.mediaSourceID)
			if ok != test.want || (ok && got != user) {
				t.Fatalf("Authorize = %v, %t; want user %t", got, ok, test.want)
			}
		})
	}
}

func TestGrantIssueIgnoresMissingUserOrSession(t *testing.T) {
	store := playgrant.New(clock.NewFake(time.Time{}))
	item := uuid(t, "00000000-0000-0000-0000-0000000000b1")
	store.Issue("session", dto.NilUUID, item, nil)
	store.Issue("", uuid(t, "00000000-0000-0000-0000-0000000000a1"), item, nil)
	if store.Len() != 0 {
		t.Fatalf("Len = %d, want 0", store.Len())
	}
	var absent *playgrant.Store
	absent.Issue("session", uuid(t, "00000000-0000-0000-0000-0000000000a1"), item, nil)
	if _, ok := absent.Authorize("session", item, ""); ok {
		t.Fatal("a nil store must authorize nothing")
	}
}

func TestGrantExpiresWhenIdleAndUseRefreshesIt(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	store := playgrant.New(fake)
	user := uuid(t, "00000000-0000-0000-0000-0000000000a1")
	stranger := uuid(t, "00000000-0000-0000-0000-0000000000a2")
	item := uuid(t, "00000000-0000-0000-0000-0000000000b1")
	store.Issue("streamed", user, item, nil)
	store.Issue("reported", user, item, nil)
	store.Issue("idle", user, item, nil)

	fake.Advance(playgrant.IdleTimeout - time.Minute)
	if _, ok := store.Authorize("streamed", item, ""); !ok {
		t.Fatal("stream use inside the idle timeout must authorize")
	}
	store.Touch("reported", user)
	store.Touch("idle", stranger) // another user's report must not refresh it

	fake.Advance(2 * time.Minute)
	if _, ok := store.Authorize("idle", item, ""); ok {
		t.Fatal("an idle grant must expire after IdleTimeout")
	}
	for _, session := range []string{"streamed", "reported"} {
		if _, ok := store.Authorize(session, item, ""); !ok {
			t.Fatalf("refreshed grant %q expired", session)
		}
	}
}

func TestGrantStoreBoundsUsersAndTotal(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	store := playgrant.New(fake)
	item := uuid(t, "00000000-0000-0000-0000-0000000000b1")
	heavy := uuid(t, "00000000-0000-0000-0000-0000000000a1")
	light := uuid(t, "00000000-0000-0000-0000-0000000000a2")
	store.Issue("light", light, item, nil)

	for i := 0; i <= playgrant.MaxGrantsPerUser; i++ {
		fake.Advance(time.Second)
		store.Issue(fmt.Sprintf("heavy-%d", i), heavy, item, nil)
	}
	if _, ok := store.Authorize("heavy-0", item, ""); ok {
		t.Fatal("the user's least recently used grant must be evicted past MaxGrantsPerUser")
	}
	if _, ok := store.Authorize("light", item, ""); !ok {
		t.Fatal("one user's grants must not evict another user's grant")
	}
	if got := store.Len(); got != playgrant.MaxGrantsPerUser+1 {
		t.Fatalf("Len = %d, want %d", got, playgrant.MaxGrantsPerUser+1)
	}

	for i := 0; i < playgrant.MaxGrants+10; i++ {
		fake.Advance(time.Millisecond)
		store.Issue(fmt.Sprintf("crowd-%d", i), uuid(t, fmt.Sprintf("00000000-0000-0000-0001-%012x", i)), item, nil)
	}
	if got := store.Len(); got != playgrant.MaxGrants {
		t.Fatalf("Len = %d, want the MaxGrants bound %d", got, playgrant.MaxGrants)
	}
}

func TestGrantStoreIsSafeForConcurrentUse(t *testing.T) {
	store := playgrant.New(clock.NewFake(time.Time{}))
	user := uuid(t, "00000000-0000-0000-0000-0000000000a1")
	item := uuid(t, "00000000-0000-0000-0000-0000000000b1")
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 100; i++ {
				session := fmt.Sprintf("s-%d-%d", worker, i%4)
				store.Issue(session, user, item, nil)
				store.Authorize(session, item, "")
				store.Touch(session, user)
			}
		}()
	}
	wait.Wait()
}
