// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

func TestFsCleanRootedAndSymlinkSafe(t *testing.T) {
	root := t.TempDir()

	// Create an old file inside root.
	oldFile := filepath.Join(root, "old.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := clock.NewReal().Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}

	// Create a recent file inside root.
	recentFile := filepath.Join(root, "recent.txt")
	if err := os.WriteFile(recentFile, []byte("recent"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Explicitly set the recent file's mtime to now.
	// Set the recent file's mtime to 1 minute from now so it's clearly after olderThan.
	future := clock.NewReal().Now().Add(time.Minute)
	if err := os.Chtimes(recentFile, future, future); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside root pointing outside.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "link.txt")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skip("symlink not supported")
	}
	// Make the link old.
	if err := os.Chtimes(linkPath, past, past); err != nil {
		// symlinks may not support Chtimes on all platforms
		_ = err
	}
	// olderThan is now; the old file's mtime is 1h ago, the recent file's
	// mtime is now (set explicitly above). Fsclean deletes entries whose
	// mtime is *before* olderThan.
	olderThan := clock.NewReal().Now()

	err := tasks.FsClean(context.Background(), tasks.FsCleanOptions{
		Root:      root,
		OlderThan: olderThan,
	})
	if err != nil {
		t.Fatalf("FsClean: %v", err)
	}

	// Old file should be deleted.
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file was not deleted")
	}

	// Recent file should survive.
	if _, err := os.Stat(recentFile); err != nil {
		t.Error("recent file was deleted")
	}

	// Symlink should be removed (the link, not the target).
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Error("symlink was not removed")
	}

	// Target outside root should survive.
	if _, err := os.Stat(outsideFile); err != nil {
		t.Error("outside target was deleted — symlink safety violated")
	}
}

func TestFsCleanCancellationStopsMidWalk(t *testing.T) {
	root := t.TempDir()
	// Create files old enough to delete.
	past := clock.NewReal().Now().Add(-time.Hour)
	for i := range 10 {
		p := filepath.Join(root, "file"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	// Deterministic cancellation: cancel the context after the first removal
	// hook fires, so the walk has started and at least one file is processed
	// before cancellation. The onBeforeRemove hook serializes the checkpoint.
	ctx, cancel := context.WithCancel(context.Background())
	opts := tasks.FsCleanOptions{
		Root:      root,
		OlderThan: clock.NewReal().Now().Add(time.Hour),
		OnBeforeRemove: func(_ string) {
			cancel() // cancel before the NEXT removal
		},
	}
	err := tasks.FsClean(ctx, opts)
	if err != context.Canceled {
		t.Fatalf("FsClean with mid-walk cancellation = %v, want context.Canceled", err)
	}
	// At least one file was removed (the walk started), but not all.
	remaining := 0
	for i := range 10 {
		if _, err := os.Stat(filepath.Join(root, "file"+strconv.Itoa(i)+".txt")); err == nil {
			remaining++
		}
	}
	if remaining == 0 {
		t.Fatal("all files were deleted despite mid-walk cancellation")
	}
	if remaining == 10 {
		t.Fatal("no files were deleted — cancellation happened before the walk started")
	}
}

func TestFsCleanRejectsRelativeRoot(t *testing.T) {
	err := tasks.FsClean(context.Background(), tasks.FsCleanOptions{
		Root:      "relative/path",
		OlderThan: clock.NewReal().Now(),
	})
	if err == nil {
		t.Fatal("FsClean with relative root returned nil, want error")
	}
}

// TestFsCleanSymlinkRaceAncestorSwap proves a real ancestor-directory swap
// cannot redirect deletion outside the root: an old directory inside root is
// swapped for a symlink pointing outside, and the outside sentinel survives.
func TestFsCleanSymlinkRaceAncestorSwap(t *testing.T) {
	root := t.TempDir()

	// Outside directory with a victim file — the sentinel that must survive.
	outside := t.TempDir()
	outsideVictim := filepath.Join(outside, "victim")
	if err := os.WriteFile(outsideVictim, []byte("outside-sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create root/dir/victim — an old regular file inside a real ancestor
	// directory. The walk enumerates this entry before the hook swaps the
	// ancestor for a symlink.
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := clock.NewReal().Now().Add(-time.Hour)
	if err := os.Chtimes(victim, past, past); err != nil {
		t.Fatal(err)
	}

	// Swap root/dir for a symlink to the outside directory AFTER the walk
	// enumerates dir/victim but BEFORE the removal — deterministically via
	// the hook. If os.Root removal followed the symlink, it would delete
	// the outside victim through the swapped ancestor.
	swapped := make(chan struct{})
	opts := tasks.FsCleanOptions{
		Root:      root,
		OlderThan: clock.NewReal().Now(),
		OnBeforeRemove: func(path string) {
			rel := filepath.FromSlash(path)
			if rel == filepath.Join("dir", "victim") {
				// Replace the ancestor directory with a symlink to the
				// outside directory. The removal of dir/victim will now
				// resolve through the symlink — os.Root must prevent the
				// escape at the syscall level.
				_ = os.RemoveAll(dir)
				_ = os.Symlink(outside, dir)
				close(swapped)
			}
		},
	}
	if err := tasks.FsClean(context.Background(), opts); err != nil {
		t.Fatalf("FsClean: %v", err)
	}
	select {
	case <-swapped:
	case <-time.After(time.Second):
		t.Fatal("ancestor swap never happened — dir/victim was not enumerated")
	}
	// The outside victim survives — the symlink swap could not redirect
	// the root-relative removal outside the root boundary.
	if _, err := os.Stat(outsideVictim); err != nil {
		t.Fatal("outside victim was deleted through swapped ancestor symlink — root boundary violated")
	}
}

// TestFsCleanReportsProgress proves fsclean reports monotonic entry-based
// progress ending at 100 through the catalog projection.
func TestFsCleanReportsProgress(t *testing.T) {
	root := t.TempDir()
	past := clock.NewReal().Now().Add(-time.Hour)
	for i := range 5 {
		p := filepath.Join(root, "file"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	var reports []float64
	opts := tasks.FsCleanOptions{
		Root:      root,
		OlderThan: clock.NewReal().Now().Add(time.Hour),
		Progress:  func(p float64) { reports = append(reports, p) },
	}
	if err := tasks.FsClean(context.Background(), opts); err != nil {
		t.Fatalf("FsClean: %v", err)
	}
	if len(reports) == 0 {
		t.Fatal("fsclean reported no progress")
	}
	// Progress is monotonic non-decreasing and ends at 100.
	for i := 1; i < len(reports); i++ {
		if reports[i] < reports[i-1] {
			t.Errorf("progress not monotonic: reports[%d]=%v < reports[%d]=%v", i, reports[i], i-1, reports[i-1])
		}
	}
	if reports[len(reports)-1] != 100 {
		t.Errorf("final progress = %v, want 100", reports[len(reports)-1])
	}
}

// TestDeleteCacheFilesPreservesTranscodeSubtree proves DeleteCacheFiles'
// empty-dir removal never touches the transcodes subtree.
func TestDeleteCacheFilesPreservesTranscodeSubtree(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	cacheDir := t.TempDir()
	transcodeDir := filepath.Join(cacheDir, "transcodes")
	// Nested empty directories under transcodes must survive.
	nested := filepath.Join(transcodeDir, "session1")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty cache directory that should be removed by empty-dir cleanup.
	emptyCache := filepath.Join(cacheDir, "empty-cache")
	if err := os.MkdirAll(emptyCache, 0o755); err != nil {
		t.Fatal(err)
	}

	deps := tasks.BuiltinDeps{
		Clock:         c,
		CachePath:     cacheDir,
		TranscodePath: transcodeDir,
	}
	if err := tasks.RegisterBuiltins(s, deps); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(tasks.DeleteCacheFilesID); err != nil {
		t.Fatal(err)
	}
	waitForTaskIdle(t, s, tasks.DeleteCacheFilesID)

	// The transcodes root and its nested directory survive the runnable's
	// fsclean + empty-dir removal (spec §4: exclude the paths.Transcodes
	// subtree owned by TranscodeManager).
	if _, err := os.Stat(transcodeDir); err != nil {
		t.Error("transcodes root was removed")
	}
	if _, err := os.Stat(nested); err != nil {
		t.Error("transcodes nested directory was removed")
	}
	// The unrelated empty cache directory was removed by the same pass
	// (fscleanRemoveEmptyDirs excludes only the transcodes subtree).
	if _, err := os.Stat(emptyCache); err == nil {
		t.Error("unrelated empty cache directory was not removed")
	}
	// Direct proof: re-create the empty dir and confirm the exported helper
	// removes it while preserving the transcodes subtree.
	if err := os.MkdirAll(emptyCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tasks.FsCleanRemoveEmptyDirs(context.Background(), cacheDir, transcodeDir); err != nil {
		t.Fatalf("FsCleanRemoveEmptyDirs: %v", err)
	}
	if _, err := os.Stat(emptyCache); err == nil {
		t.Error("direct FsCleanRemoveEmptyDirs did not remove the empty cache directory")
	}
	if _, err := os.Stat(transcodeDir); err != nil {
		t.Error("direct FsCleanRemoveEmptyDirs removed the transcodes subtree")
	}
}
