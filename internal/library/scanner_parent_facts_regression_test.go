// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
)

func TestScannerRegistryRefreshesNestedTVProvidersFromFrozenParentFacts(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	rewriteScannerCollection(t, harness.topology, "movies", "tvshows")

	show := scannerPath(t, harness.source, "Show")
	season := scannerPath(t, show, "Season 01")
	episode := scannerPath(t, season, "Show S01E01.mkv")
	poster := scannerPath(t, show, "season01-poster.jpg")
	writeScannerFile(t, episode, "episode")
	writeScannerFile(t, scannerPath(t, show, "tvshow.nfo"), `<tvshow><title>Scanner Show</title></tvshow>`)
	writeScannerFile(t, poster, "poster")
	writeScannerFile(t, scannerPath(t, season, "season.nfo"), `<season><title>Scanner Show: Season One</title></season>`)

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	registry := providers.NewRegistry(
		providers.NewLocalImageProvider(),
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			return os.Open(path)
		}}),
	)
	var readsMu sync.Mutex
	reads := make(map[string]int)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   registry,
		ReadDir: func(path string) ([]library.Entry, error) {
			readsMu.Lock()
			reads[path]++
			readsMu.Unlock()
			return scannerReadDir(path)
		},
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("nested TV provider scan: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("nested TV provider scan errors = %v, want none", result.Errors)
	}

	series := scannerItemAtPath(t, harness.items, show)
	if series.Kind != item.KindSeries || series.Name == nil || *series.Name != "Scanner Show" {
		t.Fatalf("persisted series = %#v, want Series titled from tvshow.nfo", series)
	}
	persistedSeason := scannerItemAtPath(t, harness.items, season)
	if persistedSeason.Kind != item.KindSeason || persistedSeason.Name == nil || *persistedSeason.Name != "Scanner Show: Season One" {
		t.Fatalf("persisted season = %#v, want Season titled from season.nfo", persistedSeason)
	}
	if !adapterHasScannerImage(persistedSeason.ImageInfos, item.ImageTypePrimary, poster) {
		t.Fatalf("persisted season images = %#v, want primary parent poster %q", persistedSeason.ImageInfos, poster)
	}
	persistedEpisode := scannerItemAtPath(t, harness.items, episode)
	if persistedEpisode.Kind != item.KindEpisode || persistedEpisode.ParentID == nil || *persistedEpisode.ParentID != persistedSeason.ID || persistedEpisode.SeriesID == nil || *persistedEpisode.SeriesID != series.ID || persistedEpisode.SeasonID == nil || *persistedEpisode.SeasonID != persistedSeason.ID {
		t.Fatalf("persisted episode hierarchy = %#v, series %#v, season %#v", persistedEpisode, series, persistedSeason)
	}

	readsMu.Lock()
	defer readsMu.Unlock()
	for _, directory := range []string{harness.source, show, season} {
		if reads[directory] != 1 {
			t.Fatalf("ReadDir(%q) calls = %d, want exactly one normal scan traversal", directory, reads[directory])
		}
	}
	if len(reads) != 3 {
		t.Fatalf("ReadDir paths = %#v, want only source, series, and season normal traversals", reads)
	}
}
