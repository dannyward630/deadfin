// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

func resolveSeries(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || hasAncestorKind(input, item.KindSeries, item.KindSeason) {
			return nil, nil
		}
		mixed := input.CollectionType == item.CollectionTypeUnknown
		if !collectionIs(input, item.CollectionTypeTVShows) && (!mixed || !hasChildNamed(input.Children, "tvshow.nfo") && !isMixedSeriesFolder(input, options)) {
			return nil, nil
		}
		info, err := naming.ResolveSeries(input.Entry.Path, options)
		if err != nil {
			return nil, err
		}
		resolved := initializeContext(input, item.KindSeries)
		setName(&resolved, info.Name)
		// SeriesResolver sets ProductionYear only in a tvshows library.
		if !mixed {
			resolved.ProductionYear = info.Year
		}
		resolved.IsSeries = true
		return &resolved, nil
	}
}

// isMixedSeriesFolder ports SeriesResolver.IsSeriesFolder for a null
// CollectionType. A child folder that SeasonPathParser accepts without aliases
// or numeric names makes a series. Upstream tests the series folder path, not
// the child file, with VideoResolver.IsVideoFile, so a child file counts only
// when the folder name itself has a video extension. Deadfin keeps that.
func isMixedSeriesFolder(input ResolveContext, options naming.NamingOptions) bool {
	for _, child := range input.Children {
		if child.IsDir {
			if naming.ParseSeason(child.Path, input.Entry.Path, false, false).SeasonNumber != nil {
				return true
			}
			continue
		}
		if !hasVideoExtension(input.Entry.Path, options) || !hasVideoExtension(child.Path, options) && !isStub(child.Path, options) {
			continue
		}
		named, optimistic := true, false
		episode, err := naming.ParseEpisodeWithFilters(child.Path, false, options, naming.EpisodeParseFilters{IsNamed: &named, IsOptimistic: &optimistic})
		if err == nil && episode != nil && episode.EpisodeNumber != nil {
			return true
		}
	}
	return false
}

func resolveSeason(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || input.Parent == nil || input.Parent.Kind != item.KindSeries {
			return nil, nil
		}
		// Upstream SeasonResolver passes series.ContainingFolderPath, so a
		// folder such as "Archer Season 1 (1080p)" under "Archer" parses
		// after the series name is removed.
		parentPath := ""
		if input.Parent.Path != nil {
			parentPath = *input.Parent.Path
		}
		parsed := naming.ParseSeason(input.Entry.Path, parentPath, true, true)
		if !parsed.Success {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindSeason)
		resolved.IndexNumber = parsed.SeasonNumber
		if parsed.SeasonNumber != nil {
			setName(&resolved, seasonName(*parsed.SeasonNumber, input.Options))
		}
		series := input.Parent
		resolved.SeriesID = &series.ID
		if series.Name != nil {
			name := *series.Name
			resolved.SeriesName = &name
		}
		return &resolved, nil
	}
}

// seasonName ports the name SeasonResolver gives a numbered season:
// LibraryOptions.SeasonZeroDisplayName for season 0, else the en-US
// "NameSeasonNumber" string "Season {0}".
func seasonName(number int, options LibraryOptions) string {
	if number == 0 {
		return options.SeasonZeroDisplayName
	}
	return fmt.Sprintf("Season %d", number)
}

var namedEpisodeTitle = regexp.MustCompile(`(?i)s[0-9]{1,4}e[0-9]{1,3}\s*-\s*(.+?)\s*$`)

func episodeTitle(path string) string {
	name := filepath.Base(path)
	name = name[:len(name)-len(filepath.Ext(name))]
	match := namedEpisodeTitle.FindStringSubmatch(name)
	if match == nil {
		return ""
	}
	return match[1]
}

func resolveEpisode(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if input.Entry.IsDir || !hasSeries(input) {
			return nil, nil
		}
		parsed, err := naming.ResolveEpisode(input.Entry.Path, false, options, true)
		if err != nil {
			return nil, err
		}
		if parsed == nil {
			video, resolveErr := naming.ResolveVideoWithError(input.Entry.Path, options)
			if resolveErr != nil || video == nil {
				return nil, resolveErr
			}
			parsed = &naming.EpisodeInfo{Path: video.Path, Container: video.Container}
		}
		resolved := initializeContext(input, item.KindEpisode)
		if name := episodeTitle(input.Entry.Path); name != "" {
			setName(&resolved, name)
		}
		resolved.IndexNumber = parsed.EpisodeNumber
		resolved.ParentIndexNumber = parsed.SeasonNumber
		resolved.ProductionYear = parsed.Year
		if parsed.Container != "" {
			container := parsed.Container
			resolved.Container = &container
		}
		if series := seriesParent(input); series != nil {
			resolved.SeriesID = &series.ID
			if series.Name != nil {
				seriesName := *series.Name
				resolved.SeriesName = &seriesName
			}
		} else if parsed.SeriesName != "" {
			resolved.SeriesName = &parsed.SeriesName
		}
		if input.Parent != nil && input.Parent.Kind == item.KindSeason {
			resolved.SeasonID = &input.Parent.ID
			if input.Parent.Name != nil {
				seasonName := *input.Parent.Name
				resolved.SeasonName = &seasonName
			}
		}
		return &resolved, nil
	}
}

func seriesParent(input ResolveContext) *item.Item {
	if input.Parent != nil && input.Parent.Kind == item.KindSeries {
		return input.Parent
	}
	for _, ancestor := range input.Ancestors {
		if ancestor != nil && ancestor.Kind == item.KindSeries {
			return ancestor
		}
	}
	return nil
}

func hasSeries(input ResolveContext) bool {
	return collectionIs(input, item.CollectionTypeTVShows) || seriesParent(input) != nil
}

// hasVideoExtension ports VideoResolver.IsVideoFile.
func hasVideoExtension(path string, options naming.NamingOptions) bool {
	extension := filepath.Ext(path)
	for _, candidate := range options.VideoFileExtensions {
		if strings.EqualFold(candidate, extension) {
			return true
		}
	}
	return false
}

func isStub(path string, options naming.NamingOptions) bool {
	_, ok := naming.ResolveStub(path, options)
	return ok
}
