// SPDX-License-Identifier: GPL-2.0-only

package rootfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/rootfs"
	"golang.org/x/sys/unix"
)

func TestDirRejectsInvalidSingleComponentNames(t *testing.T) {
	root := pinTestDir(t, t.TempDir())
	otherPath := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(otherPath, 0o755); err != nil {
		t.Fatalf("Mkdir(other): %v", err)
	}
	other := pinTestDir(t, otherPath)

	for _, name := range []string{"", ".", "..", "nested/name", "/absolute"} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			assertInvalidName(t, "OpenDir", func() error {
				child, err := root.OpenDir(name)
				if child != nil {
					_ = child.Close()
				}
				return err
			})
			assertInvalidName(t, "Mkdir", func() error { return root.Mkdir(name, 0o755) })
			assertInvalidName(t, "EnsureDir", func() error {
				_, err := root.EnsureDir(name, 0o755)
				return err
			})
			assertInvalidName(t, "Lstat", func() error {
				_, err := root.Lstat(name)
				return err
			})
			assertInvalidName(t, "OpenFile", func() error {
				file, err := root.OpenFile(name)
				if file != nil {
					_ = file.Close()
				}
				return err
			})
			assertInvalidName(t, "ReadFile", func() error {
				_, err := root.ReadFile(name, 1024)
				return err
			})
			assertInvalidName(t, "CreateExclusive", func() error {
				file, err := root.CreateExclusive(name, 0o600)
				if file != nil {
					_ = file.Close()
				}
				return err
			})
			assertInvalidName(t, "ReplaceFileDurable", func() error {
				return root.ReplaceFileDurable(name, ".tmp-", []byte("payload"), 0o600, func(*os.File) error { return nil })
			})
			assertInvalidName(t, "Remove", func() error { return root.Remove(name) })
			assertInvalidName(t, "RemoveDir", func() error { return root.RemoveDir(name) })
			assertInvalidName(t, "RemoveAll", func() error { return root.RemoveAll(name) })
			assertInvalidName(t, "Rename source", func() error { return root.Rename(name, other, "destination") })
			assertInvalidName(t, "Rename destination", func() error { return root.Rename("source", other, name) })
			assertInvalidName(t, "Exchange source", func() error { return root.Exchange(name, other, "destination") })
			assertInvalidName(t, "Exchange destination", func() error { return root.Exchange("source", other, name) })
		})
	}

	assertInvalidName(t, "ReplaceFileDurable temp prefix", func() error {
		return root.ReplaceFileDurable("target", "nested/temp-", []byte("payload"), 0o600, func(*os.File) error { return nil })
	})
}

func TestDirReadNamesStartsAtOffsetZeroEachTime(t *testing.T) {
	path := t.TempDir()
	for _, name := range []string{"bravo", "alpha", "charlie"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(name), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
	root := pinTestDir(t, path)

	first, err := root.ReadNames()
	if err != nil {
		t.Fatalf("first ReadNames(): %v", err)
	}
	second, err := root.ReadNames()
	if err != nil {
		t.Fatalf("second ReadNames(): %v", err)
	}
	if got, want := dirEntryNames(first), []string{"alpha", "bravo", "charlie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("first ReadNames() = %v, want %v", got, want)
	}
	if got, want := dirEntryNames(second), []string{"alpha", "bravo", "charlie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("second ReadNames() = %v, want %v; calls must use fresh directory offsets", got, want)
	}
}

func TestDirRemoveAllUnlinksSymlinksWithoutTraversingThem(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := t.TempDir()
	outsideFile := filepath.Join(outsidePath, "must-survive")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, "tree", "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll(tree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "tree", "nested", "inside"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("WriteFile(inside): %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "tree", "outside-link")); err != nil {
		t.Fatalf("Symlink(outside): %v", err)
	}
	root := pinTestDir(t, rootPath)

	if err := root.RemoveAll("tree"); err != nil {
		t.Fatalf("RemoveAll(tree): %v", err)
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside target after RemoveAll(tree): %v; symlinks must be unlinked, never traversed", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "tree")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed tree Lstat error = %v, want not exist", err)
	}
}

func TestDirReplaceFileDurableReplacesAndCleansTemporaryFiles(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "destination"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination): %v", err)
	}
	root := pinTestDir(t, path)
	var syncCalls int
	if err := root.ReplaceFileDurable("destination", ".replace-", []byte("new"), 0o640, func(*os.File) error {
		syncCalls++
		return nil
	}); err != nil {
		t.Fatalf("ReplaceFileDurable(success): %v", err)
	}
	if syncCalls == 0 {
		t.Error("ReplaceFileDurable(success) did not sync the parent directory")
	}
	assertFileContents(t, filepath.Join(path, "destination"), "new")
	assertNoPrefix(t, path, ".replace-")

	syncFailure := errors.New("injected parent sync failure")
	if err := root.ReplaceFileDurable("destination", ".replace-", []byte("after-sync-error"), 0o600, func(*os.File) error {
		return syncFailure
	}); !errors.Is(err, syncFailure) {
		t.Fatalf("ReplaceFileDurable(sync failure) error = %v, want injected failure", err)
	}
	// A parent sync occurs after rename. The replacement is therefore visible even
	// when durability reporting fails, but no temporary artifact may remain.
	assertFileContents(t, filepath.Join(path, "destination"), "after-sync-error")
	assertNoPrefix(t, path, ".replace-")
}

func TestDirExchangeSwapsSameAndCrossParentNames(t *testing.T) {
	rootPath := t.TempDir()
	for _, directory := range []string{"left", "right"} {
		if err := os.Mkdir(filepath.Join(rootPath, directory), 0o755); err != nil {
			t.Fatalf("Mkdir(%s): %v", directory, err)
		}
	}
	root := pinTestDir(t, rootPath)
	left, err := root.OpenDir("left")
	if err != nil {
		t.Fatalf("OpenDir(left): %v", err)
	}
	t.Cleanup(func() { _ = left.Close() })
	right, err := root.OpenDir("right")
	if err != nil {
		t.Fatalf("OpenDir(right): %v", err)
	}
	t.Cleanup(func() { _ = right.Close() })

	writeRelativeFile(t, filepath.Join(rootPath, "left", "one"), "one")
	writeRelativeFile(t, filepath.Join(rootPath, "left", "two"), "two")
	if err := left.Exchange("one", left, "two"); err != nil {
		t.Fatalf("Exchange(same parent): %v", err)
	}
	assertFileContents(t, filepath.Join(rootPath, "left", "one"), "two")
	assertFileContents(t, filepath.Join(rootPath, "left", "two"), "one")

	writeRelativeFile(t, filepath.Join(rootPath, "right", "remote"), "remote")
	if err := left.Exchange("one", right, "remote"); err != nil {
		t.Fatalf("Exchange(cross parent): %v", err)
	}
	assertFileContents(t, filepath.Join(rootPath, "left", "one"), "remote")
	assertFileContents(t, filepath.Join(rootPath, "right", "remote"), "two")
}

func TestDirContainsPinnedDescendantForeignDirectoryAndDepthCap(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "one", "two"), 0o755); err != nil {
		t.Fatalf("MkdirAll(descendant): %v", err)
	}
	root := pinTestDir(t, rootPath)
	one, err := root.OpenDir("one")
	if err != nil {
		t.Fatalf("OpenDir(one): %v", err)
	}
	t.Cleanup(func() { _ = one.Close() })
	two, err := one.OpenDir("two")
	if err != nil {
		t.Fatalf("OpenDir(two): %v", err)
	}
	t.Cleanup(func() { _ = two.Close() })
	foreign := pinTestDir(t, t.TempDir())

	if got, err := root.Contains(two, 2); err != nil || !got {
		t.Fatalf("Contains(two, 2) = (%t, %v), want (true, nil)", got, err)
	}
	if got, err := root.Contains(foreign, 32); err != nil || got {
		t.Fatalf("Contains(foreign, 32) = (%t, %v), want (false, nil)", got, err)
	}
	if got, err := root.Contains(two, 1); err != nil || got {
		t.Fatalf("Contains(two, 1) = (%t, %v), want (false, nil) because the root is two ascents away", got, err)
	}
}

func TestDirPathStillPinnedDetectsRenameAndAncestorSymlinkRetarget(t *testing.T) {
	t.Run("root renamed", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "root")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("Mkdir(root): %v", err)
		}
		root := pinTestDir(t, path)
		if err := os.Rename(path, filepath.Join(base, "root-moved")); err != nil {
			t.Fatalf("Rename(root): %v", err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("Mkdir(replacement root): %v", err)
		}
		if err := root.PathStillPinned(path); err == nil {
			t.Fatal("PathStillPinned(replaced root pathname) error = nil, want identity mismatch")
		}
	})

	t.Run("ancestor symlink retargeted", func(t *testing.T) {
		base := t.TempDir()
		first := filepath.Join(base, "first")
		second := filepath.Join(base, "second")
		for _, path := range []string{filepath.Join(first, "root"), filepath.Join(second, "root")} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("MkdirAll(%s): %v", path, err)
			}
		}
		live := filepath.Join(base, "live")
		if err := os.Symlink(first, live); err != nil {
			t.Fatalf("Symlink(first): %v", err)
		}
		pinnedPath := filepath.Join(live, "root")
		root := pinTestDir(t, pinnedPath)
		if err := os.Remove(live); err != nil {
			t.Fatalf("Remove(live symlink): %v", err)
		}
		if err := os.Symlink(second, live); err != nil {
			t.Fatalf("Symlink(second): %v", err)
		}
		if err := root.PathStillPinned(pinnedPath); err == nil {
			t.Fatal("PathStillPinned(retargeted ancestor symlink) error = nil, want identity mismatch")
		}
	})
}

func TestAdoptOwnsDirectoryDescriptorAndClosesSafely(t *testing.T) {
	path := t.TempDir()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}

	dir, err := rootfs.Adopt(fd, path)
	if err != nil {
		t.Fatalf("Adopt(%q): %v", path, err)
	}
	if err := dir.Mkdir("before-close", 0o755); err != nil {
		t.Fatalf("Mkdir(before-close): %v", err)
	}
	if info, err := os.Stat(filepath.Join(path, "before-close")); err != nil {
		t.Fatalf("Stat(before-close): %v", err)
	} else if !info.IsDir() {
		t.Errorf("before-close mode = %v, want directory", info.Mode())
	}

	if err := dir.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := dir.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); !errors.Is(err, unix.EBADF) {
		t.Errorf("Fstat(adopted fd) after Close error = %v, want EBADF", err)
	}
	if err := dir.Mkdir("after-close", 0o755); err == nil {
		t.Error("Mkdir() after Close error = nil, want safe closed-descriptor failure")
	}
}

func TestDirCloseIsIdempotentAndClosedDescriptorFailsSafely(t *testing.T) {
	root := pinTestDir(t, t.TempDir())
	if err := root.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}

	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"Stat", func() error { _, err := root.Stat(); return err }},
		{"ReadNames", func() error { _, err := root.ReadNames(); return err }},
		{"OpenDir", func() error { _, err := root.OpenDir("child"); return err }},
		{"Mkdir", func() error { return root.Mkdir("child", 0o755) }},
		{"PathStillPinned", func() error { return root.PathStillPinned(t.TempDir()) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err == nil {
				t.Errorf("%s() after Close error = nil, want safe closed-descriptor failure", operation.name)
			}
		})
	}
}

func pinTestDir(t *testing.T, path string) *rootfs.Dir {
	t.Helper()
	dir, err := rootfs.Pin(path)
	if err != nil {
		t.Fatalf("Pin(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Errorf("Close(%q): %v", path, err)
		}
	})
	return dir
}

func assertInvalidName(t *testing.T, operation string, call func() error) {
	t.Helper()
	if err := call(); err == nil {
		t.Errorf("%s accepted an invalid single-component name", operation)
	}
}

func dirEntryNames(entries []fs.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	sort.Strings(names)
	return names
}

func writeRelativeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != want {
		t.Errorf("ReadFile(%q) = %q, want %q", path, got, want)
	}
}

func assertNoPrefix(t *testing.T, directory, prefix string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", directory, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("temporary file %q remains in %q", entry.Name(), directory)
		}
	}
}
