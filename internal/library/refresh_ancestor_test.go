// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// TestNewRefreshAncestorCopiesIdentity proves the snapshot shares no state
// with the scanner item: later changes to the item do not reach a provider.
func TestNewRefreshAncestorCopiesIdentity(t *testing.T) {
	name, index, order := "Breaking Bad", 1, "absolute"
	series := item.Item{Kind: item.KindSeries, Name: &name, IndexNumber: &index}
	series.ProviderIDs.Set("Tmdb", "1396")
	series.Extra.Series = &item.SeriesExtra{DisplayOrder: &order}

	snapshot := library.NewRefreshAncestor(&series)

	name, index, order = "changed", 9, "dvd"
	series.ProviderIDs.Set("Tmdb", "1")
	series.ProviderIDs.Set("Tvdb", "2")

	if snapshot.Kind != item.KindSeries || snapshot.Name != "Breaking Bad" || snapshot.DisplayOrder != "absolute" {
		t.Errorf("snapshot = %+v, want the original identity", snapshot)
	}
	if snapshot.IndexNumber == nil || *snapshot.IndexNumber != 1 {
		t.Errorf("IndexNumber = %v, want 1", snapshot.IndexNumber)
	}
	if id, _ := snapshot.ProviderIDs.Get("Tmdb"); id != "1396" {
		t.Errorf("Tmdb = %q, want 1396", id)
	}
	if _, ok := snapshot.ProviderIDs.Get("Tvdb"); ok {
		t.Error("snapshot gained a provider id set after the copy")
	}
}

func TestRefreshContextNearestAncestor(t *testing.T) {
	season := library.RefreshAncestor{Kind: item.KindSeason}
	near := library.RefreshAncestor{Kind: item.KindSeries, Name: "near"}
	far := library.RefreshAncestor{Kind: item.KindSeries, Name: "far"}
	refresh := library.RefreshContext{Ancestors: []library.RefreshAncestor{season, near, far}}

	if got, ok := refresh.NearestAncestor(item.KindSeries); !ok || got.Name != "near" {
		t.Errorf("NearestAncestor(Series) = (%+v, %v), want near", got, ok)
	}
	if _, ok := refresh.NearestAncestor(item.KindMovie); ok {
		t.Error("NearestAncestor(Movie) found an ancestor that is not there")
	}
}
