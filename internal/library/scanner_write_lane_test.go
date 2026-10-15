// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestScannerStoreWriteLetsAnInteractiveWriteGoBetweenScanWrites models a
// login or a playback progress report during a scan. Four folder workers write
// in a loop through the scanner's write lane, and each scan transaction holds
// the single SQLite writer until the test releases it. An interactive write
// that arrives during a scan transaction must get the writer when that
// transaction commits, before the next scan transaction. The test controls
// each step, so it needs no clock and no sleep.
func TestScannerStoreWriteLetsAnInteractiveWriteGoBetweenScanWrites(t *testing.T) {
	stores, err := storesqlite.Open(t.Context(), filepath.Join(t.TempDir(), "lane.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	scanner := &Scanner{writeLane: make(chan struct{}, 1)}
	writer := stores.Writer()

	const workers, writesPerWorker = 4, 2
	events := make(chan string, workers*writesPerWorker+1)
	release := make(chan struct{})
	scanDone := make(chan error, workers)
	for range workers {
		go func() {
			for range writesPerWorker {
				err := scanner.storeWrite(t.Context(), func() error {
					tx, err := writer.BeginTx(t.Context(), nil)
					if err != nil {
						return err
					}
					events <- "scan"
					<-release
					return tx.Commit()
				})
				if err != nil {
					scanDone <- err
					return
				}
			}
			scanDone <- nil
		}()
	}

	if got := <-events; got != "scan" {
		t.Fatalf("first writer = %q, want a scan transaction", got)
	}
	interactiveDone := make(chan error, 1)
	go func() {
		tx, err := writer.BeginTx(context.Background(), nil)
		if err != nil {
			interactiveDone <- err
			return
		}
		events <- "interactive"
		interactiveDone <- tx.Commit()
	}()
	// Wait until the interactive write queues for the writer connection.
	for writer.Stats().WaitCount == 0 {
		runtime.Gosched()
	}

	release <- struct{}{}
	if got := <-events; got != "interactive" {
		t.Fatalf("writer after the first scan transaction = %q, want the waiting interactive write", got)
	}
	if err := <-interactiveDone; err != nil {
		t.Fatalf("interactive write: %v", err)
	}
	for remaining := workers*writesPerWorker - 1; remaining > 0; remaining-- {
		if got := <-events; got != "scan" {
			t.Fatalf("writer = %q, want a scan transaction", got)
		}
		release <- struct{}{}
	}
	for range workers {
		if err := <-scanDone; err != nil {
			t.Fatalf("scan write: %v", err)
		}
	}
}

func TestScannerStoreWriteReturnsWhenContextEndsWhileWaiting(t *testing.T) {
	scanner := &Scanner{writeLane: make(chan struct{}, 1)}
	scanner.writeLane <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	if err := scanner.storeWrite(ctx, func() error { called = true; return nil }); err != context.Canceled {
		t.Fatalf("storeWrite with ended context = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("storeWrite ran the write without the lane")
	}
}
