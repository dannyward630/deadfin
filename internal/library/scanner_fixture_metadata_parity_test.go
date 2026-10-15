// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// TestScannerFixtureMetadataUsesPinnedStockNamesAndBoxSetYear protects the
// scanner values whose source is present in the Goldenrec media fixture. These
// exact title-only Names and the boxset's earliest release year are pinned by
// tools/goldenrec/state/snapshots/databases/data/jellyfin.db.
func TestScannerFixtureMetadataUsesPinnedStockNamesAndBoxSetYear(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	movies := harness.source
	tv := scannerPath(t, t.TempDir(), "tv")
	music := scannerPath(t, t.TempDir(), "music")
	for _, directory := range []string{tv, music} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatalf("create fixture source %q: %v", directory, err)
		}
	}
	addScannerFixtureCollection(t, harness, "Golden TV", "tvshows", tv)
	addScannerFixtureCollection(t, harness, "Golden Music", "music", music)

	boxset := scannerPath(t, movies, "Synthetic Collection [boxset]")
	for _, filename := range []string{
		"Collection Film One (2020).mkv",
		"Collection Film Two (2021).mkv",
		"Collection Film Three (2022).mkv",
	} {
		writeScannerFile(t, scannerPath(t, boxset, filename), "fixture video")
	}

	series := scannerPath(t, tv, "Synthetic Show (2024)")
	episodes := map[string]string{
		scannerPath(t, series, "Specials", "Synthetic Show S00E01 - Pilot Extra.mkv"):     "Pilot Extra",
		scannerPath(t, series, "Specials", "Synthetic Show S00E02 - Behind The Bars.mkv"): "Behind The Bars",
		scannerPath(t, series, "Season 01", "Synthetic Show S01E01 - First Signal.mkv"):   "First Signal",
		scannerPath(t, series, "Season 01", "Synthetic Show S01E02 - Second Signal.mkv"):  "Second Signal",
		scannerPath(t, series, "Season 01", "Synthetic Show S01E03 - Third Signal.mkv"):   "Third Signal",
		scannerPath(t, series, "Season 02", "Synthetic Show S02E01 - Return Signal.mkv"):  "Return Signal",
		scannerPath(t, series, "Season 02", "Synthetic Show S02E02 - Wide Signal.mkv"):    "Wide Signal",
		scannerPath(t, series, "Season 02", "Synthetic Show S02E03 - Final Signal.mkv"):   "Final Signal",
	}
	for path := range episodes {
		writeScannerFile(t, path, "fixture video")
	}

	tracks := make(map[string]string, 11)
	album := scannerPath(t, music, "Goldenrec Generator", "Synthetic Album")
	for number := 1; number <= 11; number++ {
		name := "Synthetic Track " + strconv.Itoa(number)
		path := scannerPath(t, album, strconv.Itoa(number)+" - "+name+".mp3")
		tracks[path] = name
		writeScannerFile(t, path, "fixture audio")
	}

	resetScannerFixtureToDefaultChain(t, harness, nil)
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan Goldenrec metadata fixture: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan Goldenrec metadata fixture errors = %v, want none", result.Errors)
	}

	for path, want := range tracks {
		path, want := path, want
		t.Run("audio/"+filepath.Base(path), func(t *testing.T) {
			got := scannerItemAtPath(t, harness.items, path)
			if got.Kind != item.KindAudio || got.Name == nil || *got.Name != want {
				t.Fatalf("audio %q Kind=%s Name=%q, want Audio Name %q", path, got.Kind, scannerFixtureString(got.Name), want)
			}
		})
	}
	for path, want := range episodes {
		path, want := path, want
		t.Run("episode/"+filepath.Base(path), func(t *testing.T) {
			got := scannerItemAtPath(t, harness.items, path)
			if got.Kind != item.KindEpisode || got.Name == nil || *got.Name != want {
				t.Fatalf("episode %q Kind=%s Name=%q, want Episode Name %q", path, got.Kind, scannerFixtureString(got.Name), want)
			}
		})
	}

	gotBoxSet := scannerItemAtPath(t, harness.items, boxset)
	if gotBoxSet.Kind != item.KindBoxSet || gotBoxSet.ProductionYear == nil || *gotBoxSet.ProductionYear != 2020 {
		t.Fatalf("boxset %q Kind=%s ProductionYear=%s, want BoxSet ProductionYear 2020", boxset, gotBoxSet.Kind, scannerFixtureInt(gotBoxSet.ProductionYear))
	}
}

// TestScannerFixturePersistsNFOCreditAsStockPersonEntity verifies the scanner
// turns a locally credited artist into the same durable Person entity shape as
// the pinned stock row, rather than retaining only a people-map satellite.
func TestScannerFixturePersistsNFOCreditAsStockPersonEntity(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	music := scannerPath(t, t.TempDir(), "music")
	if err := os.Mkdir(music, 0o755); err != nil {
		t.Fatalf("create music fixture source: %v", err)
	}
	addScannerFixtureCollection(t, harness, "Golden Music", "music", music)
	album := scannerPath(t, music, "Goldenrec Generator", "Synthetic Album")
	track := scannerPath(t, album, "1 - Synthetic Track 1.mp3")
	writeScannerFile(t, track, "fixture audio")
	writeScannerFile(t, scannerPath(t, album, "album.nfo"), `<album><title>Synthetic Album</title><albumartistcredits><artist>Goldenrec Generator</artist></albumartistcredits></album>`)

	resetScannerFixtureToDefaultChain(t, harness, providers.NewRegistry(
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			return os.Open(path)
		}}),
	))
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan NFO person fixture: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan NFO person fixture errors = %v, want none", result.Errors)
	}

	const (
		wantPersonID   = "A8CD1576-633D-4885-D967-0B53497A365B"
		wantPersonPath = "%MetadataPath%/People/G/Goldenrec Generator"
		wantName       = "Goldenrec Generator"
		wantCleanName  = "goldenrec generator"
		wantKey        = "Person-Goldenrec Generator"
	)
	personID, err := dto.ParseUUID(wantPersonID)
	if err != nil {
		t.Fatalf("parse pinned stock person ID: %v", err)
	}
	person, err := harness.items.Get(t.Context(), personID)
	if err != nil {
		t.Fatalf("load persisted NFO person %s: %v", wantPersonID, err)
	}
	if person.Kind != item.KindPerson || person.Path == nil || *person.Path != wantPersonPath || person.Name == nil || *person.Name != wantName || person.CleanName == nil || *person.CleanName != wantCleanName || person.PresentationUniqueKey == nil || *person.PresentationUniqueKey != wantKey {
		t.Fatalf("persisted NFO person = %#v, want Person ID %s path %q name %q clean name %q key %q", person, wantPersonID, wantPersonPath, wantName, wantCleanName, wantKey)
	}
}

// TestScannerFixtureTopologyUsesOneAggregateForAllCollections pins the stock
// hierarchy: exactly one aggregate root directly owns its four collection
// folders. Physical media paths are not aggregate roots themselves.
func TestScannerFixtureTopologyUsesOneAggregateForAllCollections(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	for _, fixture := range []struct {
		name       string
		collection string
	}{
		{name: "Golden TV", collection: "tvshows"},
		{name: "Golden Music", collection: "music"},
		{name: "Golden Photos", collection: "photos"},
	} {
		source := scannerPath(t, t.TempDir(), fixture.collection)
		if err := os.Mkdir(source, 0o755); err != nil {
			t.Fatalf("create %s fixture source: %v", fixture.name, err)
		}
		addScannerFixtureCollection(t, harness, fixture.name, fixture.collection, source)
	}
	resetScannerFixtureToDefaultChain(t, harness, nil)
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan topology fixture: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan topology fixture errors = %v, want none", result.Errors)
	}

	var aggregate []item.Item
	var collections []item.Item
	for _, candidate := range scannerItems(t, harness.items) {
		switch candidate.Kind {
		case item.KindAggregateFolder:
			aggregate = append(aggregate, candidate)
		case item.KindCollectionFolder:
			collections = append(collections, candidate)
		}
	}
	if len(aggregate) != 1 {
		t.Fatalf("aggregate folders = %#v, want exactly one stock aggregate root", aggregate)
	}
	if len(collections) != 4 {
		t.Fatalf("collection folders = %#v, want four stock collection children", collections)
	}
	for _, collection := range collections {
		if collection.ParentID == nil || *collection.ParentID != aggregate[0].ID {
			t.Fatalf("collection folder %#v parent = %v, want aggregate root %s", collection, collection.ParentID, aggregate[0].ID)
		}
	}
}

func addScannerFixtureCollection(t *testing.T, harness *scannerHarness, name, collection, source string) {
	t.Helper()
	if _, err := harness.topology.AddVirtualFolder(t.Context(), name, collection, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add %s fixture collection: %v", name, err)
	}
}

func resetScannerFixtureToDefaultChain(t *testing.T, harness *scannerHarness, refresher library.Refresher) {
	t.Helper()
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: refresher})
}

func scannerFixtureString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func scannerFixtureInt(value *int) string {
	if value == nil {
		return "<nil>"
	}
	return strconv.Itoa(*value)
}
