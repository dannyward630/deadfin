// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestRemoveVirtualFolderRestoresStagedDirectoryAndRegistryWhenParentSyncFails(t *testing.T) {
	topology, registry := openInternalTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}

	originalSync := topologyRootSyncDir
	failed := false
	topologyRootSyncDir = func(gotTopology *Topology, name string) error {
		if gotTopology == topology && name == "." && !failed {
			failed = true
			return errors.New("injected parent directory sync failure")
		}
		return originalSync(gotTopology, name)
	}
	t.Cleanup(func() { topologyRootSyncDir = originalSync })

	if err := topology.RemoveVirtualFolder(t.Context(), created.ID); err == nil {
		t.Fatal("remove virtual folder succeeded with staged-parent sync failure")
	}
	assertVirtualFolderRemovalRestored(t, topology, registry, created)
}

func TestRemoveVirtualFolderRestoresStagedDirectoryAndRegistryWhenRemoveAllFails(t *testing.T) {
	topology, registry := openInternalTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}

	originalRemoveAll := topologyRootRemoveAll
	topologyRootRemoveAll = func(gotTopology *Topology, name string) error {
		if gotTopology == topology && name == ".removing-"+created.ID.String() {
			return errors.New("injected remove virtual-folder directory failure")
		}
		return originalRemoveAll(gotTopology, name)
	}
	t.Cleanup(func() { topologyRootRemoveAll = originalRemoveAll })

	if err := topology.RemoveVirtualFolder(t.Context(), created.ID); err == nil {
		t.Fatal("remove virtual folder succeeded with injected RemoveAll failure")
	}
	assertVirtualFolderRemovalRestored(t, topology, registry, created)
}

func TestRemoveVirtualFolderDeletesFolderAndRegistryOnSuccess(t *testing.T) {
	topology, registry := openInternalTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}

	if err := topology.RemoveVirtualFolder(t.Context(), created.ID); err != nil {
		t.Fatalf("remove virtual folder: %v", err)
	}
	if _, err := os.Lstat(created.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed virtual folder stat error = %v, want not exist", err)
	}
	if _, err := registry.Get(t.Context(), created.ID); err == nil {
		t.Fatal("removed virtual folder remains in registry")
	}

	if err := topology.Close(); err != nil {
		t.Fatalf("close topology before reopen: %v", err)
	}
	reopened, err := NewTopology(topology.anchor, registry)
	if err != nil {
		t.Fatalf("reopen topology: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened topology: %v", err)
		}
	})
	folders, err := reopened.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list virtual folders after reopen: %v", err)
	}
	if len(folders) != 0 {
		t.Fatalf("virtual folders after successful removal and reopen = %#v, want none", folders)
	}
}

func TestAddMediaPathConcurrentDuplicateHasOneShortcutAndOneSuccess(t *testing.T) {
	topology, _ := openInternalTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	mediaPath := internalMediaPath(t)

	originalHook := topologyBeforeShortcutCreate
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	topologyBeforeShortcutCreate = func() {
		entered <- struct{}{}
		<-release
	}
	t.Cleanup(func() { topologyBeforeShortcutCreate = originalHook })

	results := make(chan error, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	go func() {
		defer callers.Done()
		results <- topology.AddMediaPath(t.Context(), created.ID, mediaPath)
	}()
	<-entered
	secondStarted := make(chan struct{})
	go func() {
		defer callers.Done()
		close(secondStarted)
		results <- topology.AddMediaPath(t.Context(), created.ID, mediaPath)
	}()
	<-secondStarted
	close(release)
	callers.Wait()
	close(results)

	successes := 0
	failures := 0
	for result := range results {
		if result == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent duplicate add successes=%d failures=%d, want one each", successes, failures)
	}
	shortcuts, err := filepath.Glob(filepath.Join(created.Path, "*.mblink"))
	if err != nil {
		t.Fatalf("glob concurrent shortcuts: %v", err)
	}
	if len(shortcuts) != 1 {
		t.Fatalf("concurrent duplicate add shortcut count = %d, want 1", len(shortcuts))
	}
	contents, err := os.ReadFile(shortcuts[0])
	if err != nil {
		t.Fatalf("read concurrent shortcut: %v", err)
	}
	if string(contents) != mediaPath {
		t.Fatalf("concurrent shortcut text = %q, want %q", contents, mediaPath)
	}
}

func TestAddMediaPathRejectsFinalFolderSymlinkSwapBeforeShortcutWrite(t *testing.T) {
	topology, _ := openInternalTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	outside := t.TempDir()
	originalHook := topologyBeforeShortcutWrite
	topologyBeforeShortcutWrite = func() {
		if err := os.RemoveAll(created.Path); err != nil {
			t.Fatalf("remove final folder before symlink swap: %v", err)
		}
		if err := os.Symlink(outside, created.Path); err != nil {
			t.Fatalf("replace final folder with symlink: %v", err)
		}
	}
	t.Cleanup(func() { topologyBeforeShortcutWrite = originalHook })

	if err := topology.AddMediaPath(t.Context(), created.ID, internalMediaPath(t)); err == nil {
		t.Fatal("add media path after final-folder symlink swap succeeded, want root-escape rejection")
	}
	shortcuts, err := filepath.Glob(filepath.Join(outside, "*.mblink"))
	if err != nil {
		t.Fatalf("glob outside shortcuts: %v", err)
	}
	if len(shortcuts) != 0 {
		t.Fatalf("final-folder symlink swap wrote shortcuts outside topology root = %#v", shortcuts)
	}
}

func TestTopologyRejectsPostValidationSymlinkSwapsForEveryEntrypoint(t *testing.T) {
	for _, swap := range []struct {
		name  string
		apply func(*testing.T, *Topology, string, string) string
	}{
		{
			name: "final folder",
			apply: func(t *testing.T, _ *Topology, folder, outside string) string {
				t.Helper()
				outsideFolder := filepath.Join(outside, "Movies")
				if err := os.Rename(folder, outsideFolder); err != nil {
					t.Fatalf("move final folder outside root: %v", err)
				}
				if err := os.Symlink(outsideFolder, folder); err != nil {
					t.Fatalf("replace final folder with symlink: %v", err)
				}
				return outsideFolder
			},
		},
		{
			name: "topology ancestor",
			apply: func(t *testing.T, topology *Topology, _ string, outside string) string {
				t.Helper()
				outsideRoot := filepath.Join(outside, "default")
				if err := os.Rename(topology.root, outsideRoot); err != nil {
					t.Fatalf("move topology ancestor outside root: %v", err)
				}
				if err := os.Symlink(outsideRoot, topology.root); err != nil {
					t.Fatalf("replace topology ancestor with symlink: %v", err)
				}
				return filepath.Join(outsideRoot, "Movies")
			},
		},
	} {
		for _, operation := range []struct {
			name            string
			preexistingLink bool
			invoke          func(*Topology, string, string) error
		}{
			{
				name: "VirtualFolders read",
				invoke: func(topology *Topology, _ string, _ string) error {
					_, err := topology.VirtualFolders(t.Context())
					return err
				},
			},
			{
				name: "AddMediaPath write",
				invoke: func(topology *Topology, id string, mediaPath string) error {
					parsed, err := dto.ParseUUID(id)
					if err != nil {
						t.Fatalf("parse virtual-folder ID: %v", err)
					}
					return topology.AddMediaPath(t.Context(), parsed, mediaPath)
				},
			},
			{
				name:            "RemoveMediaPath remove",
				preexistingLink: true,
				invoke: func(topology *Topology, id string, mediaPath string) error {
					parsed, err := dto.ParseUUID(id)
					if err != nil {
						t.Fatalf("parse virtual-folder ID: %v", err)
					}
					return topology.RemoveMediaPath(t.Context(), parsed, mediaPath)
				},
			},
		} {
			t.Run(swap.name+"/"+operation.name, func(t *testing.T) {
				topology, _ := openInternalTopology(t)
				created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
				if err != nil {
					t.Fatalf("create virtual folder: %v", err)
				}
				mediaPath := internalMediaPath(t)
				if operation.preexistingLink {
					if err := topology.AddMediaPath(t.Context(), created.ID, mediaPath); err != nil {
						t.Fatalf("add media path before removal test: %v", err)
					}
				}

				originalHook := topologyAfterFolderValidation
				outside := t.TempDir()
				var once sync.Once
				topologyAfterFolderValidation = func() {
					once.Do(func() { swap.apply(t, topology, created.Path, outside) })
				}
				t.Cleanup(func() { topologyAfterFolderValidation = originalHook })

				if err := operation.invoke(topology, created.ID.String(), mediaPath); err == nil {
					t.Fatalf("%s through %s symlink swap succeeded, want containment rejection", operation.name, swap.name)
				}
				outsideFolder := filepath.Join(outside, "Movies")
				if swap.name == "topology ancestor" {
					outsideFolder = filepath.Join(outside, "default", "Movies")
				}
				shortcuts, err := filepath.Glob(filepath.Join(outsideFolder, "*.mblink"))
				if err != nil {
					t.Fatalf("glob outside shortcuts: %v", err)
				}
				wantShortcuts := 0
				if operation.preexistingLink {
					wantShortcuts = 1
				}
				if len(shortcuts) != wantShortcuts {
					t.Fatalf("outside shortcuts after %s through %s swap = %#v, want %d unchanged shortcut(s)", operation.name, swap.name, shortcuts, wantShortcuts)
				}
			})
		}
	}
}

func TestVirtualFolderMutationsRejectPostValidationSymlinkSwaps(t *testing.T) {
	t.Run("AddVirtualFolder ancestor", func(t *testing.T) {
		topology, _ := openInternalTopology(t)
		outside := t.TempDir()
		originalHook := topologyAfterVirtualFolderValidation
		topologyAfterVirtualFolderValidation = func() {
			outsideRoot := filepath.Join(outside, "default")
			if err := os.Rename(topology.root, outsideRoot); err != nil {
				t.Fatalf("move topology root outside anchor: %v", err)
			}
			if err := os.Symlink(outsideRoot, topology.root); err != nil {
				t.Fatalf("replace topology root with symlink: %v", err)
			}
		}
		t.Cleanup(func() { topologyAfterVirtualFolderValidation = originalHook })

		if _, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false}); err == nil {
			t.Fatal("add virtual folder through swapped root succeeded, want containment rejection")
		}
		if _, err := os.Stat(filepath.Join(outside, "default", "Movies")); !os.IsNotExist(err) {
			t.Fatalf("add virtual folder created outside anchor: %v", err)
		}
	})

	for _, swap := range []struct {
		name  string
		apply func(*testing.T, *Topology, string, string) string
	}{
		{
			name: "final folder",
			apply: func(t *testing.T, _ *Topology, folder, outside string) string {
				t.Helper()
				outsideFolder := filepath.Join(outside, "Movies")
				if err := os.Rename(folder, outsideFolder); err != nil {
					t.Fatalf("move final folder outside anchor: %v", err)
				}
				if err := os.Symlink(outsideFolder, folder); err != nil {
					t.Fatalf("replace final folder with symlink: %v", err)
				}
				return outsideFolder
			},
		},
		{
			name: "topology ancestor",
			apply: func(t *testing.T, topology *Topology, _ string, outside string) string {
				t.Helper()
				outsideRoot := filepath.Join(outside, "default")
				if err := os.Rename(topology.root, outsideRoot); err != nil {
					t.Fatalf("move topology root outside anchor: %v", err)
				}
				if err := os.Symlink(outsideRoot, topology.root); err != nil {
					t.Fatalf("replace topology root with symlink: %v", err)
				}
				return filepath.Join(outsideRoot, "Movies")
			},
		},
	} {
		t.Run("RemoveVirtualFolder "+swap.name, func(t *testing.T) {
			topology, _ := openInternalTopology(t)
			created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", LibraryOptions{Enabled: false})
			if err != nil {
				t.Fatalf("create virtual folder: %v", err)
			}
			outside := t.TempDir()
			originalHook := topologyAfterVirtualFolderValidation
			var once sync.Once
			topologyAfterVirtualFolderValidation = func() {
				once.Do(func() { swap.apply(t, topology, created.Path, outside) })
			}
			t.Cleanup(func() { topologyAfterVirtualFolderValidation = originalHook })

			if err := topology.RemoveVirtualFolder(t.Context(), created.ID); err == nil {
				t.Fatalf("remove virtual folder through %s swap succeeded, want containment rejection", swap.name)
			}
			outsideFolder := filepath.Join(outside, "Movies")
			if swap.name == "topology ancestor" {
				outsideFolder = filepath.Join(outside, "default", "Movies")
			}
			if _, err := os.Stat(filepath.Join(outsideFolder, "options.xml")); err != nil {
				t.Fatalf("remove virtual folder mutated outside folder: options XML stat: %v", err)
			}
		})
	}
}

func assertVirtualFolderRemovalRestored(t *testing.T, topology *Topology, registry *store.LibraryStore, created VirtualFolder) {
	t.Helper()
	registered, err := registry.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("registry row after failed remove: %v", err)
	}
	if registered.ID != created.ID {
		t.Fatalf("registry UUID after failed remove = %s, want %s", registered.ID, created.ID)
	}
	info, err := os.Lstat(created.Path)
	if err != nil {
		t.Fatalf("original folder after failed remove: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("original folder after failed remove is not a directory: %#v", info)
	}
	if _, err := os.Lstat(filepath.Join(topology.root, ".removing-"+created.ID.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged folder after failed remove stat error = %v, want absent", err)
	}

	if err := topology.Close(); err != nil {
		t.Fatalf("close topology before reopen: %v", err)
	}
	reopened, err := NewTopology(topology.anchor, registry)
	if err != nil {
		t.Fatalf("reopen topology after failed removal: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened topology: %v", err)
		}
	})
	folders, err := reopened.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list virtual folders after failed removal reopen: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("virtual folders after failed removal reopen = %#v, want one", folders)
	}
	if folders[0].ID != created.ID {
		t.Fatalf("reopened registry UUID after failed removal = %s, want %s", folders[0].ID, created.ID)
	}
}

func openInternalTopology(t *testing.T) (*Topology, *store.LibraryStore) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite stores: %v", err)
		}
	})
	registry := store.NewLibraryStore(stores)
	topology, err := NewTopology(t.TempDir(), registry)
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	return topology, registry
}

func internalMediaPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create media path: %v", err)
	}
	return path
}
