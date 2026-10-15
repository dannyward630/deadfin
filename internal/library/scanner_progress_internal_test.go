// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"testing"
)

// TestProgressTrackerHoldsMutexThroughCallback proves that progressTracker
// holds its mutex through the callback. The test uses channel entry and
// blocking instead of a wall-clock wait.
//
// The callback signals entry on the entry channel and then blocks on the
// release channel. While the callback is blocked, the test calls
// t.mu.TryLock. A successful TryLock means that the tracker released its
// mutex before the callback, and the test fails. A failed TryLock proves
// that the tracker holds the mutex through the callback.
//
// This test fails against the head that releases the mutex before the
// callback (50e4d01) and passes after the tracker holds the mutex through
// the callback (30b4c77).
func TestProgressTrackerHoldsMutexThroughCallback(t *testing.T) {
	entry := make(chan struct{})
	release := make(chan struct{})

	tracker := newProgressTracker(func(float64) {
		// Signal that the callback has started and the tracker has
		// reached the callback call site.
		entry <- struct{}{}
		// Block until the test has checked the mutex state.
		<-release
	})

	// Register two items so complete(1) emits a value in (0,100) and
	// reaches the callback. Without a discovered denominator, emitLocked
	// returns 0 and the tracker never calls the callback.
	tracker.register(2)

	// Start complete in a goroutine. It takes the mutex, advances the
	// counter, computes the value, and then calls the callback while the
	// mutex stays held (or not, at the broken head).
	done := make(chan struct{})
	go func() {
		tracker.complete(1)
		close(done)
	}()

	// Wait until the callback has started. This proves that complete has
	// reached the callback call site. No wall-clock wait is used.
	<-entry

	// While the callback is blocked on release, the tracker must still
	// hold its mutex. TryLock returns true only when the mutex is free.
	// A true result means that the tracker released the mutex before the
	// callback, which lets two concurrent workers call the callback at the
	// same time. That is the defect, so the test fails.
	if tracker.mu.TryLock() {
		tracker.mu.Unlock()
		t.Fatal("tracker released its mutex before the callback; the mutex must stay held through the callback")
	}

	// Unblock the callback and wait for complete to return.
	close(release)
	<-done
}
