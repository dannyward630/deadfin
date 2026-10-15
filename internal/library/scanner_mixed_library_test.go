// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// newMixedScannerHarness gives the fixture library upstream's null
// CollectionType: the folder holds no .collection marker.
func newMixedScannerHarness(t *testing.T) *scannerHarness {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("list virtual folders: %#v, %v", folders, err)
	}
	if err := os.Remove(filepath.Join(folders[0].Path, "movies.collection")); err != nil {
		t.Fatalf("remove movies collection marker: %v", err)
	}
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 2})
	folders, err = harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 || folders[0].CollectionType != "" {
		t.Fatalf("mixed virtual folders = %#v, %v; want one folder with no collection type", folders, err)
	}
	return harness
}

func requireKindName(t *testing.T, value item.Item, kind item.Kind, name string) {
	t.Helper()
	if value.Kind != kind || value.Name == nil || *value.Name != name {
		got := "<nil>"
		if value.Name != nil {
			got = *value.Name
		}
		t.Fatalf("%v = %s %q, want %s %q", value.Path, value.Kind, got, kind, name)
	}
}

// TestScanMixedLibraryResolvesMoviesAndSeries ports the resolvers' null
// CollectionType branches. SeriesResolver makes a series of a folder with
// tvshow.nfo or a season folder; MovieResolver makes a movie of a self-contained
// movie folder and of a loose video file; the folder holds no other type.
func TestScanMixedLibraryResolvesMoviesAndSeries(t *testing.T) {
	harness := newMixedScannerHarness(t)
	movieDir := scannerPath(t, harness.source, "Apollo 13 (1995)")
	movieFile := scannerPath(t, movieDir, "Apollo.13.1995.1080p.WEB-DL.mkv")
	looseMovie := scannerPath(t, harness.source, "Heat (1995).mkv")
	show := scannerPath(t, harness.source, "Bluey (2018)")
	season := scannerPath(t, show, "Season 1")
	episode := scannerPath(t, season, "Bluey S01E01.mkv")
	nfoShow := scannerPath(t, harness.source, "Frieren")
	nfoEpisode := scannerPath(t, nfoShow, "Frieren S01E02.mkv")
	// SeriesResolver.IsSeriesFolder tests the folder path, not the child, with
	// VideoResolver.IsVideoFile; loose episode files alone never make a mixed
	// library series. The folder resolves as a movie folder instead.
	flatShow := scannerPath(t, harness.source, "Flat Show")
	flatEpisode := scannerPath(t, flatShow, "Flat Show S01E01.mkv")
	for _, path := range []string{movieFile, looseMovie, episode, nfoEpisode, flatEpisode} {
		writeScannerFile(t, path, "video")
	}
	writeScannerFile(t, scannerPath(t, nfoShow, "tvshow.nfo"), "<tvshow><title>Frieren</title></tvshow>")
	scanTV(t, harness)

	// FindMovie returns the video as the folder's item: no Folder remains.
	movie := scannerItemAtPath(t, harness.items, movieFile)
	requireKindName(t, movie, item.KindMovie, "Apollo 13 (1995)")
	if movie.IsInMixedFolder {
		t.Fatalf("folder movie IsInMixedFolder = true, want false")
	}
	assertScannerNoItemAtPath(t, harness.items, movieDir)
	loose := scannerItemAtPath(t, harness.items, looseMovie)
	requireKindName(t, loose, item.KindMovie, "Heat")
	if loose.ProductionYear == nil || *loose.ProductionYear != 1995 {
		t.Fatalf("loose movie year = %v, want 1995", loose.ProductionYear)
	}

	series := scannerItemAtPath(t, harness.items, show)
	requireKindName(t, series, item.KindSeries, "Bluey")
	seasonItem := scannerItemAtPath(t, harness.items, season)
	requireKindName(t, seasonItem, item.KindSeason, "Season 1")
	episodeItem := scannerItemAtPath(t, harness.items, episode)
	if episodeItem.Kind != item.KindEpisode || episodeItem.SeriesID == nil || *episodeItem.SeriesID != series.ID {
		t.Fatalf("episode = %s series %v, want Episode of %s", episodeItem.Kind, episodeItem.SeriesID, series.ID)
	}
	requireEpisodeSeason(t, harness.items, episode, seasonItem)

	nfoSeries := scannerItemAtPath(t, harness.items, nfoShow)
	if nfoSeries.Kind != item.KindSeries {
		t.Fatalf("tvshow.nfo folder = %s, want Series", nfoSeries.Kind)
	}
	nfoEpisodeItem := scannerItemAtPath(t, harness.items, nfoEpisode)
	if nfoEpisodeItem.Kind != item.KindEpisode || nfoEpisodeItem.SeriesID == nil || *nfoEpisodeItem.SeriesID != nfoSeries.ID {
		t.Fatalf("tvshow.nfo episode = %s series %v, want Episode of %s", nfoEpisodeItem.Kind, nfoEpisodeItem.SeriesID, nfoSeries.ID)
	}
	// The loose episode gets a virtual Season 1.
	virtual := seasonsByName(t, harness.items, nfoSeries.ID)["Season 1"]
	if virtual.Path != nil || virtual.Kind != item.KindSeason {
		t.Fatalf("virtual season = %+v, want a pathless Season 1", virtual)
	}
	requireEpisodeSeason(t, harness.items, nfoEpisode, virtual)

	requireKindName(t, scannerItemAtPath(t, harness.items, flatEpisode), item.KindMovie, "Flat Show")

	// The collection folder renders upstream's null CollectionType and the
	// library keeps its stored mixed type.
	root := scannerItemAtPath(t, harness.items, mixedLibraryPath(t, harness))
	if root.Kind != item.KindCollectionFolder || root.Extra.CollectionFolder == nil || root.Extra.CollectionFolder.CollectionType == nil || *root.Extra.CollectionFolder.CollectionType != item.CollectionTypeUnknown {
		t.Fatalf("collection folder = %s %+v, want CollectionFolder with the unknown type", root.Kind, root.Extra.CollectionFolder)
	}

	// A rescan keeps every item and writes no row.
	before := scannerItems(t, harness.items)
	observer, err := storetest.NewSQLiteItemUpdateObserver(t.Context(), harness.stores)
	if err != nil {
		t.Fatalf("observe item updates: %v", err)
	}
	scanTV(t, harness)
	if writes, err := observer.Count(t.Context()); err != nil || writes != 0 {
		t.Fatalf("unchanged rescan wrote %d item rows (%v)", writes, err)
	}
	after := scannerItems(t, harness.items)
	if len(after) != len(before) {
		t.Fatalf("rescan items = %d, want %d", len(after), len(before))
	}
	ids := make(map[string]item.Kind, len(before))
	for _, value := range before {
		ids[value.ID.String()] = value.Kind
	}
	for _, value := range after {
		if kind, ok := ids[value.ID.String()]; !ok || kind != value.Kind {
			t.Fatalf("rescan item %s %v is new or changed kind", value.ID, value.Path)
		}
	}
}

func mixedLibraryPath(t *testing.T, harness *scannerHarness) string {
	t.Helper()
	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("list virtual folders: %#v, %v", folders, err)
	}
	return folders[0].Path
}

// TestScanMixedMarkerLibraryIsMixed proves that a mixed.collection marker, as
// upstream AddVirtualFolder writes for CollectionTypeOptions.mixed, scans as a
// null CollectionType library.
func TestScanMixedMarkerLibraryIsMixed(t *testing.T) {
	harness := newMixedScannerHarness(t)
	if err := os.WriteFile(filepath.Join(mixedLibraryPath(t, harness), "mixed.collection"), nil, 0o600); err != nil {
		t.Fatalf("write mixed collection marker: %v", err)
	}
	movie := scannerPath(t, harness.source, "Heat (1995).mkv")
	writeScannerFile(t, movie, "video")
	scanTV(t, harness)
	requireKindName(t, scannerItemAtPath(t, harness.items, movie), item.KindMovie, "Heat")
}
