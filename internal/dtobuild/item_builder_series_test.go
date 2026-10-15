// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderProjectsSeriesAndSeasonNames defends episode context resolution:
// callers receive the display series and season names, not only opaque IDs.
func TestBuilderProjectsSeriesAndSeasonNames(t *testing.T) {
	items := openBuilderItemStore(t)
	id := builderUUID(t, 30)
	name := "The Episode"
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindEpisode, Name: &name}); err != nil {
		t.Fatalf("upsert episode fixture: %v", err)
	}
	loaded, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load episode fixture: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, seriesNameEnricher{values: map[dto.UUID]dtobuild.Enrichment{id: {SeriesName: "Orbit", SeasonName: "Season 2"}}}).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build episode DTO: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])
	if got, want := string(wire["SeriesName"]), `"Orbit"`; got != want {
		t.Errorf("SeriesName = %s, want %s", got, want)
	}
	if got, want := string(wire["SeasonName"]), `"Season 2"`; got != want {
		t.Errorf("SeasonName = %s, want %s", got, want)
	}
}

// TestBuilderProjectsSeasonSeriesID protects the Season DTO used by
// Swiftfin's series selector. Jellyfin emits SeriesId without requiring a
// Fields projection, and Swiftfin subsequently uses the returned season graph
// to request episodes.
func TestBuilderProjectsSeasonSeriesID(t *testing.T) {
	seriesID, seasonID := builderUUID(t, 31), builderUUID(t, 32)
	name := "Season 2"
	result, err := dtobuild.NewBuilder(nil, seriesNameEnricher{values: map[dto.UUID]dtobuild.Enrichment{}}).Build(
		t.Context(),
		[]item.Item{{ID: seasonID, Kind: item.KindSeason, IsFolder: true, SeriesID: &seriesID, Name: &name}},
		store.ItemQueryDTOOptions{},
		nil,
	)
	if err != nil {
		t.Fatalf("build season DTO: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])
	if got, want := string(wire["SeriesId"]), `"`+seriesID.String()+`"`; got != want {
		t.Errorf("season SeriesId = %s, want %s", got, want)
	}
}

type seriesNameEnricher struct {
	values map[dto.UUID]dtobuild.Enrichment
}

func (e seriesNameEnricher) Enrich(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	return e.values, nil
}
