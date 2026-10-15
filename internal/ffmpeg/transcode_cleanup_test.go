// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestCleanupInactiveDeletesOldFilesPreservesRecent(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewTranscodeManager(progressiveFakeFFmpeg(t, "hold"), dir, TranscodeManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)

	// Create an old file (mtime 2 days ago).
	oldFile := filepath.Join(manager.directory, "old.ts")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := clock.NewReal().Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}

	// Create a recent file (mtime now).
	recentFile := filepath.Join(manager.directory, "recent.ts")
	if err := os.WriteFile(recentFile, []byte("recent"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Cleanup with a 1-day cutoff: old file deleted, recent survives.
	cutoff := clock.NewReal().Now().Add(-24 * time.Hour)
	if err := manager.CleanupInactive(context.Background(), cutoff); err != nil {
		t.Fatalf("CleanupInactive: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file was not deleted")
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Error("recent file was deleted")
	}
}

func TestCleanupInactiveSkipsLiveJobOutput(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewTranscodeManager(progressiveFakeFFmpeg(t, "hold"), dir, TranscodeManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)

	// Simulate a live job by registering it in the jobs map directly.
	output := filepath.Join(manager.directory, "live.ts")
	if err := os.WriteFile(output, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := clock.NewReal().Now().Add(-48 * time.Hour)
	if err := os.Chtimes(output, past, past); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	manager.jobs["test-live"] = &transcodeJob{
		manager: manager,
		output:  output,
		state:   transcodeRunning,
	}
	manager.mu.Unlock()

	// Cleanup must not remove the live job's output even though it's old.
	cutoff := clock.NewReal().Now().Add(-24 * time.Hour)
	if err := manager.CleanupInactive(context.Background(), cutoff); err != nil {
		t.Fatalf("CleanupInactive: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Error("live job output was deleted")
	}
}

func TestCleanupInactiveReChecksLivenessUnderMutex(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewTranscodeManager(progressiveFakeFFmpeg(t, "hold"), dir, TranscodeManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)

	// An old file that will become a live job's output AFTER enumeration
	// but BEFORE the check-then-delete — proving the atomic re-check under
	// the mutex prevents the race.
	output := filepath.Join(manager.directory, "becoming-live.ts")
	if err := os.WriteFile(output, []byte("becoming"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := clock.NewReal().Now().Add(-48 * time.Hour)
	if err := os.Chtimes(output, past, past); err != nil {
		t.Fatal(err)
	}

	// Pause cleanup after enumeration so the test can make the output live
	// before the check-then-delete runs under the mutex.
	enumerated := make(chan struct{})
	resume := make(chan struct{})
	manager.afterCleanupEnumeration = func() {
		close(enumerated)
		<-resume
	}

	cleanupDone := make(chan error, 1)
	go func() {
		cutoff := clock.NewReal().Now().Add(-24 * time.Hour)
		cleanupDone <- manager.CleanupInactive(context.Background(), cutoff)
	}()

	// Wait for enumeration to complete, then register the job as live.
	<-enumerated
	manager.mu.Lock()
	manager.jobs["becoming-live"] = &transcodeJob{
		manager: manager,
		output:  output,
		state:   transcodeRunning,
	}
	manager.mu.Unlock()

	// Resume cleanup; the re-check under the mutex must find the live job.
	close(resume)
	if err := <-cleanupDone; err != nil {
		t.Fatalf("CleanupInactive: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Error("output that became live was deleted despite re-check under mutex")
	}
}
