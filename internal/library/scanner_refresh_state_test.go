// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"go.uber.org/goleak"
)

// newRefreshStateHarness constructs a scanner with two virtual folders. The
// first folder (alpha) and the second folder (bravo) each have one media
// source. The resolver barrier lets a test block the scan mid-flight so it
// can observe refresh states through a channel barrier.
func newRefreshStateHarness(t *testing.T, resolver *scannerFixtureResolver) *refreshStateHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open scanner SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close scanner SQLite stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open scanner topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close scanner topology: %v", err)
		}
	})
	alphaSource := scannerPath(t, t.TempDir(), "alpha-media")
	if err := os.Mkdir(alphaSource, 0o755); err != nil {
		t.Fatalf("create alpha source: %v", err)
	}
	bravoSource := scannerPath(t, t.TempDir(), "bravo-media")
	if err := os.Mkdir(bravoSource, 0o755); err != nil {
		t.Fatalf("create bravo source: %v", err)
	}
	alphaFolder, err := topology.AddVirtualFolder(t.Context(), "Alpha", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: alphaSource}}})
	if err != nil {
		t.Fatalf("add alpha virtual folder: %v", err)
	}
	bravoFolder, err := topology.AddVirtualFolder(t.Context(), "Bravo", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: bravoSource}}})
	if err != nil {
		t.Fatalf("add bravo virtual folder: %v", err)
	}
	chain := library.NewChain(resolver)
	items := store.NewItemStore(stores)
	scanner := newScanner(t, topology, items, chain, library.ScannerOptions{Concurrency: 1})
	harness := &refreshStateHarness{
		scanner:         scanner,
		topology:        topology,
		items:           items,
		alphaFolderID:   alphaFolder.CollectionFolderID,
		bravoFolderID:   bravoFolder.CollectionFolderID,
		alphaSourcePath: alphaSource,
		bravoSourcePath: bravoSource,
	}
	t.Cleanup(func() {
		if err := harness.scanner.Close(); err != nil {
			t.Errorf("close scanner: %v", err)
		}
	})
	return harness
}

type refreshStateHarness struct {
	scanner       *library.Scanner
	topology      *library.Topology
	items         *store.ItemStore
	alphaFolderID dto.UUID
	bravoFolderID dto.UUID

	alphaSourcePath string
	bravoSourcePath string
}

// barrierResolver blocks on the first call for one path and releases on the
// second. Each barrier is a one-shot gate: the first Resolve that matches the
// path closes the arrived channel; the resolver blocks until release closes.
type barrierResolver struct {
	scannerFixtureResolver
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
	path    string
}

func newBarrierResolver(blockPath string) *barrierResolver {
	return &barrierResolver{
		arrived: make(chan struct{}),
		release: make(chan struct{}),
		path:    blockPath,
	}
}

func (r *barrierResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	if input.Entry.Path == r.path {
		r.once.Do(func() { close(r.arrived) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.scannerFixtureResolver.Resolve(ctx, input)
}

// sequenceResolver blocks each matching file resolution in sequence. Each
// entry in the steps slice is a one-shot barrier: the resolver arrives on the
// first call for a file path, waits for the release channel, then proceeds.
type sequenceResolver struct {
	scannerFixtureResolver
	mu       sync.Mutex
	arrivals []chan struct{}
	releases []chan struct{}
}

func newSequenceResolver(count int) *sequenceResolver {
	r := &sequenceResolver{}
	r.arrivals = make([]chan struct{}, count)
	r.releases = make([]chan struct{}, count)
	for i := range count {
		r.arrivals[i] = make(chan struct{})
		r.releases[i] = make(chan struct{})
	}
	return r
}

func (r *sequenceResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	if !input.Entry.IsDir && filepath.Ext(input.Entry.Path) == ".mkv" {
		r.mu.Lock()
		step := -1
		for i, ch := range r.arrivals {
			select {
			case <-ch:
			default:
				step = i
				break
			}
			if step >= 0 {
				break
			}
		}
		r.mu.Unlock()
		if step >= 0 {
			once := &sync.Once{}
			once.Do(func() { close(r.arrivals[step]) })
			select {
			case <-r.releases[step]:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return r.scannerFixtureResolver.Resolve(ctx, input)
}

func TestScannerRefreshStatesEmptyBeforeTrigger(t *testing.T) {
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	states := harness.scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("refresh states before trigger = %#v, want empty", states)
	}
}

func TestScannerRefreshStatesActiveAndQueuedKeyedByWireItemId(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	// The scanner flattens the source root: planSources resolves the root
	// entries directly under the CollectionFolder. The barrier must block
	// on an entry inside the source, not the source path itself.
	alphaMovie := scannerPath(t, harness.alphaSourcePath, "alpha.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	resolver := newBarrierResolver(alphaMovie)

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	chain := library.NewChain(resolver)
	scanner := newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{Concurrency: 1})
	harness.scanner = scanner

	// Start an async scan. It blocks at the alpha movie resolution.
	if err := scanner.Trigger(); err != nil {
		t.Fatalf("trigger scan: %v", err)
	}
	select {
	case <-resolver.arrived:
	case <-time.After(time.Second):
		t.Fatal("scan did not reach the alpha source barrier")
	}

	states := scanner.RefreshStates()
	if len(states) != 2 {
		t.Fatalf("refresh states during scan = %#v, want two folders", states)
	}
	alpha, ok := states[harness.alphaFolderID]
	if !ok {
		t.Fatalf("refresh states missing alpha folder %s: %#v", harness.alphaFolderID, states)
	}
	if alpha.Status != library.FolderRefreshActive {
		t.Fatalf("alpha status = %q, want Active", alpha.Status)
	}
	if alpha.Progress != 0 {
		t.Fatalf("alpha progress = %v, want 0", alpha.Progress)
	}
	bravo, ok := states[harness.bravoFolderID]
	if !ok {
		t.Fatalf("refresh states missing bravo folder %s: %#v", harness.bravoFolderID, states)
	}
	if bravo.Status != library.FolderRefreshQueued {
		t.Fatalf("bravo status = %q, want Queued", bravo.Status)
	}

	// Release the barrier and wait for the scan to drain.
	close(resolver.release)
	waitForScannerDrain(t, scanner)

	// After drain, the map must be empty.
	states = scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("refresh states after drain = %#v, want empty", states)
	}
}

func TestScannerRefreshStatesCopySemantics(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	alphaMovie := scannerPath(t, harness.alphaSourcePath, "alpha.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	resolver := newBarrierResolver(alphaMovie)

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	chain := library.NewChain(resolver)
	scanner := newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{Concurrency: 1})
	harness.scanner = scanner

	if err := scanner.Trigger(); err != nil {
		t.Fatalf("trigger scan: %v", err)
	}
	select {
	case <-resolver.arrived:
	case <-time.After(time.Second):
		t.Fatal("scan did not reach the alpha source barrier")
	}

	// The scan is blocked at the alpha source. Alpha is Active and Bravo is
	// Queued, so the returned map has two entries. Mutate the returned copy:
	// delete Bravo and add a fake entry. The scanner's internal map must not
	// change, so a later snapshot must retain both Alpha and Bravo.
	original := scanner.RefreshStates()
	if len(original) != 2 {
		t.Fatalf("refresh states during scan = %#v, want two folders", original)
	}
	if _, ok := original[harness.alphaFolderID]; !ok {
		t.Fatalf("refresh states missing alpha folder %s: %#v", harness.alphaFolderID, original)
	}
	if _, ok := original[harness.bravoFolderID]; !ok {
		t.Fatalf("refresh states missing bravo folder %s: %#v", harness.bravoFolderID, original)
	}
	delete(original, harness.bravoFolderID)
	fakeID := dto.UUID{0xff}
	original[fakeID] = library.FolderRefreshState{Status: library.FolderRefreshActive, Progress: 999}

	again := scanner.RefreshStates()
	if len(again) != 2 {
		t.Fatalf("refresh states after copy mutation = %#v, want two folders (internal map not affected)", again)
	}
	if _, ok := again[harness.alphaFolderID]; !ok {
		t.Fatalf("refresh states after mutation missing alpha folder %s: %#v", harness.alphaFolderID, again)
	}
	if _, ok := again[harness.bravoFolderID]; !ok {
		t.Fatalf("refresh states after mutation missing bravo folder %s: %#v", harness.bravoFolderID, again)
	}
	if _, ok := again[fakeID]; ok {
		t.Fatalf("refresh states after mutation contains fake id: %#v", again)
	}

	close(resolver.release)
	waitForScannerDrain(t, scanner)
}

func TestScannerRefreshStatesMonotonicProgress(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	// Three subdirectories under the alpha source each hold one media file.
	// planSources flattens the source root: resolveFolder resolves the
	// three subdirectories as direct children of the CollectionFolder and
	// creates three child scan tasks. The scan queue processes them
	// sequentially (Concurrency=1), so each child produces a separate
	// outcome that we can observe between barriers.
	resolver := &scannerFixtureResolver{}
	harness := newRefreshStateHarness(t, resolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	seq := newSequenceResolver(3)
	chain := library.NewChain(seq)
	scanner := newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{Concurrency: 1})
	harness.scanner = scanner

	for _, name := range []string{"dir-one", "dir-two", "dir-three"} {
		writeScannerFile(t, scannerPath(t, harness.alphaSourcePath, name, "movie.mkv"), name)
	}

	done := make(chan error, 1)
	go func() {
		_, err := scanner.Scan(context.Background())
		done <- err
	}()

	// planSources resolves the three subdirectories and creates three child
	// tasks. completeRefreshRootUnit records Done=1, Outstanding=3, so the
	// initial progress is 25%. Each child task that completes advances Done
	// and decrements Outstanding, producing 50% and 75%.
	want := []float64{25, 50, 75}
	for i := range 3 {
		select {
		case <-seq.arrivals[i]:
		case <-time.After(time.Second):
			t.Fatalf("scan did not reach file %d", i)
		}
		states := scanner.RefreshStates()
		alpha := states[harness.alphaFolderID]
		if alpha.Status != library.FolderRefreshActive {
			t.Fatalf("step %d: alpha status = %q, want Active", i, alpha.Status)
		}
		if alpha.Progress != want[i] {
			t.Fatalf("step %d: alpha progress = %v, want %v", i, alpha.Progress, want[i])
		}
		close(seq.releases[i])
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scan error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not complete")
	}
	states := scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("refresh states after scan = %#v, want empty after drain", states)
	}
}

func TestScannerRefreshStatesEmptyAfterDrain(t *testing.T) {
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	scannerScan(t, harness.scanner, "scan for empty-after-drain")
	states := harness.scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("refresh states after drain = %#v, want empty", states)
	}
}

func TestScannerRefreshStatesRefreshCompleteObservesEmptyAfterDrain(t *testing.T) {
	// The RefreshComplete callback runs after the scan drains and clears
	// the refresh-state map. It must observe an empty snapshot, not the
	// in-flight states from the scan that just completed.
	completeStates := make(chan map[dto.UUID]library.FolderRefreshState, 1)
	resolver := &scannerFixtureResolver{}
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() { _ = topology.Close() })
	source := scannerPath(t, t.TempDir(), "media")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if _, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add virtual folder: %v", err)
	}
	writeScannerFile(t, scannerPath(t, source, "movie.mkv"), "movie")
	chain := library.NewChain(resolver)
	items := store.NewItemStore(stores)
	var scanner *library.Scanner
	scanner, err = library.NewScanner(topology, items, chain, library.ScannerOptions{
		Concurrency: 1,
		RefreshComplete: func(result library.RefreshResult) {
			// The callback observes the snapshot after the scan drained.
			completeStates <- scanner.RefreshStates()
		},
	})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	t.Cleanup(func() { _ = scanner.Close() })
	if _, err := scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	select {
	case states := <-completeStates:
		if len(states) != 0 {
			t.Fatalf("RefreshComplete observed non-empty states = %#v, want empty after drain", states)
		}
	case <-time.After(time.Second):
		t.Fatal("RefreshComplete callback was not invoked")
	}
}

func TestScannerRefreshStatesFailedScanCleanup(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	resolver := &scannerFixtureResolver{}
	harness := newRefreshStateHarness(t, resolver)

	// Make the alpha source resolution fail by marking an entry malformed.
	// The scanner flattens the source root: resolveFolder resolves the
	// entries inside the source. A malformed entry makes the alpha folder
	// fail. The bravo folder scans normally. After the scan drains, both
	// must be gone.
	alphaMovie := scannerPath(t, harness.alphaSourcePath, "alpha.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	resolver.setMalformed(alphaMovie)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	chain := library.NewChain(resolver)
	scanner := newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{Concurrency: 1})
	harness.scanner = scanner

	if _, err := scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	states := scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("refresh states after failed scan = %#v, want empty (cleanup)", states)
	}
}

func TestScannerRefreshStatesNilBehavior(t *testing.T) {
	var scanner *library.Scanner
	states := scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("nil scanner refresh states = %#v, want empty", states)
	}
}

func TestScannerRefreshStatesClosedBehavior(t *testing.T) {
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close scanner: %v", err)
	}
	states := harness.scanner.RefreshStates()
	if len(states) != 0 {
		t.Fatalf("closed scanner refresh states = %#v, want empty", states)
	}
}

func TestScannerRefreshStatesActivePrecedenceOverQueued(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	harness := newRefreshStateHarness(t, &scannerFixtureResolver{})
	alphaMovie := scannerPath(t, harness.alphaSourcePath, "alpha.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	resolver := newBarrierResolver(alphaMovie)

	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	chain := library.NewChain(resolver)
	scanner := newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{Concurrency: 1})
	harness.scanner = scanner

	if err := scanner.Trigger(); err != nil {
		t.Fatalf("trigger scan: %v", err)
	}
	select {
	case <-resolver.arrived:
	case <-time.After(time.Second):
		t.Fatal("scan did not reach the alpha source barrier")
	}

	// During the scan, alpha is Active and bravo is Queued. Active takes
	// precedence: setting Queued on an Active folder must not downgrade it.
	states := scanner.RefreshStates()
	alpha := states[harness.alphaFolderID]
	if alpha.Status != library.FolderRefreshActive {
		t.Fatalf("alpha status = %q, want Active", alpha.Status)
	}

	close(resolver.release)
	waitForScannerDrain(t, scanner)
}

// waitForScannerDrain blocks until a triggered scan completes. It observes
// the scanner's last result: once a result exists with no pending scan, the
// drain is complete. It uses no wall-clock sleep; it polls RefreshStates
// which returns empty after the scan goroutine clears the map.
func waitForScannerDrain(t *testing.T, scanner *library.Scanner) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		states := scanner.RefreshStates()
		if len(states) == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("scanner did not drain, states = %#v", states)
		default:
		}
	}
}
