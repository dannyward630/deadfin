// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func planUUID(value byte) dto.UUID { return dto.UUID{15: value} }

// TestVirtualSeasonPlanFollowsUpstreamRules covers the CreateSeasonsAsync
// branches that the scanner fixtures do not reach.
func TestVirtualSeasonPlanFollowsUpstreamRules(t *testing.T) {
	seriesName := "Show"
	series := item.Item{ID: planUUID(1), Kind: item.KindSeries, Name: &seriesName}
	zero, minus, two := 0, -1, 2
	physicalPath := "/tv/Show/Season 2"
	physical := item.Item{ID: planUUID(2), Kind: item.KindSeason, ParentID: &series.ID, Path: &physicalPath, IndexNumber: &two}
	physicalID := physical.ID
	state := store.SeriesSeasons{
		Seasons: []item.Item{physical},
		Episodes: []store.SeriesSeasonEpisode{
			{ID: planUUID(10), ParentIndexNumber: &zero},
			{ID: planUUID(11), ParentIndexNumber: &minus},
			{ID: planUUID(12), SeasonID: &physicalID},
			{ID: planUUID(13), ParentIndexNumber: &two},
			{ID: planUUID(14), ParentIndexNumber: &two, InSeasonFolder: true, SeasonID: &physicalID},
		},
	}
	plan := virtualSeasonPlan(series, state, LibraryOptions{SeasonZeroDisplayName: "Specials"}, time.Unix(0, 0).UTC())

	names := make(map[string]item.Item)
	for _, season := range plan.Create {
		names[*season.Name] = season
	}
	if len(plan.Create) != 2 || names["Specials"].IndexNumber == nil || *names["Specials"].IndexNumber != 0 {
		t.Fatalf("created = %+v, want Specials (0) and Season Unknown", plan.Create)
	}
	// A negative season number makes the unknown season.
	unknown, found := names["Season Unknown"]
	if !found || unknown.IndexNumber != nil {
		t.Fatalf("created = %+v, want Season Unknown for the negative number", plan.Create)
	}
	links := make(map[dto.UUID]dto.UUID)
	for _, assignment := range plan.Assign {
		links[assignment.EpisodeID] = assignment.SeasonID
	}
	want := map[dto.UUID]dto.UUID{
		planUUID(10): names["Specials"].ID,
		// An episode without a number linked to a physical season needs no
		// season of its own, but the unknown season still takes it, as the
		// episode loop matches null to null.
		planUUID(12): unknown.ID,
		// A loose episode joins the season folder with its number.
		planUUID(13): physical.ID,
	}
	// Upstream matches -1 to no season, so that episode keeps no link.
	if len(links) != len(want) {
		t.Fatalf("links = %v, want %v", links, want)
	}
	for episode, season := range want {
		if links[episode] != season {
			t.Fatalf("episode %s season = %s, want %s", episode, links[episode], season)
		}
	}
}
