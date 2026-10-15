// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryPeopleParitySQLite protects the public ItemQuery people
// predicates against SQLite. The PostgreSQL twin exercises the same persisted
// people relations when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreQueryPeopleParitySQLite(t *testing.T) {
	runItemStoreQueryPeopleParity(t, openItemStore)
}

func TestItemStoreQueryPeopleParityPostgres(t *testing.T) {
	runItemStoreQueryPeopleParity(t, openPostgresItemStore)
}

func runItemStoreQueryPeopleParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newPeopleQueryFixture(t, open(t))

	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
	}{
		{
			name:  "person ID and name predicates can match different credits",
			query: store.ItemQuery{PersonIDs: []dto.UUID{fixture.personAID}, Person: "Johnny"},
			want:  []dto.UUID{fixture.bothCredits.ID},
		},
		{
			name:  "person name is exact after clean-name normalization rather than a prefix",
			query: store.ItemQuery{Person: "John"},
			want:  []dto.UUID{fixture.bothCredits.ID},
		},
		{
			name:  "person name trims then cleans case diacritics and punctuation",
			query: store.ItemQuery{Person: "  JÖHN!\t"},
			want:  []dto.UUID{fixture.bothCredits.ID},
		},
		{
			name:  "person types alone leave the explicitly scoped set unchanged",
			query: store.ItemQuery{ItemIDs: fixture.allIDs(), PersonTypes: []string{"Writer"}},
			want:  fixture.allIDs(),
		},
		{
			name:  "person types do not narrow a person ID match",
			query: store.ItemQuery{PersonIDs: []dto.UUID{fixture.personAID}, PersonTypes: []string{"Writer"}},
			want:  []dto.UUID{fixture.bothCredits.ID},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture.assertQuery(t, tc.query, tc.want)
		})
	}
}

type peopleQueryFixture struct {
	items       *store.ItemStore
	bothCredits item.Item
	johnnyOnly  item.Item
	unrelated   item.Item
	personAID   dto.UUID
}

func newPeopleQueryFixture(t *testing.T, items *store.ItemStore) peopleQueryFixture {
	t.Helper()
	personAID := peopleQueryUUID(t, 301)
	personBID := peopleQueryUUID(t, 302)
	leadRole := "Lead"
	directorRole := "Director"

	bothCredits := peopleQueryItem(t, 1, "a both credits")
	bothCredits.People = []item.PersonCredit{
		{PersonID: personAID, Name: "Jöhn", Role: &leadRole, Type: item.PersonKindActor},
		{PersonID: personBID, Name: "Johnny", Role: &directorRole, Type: item.PersonKindDirector},
	}
	johnnyOnly := peopleQueryItem(t, 2, "b johnny only")
	johnnyOnly.People = []item.PersonCredit{{PersonID: personBID, Name: "Johnny", Role: &directorRole, Type: item.PersonKindDirector}}
	unrelated := peopleQueryItem(t, 3, "c unrelated")

	for _, it := range []item.Item{bothCredits, johnnyOnly, unrelated} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting people-query fixture item %s: %v", it.ID, err)
		}
	}
	return peopleQueryFixture{items: items, bothCredits: bothCredits, johnnyOnly: johnnyOnly, unrelated: unrelated, personAID: personAID}
}

func (f peopleQueryFixture) allIDs() []dto.UUID {
	return []dto.UUID{f.bothCredits.ID, f.johnnyOnly.ID, f.unrelated.ID}
}

func (f peopleQueryFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID) {
	t.Helper()
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	query.EnableTotalRecordCount = true
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying people fixture: %v", err)
	}
	if ids := peopleQueryIDs(got.Items); !reflect.DeepEqual(ids, want) {
		t.Fatalf("item IDs = %v, want %v", ids, want)
	}
	if got.TotalRecordCount != len(want) {
		t.Fatalf("total record count = %d, want %d", got.TotalRecordCount, len(want))
	}
}

func peopleQueryItem(t *testing.T, value int, sortName string) item.Item {
	t.Helper()
	return item.Item{
		ID:       peopleQueryUUID(t, value),
		Kind:     item.KindMovie,
		Name:     peopleQueryString(sortName),
		SortName: peopleQueryString(sortName),
	}
}

func peopleQueryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing people-query UUID %d: %v", value, err)
	}
	return id
}
func peopleQueryString(value string) *string { return &value }

func peopleQueryIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
