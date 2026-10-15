// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreReconcileScannedPlanPeopleSQLite protects the scanner-plan
// transaction on the production SQLite backend.
func TestItemStoreReconcileScannedPlanPeopleSQLite(t *testing.T) {
	runItemStoreReconcileScannedPlanPeople(t, openReviewerItemStore, installSQLitePersonCreditWriteFailure)
}

// TestItemStoreReconcileScannedPlanPeoplePostgres protects the same atomic
// contract on PostgreSQL when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreReconcileScannedPlanPeoplePostgres(t *testing.T) {
	runItemStoreReconcileScannedPlanPeople(t, openPostgresReviewerItemStore, installPostgresPersonCreditWriteFailure)
}

func runItemStoreReconcileScannedPlanPeople(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores), failCreditWrite func(*testing.T, store.Stores)) {
	t.Helper()

	t.Run("commits virtual Person and all case-folded credits together", func(t *testing.T) {
		items, _ := open(t)
		parent, movie, person := scannedPlanPeopleFixture(t)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert scanned-plan parent: %v", err)
		}

		result, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{person})
		if err != nil {
			t.Fatalf("reconcile scanned plan with person entity: %v", err)
		}
		if !sameUUIDSet(result.Inserted, []dto.UUID{movie.ID}) || len(result.Updated) != 0 || len(result.Deleted) != 0 {
			t.Fatalf("reconcile scanned plan result = %#v, want only movie insertion", result)
		}

		storedMovie, err := items.Get(t.Context(), movie.ID)
		if err != nil {
			t.Fatalf("get committed scanned movie: %v", err)
		}
		assertScannedPlanPeople(t, storedMovie.People, person)
		storedPerson, err := items.Get(t.Context(), person.ID)
		if err != nil {
			t.Fatalf("get committed Person entity: %v", err)
		}
		if storedPerson.Kind != item.KindPerson || storedPerson.Name == nil || *storedPerson.Name != "Alice" {
			t.Fatalf("committed Person entity = %#v, want Person Alice", storedPerson)
		}
	})

	t.Run("rolls back Person materialization when credit write fails", func(t *testing.T) {
		items, stores := open(t)
		parent, movie, person := scannedPlanPeopleFixture(t)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert failed-plan parent: %v", err)
		}
		failCreditWrite(t, stores)

		if _, err := items.ReconcileScannedPlan(t.Context(), parent.ID, []item.Item{movie}, nil, []item.Item{person}); err == nil {
			t.Fatal("reconcile scanned plan succeeded after people-map write failure")
		}
		if _, err := items.Get(t.Context(), movie.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("failed scanned plan retained movie: %v, want not found", err)
		}
		credits, err := items.People(t.Context(), movie.ID)
		if err != nil {
			t.Fatalf("read credits after failed scanned plan: %v", err)
		}
		if len(credits) != 0 {
			t.Fatalf("failed scanned plan retained orphan credits = %#v, want none", credits)
		}
		if _, err := items.Get(t.Context(), person.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("failed scanned plan retained unreferenced Person entity: %v, want not found", err)
		}
	})
}

func scannedPlanPeopleFixture(t *testing.T) (item.Item, item.Item, item.Item) {
	t.Helper()
	parent := hierarchyItem(t, 14600, item.KindFolder, nil)
	person := library.PersonEntity("Alice")
	movie := reconciliationChild(t, 14601, item.KindMovie, parent.ID, "case-folded Person movie")
	movie.People = []item.PersonCredit{
		{PersonID: person.ID, Name: "Alice", Type: item.PersonKindActor},
		{PersonID: person.ID, Name: "Alice", Type: item.PersonKindDirector},
	}
	return parent, movie, person
}

func assertScannedPlanPeople(t *testing.T, credits []item.PersonCredit, person item.Item) {
	t.Helper()
	byKind := make(map[item.PersonKind]item.PersonCredit, len(credits))
	for _, credit := range credits {
		byKind[credit.Type] = credit
	}
	if len(byKind) != 2 || byKind[item.PersonKindActor].PersonID != person.ID || byKind[item.PersonKindDirector].PersonID != person.ID || byKind[item.PersonKindActor].Name != "Alice" || byKind[item.PersonKindDirector].Name != "Alice" {
		t.Fatalf("committed movie credits = %#v, want Actor and Director credits for Person %s named Alice", credits, person.ID)
	}
}

func installSQLitePersonCreditWriteFailure(t *testing.T, stores store.Stores) {
	t.Helper()
	if _, err := stores.Writer().ExecContext(t.Context(), `
		CREATE TRIGGER fail_scanned_plan_person_credit
		BEFORE INSERT ON people_map
		BEGIN
			SELECT RAISE(ABORT, 'injected people-map write failure');
		END`); err != nil {
		t.Fatalf("install SQLite people-map failure trigger: %v", err)
	}
}

func installPostgresPersonCreditWriteFailure(t *testing.T, stores store.Stores) {
	t.Helper()
	if _, err := stores.Writer().ExecContext(t.Context(), `
		CREATE FUNCTION fail_scanned_plan_person_credit() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected people-map write failure';
		END;
		$$;
		CREATE TRIGGER fail_scanned_plan_person_credit
		BEFORE INSERT ON people_map
		FOR EACH ROW EXECUTE FUNCTION fail_scanned_plan_person_credit()`); err != nil {
		t.Fatalf("install PostgreSQL people-map failure trigger: %v", err)
	}
}
