// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/library"
	"go.uber.org/goleak"
)

func TestScannerCloseContextReturnsOnCancellationUntilBlockedResolverDrains(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	releaseWork := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWork)

	resolver := &scannerFixtureResolver{
		block: func(_ context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			enteredOnce.Do(func() { close(entered) })
			<-release
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "blocked", "movie.mkv"), "blocked")

	scanDone := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(context.Background())
		scanDone <- err
	}()
	awaitScannerCloseSignal(t, entered, "scanner did not reach blocked resolver")

	closeContext, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	closeDone := make(chan error, 1)
	go func() { closeDone <- harness.scanner.CloseContext(closeContext) }()
	if err := awaitScannerCloseResult(t, closeDone, "canceled CloseContext did not return while resolver remained blocked"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CloseContext error = %v, want context.Canceled", err)
	}

	select {
	case err := <-scanDone:
		t.Fatalf("scan returned before blocked resolver was released: %v", err)
	default:
	}

	releaseWork()
	if err := awaitScannerCloseResult(t, scanDone, "scan did not drain after blocked resolver release"); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan error after CloseContext = %v, want context.Canceled", err)
	}
	if err := harness.scanner.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext after worker drain: %v", err)
	}
}

func TestScannerCloseContextIsSafeAcrossConcurrentCalls(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	releaseWork := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWork)

	resolver := &scannerFixtureResolver{
		block: func(_ context.Context, path string) {
			if filepath.Ext(path) != ".mkv" {
				return
			}
			enteredOnce.Do(func() { close(entered) })
			<-release
		},
	}
	harness := newScannerHarness(t, resolver)
	writeScannerFile(t, scannerPath(t, harness.source, "blocked", "movie.mkv"), "blocked")

	scanDone := make(chan error, 1)
	go func() {
		_, err := harness.scanner.Scan(context.Background())
		scanDone <- err
	}()
	awaitScannerCloseSignal(t, entered, "scanner did not reach blocked resolver")

	const concurrentClosers = 8
	closeContext, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	closeResults := make(chan error, concurrentClosers)
	for range concurrentClosers {
		go func() { closeResults <- harness.scanner.CloseContext(closeContext) }()
	}
	for range concurrentClosers {
		if err := awaitScannerCloseResult(t, closeResults, "concurrent canceled CloseContext did not return while resolver remained blocked"); !errors.Is(err, context.Canceled) {
			t.Fatalf("concurrent canceled CloseContext error = %v, want context.Canceled", err)
		}
	}

	releaseWork()
	if err := awaitScannerCloseResult(t, scanDone, "scan did not drain after concurrent close release"); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan error after concurrent CloseContext = %v, want context.Canceled", err)
	}

	closeResults = make(chan error, concurrentClosers)
	for range concurrentClosers {
		go func() { closeResults <- harness.scanner.CloseContext(context.Background()) }()
	}
	for range concurrentClosers {
		if err := awaitScannerCloseResult(t, closeResults, "concurrent CloseContext did not finish after worker drain"); err != nil {
			t.Fatalf("concurrent CloseContext after worker drain: %v", err)
		}
	}
}

// TestScannerCloseContextWaitsForAsyncRefreshCompletion verifies shutdown owns
// the successful completion callback, not merely scan work or progress
// delivery. A callback can still hand off its result after CloseContext begins.
func TestScannerCloseContextWaitsForAsyncRefreshCompletion(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	writeScannerFile(t, scannerPath(t, harness.source, "completion", "movie.mkv"), "completion")

	callbackEntered := make(chan struct{})
	verifyClosed := make(chan struct{})
	callbackChecked := make(chan error, 1)
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(release)

	var scanner *library.Scanner
	scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		RefreshComplete: func(library.RefreshResult) {
			close(callbackEntered)
			<-verifyClosed
			callbackChecked <- scanner.Trigger()
			<-releaseCallback
		},
	})
	harness.scanner = scanner
	if err := scanner.Trigger(); err != nil {
		t.Fatalf("trigger successful async scan: %v", err)
	}
	awaitScannerCloseSignal(t, callbackEntered, "successful async scan did not enter RefreshComplete")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanner.CloseContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CloseContext before RefreshComplete release = %v, want context.Canceled", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- scanner.CloseContext(context.Background()) }()
	close(verifyClosed)
	if err := awaitScannerCloseResult(t, callbackChecked, "callback did not observe scanner closure"); !errors.Is(err, library.ErrScannerClosed) {
		t.Fatalf("Trigger from blocked completion after CloseContext began = %v, want ErrScannerClosed", err)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("CloseContext returned while RefreshComplete remained blocked: %v", err)
	default:
	}

	release()
	if err := awaitScannerCloseResult(t, closeDone, "CloseContext did not finish after RefreshComplete release"); err != nil {
		t.Fatalf("CloseContext after RefreshComplete release: %v", err)
	}
}

// TestScannerCloseContextWaitsForSynchronousRefreshCompletion applies the
// same ownership rule to ScanWithOptions, whose caller drains queued work
// synchronously instead of through the background worker.
func TestScannerCloseContextWaitsForSynchronousRefreshCompletion(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	writeScannerFile(t, scannerPath(t, harness.source, "synchronous-completion", "movie.mkv"), "completion")

	callbackEntered := make(chan struct{})
	verifyClosed := make(chan struct{})
	callbackChecked := make(chan error, 1)
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(release)

	var scanner *library.Scanner
	scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		RefreshComplete: func(library.RefreshResult) {
			close(callbackEntered)
			<-verifyClosed
			callbackChecked <- scanner.Trigger()
			<-releaseCallback
		},
	})
	harness.scanner = scanner
	scanDone := make(chan error, 1)
	go func() {
		_, err := scanner.ScanWithOptions(context.Background(), library.DefaultRefreshOptions())
		scanDone <- err
	}()
	awaitScannerCloseSignal(t, callbackEntered, "successful synchronous scan did not enter RefreshComplete")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanner.CloseContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CloseContext before synchronous RefreshComplete release = %v, want context.Canceled", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- scanner.CloseContext(context.Background()) }()
	close(verifyClosed)
	if err := awaitScannerCloseResult(t, callbackChecked, "synchronous callback did not observe scanner closure"); !errors.Is(err, library.ErrScannerClosed) {
		t.Fatalf("Trigger from blocked synchronous completion after CloseContext began = %v, want ErrScannerClosed", err)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("CloseContext returned while synchronous RefreshComplete remained blocked: %v", err)
	default:
	}
	select {
	case err := <-scanDone:
		t.Fatalf("ScanWithOptions returned while RefreshComplete remained blocked: %v", err)
	default:
	}

	release()
	if err := awaitScannerCloseResult(t, scanDone, "ScanWithOptions did not finish after RefreshComplete release"); err != nil {
		t.Fatalf("ScanWithOptions after RefreshComplete release: %v", err)
	}
	if err := awaitScannerCloseResult(t, closeDone, "CloseContext did not finish after synchronous RefreshComplete release"); err != nil {
		t.Fatalf("CloseContext after synchronous RefreshComplete release: %v", err)
	}
}

func awaitScannerCloseSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func awaitScannerCloseResult(t *testing.T, result <-chan error, failure string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal(failure)
		return nil
	}
}
