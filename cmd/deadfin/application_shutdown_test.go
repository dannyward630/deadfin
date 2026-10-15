// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// TestApplicationCloseHonorsCanceledContextBeforeClosingDependentStores keeps
// a scanner worker deliberately inside its non-context-aware filesystem seam.
// Close must return the caller's cancellation without releasing stores that the
// worker can still reach; a later close after the worker exits owns cleanup.
func TestApplicationCloseHonorsCanceledContextBeforeClosingDependentStores(t *testing.T) {
	app, err := newApplication(t.Context(), filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("create application: %v", err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseWorker()
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("cleanup application: %v", err)
		}
	})

	source := t.TempDir()
	if _, err := app.topology.AddVirtualFolder(t.Context(), "blocked", "movies", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: source}},
	}); err != nil {
		t.Fatalf("add blocked scan source: %v", err)
	}

	oldScanner := app.scanner
	if err := oldScanner.Close(); err != nil {
		t.Fatalf("close initial scanner: %v", err)
	}
	workerStarted := make(chan struct{})
	var workerStartedOnce sync.Once
	blockedScanner, err := library.NewScanner(
		app.topology,
		store.NewItemStore(app.stores),
		library.NewDefaultChain(naming.DefaultOptions()),
		library.ScannerOptions{
			Concurrency: 1,
			ReadDir: func(string) ([]library.Entry, error) {
				workerStartedOnce.Do(func() { close(workerStarted) })
				<-release
				return nil, errors.New("controlled scan blocker released")
			},
		},
	)
	if err != nil {
		t.Fatalf("create blocked scanner: %v", err)
	}
	app.scanner = blockedScanner

	trackedStores := &applicationCloseTrackingStores{
		Stores: app.stores,
		closed: make(chan struct{}),
	}
	app.stores = trackedStores

	scanDone := make(chan error, 1)
	go func() {
		_, err := app.scanner.Scan(context.Background())
		scanDone <- err
	}()
	select {
	case <-workerStarted:
	case <-t.Context().Done():
		t.Fatalf("wait for controlled scan blocker: %v", t.Context().Err())
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- app.Close(canceled) }()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	select {
	case err := <-closeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("close with canceled context error = %v, want context.Canceled", err)
		}
	case <-deadline.C:
		releaseWorker()
		<-closeDone
		t.Fatal("close with canceled context waited for the blocked scanner worker")
	}

	select {
	case <-trackedStores.closed:
		t.Fatal("close with canceled context closed stores while scanner worker was live")
	default:
	}

	releaseWorker()
	if err := <-scanDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("released scanner error = %v, want context.Canceled", err)
	}
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("close application after scanner worker release: %v", err)
	}
	select {
	case <-trackedStores.closed:
	default:
		t.Fatal("application cleanup did not close dependent stores after scanner worker exited")
	}
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("repeat application close: %v", err)
	}
}

// TestApplicationCloseCancelsBlockedScannerWhenSchedulerDrainExpires pins the
// order of shutdown requests. A scheduler worker that ignores its context
// keeps Scheduler.Close from draining, so Close with an expired context sees
// the scheduler's ctx error first. Close must still seal and cancel the
// scanner before it returns; it must not close dependents or drop the owned
// workers a later Close has to drain.
func TestApplicationCloseCancelsBlockedScannerWhenSchedulerDrainExpires(t *testing.T) {
	schedulerRelease := make(chan struct{})
	var schedulerReleaseOnce sync.Once
	releaseScheduler := func() { schedulerReleaseOnce.Do(func() { close(schedulerRelease) }) }
	schedulerStarted := make(chan struct{}, 1)
	taskDef := tasks.Definition{
		ID:   "shutdown-order-000000000000000000000000001",
		Key:  "shutdown-order",
		Name: "Shutdown Order Test",
	}
	app, err := newApplicationWithListen(t.Context(), filepath.Join(t.TempDir(), "program-data"), defaultListenAddress,
		withScheduledTask(taskDef, applicationIgnoringContextRunnable{block: schedulerRelease, started: schedulerStarted}))
	if err != nil {
		t.Fatalf("create application: %v", err)
	}

	scannerRelease := make(chan struct{})
	var scannerReleaseOnce sync.Once
	releaseScanner := func() { scannerReleaseOnce.Do(func() { close(scannerRelease) }) }
	t.Cleanup(func() {
		releaseScanner()
		releaseScheduler()
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("cleanup application: %v", err)
		}
	})

	if err := app.scheduler.Run(taskDef.ID); err != nil {
		t.Fatalf("run blocking scheduled task: %v", err)
	}
	awaitApplicationShutdownSignal(t, schedulerStarted, "blocking scheduled task did not start")

	source := t.TempDir()
	if _, err := app.topology.AddVirtualFolder(t.Context(), "blocked", "movies", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: source}},
	}); err != nil {
		t.Fatalf("add blocked scan source: %v", err)
	}
	if err := app.scanner.Close(); err != nil {
		t.Fatalf("close initial scanner: %v", err)
	}
	workerStarted := make(chan struct{})
	var workerStartedOnce sync.Once
	blockedScanner, err := library.NewScanner(
		app.topology,
		store.NewItemStore(app.stores),
		library.NewDefaultChain(naming.DefaultOptions()),
		library.ScannerOptions{
			Concurrency: 1,
			ReadDir: func(string) ([]library.Entry, error) {
				workerStartedOnce.Do(func() { close(workerStarted) })
				<-scannerRelease
				return nil, errors.New("controlled scan blocker released")
			},
		},
	)
	if err != nil {
		t.Fatalf("create blocked scanner: %v", err)
	}
	app.scanner = blockedScanner

	trackedStores := &applicationCloseTrackingStores{
		Stores: app.stores,
		closed: make(chan struct{}),
	}
	app.stores = trackedStores

	scanDone := make(chan error, 1)
	go func() {
		_, err := app.scanner.Scan(context.Background())
		scanDone <- err
	}()
	awaitApplicationShutdownSignal(t, workerStarted, "controlled scan blocker did not start")

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- app.Close(canceled) }()
	closeErr := awaitApplicationShutdownResult(t, closeDone, "close with canceled context waited for blocked workers")
	if !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("close with canceled context error = %v, want context.Canceled", closeErr)
	}

	// Nothing is released yet: the scanner must already be sealed.
	if err := app.scanner.Trigger(); !errors.Is(err, library.ErrScannerClosed) {
		t.Fatalf("scanner Trigger after expired close error = %v, want library.ErrScannerClosed", err)
	}
	if app.scheduler == nil {
		t.Fatal("expired close dropped the undrained scheduler")
	}
	select {
	case <-trackedStores.closed:
		t.Fatal("close with canceled context closed stores while workers were live")
	default:
	}

	releaseScanner()
	if err := <-scanDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("released scanner error = %v, want context.Canceled", err)
	}

	releaseScheduler()
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("close application after worker release: %v", err)
	}
	select {
	case <-trackedStores.closed:
	default:
		t.Fatal("application cleanup did not close dependent stores after workers exited")
	}
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("repeat application close: %v", err)
	}
}

// applicationIgnoringContextRunnable signals started, then blocks on release
// and ignores its context, so Scheduler.Close cannot drain until released.
type applicationIgnoringContextRunnable struct {
	block   <-chan struct{}
	started chan<- struct{}
}

func (r applicationIgnoringContextRunnable) Run(context.Context, func(float64)) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.block
	return nil
}

// TestApplicationCloseDeliversRefreshRequestsBeforeRunnerShutdown pins the
// scanner-to-runner handoff across the application shutdown boundary. The
// completion callback is deliberately held after a successful async refresh;
// an expired Close may begin scanner shutdown but must leave the runner open
// until the callback submits every requested item.
func TestApplicationCloseDeliversRefreshRequestsBeforeRunnerShutdown(t *testing.T) {
	app, err := newApplication(t.Context(), filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("create application: %v", err)
	}
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(func() {
		release()
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("cleanup application: %v", err)
		}
	})

	if err := app.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	requested := []dto.UUID{{0x11}, {0x22}}
	executable := &applicationRefreshCompletionExecutable{}
	app.trickplayRunner = tasks.NewTrickplayRunner(executable, nil)
	callbackEntered := make(chan struct{})
	source := t.TempDir()
	if _, err := app.topology.AddVirtualFolder(t.Context(), "completion", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add refresh completion source: %v", err)
	}
	scanner, err := library.NewScanner(app.topology, store.NewItemStore(app.stores), library.NewChain(), library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     func(string) ([]library.Entry, error) { return nil, nil },
		RefreshComplete: func(result library.RefreshResult) {
			close(callbackEntered)
			<-releaseCallback
			dispatchRefreshTrickplay(result, app.trickplayRunner.TriggerRegenerateTrickplay)
		},
	})
	if err != nil {
		t.Fatalf("create completion-blocked scanner: %v", err)
	}
	app.scanner = scanner
	if err := scanner.TriggerRefresh(library.RefreshOptions{RegenerateTrickplayItemIDs: requested}); err != nil {
		t.Fatalf("trigger successful async refresh: %v", err)
	}
	awaitApplicationShutdownSignal(t, callbackEntered, "successful refresh did not enter completion callback")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with blocked completion = %v, want context.Canceled", err)
	}
	if app.trickplayRunner == nil {
		t.Fatal("expired scanner drain closed trickplay runner before RefreshComplete dispatch")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- app.Close(context.Background()) }()
	release()
	if err := awaitApplicationShutdownResult(t, closeDone, "application Close did not finish after RefreshComplete dispatch"); err != nil {
		t.Fatalf("Close after RefreshComplete dispatch: %v", err)
	}
	if got := executable.Requested(); len(got) != len(requested) || got[0] != requested[0] || got[1] != requested[1] {
		t.Fatalf("runner accepted refresh IDs = %#v, want %#v before runner Close", got, requested)
	}
}

// TestApplicationCloseKeepsTrickplayDependenciesOpenAfterScannerTimeout
// verifies a scanner drain timeout does not tear down components reachable by
// a live callback. A later Close joins that callback and performs normal
// dependency cleanup.
func TestApplicationCloseKeepsTrickplayDependenciesOpenAfterScannerTimeout(t *testing.T) {
	app, err := newApplicationWithListen(
		t.Context(),
		filepath.Join(t.TempDir(), "program-data"),
		"127.0.0.1:8096",
		withTileComposer(&images.TileComposer{}),
	)
	if err != nil {
		t.Fatalf("create application with trickplay dependencies: %v", err)
	}
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(func() {
		release()
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("cleanup application: %v", err)
		}
	})

	if err := app.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	source := t.TempDir()
	if _, err := app.topology.AddVirtualFolder(t.Context(), "timeout", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add timeout refresh source: %v", err)
	}
	callbackEntered := make(chan struct{})
	scanner, err := library.NewScanner(app.topology, store.NewItemStore(app.stores), library.NewChain(), library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     func(string) ([]library.Entry, error) { return nil, nil },
		RefreshComplete: func(library.RefreshResult) {
			close(callbackEntered)
			<-releaseCallback
		},
	})
	if err != nil {
		t.Fatalf("create timeout-blocked scanner: %v", err)
	}
	app.scanner = scanner
	if err := scanner.Trigger(); err != nil {
		t.Fatalf("trigger successful async scan: %v", err)
	}
	awaitApplicationShutdownSignal(t, callbackEntered, "successful scan did not enter completion callback")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with scanner drain timeout = %v, want context.Canceled", err)
	}
	if app.trickplayRunner == nil || app.trickplayGenerator == nil || app.trickplayReader == nil {
		t.Fatal("expired scanner drain closed trickplay dependency before callback drained")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- app.Close(context.Background()) }()
	release()
	if err := awaitApplicationShutdownResult(t, closeDone, "later application Close did not finish after scanner drain"); err != nil {
		t.Fatalf("later Close after scanner drain: %v", err)
	}
	if app.trickplayRunner != nil || app.trickplayGenerator != nil || app.trickplayReader != nil {
		t.Fatal("later Close left trickplay dependencies open after scanner drain")
	}
}

type applicationRefreshCompletionExecutable struct {
	mu        sync.Mutex
	requested []dto.UUID
}

func (executable *applicationRefreshCompletionExecutable) RequestRegenerateTrickplay(itemID dto.UUID) {
	executable.mu.Lock()
	defer executable.mu.Unlock()
	executable.requested = append(executable.requested, itemID)
}

func (executable *applicationRefreshCompletionExecutable) Execute(context.Context) error { return nil }

func (executable *applicationRefreshCompletionExecutable) Requested() []dto.UUID {
	executable.mu.Lock()
	defer executable.mu.Unlock()
	return append([]dto.UUID(nil), executable.requested...)
}

func awaitApplicationShutdownSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal(failure)
	}
}

func awaitApplicationShutdownResult(t *testing.T, result <-chan error, failure string) error {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal(failure)
		return nil
	}
}

type applicationCloseTrackingStores struct {
	store.Stores
	closed chan struct{}

	once sync.Once
	err  error
}

func (s *applicationCloseTrackingStores) Close() error {
	s.once.Do(func() {
		s.err = s.Stores.Close()
		close(s.closed)
	})
	return s.err
}
