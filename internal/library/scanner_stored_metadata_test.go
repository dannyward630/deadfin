// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

// storedMetadataHarness scans a movie library through the real resolver
// chain, the NFO reader, and the TMDb provider on checked-in cassettes. It
// counts TMDb invocations and HTTP requests; the remote client has no cache,
// so a TMDb invocation always reaches the cassette.
type storedMetadataHarness struct {
	*scannerHarness
	now      time.Time
	remote   *countingRemoteProvider
	requests *countingDoer
}

type countingDoer struct {
	inner remote.Doer
	calls atomic.Int64
}

func (doer *countingDoer) Do(request *http.Request) (*http.Response, error) {
	doer.calls.Add(1)
	return doer.inner.Do(request)
}

type countingRemoteProvider struct {
	providers.Provider
	calls atomic.Int64
	fail  atomic.Bool
}

func (provider *countingRemoteProvider) Refresh(ctx context.Context, refresh library.RefreshContext, target *item.Item) (bool, error) {
	provider.calls.Add(1)
	if provider.fail.Load() {
		return false, errors.New("remote provider unavailable")
	}
	return provider.Provider.Refresh(ctx, refresh, target)
}

func newStoredMetadataHarness(t *testing.T) *storedMetadataHarness {
	t.Helper()
	base := newScannerHarness(t, &scannerFixtureResolver{})
	if err := base.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	folders, err := base.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 {
		t.Fatalf("virtual folders = %#v, %v", folders, err)
	}
	options := folders[0].Options
	options.PreferredMetadataLanguage = "en"
	options.MetadataCountryCode = "US"
	if err := base.topology.UpdateLibraryOptions(t.Context(), folders[0].CollectionFolderID, options); err != nil {
		t.Fatalf("set library metadata language: %v", err)
	}

	var interactions []remotetest.Interaction
	for _, path := range []string{"../providers/tmdb/testdata/the-matrix.json", "../providers/tmdb/testdata/emperors-new-groove.json"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read cassette: %v", err)
		}
		var recorded []remotetest.Interaction
		if err := json.Unmarshal(contents, &recorded); err != nil {
			t.Fatalf("decode cassette %s: %v", path, err)
		}
		interactions = append(interactions, recorded...)
	}
	requests := &countingDoer{inner: remotetest.NewCassette(interactions...)}
	config := tmdb.DefaultConfig()
	config.APIKey = "test-key"
	remoteProvider := &countingRemoteProvider{Provider: tmdb.NewProvider(tmdb.Options{
		HTTP:   remote.NewClient(remote.Config{Doer: requests}),
		Config: config,
	})}

	harness := &storedMetadataHarness{
		scannerHarness: base,
		now:            time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC),
		remote:         remoteProvider,
		requests:       requests,
	}
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return harness.now },
		Refresher: providers.NewRegistry(
			providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }}),
			remoteProvider,
		),
	})
	return harness
}

// writeAt writes a fixture file with a fixed write time.
func (harness *storedMetadataHarness) writeAt(t *testing.T, name, contents string, modified time.Time) string {
	t.Helper()
	path := scannerPath(t, harness.source, name)
	writeScannerFile(t, path, contents)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set write time of %s: %v", path, err)
	}
	return path
}

func (harness *storedMetadataHarness) scan(t *testing.T, options library.RefreshOptions) library.RefreshResult {
	t.Helper()
	result, err := harness.scanner.ScanWithOptions(t.Context(), options)
	if err != nil {
		t.Fatalf("scan %#v: %v", options, err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan %#v errors = %v", options, result.Errors)
	}
	return result
}

// storedMetadataSnapshot is the remote metadata a rescan must keep.
type storedMetadataSnapshot struct {
	Name, Overview, Tagline, OfficialRating string
	Genres, Studios, Tags                   []string
	People                                  []string
	Tmdb                                    string
	DateLastRefreshed                       *time.Time
}

func snapshotStoredMetadata(t *testing.T, value item.Item) storedMetadataSnapshot {
	t.Helper()
	text := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	snapshot := storedMetadataSnapshot{
		Name:              text(value.Name),
		Overview:          text(value.Overview),
		Tagline:           text(value.Tagline),
		OfficialRating:    text(value.OfficialRating),
		Genres:            value.Values.Genres,
		Studios:           value.Values.Studios,
		Tags:              value.Values.Tags,
		DateLastRefreshed: value.DateLastRefreshed,
	}
	snapshot.Tmdb, _ = value.ProviderIDs.Get("Tmdb")
	for _, credit := range value.People {
		role := ""
		if credit.Role != nil {
			role = *credit.Role
		}
		snapshot.People = append(snapshot.People, credit.Name+"/"+credit.Type.String()+"/"+role)
	}
	return snapshot
}

const matrixOverview = "A computer hacker learns from mysterious rebels.\nHe joins the fight."

// TestScannerRescanKeepsRemoteMetadataWithoutRemoteCalls is the regression
// for remote metadata lost on rescan. Upstream refreshes the stored item in
// place; MetadataService.GetProviders runs remote providers only on a first
// refresh, a required refresh, FullRefresh, or ReplaceAllMetadata. A routine
// rescan of an unchanged item therefore makes no remote call and keeps the
// stored overview, genres, and people.
func TestScannerRescanKeepsRemoteMetadataWithoutRemoteCalls(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	movie := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))

	harness.scan(t, library.DefaultRefreshOptions())
	first := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, movie))
	if first.Tmdb != "603" || first.Overview != matrixOverview || !reflect.DeepEqual(first.Genres, []string{"Action", "Science Fiction"}) || len(first.People) == 0 {
		t.Fatalf("first scan metadata = %#v, want the TMDb movie 603 with overview, genres, and people", first)
	}
	if first.DateLastRefreshed == nil || !first.DateLastRefreshed.Equal(harness.now) {
		t.Fatalf("first scan DateLastRefreshed = %v, want the scan time %v", first.DateLastRefreshed, harness.now)
	}
	invocations, requests := harness.remote.calls.Load(), harness.requests.calls.Load()
	if invocations != 1 || requests == 0 {
		t.Fatalf("first scan TMDb invocations = %d, requests = %d; want one invocation that reaches the cassette", invocations, requests)
	}

	harness.now = harness.now.Add(time.Hour)
	second := harness.scan(t, library.DefaultRefreshOptions())
	if got := harness.remote.calls.Load(); got != invocations {
		t.Fatalf("rescan TMDb invocations = %d, want none after the first scan", got-invocations)
	}
	if got := harness.requests.calls.Load(); got != requests {
		t.Fatalf("rescan TMDb requests = %d, want none after the first scan", got-requests)
	}
	if second.Updated != 0 {
		t.Fatalf("rescan updated %d items, want an unchanged item left alone", second.Updated)
	}
	if again := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, movie)); !reflect.DeepEqual(again, first) {
		t.Fatalf("rescan metadata = %#v, want unchanged %#v", again, first)
	}
}

// TestScannerRescanFetchesRemoteMetadataForNewItem proves that a new item is
// a first refresh: it gets remote metadata on the rescan that finds it, and
// the stored item beside it makes no remote call.
func TestScannerRescanFetchesRemoteMetadataForNewItem(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	matrix := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	harness.scan(t, library.DefaultRefreshOptions())
	before := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, matrix))
	invocations := harness.remote.calls.Load()

	harness.now = harness.now.Add(time.Hour)
	groove := harness.writeAt(t, "The Emperors New Groove (2000).mkv", "groove", harness.now.Add(-time.Minute))
	harness.scan(t, library.DefaultRefreshOptions())
	if got := harness.remote.calls.Load() - invocations; got != 1 {
		t.Fatalf("rescan TMDb invocations = %d, want one for the new item only", got)
	}
	added := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, groove))
	if added.Tmdb != "11688" || added.Overview == "" || added.DateLastRefreshed == nil {
		t.Fatalf("new item metadata = %#v, want TMDb movie 11688 with an overview and a refresh time", added)
	}
	if after := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, matrix)); !reflect.DeepEqual(after, before) {
		t.Fatalf("stored item metadata = %#v, want unchanged %#v", after, before)
	}
}

// TestScannerChangedNFOWinsOnItsFields proves the local-change path of
// MetadataService.RefreshWithProviders: a changed NFO (BaseNfoProvider
// .HasChanged) runs the local reader without remote providers. The NFO
// values win on the fields they set; the stored values fill the rest; the
// additive studios take the union.
func TestScannerChangedNFOWinsOnItsFields(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	movie := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	nfo := `<movie><title>The Matrix</title><year>1999</year></movie>`
	harness.writeAt(t, "The Matrix (1999).nfo", nfo, harness.now.Add(-24*time.Hour))
	harness.scan(t, library.DefaultRefreshOptions())
	stored := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, movie))
	if stored.Overview != matrixOverview || stored.Tagline != "Free your mind." {
		t.Fatalf("first scan metadata = %#v, want the TMDb overview and tagline", stored)
	}
	invocations := harness.remote.calls.Load()

	// An unchanged NFO leaves the stored item alone.
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	if again := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, movie)); !reflect.DeepEqual(again, stored) {
		t.Fatalf("unchanged NFO rescan metadata = %#v, want %#v", again, stored)
	}

	harness.now = harness.now.Add(time.Hour)
	harness.writeAt(t, "The Matrix (1999).nfo", `<movie><title>The Matrix</title><year>1999</year><plot>A local plot.</plot><genre>Drama</genre><studio>Local Studio</studio></movie>`, harness.now)
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	if got := harness.remote.calls.Load(); got != invocations {
		t.Fatalf("changed NFO rescan TMDb invocations = %d, want none", got-invocations)
	}
	changed := snapshotStoredMetadata(t, scannerItemAtPath(t, harness.items, movie))
	if changed.Overview != "A local plot." || !reflect.DeepEqual(changed.Genres, []string{"Drama"}) {
		t.Fatalf("changed NFO overview = %q, genres = %v; want the NFO values", changed.Overview, changed.Genres)
	}
	if changed.Tagline != stored.Tagline || changed.OfficialRating != stored.OfficialRating || !reflect.DeepEqual(changed.People, stored.People) || changed.Tmdb != "603" {
		t.Fatalf("changed NFO metadata = %#v, want the stored tagline, rating, people, and TMDb id from %#v", changed, stored)
	}
	if want := append([]string{"Local Studio"}, stored.Studios...); !reflect.DeepEqual(changed.Studios, want) {
		t.Fatalf("changed NFO studios = %v, want the NFO studio then the stored ones %v", changed.Studios, want)
	}
	if changed.DateLastRefreshed == nil || !changed.DateLastRefreshed.Equal(*stored.DateLastRefreshed) {
		t.Fatalf("changed NFO DateLastRefreshed = %v, want the stored full refresh time %v", changed.DateLastRefreshed, stored.DateLastRefreshed)
	}
}

// TestScannerNeverOverwritesLockedField proves that a locked field keeps its
// stored value under a changed NFO and under a refresh that replaces all
// metadata.
func TestScannerNeverOverwritesLockedField(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	movie := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	harness.writeAt(t, "The Matrix (1999).nfo", `<movie><title>The Matrix</title><year>1999</year></movie>`, harness.now.Add(-24*time.Hour))
	harness.scan(t, library.DefaultRefreshOptions())

	edited := scannerItemAtPath(t, harness.items, movie)
	operator := "An operator overview."
	edited.Overview = &operator
	edited.LockedFields = []item.MetadataField{item.MetadataFieldOverview}
	if err := harness.items.Upsert(t.Context(), edited); err != nil {
		t.Fatalf("lock overview: %v", err)
	}

	harness.now = harness.now.Add(time.Hour)
	harness.writeAt(t, "The Matrix (1999).nfo", `<movie><title>The Matrix</title><year>1999</year><plot>An NFO plot.</plot></movie>`, harness.now)
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	if got := scannerItemAtPath(t, harness.items, movie).Overview; got == nil || *got != operator {
		t.Fatalf("changed NFO overview = %v, want the locked %q", got, operator)
	}

	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.RefreshOptions{Metadata: library.RefreshModeFullRefresh, Image: library.RefreshModeFullRefresh, ReplaceAllMetadata: true})
	replaced := scannerItemAtPath(t, harness.items, movie)
	if replaced.Overview == nil || *replaced.Overview != operator {
		t.Fatalf("replace-all overview = %v, want the locked %q", replaced.Overview, operator)
	}
	if replaced.Tagline == nil || *replaced.Tagline != "Free your mind." {
		t.Fatalf("replace-all tagline = %v, want the unlocked field refreshed from TMDb", replaced.Tagline)
	}
}

// TestScannerFullRefreshCallsRemoteProviders proves that FullRefresh still
// runs the remote providers on an unchanged, already refreshed item, and
// that without ReplaceAllMetadata it keeps the stored values
// (MergeData with replaceData false).
func TestScannerFullRefreshCallsRemoteProviders(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	movie := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	harness.scan(t, library.DefaultRefreshOptions())
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	invocations := harness.remote.calls.Load()
	if invocations != 1 {
		t.Fatalf("TMDb invocations before FullRefresh = %d, want 1", invocations)
	}

	edited := scannerItemAtPath(t, harness.items, movie)
	kept := "A stored overview."
	edited.Overview = &kept
	if err := harness.items.Upsert(t.Context(), edited); err != nil {
		t.Fatalf("store edited overview: %v", err)
	}

	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.RefreshOptions{Metadata: library.RefreshModeFullRefresh, Image: library.RefreshModeFullRefresh})
	if got := harness.remote.calls.Load(); got != invocations+1 {
		t.Fatalf("FullRefresh TMDb invocations = %d, want one", got-invocations)
	}
	refreshed := scannerItemAtPath(t, harness.items, movie)
	if refreshed.Overview == nil || *refreshed.Overview != kept {
		t.Fatalf("FullRefresh overview = %v, want the stored %q", refreshed.Overview, kept)
	}
	if refreshed.DateLastRefreshed == nil || !refreshed.DateLastRefreshed.Equal(harness.now) {
		t.Fatalf("FullRefresh DateLastRefreshed = %v, want %v", refreshed.DateLastRefreshed, harness.now)
	}
}

// TestScannerRetriesRemoteMetadataAfterFailure proves that a failed remote
// provider leaves DateLastRefreshed unset, so the next routine scan retries,
// and that a successful refresh then stops the retries.
func TestScannerRetriesRemoteMetadataAfterFailure(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	movie := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	harness.remote.fail.Store(true)
	harness.scan(t, library.DefaultRefreshOptions())
	if got := scannerItemAtPath(t, harness.items, movie).DateLastRefreshed; got != nil {
		t.Fatalf("failed refresh DateLastRefreshed = %v, want none", got)
	}

	harness.remote.fail.Store(false)
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	if got := harness.remote.calls.Load(); got != 2 {
		t.Fatalf("TMDb invocations = %d, want a retry on the second scan", got)
	}
	if got := scannerItemAtPath(t, harness.items, movie); got.Overview == nil || got.DateLastRefreshed == nil {
		t.Fatalf("retried item = overview %v, DateLastRefreshed %v; want both set", got.Overview, got.DateLastRefreshed)
	}

	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.DefaultRefreshOptions())
	if got := harness.remote.calls.Load(); got != 2 {
		t.Fatalf("TMDb invocations = %d, want no call after the successful refresh", got)
	}
}

// TestScannerTargetedFullRefreshCallsRemoteForTargetOnly proves that an item
// FullRefresh sends only that item to the remote providers, as
// ProviderManager.RefreshItem does, although the scanner scans its whole
// library.
func TestScannerTargetedFullRefreshCallsRemoteForTargetOnly(t *testing.T) {
	harness := newStoredMetadataHarness(t)
	matrix := harness.writeAt(t, "The Matrix (1999).mkv", "matrix", harness.now.Add(-24*time.Hour))
	harness.writeAt(t, "The Emperors New Groove (2000).mkv", "groove", harness.now.Add(-24*time.Hour))
	harness.scan(t, library.DefaultRefreshOptions())
	invocations := harness.remote.calls.Load()
	if invocations != 2 {
		t.Fatalf("first scan TMDb invocations = %d, want 2", invocations)
	}

	target := scannerItemAtPath(t, harness.items, matrix)
	harness.now = harness.now.Add(time.Hour)
	harness.scan(t, library.RefreshOptions{
		Metadata: library.RefreshModeFullRefresh,
		Image:    library.RefreshModeFullRefresh,
		Targets:  []library.RefreshTarget{{ItemID: target.ID, Path: matrix}},
	})
	if got := harness.remote.calls.Load() - invocations; got != 1 {
		t.Fatalf("targeted FullRefresh TMDb invocations = %d, want one for the target only", got)
	}
}
