// SPDX-License-Identifier: GPL-2.0-only

package tasks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fscleanOptions controls one rooted age-based deletion pass.
type fscleanOptions struct {
	Root           string                 // absolute, cleaned root; never deleted itself
	OlderThan      time.Time              // only entries with mtime before this are deleted
	skipName       func(name string) bool // optional: skip entries by base name
	Exclude        string                 // optional: absolute subtree path to skip entirely
	Progress       func(float64)          // optional: report index/total × 100 (spec §5)
	OnBeforeRemove func(path string)      // optional: hook fired before each removal; enables deterministic cancellation
}

// FsCleanOptions is the exported form of fscleanOptions for tests.
type FsCleanOptions = fscleanOptions

// FsClean is the exported form of fsclean for tests.
func FsClean(ctx context.Context, opts FsCleanOptions) error {
	return fsclean(ctx, opts)
}

// fsclean walks root under an os.Root (symlink-safe) and deletes files whose
// mtime is before olderThan. Per-file delete errors are logged and skipped;
// the task still Completed (upstream catches per-file IO errors). Root
// enumeration failure → Failed.
//
// Safety properties:
//   - All traversal uses os.Root + fs.WalkDir(root.FS(), …): escapes via
//     symlink or .. are impossible at the syscall level. Directory symlinks
//     are never followed; symlink entries are deletable by age (removes the
//     link only, never its target).
//   - Cancellation checkpoint (ctx.Err()) before every entry.
//   - Never deletes directories except provably empty ones under the root
//     when removeEmptyDirs is set, and never the root itself.
//
// Progress is index/total × 100 (upstream DeleteLogFileTask.cs:83-95): the
// walk first enumerates candidate entries, then reports the running index
// against the total before each deletion. Cancellation between enumeration
// and a removal is deterministic when an onBeforeRemove hook serializes it.
func fsclean(ctx context.Context, opts fscleanOptions) error {
	if strings.TrimSpace(opts.Root) == "" {
		return errors.New("tasks: fsclean root is required")
	}
	// Reject relative roots — the approved safety contract says "Empty/relative
	// root → task Failed." Allowing filepath.Abs to resolve a relative root
	// against the process working directory would break the root boundary.
	if !filepath.IsAbs(opts.Root) {
		return fmt.Errorf("tasks: fsclean root %q is not absolute", opts.Root)
	}
	opts.Root = filepath.Clean(opts.Root)

	root, err := os.OpenRoot(opts.Root)
	if err != nil {
		return fmt.Errorf("tasks: open fsclean root %s: %w", opts.Root, err)
	}
	defer root.Close()

	rootFS := root.FS()

	// First pass: enumerate the candidate entries (files and symlink entries
	// whose mtime is before olderThan) so progress can be index/total × 100
	// and cancellation can be observed deterministically between entries.
	type candidate struct {
		path string
		info fs.FileInfo
	}
	var candidates []candidate
	walkErr := fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			if d != nil && d.IsDir() {
				// Root enumeration failure is fatal; sub-dir errors are logged.
				if path == "." {
					return walkErr
				}
				log.Printf("tasks: fsclean walk error at %s: %v", path, walkErr)
				return fs.SkipDir
			}
			return walkErr
		}
		if path == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			log.Printf("tasks: fsclean stat %s: %v", path, err)
			return nil
		}

		// Skip the excluded subtree by root-relative path comparison.
		if opts.Exclude != "" {
			excludeRel := strings.TrimPrefix(
				filepath.Clean(opts.Exclude), opts.Root+string(filepath.Separator))
			if excludeRel != opts.Exclude { // Exclude is under Root
				relPath := filepath.FromSlash(path)
				if relPath == excludeRel || strings.HasPrefix(relPath, excludeRel+string(filepath.Separator)) {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
			}
		}

		// Skip by name predicate.
		base := d.Name()
		if opts.skipName != nil && opts.skipName(base) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		// Delete by age (regular files and symlink entries alike). Removal
		// is root-relative through os.Root — escapes via symlink or .. are
		// impossible at the syscall level.
		if info.ModTime().Before(opts.OlderThan) {
			candidates = append(candidates, candidate{path: path, info: info})
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	total := len(candidates)
	for i, c := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if opts.OnBeforeRemove != nil {
			opts.OnBeforeRemove(filepath.FromSlash(c.path))
		}
		if opts.Progress != nil {
			opts.Progress(float64(i+1) / float64(total) * 100)
		}
		if err := root.Remove(filepath.FromSlash(c.path)); err != nil {
			log.Printf("tasks: fsclean delete %s: %v", c.path, err)
		}
	}
	if opts.Progress != nil && total > 0 {
		opts.Progress(100)
	}
	return nil
}

// FsCleanRemoveEmptyDirs is the exported form of fscleanRemoveEmptyDirs for tests.
func FsCleanRemoveEmptyDirs(ctx context.Context, rootPath, exclude string) error {
	return fscleanRemoveEmptyDirs(ctx, rootPath, exclude)
}

// fscleanRemoveEmptyDirs removes provably empty subdirectories under root,
// skipping the optional exclude subtree (e.g. the transcodes dir owned by
// TranscodeManager). It never deletes the root itself. All traversal and
// removal is root-relative through os.Root (symlink-safe, no .. escapes).
func fscleanRemoveEmptyDirs(ctx context.Context, rootPath, exclude string) error {
	if !filepath.IsAbs(rootPath) {
		return fmt.Errorf("tasks: fsclean root %q is not absolute", rootPath)
	}
	rootPath = filepath.Clean(rootPath)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("tasks: open fsclean root %s: %w", rootPath, err)
	}
	defer root.Close()

	rootFS := root.FS()
	var dirs []string
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && path != "." {
			// Skip the excluded subtree so the manager-owned transcodes root
			// and its directories are never removed.
			if exclude != "" {
				excludeRel := strings.TrimPrefix(
					filepath.Clean(exclude), rootPath+string(filepath.Separator))
				if excludeRel != exclude {
					relPath := filepath.FromSlash(path)
					if relPath == excludeRel || strings.HasPrefix(relPath, excludeRel+string(filepath.Separator)) {
						return fs.SkipDir
					}
				}
			}
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Walk deepest-first so parent empties only if all children are gone.
	for i := len(dirs) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dir := dirs[i]
		entries, err := fs.ReadDir(rootFS, dir)
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			_ = root.Remove(filepath.FromSlash(dir))
		}
	}
	return nil
}
