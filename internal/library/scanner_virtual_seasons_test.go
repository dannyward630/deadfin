// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// virtualSeasonID is upstream CreateSeasonAsync's id:
// GetNewItemId(series.Id + (seasonNumber ?? -1) + seasonName, typeof(Season)).
func virtualSeasonID(series dto.UUID, number int, name string) dto.UUID {
	return library.ItemID(item.KindSeason, series.Dashed()+strconv.Itoa(number)+name, "")
}

func newTVScannerHarness(t *testing.T) *scannerHarness {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	rewriteScannerCollection(t, harness.topology, "movies", "tvshows")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 2})
	return harness
}

func scanTV(t *testing.T, harness *scannerHarness) {
	t.Helper()
	result, err := harness.scanner.Scan(t.Context())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %v, errors %v", err, result.Errors)
	}
}

// seasonsByName returns the Season children of series by name.
func seasonsByName(t *testing.T, items *store.ItemStore, series dto.UUID) map[string]item.Item {
	t.Helper()
	state, err := items.SeriesSeasons(t.Context(), series)
	if err != nil {
		t.Fatalf("read series seasons: %v", err)
	}
	out := make(map[string]item.Item, len(state.Seasons))
	for _, season := range state.Seasons {
		if season.Name == nil {
			t.Fatalf("season %s has no name", season.ID)
		}
		out[*season.Name] = season
	}
	return out
}

func requireEpisodeSeason(t *testing.T, items *store.ItemStore, path string, season item.Item) {
	t.Helper()
	episode := scannerItemAtPath(t, items, path)
	if episode.SeasonID == nil || *episode.SeasonID != season.ID {
		t.Fatalf("episode %s SeasonId = %v, want %s (%s)", path, episode.SeasonID, season.ID, *season.Name)
	}
	if season.Path == nil && (episode.SeasonName == nil || *episode.SeasonName != *season.Name) {
		t.Fatalf("episode %s SeasonName = %v, want %q", path, episode.SeasonName, *season.Name)
	}
}

// TestScanCreatesVirtualSeasonsForLooseEpisodes ports
// SeriesMetadataService.CreateSeasonsAsync: episodes outside a season folder
// get one pathless season per season number, beside a real season folder,
// with upstream's stable id and "Season N" name.
func TestScanCreatesVirtualSeasonsForLooseEpisodes(t *testing.T) {
	harness := newTVScannerHarness(t)
	show := scannerPath(t, harness.source, "Bluey (2018)")
	s1e1 := scannerPath(t, show, "Bluey (2018) - S01E01 - Magic Xylophone.mkv")
	s1e2 := scannerPath(t, show, "Bluey (2018) - S01E02 - Hospital.mkv")
	s2e1 := scannerPath(t, show, "Bluey (2018) - S02E01 - Dance Mode.mkv")
	s3e1 := scannerPath(t, show, "Season 3", "Bluey (2018) - S03E01 - Stickbird.mkv")
	for _, path := range []string{s1e1, s1e2, s2e1, s3e1} {
		writeScannerFile(t, path, "episode")
	}
	scanTV(t, harness)

	series := scannerItemAtPath(t, harness.items, show)
	seasons := seasonsByName(t, harness.items, series.ID)
	if len(seasons) != 3 {
		t.Fatalf("seasons = %v, want Season 1, Season 2, Season 3", seasons)
	}
	for number := 1; number <= 2; number++ {
		name := "Season " + strconv.Itoa(number)
		season := seasons[name]
		if season.Path != nil || season.IndexNumber == nil || *season.IndexNumber != number || !season.IsFolder {
			t.Fatalf("%s = %+v, want a pathless folder with index %d", name, season, number)
		}
		if want := virtualSeasonID(series.ID, number, name); season.ID != want {
			t.Fatalf("%s id = %s, want upstream id %s", name, season.ID, want)
		}
		if season.SeriesID == nil || *season.SeriesID != series.ID || season.SeriesName == nil || *season.SeriesName != "Bluey" {
			t.Fatalf("%s series = %v %v", name, season.SeriesID, season.SeriesName)
		}
		if season.TopParentID == nil || series.TopParentID == nil || *season.TopParentID != *series.TopParentID {
			t.Fatalf("%s top parent = %v, want %v", name, season.TopParentID, series.TopParentID)
		}
		if season.PresentationUniqueKey == nil || season.SeriesPresentationUniqueKey == nil || *season.SeriesPresentationUniqueKey != *series.PresentationUniqueKey {
			t.Fatalf("%s presentation keys = %v %v", name, season.PresentationUniqueKey, season.SeriesPresentationUniqueKey)
		}
	}
	if seasons["Season 3"].Path == nil {
		t.Fatalf("Season 3 = %+v, want the season folder", seasons["Season 3"])
	}
	requireEpisodeSeason(t, harness.items, s1e1, seasons["Season 1"])
	requireEpisodeSeason(t, harness.items, s1e2, seasons["Season 1"])
	requireEpisodeSeason(t, harness.items, s2e1, seasons["Season 2"])
	requireEpisodeSeason(t, harness.items, s3e1, seasons["Season 3"])

	// A rescan keeps the same seasons and links and writes no row.
	observer, err := storetest.NewSQLiteItemUpdateObserver(t.Context(), harness.stores)
	if err != nil {
		t.Fatalf("observe item updates: %v", err)
	}
	scanTV(t, harness)
	if writes, err := observer.Count(t.Context()); err != nil || writes != 0 {
		t.Fatalf("unchanged rescan wrote %d item rows (%v)", writes, err)
	}
	again := seasonsByName(t, harness.items, series.ID)
	for name, season := range seasons {
		if again[name].ID != season.ID {
			t.Fatalf("rescan %s id = %s, want %s", name, again[name].ID, season.ID)
		}
	}
	requireEpisodeSeason(t, harness.items, s1e1, seasons["Season 1"])
	requireEpisodeSeason(t, harness.items, s2e1, seasons["Season 2"])

	// A changed loose episode keeps its link through the rescan.
	writeScannerFile(t, s1e2, "changed episode")
	scanTV(t, harness)
	requireEpisodeSeason(t, harness.items, s1e2, seasons["Season 1"])
}

// TestScanCreatesSeasonUnknownForEpisodesWithoutSeasonNumber ports the null
// season number branch: GetValidSeasonNameForSeries names it
// NameSeasonUnknown and the id uses -1.
func TestScanCreatesSeasonUnknownForEpisodesWithoutSeasonNumber(t *testing.T) {
	harness := newTVScannerHarness(t)
	show := scannerPath(t, harness.source, "The Odyssey 1997 miniseries dvdrip x264")
	part1 := scannerPath(t, show, "The Odyssey pt1.mkv")
	part2 := scannerPath(t, show, "The Odyssey pt2.mkv")
	writeScannerFile(t, part1, "part")
	writeScannerFile(t, part2, "part")
	scanTV(t, harness)

	series := scannerItemAtPath(t, harness.items, show)
	episode := scannerItemAtPath(t, harness.items, part1)
	if episode.ParentIndexNumber != nil {
		t.Fatalf("precondition: episode ParentIndexNumber = %d, want none", *episode.ParentIndexNumber)
	}
	seasons := seasonsByName(t, harness.items, series.ID)
	unknown, found := seasons["Season Unknown"]
	if len(seasons) != 1 || !found {
		t.Fatalf("seasons = %v, want only Season Unknown", seasons)
	}
	if unknown.IndexNumber != nil || unknown.Path != nil {
		t.Fatalf("Season Unknown = %+v, want no index and no path", unknown)
	}
	if want := virtualSeasonID(series.ID, -1, "Season Unknown"); unknown.ID != want {
		t.Fatalf("Season Unknown id = %s, want upstream id %s", unknown.ID, want)
	}
	// pt1 and pt2 stack into one episode, as upstream stacks them.
	requireEpisodeSeason(t, harness.items, part1, unknown)
}

// TestScanRemovesObsoleteVirtualSeasons ports RemoveObsoleteSeasons: a
// virtual season goes when its episodes go, or when a season folder with its
// number appears. Loose episodes then link to the folder season.
func TestScanRemovesObsoleteVirtualSeasons(t *testing.T) {
	harness := newTVScannerHarness(t)
	show := scannerPath(t, harness.source, "Futurama")
	s1e1 := scannerPath(t, show, "Futurama S01E01.mkv")
	s2e1 := scannerPath(t, show, "Futurama S02E01.mkv")
	s3e1 := scannerPath(t, show, "Futurama S03E01.mkv")
	for _, path := range []string{s1e1, s2e1, s3e1} {
		writeScannerFile(t, path, "episode")
	}
	scanTV(t, harness)
	series := scannerItemAtPath(t, harness.items, show)
	if got := seasonsByName(t, harness.items, series.ID); len(got) != 3 {
		t.Fatalf("seasons = %v, want three virtual seasons", got)
	}

	if err := os.Remove(s2e1); err != nil {
		t.Fatalf("remove season 2 episode: %v", err)
	}
	s3e2 := scannerPath(t, show, "Season 3", "Futurama S03E02.mkv")
	writeScannerFile(t, s3e2, "episode")
	scanTV(t, harness)

	seasons := seasonsByName(t, harness.items, series.ID)
	if len(seasons) != 2 {
		t.Fatalf("seasons = %v, want Season 1 and the Season 3 folder", seasons)
	}
	if _, found := seasons["Season 2"]; found {
		t.Fatal("Season 2 kept without episodes")
	}
	season3 := seasons["Season 3"]
	if season3.Path == nil {
		t.Fatalf("Season 3 = %+v, want the folder season to replace the virtual one", season3)
	}
	if _, err := harness.items.Get(t.Context(), virtualSeasonID(series.ID, 3, "Season 3")); !store.IsNotFound(err) {
		t.Fatalf("virtual Season 3 lookup = %v, want not found", err)
	}
	requireEpisodeSeason(t, harness.items, s1e1, seasons["Season 1"])
	requireEpisodeSeason(t, harness.items, s3e1, season3)
	requireEpisodeSeason(t, harness.items, s3e2, season3)
}
