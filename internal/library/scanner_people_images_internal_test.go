// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestNFOPeopleCarriesProviderPersonImage covers LibraryManager.UpdatePeopleAsync:
// a credit's provider image becomes its Person entity's Primary image, the
// first credit with an image wins, and the credit no longer carries the URL.
func TestNFOPeopleCarriesProviderPersonImage(t *testing.T) {
	const first, second = "https://image.tmdb.org/t/p/original/a.jpg", "https://image.tmdb.org/t/p/original/b.jpg"
	alice, bob := PersonEntity("Alice"), PersonEntity("Bob")
	values := []item.Item{
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice"}, {PersonID: bob.ID, Name: "Bob"}}},
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ImageURL: first}}},
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ImageURL: second}}},
	}
	people := nfoPeople(values)
	clearCreditImageURLs(values)

	byID := make(map[string]item.Item, len(people))
	for _, person := range people {
		byID[person.ID.String()] = person
	}
	if got := byID[alice.ID.String()].ImageInfos; len(got) != 1 || got[0].Type != item.ImageTypePrimary || got[0].Path != first {
		t.Fatalf("Alice images = %#v, want the first credited Primary image", got)
	}
	if got := byID[bob.ID.String()].ImageInfos; len(got) != 0 {
		t.Fatalf("Bob images = %#v, want none", got)
	}
	for _, value := range values {
		for _, credit := range value.People {
			if credit.ImageURL != "" {
				t.Fatalf("credit %q kept ImageURL %q after the scan moved it", credit.Name, credit.ImageURL)
			}
		}
	}
}
