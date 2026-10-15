// SPDX-License-Identifier: GPL-2.0-only

// Package rootfs provides descriptor-rooted filesystem operations.
//
// Dir paths are diagnostic only. Every operation which touches the filesystem is
// relative to a directory descriptor retained by Dir, so a pathname replacement
// cannot redirect a descendant operation.
package rootfs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	defaultContainmentDepth = 32
	removeAllDepth          = 8
	temporaryAttempts       = 32
)

// Dir owns a pinned directory descriptor. Its path is retained for diagnostics
// only and is never used to perform an operation after Pin returns.
type Dir struct {
	mu   sync.RWMutex
	fd   int
	path string
}

// Adopt retains fd as a directory root and takes ownership of it. The caller
// must not close fd after calling Adopt. Adopt closes fd when validation fails.
// path is diagnostic only and is never used for filesystem I/O.
func Adopt(fd int, path string) (*Dir, error) {
	if fd < 0 {
		return nil, errors.New("invalid directory descriptor")
	}
	if err := ensureDirectoryFD(fd); err != nil {
		closeErr := unix.Close(fd)
		return nil, errors.Join(fmt.Errorf("adopt root directory: %w", err), closeErr)
	}
	return &Dir{fd: fd, path: path}, nil
}

// Pin opens path as a directory without following a final symlink and retains
// the resulting descriptor. Ancestor resolution is intentionally performed
// once here; descendant operations are descriptor-relative.
func Pin(path string) (*Dir, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return nil, fmt.Errorf("lstat root directory: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("root path is not a directory")
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open root directory: %w", err)
	}
	if err := ensureDirectoryFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &Dir{fd: fd, path: path}, nil
}

// Close releases the owned descriptor. It is safe to call multiple times.
func (d *Dir) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fd < 0 {
		return nil
	}
	fd := d.fd
	d.fd = -1
	return unix.Close(fd)
}

// Path returns the lexical diagnostic path supplied at Pin/OpenDir. It must not
// be used for filesystem I/O.
func (d *Dir) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// Stat returns the identity and metadata of this pinned directory.
func (d *Dir) Stat() (unix.Stat_t, error) {
	var st unix.Stat_t
	err := d.withFD(func(fd int) error { return unix.Fstat(fd, &st) })
	return st, err
}

// SameIdentity reports whether two pinned directories refer to the same inode.
func (d *Dir) SameIdentity(other *Dir) (bool, error) {
	if d == nil || other == nil {
		return false, errors.New("nil directory")
	}
	var left, right unix.Stat_t
	if d == other {
		if err := d.withFD(func(fd int) error { return unix.Fstat(fd, &left) }); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := d.withFD(func(fd int) error { return unix.Fstat(fd, &left) }); err != nil {
		return false, err
	}
	if err := other.withFD(func(fd int) error { return unix.Fstat(fd, &right) }); err != nil {
		return false, err
	}
	return left.Dev == right.Dev && left.Ino == right.Ino, nil
}

// PathStillPinned is a freshness check. It re-pins path and reports an error
// when it no longer resolves to this Dir's identity. It is not a safety
// prerequisite: all mutations through Dir are already descriptor-relative.
func (d *Dir) PathStillPinned(path string) error {
	if err := d.requireOpen(); err != nil {
		return err
	}
	current, err := Pin(path)
	if err != nil {
		return fmt.Errorf("pin current root path: %w", err)
	}
	same, compareErr := d.SameIdentity(current)
	closeErr := current.Close()
	if compareErr != nil {
		return compareErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !same {
		return errors.New("directory pathname no longer identifies pinned directory")
	}
	return nil
}

// Contains reports whether child is d or a descendant of d. It walks parent
// descriptors rather than paths and stops after maxDepth ascents. A nonpositive
// maxDepth selects the package default of 32.
func (d *Dir) Contains(child *Dir, maxDepth int) (contains bool, returnErr error) {
	if d == nil || child == nil {
		return false, errors.New("nil directory")
	}
	if maxDepth <= 0 {
		maxDepth = defaultContainmentDepth
	}
	root, err := d.Stat()
	if err != nil {
		return false, err
	}
	current, err := child.dupFD()
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(current)) }()

	for depth := 0; ; depth++ {
		var st unix.Stat_t
		if err := unix.Fstat(current, &st); err != nil {
			return false, err
		}
		if st.Dev != root.Dev {
			return false, nil
		}
		if st.Ino == root.Ino {
			return true, nil
		}
		if depth >= maxDepth {
			return false, nil
		}
		parent, err := unix.Openat(current, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return false, fmt.Errorf("open parent directory: %w", err)
		}
		var parentSt unix.Stat_t
		if err := unix.Fstat(parent, &parentSt); err != nil {
			_ = unix.Close(parent)
			return false, err
		}
		if parentSt.Dev == st.Dev && parentSt.Ino == st.Ino {
			_ = unix.Close(parent)
			return false, nil
		}
		if err := unix.Close(current); err != nil {
			_ = unix.Close(parent)
			return false, err
		}
		current = parent
	}
}

// OpenDir opens the single-component child directory without following links.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	child := -1
	err := d.withFD(func(fd int) error {
		opened, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		child = opened
		return ensureDirectoryFD(child)
	})
	if err != nil {
		if child >= 0 {
			_ = unix.Close(child)
		}
		return nil, fmt.Errorf("open child directory %q: %w", name, err)
	}
	return &Dir{fd: child, path: filepath.Join(d.path, name)}, nil
}

// Mkdir creates a single-component child directory.
func (d *Dir) Mkdir(name string, perm uint32) error {
	if err := validName(name); err != nil {
		return err
	}
	return d.withFD(func(fd int) error { return unix.Mkdirat(fd, name, perm) })
}

// EnsureDir creates name when absent, or proves the existing entry is a
// directory without following a symlink. It reports whether it created it.
func (d *Dir) EnsureDir(name string, perm uint32) (bool, error) {
	if err := validName(name); err != nil {
		return false, err
	}
	var created bool
	err := d.withFD(func(fd int) error {
		err := unix.Mkdirat(fd, name, perm)
		if err == nil {
			created = true
			return nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("existing entry %q is not a directory", name)
		}
		return nil
	})
	return created, err
}

// Lstat returns metadata for a single-component child without following links.
func (d *Dir) Lstat(name string) (unix.Stat_t, error) {
	if err := validName(name); err != nil {
		return unix.Stat_t{}, err
	}
	var st unix.Stat_t
	err := d.withFD(func(fd int) error {
		return unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	})
	return st, err
}

// ReadNames returns a new complete directory scan on every invocation.
func (d *Dir) ReadNames() ([]fs.DirEntry, error) {
	var fd int
	err := d.withFD(func(parent int) error {
		opened, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		fd = opened
		return nil
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), d.path)
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return entries, nil
}

// OpenFile opens a regular single-component child file without following links.
func (d *Dir) OpenFile(name string) (*os.File, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	opened := -1
	err := d.withFD(func(fd int) error {
		fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		opened = fileFD
		var st unix.Stat_t
		if err := unix.Fstat(opened, &st); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("entry %q is not a regular file", name)
		}
		return nil
	})
	if err != nil {
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		return nil, fmt.Errorf("open file %q: %w", name, err)
	}
	return os.NewFile(uintptr(opened), filepath.Join(d.path, name)), nil
}

// ReadFile reads a regular single-component child file, rejecting a file whose
// observed contents exceed limit.
func (d *Dir) ReadFile(name string, limit int64) (data []byte, returnErr error) {
	if limit < 0 {
		return nil, errors.New("negative read limit")
	}
	file, err := d.OpenFile(name)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("file %q exceeds read limit", name)
	}
	if limit == int64(^uint64(0)>>1) {
		return io.ReadAll(file)
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file %q exceeds read limit", name)
	}
	return data, nil
}

// CreateExclusive creates a regular single-component file with O_EXCL and
// returns its owned descriptor.
func (d *Dir) CreateExclusive(name string, perm uint32) (*os.File, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	var opened int
	err := d.withFD(func(fd int) error {
		fileFD, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm)
		if err != nil {
			return err
		}
		opened = fileFD
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create file %q: %w", name, err)
	}
	return os.NewFile(uintptr(opened), filepath.Join(d.path, name)), nil
}

// ReplaceFileDurable writes data to a new temporary child, synchronizes that
// file, atomically renames it over name, and synchronizes the parent through
// sync. A sync failure is reported after the visible rename; no temporary file
// remains in either outcome.
func (d *Dir) ReplaceFileDurable(name, tempPrefix string, data []byte, perm uint32, sync func(*os.File) error) (err error) {
	if err := validName(name); err != nil {
		return err
	}
	if err := validName(tempPrefix); err != nil {
		return fmt.Errorf("invalid temporary prefix: %w", err)
	}
	if sync == nil {
		return errors.New("nil parent sync function")
	}

	var tempName string
	var file *os.File
	for range temporaryAttempts {
		candidate, randomErr := temporaryName(tempPrefix)
		if randomErr != nil {
			return randomErr
		}
		file, err = d.CreateExclusive(candidate, perm)
		if err == nil {
			tempName = candidate
			break
		}
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
	}
	if file == nil {
		return errors.New("could not create unique temporary file")
	}
	defer func() {
		if tempName != "" {
			if removeErr := d.Remove(tempName); removeErr != nil && !errors.Is(removeErr, unix.ENOENT) && err == nil {
				err = removeErr
			}
		}
	}()

	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	file = nil
	if err = d.Rename(tempName, d, name); err != nil {
		return err
	}
	tempName = ""
	if err = d.SyncWith(sync); err != nil {
		return err
	}
	return nil
}

// Remove unlinks a single-component non-directory entry.
func (d *Dir) Remove(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return d.withFD(func(fd int) error { return unix.Unlinkat(fd, name, 0) })
}

// RemoveDir removes an empty single-component directory.
func (d *Dir) RemoveDir(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return d.withFD(func(fd int) error { return unix.Unlinkat(fd, name, unix.AT_REMOVEDIR) })
}

// RemoveAll removes one child tree without following symlinks. Traversal is
// deliberately bounded to protect the known shallow owned-tree shape.
func (d *Dir) RemoveAll(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return removeAllAt(d, name, 0)
}

func removeAllAt(parent *Dir, name string, depth int) (returnErr error) {
	st, err := parent.Lstat(name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		err := parent.Remove(name)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if depth >= removeAllDepth {
		return fmt.Errorf("remove tree exceeds maximum depth %d", removeAllDepth)
	}
	child, err := parent.OpenDir(name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	entries, err := child.ReadNames()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeAllAt(child, entry.Name(), depth+1); err != nil {
			return err
		}
	}
	err = parent.RemoveDir(name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

// Rename atomically renames a single-component entry to a single-component
// name in to. Both names are resolved from retained directory descriptors.
func (d *Dir) Rename(oldName string, to *Dir, newName string) error {
	if err := validName(oldName); err != nil {
		return err
	}
	if err := validName(newName); err != nil {
		return err
	}
	return d.withPair(to, func(fromFD, toFD int) error {
		return unix.Renameat(fromFD, oldName, toFD, newName)
	})
}

// Exchange atomically swaps two single-component entries. Availability and
// filesystem support are platform-specific; unsupported exchanges return the
// native syscall error without a fallback that could expose torn state.
func (d *Dir) Exchange(name string, other *Dir, otherName string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := validName(otherName); err != nil {
		return err
	}
	return d.withPair(other, func(fromFD, otherFD int) error {
		return exchangeAt(fromFD, name, otherFD, otherName)
	})
}

// Sync synchronizes the pinned directory using os.File.Sync.
func (d *Dir) Sync() error {
	return d.SyncWith(func(file *os.File) error { return file.Sync() })
}

// SyncWith duplicates the descriptor, invokes sync with an independently-owned
// os.File, and closes that file before returning.
func (d *Dir) SyncWith(sync func(*os.File) error) error {
	if sync == nil {
		return errors.New("nil sync function")
	}
	fd, err := d.dupFD()
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), d.path)
	syncErr := sync(file)
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// ProcessFile returns an independently-owned duplicate suitable for passing to
// exec.Cmd.ExtraFiles. The caller closes it after the child has been started.
func (d *Dir) ProcessFile() (*os.File, error) {
	fd, err := d.dupFD()
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), d.path), nil
}

func (d *Dir) withFD(fn func(int) error) error {
	if d == nil {
		return errors.New("nil directory")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.fd < 0 {
		return errors.New("directory is closed")
	}
	return fn(d.fd)
}

func (d *Dir) dupFD() (int, error) {
	var duplicate int
	err := d.withFD(func(fd int) error {
		copied, err := unix.Dup(fd)
		if err != nil {
			return err
		}
		duplicate = copied
		return nil
	})
	return duplicate, err
}

func (d *Dir) requireOpen() error {
	return d.withFD(func(int) error { return nil })
}

func (d *Dir) withPair(other *Dir, fn func(int, int) error) error {
	if d == nil || other == nil {
		return errors.New("nil directory")
	}
	if d == other {
		d.mu.RLock()
		defer d.mu.RUnlock()
		if d.fd < 0 {
			return errors.New("directory is closed")
		}
		return fn(d.fd, d.fd)
	}
	d.mu.RLock()
	other.mu.RLock()
	defer other.mu.RUnlock()
	defer d.mu.RUnlock()
	if d.fd < 0 || other.fd < 0 {
		return errors.New("directory is closed")
	}
	return fn(d.fd, other.fd)
}

func ensureDirectoryFD(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("opened descriptor is not a directory")
	}
	return nil
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid single-component name %q", name)
	}
	return nil
}

func temporaryName(prefix string) (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", fmt.Errorf("read temporary name randomness: %w", err)
	}
	return fmt.Sprintf("%s%x", prefix, random), nil
}
