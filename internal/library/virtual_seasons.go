// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// seriesSeasonTask names one scanned Series for the virtual-season pass.
type seriesSeasonTask struct {
	series  item.Item
	options LibraryOptions
}

// reconcileVirtualSeasons runs the virtual-season pass for each scanned
// series, after the scan has stored the series subtree. Upstream runs
// SeriesMetadataService.AfterMetadataRefresh for each refreshed series.
func (s *Scanner) reconcileVirtualSeasons(ctx context.Context, tasks []seriesSeasonTask) []RefreshProgress {
	var failed []RefreshProgress
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return failed
		}
		if err := s.reconcileSeriesSeasons(ctx, task); err != nil {
			path := ""
			if task.series.Path != nil {
				path = *task.series.Path
			}
			failed = append(failed, RefreshProgress{ItemID: task.series.ID, Path: path, Status: "failed", Error: fmt.Errorf("virtual seasons: %w", err)})
		}
	}
	return failed
}

func (s *Scanner) reconcileSeriesSeasons(ctx context.Context, task seriesSeasonTask) error {
	state, err := s.items.SeriesSeasons(ctx, task.series.ID)
	if err != nil {
		return err
	}
	plan := virtualSeasonPlan(task.series, state, task.options, scannerLifecycleTimestamp(s.options.Now))
	if plan.Empty() {
		return nil
	}
	for _, season := range plan.Create {
		s.logger.Info("creating virtual season", "series", task.series.ID.String(), "season", *season.Name)
	}
	for _, id := range plan.Delete {
		s.logger.Info("removing virtual season", "series", task.series.ID.String(), "season", id.String())
	}
	return s.storeWrite(ctx, func() error { return s.items.ApplySeriesSeasons(ctx, task.series.ID, plan) })
}

// virtualSeasonPlan ports SeriesMetadataService.RemoveObsoleteSeasons and
// CreateSeasonsAsync for one series. A season without a path is virtual
// (BaseItem.LocationType is Virtual for an empty path).
//
// Deadfin derives the season of an episode in a season folder from that
// folder, so only episodes outside a season folder take part. Upstream also
// considers episodes in a season folder whose ParentIndexNumber differs from
// the folder (divergence D-51).
func virtualSeasonPlan(series item.Item, state store.SeriesSeasons, options LibraryOptions, now time.Time) store.SeriesSeasonPlan {
	var plan store.SeriesSeasonPlan
	loose := make([]store.SeriesSeasonEpisode, 0, len(state.Episodes))
	for _, episode := range state.Episodes {
		if !episode.InSeasonFolder {
			loose = append(loose, episode)
		}
	}
	sort.Slice(loose, func(left, right int) bool { return loose[left].ID.String() < loose[right].ID.String() })

	// The first season with a number wins, as FirstOrDefault does. Physical
	// seasons come first so the choice does not depend on row order.
	seasons := append([]item.Item(nil), state.Seasons...)
	sort.SliceStable(seasons, func(left, right int) bool {
		if (seasons[left].Path == nil) != (seasons[right].Path == nil) {
			return seasons[left].Path != nil
		}
		return seasons[left].ID.String() < seasons[right].ID.String()
	})

	// RemoveObsoleteSeasons: delete a virtual season when a physical season
	// has its number or when it has no episodes.
	physicalNumbers := make(map[int]struct{})
	physicalIDs := make(map[dto.UUID]struct{})
	for _, season := range seasons {
		if season.Path == nil {
			continue
		}
		physicalIDs[season.ID] = struct{}{}
		if season.IndexNumber != nil {
			physicalNumbers[*season.IndexNumber] = struct{}{}
		}
	}
	kept := seasons[:0:0]
	for _, season := range seasons {
		if season.Path == nil {
			shadowed := false
			if season.IndexNumber != nil {
				_, shadowed = physicalNumbers[*season.IndexNumber]
			}
			if shadowed || !hasSeasonEpisode(loose, season.IndexNumber) {
				plan.Delete = append(plan.Delete, season.ID)
				continue
			}
		}
		kept = append(kept, season)
	}
	seasons = kept

	// CreateSeasonsAsync: one season for each distinct season number of the
	// episodes that need one. A negative number counts as unknown.
	var numbers []*int
	seen := make(map[string]struct{})
	for _, episode := range loose {
		if !needsVirtualSeason(episode, physicalIDs) {
			continue
		}
		number := episode.ParentIndexNumber
		if number != nil && *number < 0 {
			number = nil
		}
		key := "unknown"
		if number != nil {
			key = strconv.Itoa(*number)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		numbers = append(numbers, number)
	}
	sort.SliceStable(numbers, func(left, right int) bool {
		if numbers[left] == nil || numbers[right] == nil {
			return numbers[right] == nil && numbers[left] != nil
		}
		return *numbers[left] < *numbers[right]
	})
	for _, number := range numbers {
		if findSeason(seasons, number) != nil {
			continue
		}
		season := virtualSeason(series, number, options, now)
		plan.Create = append(plan.Create, season)
		seasons = append(seasons, season)
	}

	// Link each episode to the season with its number.
	for _, episode := range loose {
		season := findSeason(seasons, episode.ParentIndexNumber)
		if season == nil || episode.SeasonID != nil && *episode.SeasonID == season.ID {
			continue
		}
		var name *string
		if season.Name != nil {
			value := *season.Name
			name = &value
		}
		plan.Assign = append(plan.Assign, store.SeriesSeasonAssignment{EpisodeID: episode.ID, SeasonID: season.ID, SeasonName: name})
	}
	return plan
}

// needsVirtualSeason ports SeriesMetadataService.NeedsVirtualSeason for an
// episode outside a season folder. Such an episode is never in a physical
// season path.
func needsVirtualSeason(episode store.SeriesSeasonEpisode, physicalIDs map[dto.UUID]struct{}) bool {
	if episode.ParentIndexNumber != nil {
		return true
	}
	if episode.SeasonID != nil {
		_, physical := physicalIDs[*episode.SeasonID]
		return !physical
	}
	return true
}

// hasSeasonEpisode reports an episode outside a season folder for a season
// number, as Season.GetEpisodes filters a series' episodes by
// ParentIndexNumber. A null number matches episodes with no number.
func hasSeasonEpisode(episodes []store.SeriesSeasonEpisode, number *int) bool {
	for _, episode := range episodes {
		if sameSeasonNumber(episode.ParentIndexNumber, number) {
			return true
		}
	}
	return false
}

// findSeason returns the first season whose IndexNumber equals number. Two
// null numbers are equal, as for C# int? equality.
func findSeason(seasons []item.Item, number *int) *item.Item {
	for index := range seasons {
		if sameSeasonNumber(seasons[index].IndexNumber, number) {
			return &seasons[index]
		}
	}
	return nil
}

func sameSeasonNumber(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// virtualSeason ports SeriesMetadataService.CreateSeasonAsync. The name is
// GetValidSeasonNameForSeries: "Season Unknown" for no number,
// SeasonZeroDisplayName for 0, else "Season {0}". The id is
// GetNewItemId(series.Id + (seasonNumber ?? -1) + seasonName, typeof(Season)),
// so it is the same on every scan.
func virtualSeason(series item.Item, number *int, options LibraryOptions, now time.Time) item.Item {
	name := "Season Unknown"
	if number != nil {
		name = seasonName(*number, options)
	}
	keyNumber := -1
	if number != nil {
		keyNumber = *number
	}
	seriesID := series.ID
	season := item.Item{
		ID:            itemID(item.KindSeason, seriesID.Dashed()+strconv.Itoa(keyNumber)+name, "", false),
		Kind:          item.KindSeason,
		ParentID:      &seriesID,
		SeriesID:      &seriesID,
		IsFolder:      true,
		Name:          &name,
		DateCreated:   &now,
		DateLastSaved: &now,
	}
	if number != nil {
		value := *number
		season.IndexNumber = &value
	}
	if series.Name != nil {
		seriesName := *series.Name
		season.SeriesName = &seriesName
	}
	materializeScannedSortName(&season, item.DefaultSortOptions())
	values := []item.Item{season}
	materializePresentationKeys(values, series, nil)
	return values[0]
}

// carryVirtualSeasonLinks keeps the season link that the virtual-season
// pass gave an episode outside a season folder. The resolver sets SeasonID
// only from a Season parent, so a rescan would otherwise clear it.
func carryVirtualSeasonLinks(values []item.Item, existing map[string]item.Item) {
	for index := range values {
		value := &values[index]
		if value.Kind != item.KindEpisode || value.SeasonID != nil || value.Path == nil {
			continue
		}
		previous, found := existing[normalizedEntryPath(*value.Path)]
		if !found || previous.Kind != item.KindEpisode || previous.SeasonID == nil {
			continue
		}
		seasonID := *previous.SeasonID
		value.SeasonID = &seasonID
		if value.SeasonName == nil && previous.SeasonName != nil {
			name := *previous.SeasonName
			value.SeasonName = &name
		}
	}
}
