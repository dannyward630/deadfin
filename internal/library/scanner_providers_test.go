// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

func TestScannerRefresherUsesDiscoveredEntriesAndProducesNoOpRescan(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, resolver)
	alpha := scannerPath(t, harness.source, "alpha.mkv")
	beta := scannerPath(t, harness.source, "beta.mkv")
	writeScannerFile(t, alpha, "alpha")
	writeScannerFile(t, beta, "beta")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	locked := scannerProviderLockedFields()
	var readMu sync.Mutex
	reads := make(map[string]int)
	var refreshMu sync.Mutex
	var discovered []library.Entry
	calls := 0
	refresher := scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if refresh.Directory.Path != harness.source || refresh.LibraryRoot != harness.source {
			return fmt.Errorf("refresh directory facts = %#v, want source %q", refresh, harness.source)
		}
		refreshMu.Lock()
		defer refreshMu.Unlock()
		if calls == 0 {
			discovered = append([]library.Entry(nil), refresh.Entries...)
		} else if !reflect.DeepEqual(refresh.Entries, discovered) {
			return fmt.Errorf("refresh entries = %v, want immutable discovered facts %v", scannerProviderEntryPaths(refresh.Entries), scannerProviderEntryPaths(discovered))
		}
		calls++
		// A registry/provider receives an immutable snapshot. Mutating this local
		// slice must not corrupt the facts seen by the next desired child.
		refresh.Entries[0].Path = "mutated-by-provider"
		candidate.Name = scannerProviderString("NFO " + *candidate.Name)
		candidate.IsLocked = true
		candidate.LockedFields = append([]item.MetadataField(nil), locked...)
		candidate.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: *candidate.Path + ".provider.jpg"}}
		return nil
	})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   refresher,
		ReadDir: func(path string) ([]library.Entry, error) {
			readMu.Lock()
			reads[path]++
			readMu.Unlock()
			return scannerReadDir(path)
		},
	})

	first, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("provider-enriched scan: %v", err)
	}
	if first.Inserted != 3 || first.Updated != 0 || len(first.Errors) != 0 {
		t.Fatalf("provider-enriched scan result = %#v, want three inserts (root Folder plus two media) and no errors", first)
	}
	readMu.Lock()
	sourceReads := reads[harness.source]
	readMu.Unlock()
	if sourceReads != 1 {
		t.Fatalf("source ReadDir calls = %d, want the existing scan traversal only once", sourceReads)
	}
	if calls != 2 {
		t.Fatalf("provider refresh calls = %d, want each resolved media child once", calls)
	}
	refreshMu.Lock()
	discoveredPaths := scannerProviderEntryPaths(discovered)
	refreshMu.Unlock()
	if !scannerProviderHasPaths(discoveredPaths, alpha, beta) {
		t.Fatalf("refresh discovered entries = %v, want the two existing media paths", discoveredPaths)
	}
	for _, path := range []string{alpha, beta} {
		got := scannerItemAtPath(t, harness.items, path)
		if got.Name == nil || *got.Name != "NFO "+scannerProviderBase(path) || !got.IsLocked || !reflect.DeepEqual(got.LockedFields, locked) {
			t.Fatalf("provider-enriched item %q = %#v, want NFO title and locked fields %v", path, got, locked)
		}
		wantImages := []item.ImageInfo{{Type: item.ImageTypePrimary, Path: path + ".provider.jpg"}}
		if !reflect.DeepEqual(got.ImageInfos, wantImages) {
			t.Fatalf("provider-enriched item images %q = %#v, want %#v", path, got.ImageInfos, wantImages)
		}
	}

	second, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("unchanged provider-enriched rescan: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Deleted != 0 || len(second.Errors) != 0 {
		t.Fatalf("unchanged provider-enriched rescan = %#v, want an exact no-op", second)
	}
}

func TestScannerRegistryPersistsLocalArtworkAndNFOInOneReconcile(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	harness.chain = library.NewChain(scannerProviderContextResolver{base: baseResolver})
	media := scannerPath(t, harness.source, "movie.mkv")
	poster := scannerPath(t, harness.source, "poster.jpg")
	nfoPath := scannerPath(t, harness.source, "movie.nfo")
	writeScannerFile(t, media, "movie")
	writeScannerFile(t, poster, "poster")
	writeScannerFile(t, nfoPath, `<movie><title>NFO Scanner Movie</title><year>2024</year><genre>Drama</genre><actor><name>Actor One</name><role>Lead</role></actor><uniqueid type="tmdb">123</uniqueid><broken>`)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	registry := providers.NewRegistry(
		providers.NewLocalImageProvider(),
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			return os.Open(path)
		}}),
	)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})

	first, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("real adapter scan: %v", err)
	}
	if first.Inserted != 2 || first.Updated != 0 || len(first.Errors) != 0 {
		t.Fatalf("real adapter initial scan = %#v, want root Folder and movie inserted in one reconciliation", first)
	}
	got := scannerItemAtPath(t, harness.items, media)
	if got.Name == nil || *got.Name != "NFO Scanner Movie" || got.ProductionYear == nil || *got.ProductionYear != 2024 || !reflect.DeepEqual(got.Values.Genres, []string{"Drama"}) || len(got.People) != 1 || got.People[0].Name != "Actor One" {
		t.Fatalf("real adapter persisted fields = %#v, want malformed-tail NFO title/year/genre/person", got)
	}
	if value, ok := got.ProviderIDs.Get("tmdb"); !ok || value != "123" {
		t.Fatalf("real adapter provider IDs = %#v, want tmdb 123", got.ProviderIDs.Snapshot())
	}
	if !adapterHasScannerImage(got.ImageInfos, item.ImageTypePrimary, poster) {
		t.Fatalf("real adapter item images = %#v, want local poster %q", got.ImageInfos, poster)
	}

	second, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("real adapter unchanged rescan: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Deleted != 0 || len(second.Errors) != 0 {
		t.Fatalf("real adapter unchanged rescan = %#v, want no-op", second)
	}
}

func TestScannerRegistryPreservesPersistedNFOLocksWhenNFOChanges(t *testing.T) {
	tests := []struct {
		name        string
		replacement string
	}{
		{
			name:        "lock data changes",
			replacement: `<movie><title>Replacement Scanner Movie</title><genre>Replacement Drama</genre><lockdata>false</lockdata><lockedfields>Overview</lockedfields></movie>`,
		},
		{
			name:        "lock data is removed",
			replacement: `<movie><title>Replacement Scanner Movie</title><genre>Replacement Drama</genre></movie>`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			baseResolver := &scannerFixtureResolver{}
			harness := newScannerHarness(t, baseResolver)
			harness.chain = library.NewChain(scannerProviderContextResolver{base: baseResolver})
			media := scannerPath(t, harness.source, "movie.mkv")
			nfoPath := scannerPath(t, harness.source, "movie.nfo")
			writeScannerFile(t, media, "movie")
			writeScannerFile(t, nfoPath, `<movie><title>Locked Scanner Movie</title><genre>Locked Drama</genre><lockdata>true</lockdata><lockedfields>Name|Genres</lockedfields></movie>`)
			if err := harness.scanner.Close(); err != nil {
				t.Fatalf("close default scanner: %v", err)
			}
			registry := providers.NewRegistry(providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
				return os.Open(path)
			}}))
			harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})

			if _, err := harness.scanner.Scan(t.Context()); err != nil {
				t.Fatalf("seed locked NFO scan: %v", err)
			}
			before := scannerItemAtPath(t, harness.items, media)
			wantLockedFields := []item.MetadataField{item.MetadataFieldGenres, item.MetadataFieldName}
			if before.Name == nil || *before.Name != "Locked Scanner Movie" || !before.IsLocked || !reflect.DeepEqual(before.LockedFields, wantLockedFields) || !reflect.DeepEqual(before.Values.Genres, []string{"Locked Drama"}) {
				t.Fatalf("seed locked NFO item = %#v, want persisted locked title, genres, and lock facts", before)
			}

			writeScannerFile(t, nfoPath, testCase.replacement)
			if _, err := harness.scanner.Scan(t.Context()); err != nil {
				t.Fatalf("rescan changed NFO lock data: %v", err)
			}
			after := scannerItemAtPath(t, harness.items, media)
			if after.Name == nil || *after.Name != "Locked Scanner Movie" || !after.IsLocked || !reflect.DeepEqual(after.LockedFields, wantLockedFields) || !reflect.DeepEqual(after.Values.Genres, []string{"Locked Drama"}) {
				t.Fatalf("changed NFO replaced persisted lock facts or protected values\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

// TestScannerRegistryRescanPreservesGloballyLockedNFOFacts verifies the global
// lock boundary, not merely the fields named in lockedfields. The NFO remains
// unchanged between scans while the movie file grows, so the scanner must carry
// every persisted metadata fact forward but refresh its filesystem source fact.
func TestScannerRegistryRescanPreservesGloballyLockedNFOFacts(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	harness.chain = library.NewChain(scannerLockedEpisodeResolver{base: baseResolver})
	media := scannerPath(t, harness.source, "locked-s01e02.mkv")
	nfoPath := scannerPath(t, harness.source, "locked-s01e02.nfo")
	writeScannerFile(t, media, "small")
	writeScannerFile(t, nfoPath, `<episodedetails>
	<title>Locked Episode</title><originaltitle>Locked Original</originaltitle><plot>Locked overview</plot><tagline>Locked tagline</tagline>
	<mpaa>PG-13</mpaa><customrating>Locked custom</customrating><language>en</language><countrycode>US</countrycode>
	<dateadded>2001-02-03</dateadded><premiered>2004-07-16</premiered><enddate>2004-07-17</enddate><year>2004</year>
	<rating>8.4</rating><criticrating>91</criticrating><season>1</season><episode>2</episode>
	<genre>Science Fiction / Adventure</genre><studio>Locked Studio</studio><tag>locked-tag</tag>
	<uniqueid type="imdb">tt1234567</uniqueid><uniqueid type="tmdb">1234</uniqueid>
	<director>Locked Director</director><credits>Locked Writer</credits><actor><name>Locked Actor</name><role>Lead</role><order>7</order></actor>
	<trailer>plugin://plugin.video.youtube/?action=play_video&amp;videoid=dQw4w9WgXcQ</trailer>
	<lockdata>true</lockdata><lockedfields>Name|Genres</lockedfields>
</episodedetails>
<episodedetails><episode>3</episode></episodedetails>`)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	provider := &scannerLockedMetadataCounter{}
	registry := providers.NewRegistry(
		provider,
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			return os.Open(path)
		}}),
	)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})

	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed globally locked NFO scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	if !before.IsLocked || before.ProductionYear == nil || *before.ProductionYear != 2004 || before.IndexNumber == nil || *before.IndexNumber != 2 || before.ParentIndexNumber == nil || *before.ParentIndexNumber != 1 || before.Extra.Episode == nil || before.Extra.Episode.IndexNumberEnd == nil || *before.Extra.Episode.IndexNumberEnd != 3 || len(before.Extra.RemoteTrailers) != 1 || len(before.People) != 3 {
		t.Fatalf("seed globally locked NFO item = %#v, want parsed ratings, dates, indexes, trailers, extra, and people", before)
	}
	providerCallsBeforeRescan := provider.calls
	if providerCallsBeforeRescan != 1 {
		t.Fatalf("local metadata provider calls after seed = %d, want episode once", providerCallsBeforeRescan)
	}

	const refreshedSource = "scanner-owned-size-refresh"
	writeScannerFile(t, media, refreshedSource)
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("rescan globally locked NFO item: %v", err)
	}
	after := scannerItemAtPath(t, harness.items, media)
	assertScannerLockedNFOFacts(t, after, before)
	if after.Size == nil || *after.Size != int64(len(refreshedSource)) {
		t.Fatalf("rescanned locked item size = %v, want refreshed source size %d", after.Size, len(refreshedSource))
	}
	if provider.calls != providerCallsBeforeRescan {
		t.Fatalf("local metadata provider calls after globally locked rescan = %d, want no unlocked call because the source root is no longer an item after %d seed calls", provider.calls, providerCallsBeforeRescan)
	}
}

func TestScannerRegistryNFOOpenFailureLeavesFolderUnchanged(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "original")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	writeScannerFile(t, scannerPath(t, harness.source, "movie.nfo"), `<movie><title>must not persist</title></movie>`)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	harness.chain = library.NewChain(scannerProviderContextResolver{base: baseResolver})
	openErr := errors.New("NFO open denied")
	registry := providers.NewRegistry(providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(context.Context, string) (io.ReadCloser, error) { return nil, openErr },
	}))
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("NFO-open-failing scan returned topology error: %v", err)
	}
	if len(result.Errors) != 1 || !errors.Is(result.Errors[0], openErr) || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("NFO-open-failing scan result = %#v, want one folder error and no mutations", result)
	}
	if after := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(after, before) {
		t.Fatalf("NFO open failure changed persisted child\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestScannerRegistryCanceledNFOOpenLeavesFolderUnchanged(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "original")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	writeScannerFile(t, scannerPath(t, harness.source, "movie.nfo"), `<movie><title>must not persist</title></movie>`)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	harness.chain = library.NewChain(scannerProviderContextResolver{base: baseResolver})
	started := make(chan struct{})
	registry := providers.NewRegistry(providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(ctx context.Context, _ string) (io.ReadCloser, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}))
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1, Refresher: registry})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled NFO scan = %v, want context.Canceled", err)
	}
	if after := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled NFO open changed persisted child\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestScannerRefresherUsesChildDirectoryFactsWithoutSecondTraversal(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	resolver := scannerProviderContextResolver{base: baseResolver}
	harness.chain = library.NewChain(resolver)
	show := scannerPath(t, harness.source, "Show")
	episode := scannerPath(t, show, "episode.mkv")
	nfo := scannerPath(t, show, "tvshow.nfo")
	movie := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, episode, "episode")
	writeScannerFile(t, nfo, "cached nfo")
	writeScannerFile(t, movie, "movie")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	var readsMu sync.Mutex
	reads := make(map[string]int)
	seenShow := false
	seenMovie := false
	refresher := scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if candidate.Path == nil {
			return errors.New("refresher candidate has no path")
		}
		switch *candidate.Path {
		case show:
			if refresh.Directory.Path != show || !scannerProviderHasPaths(scannerProviderEntryPaths(refresh.Entries), episode, nfo) {
				return fmt.Errorf("show refresh context = directory %q entries %v, want child directory and its cached NFO listing", refresh.Directory.Path, scannerProviderEntryPaths(refresh.Entries))
			}
			seenShow = true
		case movie:
			if refresh.Directory.Path != harness.source || !scannerProviderHasPaths(scannerProviderEntryPaths(refresh.Entries), show, movie) {
				return fmt.Errorf("movie refresh context = directory %q entries %v, want parent and sibling listing", refresh.Directory.Path, scannerProviderEntryPaths(refresh.Entries))
			}
			seenMovie = true
		}
		return nil
	})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher:   refresher,
		ReadDir: func(path string) ([]library.Entry, error) {
			readsMu.Lock()
			reads[path]++
			readsMu.Unlock()
			return scannerReadDir(path)
		},
	})
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("child-context scan: %v", err)
	}
	if len(result.Errors) != 0 || !seenShow || !seenMovie {
		t.Fatalf("child-context scan = %#v, saw show=%t movie=%t; want successful enriched folder plan", result, seenShow, seenMovie)
	}
	readsMu.Lock()
	sourceReads, showReads := reads[harness.source], reads[show]
	readsMu.Unlock()
	if sourceReads != 1 || showReads != 1 {
		t.Fatalf("ReadDir counts = source %d show %d, want one existing traversal per directory", sourceReads, showReads)
	}
}

func TestScannerProviderErrorAbortsDirectoryPlanBeforeReconcile(t *testing.T) {
	baseResolver := &scannerFixtureResolver{}
	harness := newScannerHarness(t, baseResolver)
	resolver := scannerProviderContextResolver{base: baseResolver}
	harness.chain = library.NewChain(resolver)
	show := scannerPath(t, harness.source, "Show")
	episode := scannerPath(t, show, "episode.mkv")
	movie := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, episode, "episode")
	writeScannerFile(t, movie, "movie")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1})
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed directory plan: %v", err)
	}
	beforeShow := scannerItemAtPath(t, harness.items, show)
	beforeMovie := scannerItemAtPath(t, harness.items, movie)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	providerErr := errors.New("NFO parse failed")
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			if candidate.Path != nil && *candidate.Path == movie {
				candidate.Name = scannerProviderString("must not persist")
				return providerErr
			}
			return nil
		}),
	})
	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("provider-error directory scan returned topology error: %v", err)
	}
	if len(result.Errors) == 0 || !errors.Is(result.Errors[0], providerErr) {
		t.Fatalf("provider-error directory result = %#v, want folder error %v", result, providerErr)
	}
	if after := scannerItemAtPath(t, harness.items, show); !reflect.DeepEqual(after, beforeShow) {
		t.Fatalf("provider error changed sibling directory before reconciliation\nbefore: %#v\nafter:  %#v", beforeShow, after)
	}
	if after := scannerItemAtPath(t, harness.items, movie); !reflect.DeepEqual(after, beforeMovie) {
		t.Fatalf("provider error changed sibling file before reconciliation\nbefore: %#v\nafter:  %#v", beforeMovie, after)
	}
}

func TestScannerRejectsStructuralProviderMutationsBeforeFolderReconcile(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*item.Item)
	}{
		{name: "identifier", mutate: func(candidate *item.Item) { candidate.ID = scannerID("provider-replaced-id") }},
		{name: "parent", mutate: func(candidate *item.Item) {
			parent := scannerID("provider-foreign-parent")
			candidate.ParentID = &parent
		}},
		{name: "path", mutate: func(candidate *item.Item) { candidate.Path = scannerProviderString("/provider/invented.mkv") }},
		{name: "kind", mutate: func(candidate *item.Item) { candidate.Kind = item.KindFolder; candidate.IsFolder = true }},
		{name: "folder flag", mutate: func(candidate *item.Item) { candidate.IsFolder = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newScannerHarness(t, &scannerFixtureResolver{})
			primary := scannerPath(t, harness.source, "primary.mkv")
			sibling := scannerPath(t, harness.source, "sibling.mkv")
			writeScannerFile(t, primary, "primary")
			writeScannerFile(t, sibling, "sibling")
			if _, err := harness.scanner.Scan(t.Context()); err != nil {
				t.Fatalf("seed scan: %v", err)
			}
			beforePrimary := scannerItemAtPath(t, harness.items, primary)
			beforeSibling := scannerItemAtPath(t, harness.items, sibling)
			if err := harness.scanner.Close(); err != nil {
				t.Fatalf("close seed scanner: %v", err)
			}
			var readsMu sync.Mutex
			reads := make(map[string]int)
			harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
				Concurrency: 1,
				Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
					if candidate.Path != nil && *candidate.Path == primary {
						candidate.Name = scannerProviderString("must not persist")
						test.mutate(candidate)
					}
					return nil
				}),
				ReadDir: func(path string) ([]library.Entry, error) {
					readsMu.Lock()
					reads[path]++
					readsMu.Unlock()
					return scannerReadDir(path)
				},
			})

			result, err := harness.scanner.Scan(t.Context())
			if err != nil {
				t.Fatalf("structural mutation scan returned topology error: %v", err)
			}
			if len(result.Errors) != 1 || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 {
				t.Fatalf("structural mutation result = %#v, want one rejected folder and no mutations", result)
			}
			readsMu.Lock()
			primaryReads := reads[primary]
			readsMu.Unlock()
			if primaryReads != 0 {
				t.Fatalf("%s structural mutation triggered ReadDir(%q) %d times, want no traversal of a rejected candidate", test.name, primary, primaryReads)
			}
			if after := scannerItemAtPath(t, harness.items, primary); !reflect.DeepEqual(after, beforePrimary) {
				t.Fatalf("%s mutation changed existing primary item or satellites\nbefore: %#v\nafter:  %#v", test.name, beforePrimary, after)
			}
			if after := scannerItemAtPath(t, harness.items, sibling); !reflect.DeepEqual(after, beforeSibling) {
				t.Fatalf("%s mutation partially changed sibling item or satellites\nbefore: %#v\nafter:  %#v", test.name, beforeSibling, after)
			}
		})
	}
}

func TestScannerProviderFailureLeavesFolderUnchanged(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "original")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	providerErr := errors.New("NFO read failed")
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(context.Context, library.RefreshContext, *item.Item) error {
			return providerErr
		}),
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("provider-failing scan returned topology error: %v", err)
	}
	if len(result.Errors) != 1 || !errors.Is(result.Errors[0], providerErr) {
		t.Fatalf("provider-failing scan errors = %v, want wrapped provider error %v", result.Errors, providerErr)
	}
	if after := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(after, before) {
		t.Fatalf("provider failure changed persisted child\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestScannerProviderDuplicateImageRejectsWholeFolderWithoutChangingPrior(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "original")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(_ context.Context, _ library.RefreshContext, candidate *item.Item) error {
			candidate.Name = scannerProviderString("must not persist")
			candidate.ImageInfos = []item.ImageInfo{
				{Type: item.ImageTypePrimary, Index: 0, Path: "one.jpg"},
				{Type: item.ImageTypePrimary, Index: 0, Path: "two.jpg"},
			}
			return nil
		}),
	})

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("duplicate-image provider scan returned topology error: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("duplicate-image provider scan errors = %v, want one folder failure", result.Errors)
	}
	if after := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(after, before) {
		t.Fatalf("duplicate-image provider changed persisted child\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestScannerCanceledProviderLeavesFolderUnchanged(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	media := scannerPath(t, harness.source, "movie.mkv")
	writeScannerFile(t, media, "original")
	if _, err := harness.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	before := scannerItemAtPath(t, harness.items, media)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close seed scanner: %v", err)
	}
	started := make(chan struct{})
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Refresher: scannerRefreshFunc(func(ctx context.Context, _ library.RefreshContext, _ *item.Item) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scanner did not invoke the cancellable provider")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled provider scan = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled provider scan did not return")
	}
	if after := scannerItemAtPath(t, harness.items, media); !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled provider changed persisted child\nbefore: %#v\nafter:  %#v", before, after)
	}
}

type scannerRefreshFunc func(context.Context, library.RefreshContext, *item.Item) error

func (refresh scannerRefreshFunc) Refresh(ctx context.Context, context library.RefreshContext, candidate *item.Item) error {
	return refresh(ctx, context, candidate)
}

type scannerProviderContextResolver struct{ base *scannerFixtureResolver }

func (resolver scannerProviderContextResolver) Priority() library.ResolverPriority {
	return resolver.base.Priority()
}

func (resolver scannerProviderContextResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	switch filepath.Ext(input.Entry.Path) {
	case ".nfo", ".jpg", ".jpeg", ".png", ".webp":
		return nil, nil
	}
	return resolver.base.Resolve(ctx, input)
}

type scannerLockedEpisodeResolver struct{ base *scannerFixtureResolver }

func (resolver scannerLockedEpisodeResolver) Priority() library.ResolverPriority {
	return resolver.base.Priority()
}

func (resolver scannerLockedEpisodeResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	switch filepath.Ext(input.Entry.Path) {
	case ".nfo", ".jpg", ".jpeg", ".png", ".webp":
		return nil, nil
	}
	candidate, err := resolver.base.Resolve(ctx, input)
	if err != nil || candidate == nil || input.Entry.IsDir {
		return candidate, err
	}
	candidate.Kind = item.KindEpisode
	candidate.IsMovie = false
	return candidate, nil
}

type scannerLockedMetadataCounter struct{ calls int }

func (provider *scannerLockedMetadataCounter) Name() string { return "locked-metadata-counter" }

func (*scannerLockedMetadataCounter) Stage() providers.ProviderStage {
	return providers.ProviderStageLocalMetadata
}

func (provider *scannerLockedMetadataCounter) Refresh(context.Context, library.RefreshContext, *item.Item) (bool, error) {
	provider.calls++
	return false, nil
}

func assertScannerLockedNFOFacts(t *testing.T, got, want item.Item) {
	t.Helper()
	if !got.IsLocked || got.IsLocked != want.IsLocked || !reflect.DeepEqual(got.LockedFields, want.LockedFields) {
		t.Fatalf("global lock facts after rescan = locked %t fields %#v, want locked %t fields %#v", got.IsLocked, got.LockedFields, want.IsLocked, want.LockedFields)
	}
	if !reflect.DeepEqual(got.Name, want.Name) || !reflect.DeepEqual(got.OriginalTitle, want.OriginalTitle) || !reflect.DeepEqual(got.Overview, want.Overview) || !reflect.DeepEqual(got.Tagline, want.Tagline) || !reflect.DeepEqual(got.OfficialRating, want.OfficialRating) || !reflect.DeepEqual(got.CustomRating, want.CustomRating) || !reflect.DeepEqual(got.PreferredMetadataLanguage, want.PreferredMetadataLanguage) || !reflect.DeepEqual(got.PreferredMetadataCountryCode, want.PreferredMetadataCountryCode) {
		t.Fatalf("locked NFO text metadata changed\nbefore: %#v\nafter:  %#v", want, got)
	}
	if !reflect.DeepEqual(got.PremiereDate, want.PremiereDate) || !reflect.DeepEqual(got.EndDate, want.EndDate) || !reflect.DeepEqual(got.ProductionYear, want.ProductionYear) || !reflect.DeepEqual(got.CommunityRating, want.CommunityRating) || !reflect.DeepEqual(got.CriticRating, want.CriticRating) || !reflect.DeepEqual(got.ParentIndexNumber, want.ParentIndexNumber) || !reflect.DeepEqual(got.IndexNumber, want.IndexNumber) {
		t.Fatalf("locked NFO ratings, dates, year, or indexes changed\nbefore: %#v\nafter:  %#v", want, got)
	}
	if !reflect.DeepEqual(got.ProviderIDs.Snapshot(), want.ProviderIDs.Snapshot()) || !reflect.DeepEqual(got.Values, want.Values) || !reflect.DeepEqual(got.People, want.People) || !reflect.DeepEqual(got.Extra, want.Extra) {
		t.Fatalf("locked NFO provider, taxonomy, people, trailer, or extra metadata changed\nbefore: %#v\nafter:  %#v", want, got)
	}
}

func adapterHasScannerImage(images []item.ImageInfo, kind item.ImageType, path string) bool {
	for _, image := range images {
		if image.Type == kind && image.Path == path {
			return true
		}
	}
	return false
}

func scannerProviderLockedFields() []item.MetadataField {
	return []item.MetadataField{
		item.MetadataFieldCast,
		item.MetadataFieldGenres,
		item.MetadataFieldProductionLocations,
		item.MetadataFieldStudios,
		item.MetadataFieldTags,
		item.MetadataFieldName,
		item.MetadataFieldOverview,
		item.MetadataFieldRuntime,
		item.MetadataFieldOfficialRating,
	}
}

func scannerProviderEntryPaths(entries []library.Entry) []string {
	paths := make([]string, len(entries))
	for index, entry := range entries {
		paths[index] = entry.Path
	}
	return paths
}

func scannerProviderHasPaths(paths []string, want ...string) bool {
	if len(paths) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		seen[path] = struct{}{}
	}
	for _, path := range want {
		if _, ok := seen[path]; !ok {
			return false
		}
	}
	return true
}

func scannerProviderBase(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' {
			path = path[index+1:]
			break
		}
	}
	return path[:len(path)-len(".mkv")]
}

func scannerProviderString(value string) *string { return &value }
