// SPDX-License-Identifier: GPL-2.0-only

package tmdb_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestProviderMatchesSceneReleaseNameFromCassette proves the end-to-end effect
// of the search-query cleaning (D-13): a movie whose parsed display
// Name still carries a leading "- " (left by Emby.Naming after it strips a
// "[ www.site.com ]" prefix) now resolves against TMDb. Before the fix the
// query was " The Emperors New Groove" (leading space, from the bare CleanName
// collapse); the cassette's search interaction is keyed on the clean
// "The Emperors New Groove" the fix produces, so a match only occurs when the
// query is cleaned. No live API is used (recorded cassette).
func TestProviderMatchesSceneReleaseNameFromCassette(t *testing.T) {
	provider := newCassetteProvider(t, "testdata/emperors-new-groove.json")
	target := item.Item{
		Kind:           item.KindMovie,
		Name:           stringPtr("- The Emperors New Groove"),
		ProductionYear: intPtr(2000),
	}

	handled, err := provider.Refresh(t.Context(), movieRefreshContext(), &target)
	if err != nil {
		t.Fatalf("refresh scene-release movie: %v", err)
	}
	if !handled {
		t.Fatal("provider did not match the scene-release name; search query was not cleaned")
	}

	// The display Name is replaced by TMDb's canonical title on a match, and the
	// resolved identity is threaded through.
	if target.Name == nil || *target.Name != "The Emperor's New Groove" {
		t.Errorf("Name = %v, want The Emperor's New Groove", target.Name)
	}
	if id, ok := target.ProviderIDs.Get("Tmdb"); !ok || id != "11688" {
		t.Errorf("Tmdb id = %q, %t; want 11688", id, ok)
	}
	if target.ProductionYear == nil || *target.ProductionYear != 2000 {
		t.Errorf("ProductionYear = %v, want 2000", target.ProductionYear)
	}
}
