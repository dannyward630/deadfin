// SPDX-License-Identifier: GPL-2.0-only

//go:build cgo

package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/deadfin-org/deadfin/internal/rootfs"
	"golang.org/x/sys/unix"
)

const (
	maxNativeImageDimension = int(^uint32(0) >> 1)
	// maxTilePixels limits the decoded 8-bit RGBA canvas to 256 MiB before
	// libvips overhead. It remains well above configured 10×10 trickplay grids.
	maxTilePixels       uint64 = 64 * 1024 * 1024
	maxTileInputBytes   int64  = 64 * 1024 * 1024
	tileTemporaryPrefix        = ".deadfin-tile-"
)

// TileComposerConfig controls where composed trickplay tiles are published and
// how many compositions it may run at once. SyncDirectory replaces every
// directory durability operation: root creation and post-rename output.
type TileComposerConfig struct {
	Root          string
	MaxConcurrent int
	SyncDirectory func(*os.File) error
	// AfterRootSecureWalk runs after Root has been securely walked but before
	// its descriptor is adopted. It exists to exercise pathname replacement
	// safety at that ownership boundary.
	AfterRootSecureWalk func() error
}

// TileRequest describes one complete JPEG trickplay tile. SourceNames fill
// cells in row-major order; unused cells in the final tile remain black.
// SourceDir and OutputDir are borrowed for the duration of Compose.
type TileRequest struct {
	SourceDir   *rootfs.Dir
	SourceNames []string
	OutputDir   *rootfs.Dir
	OutputName  string
	CellWidth   int
	CellHeight  int
	Columns     int
	Rows        int
	JPEGQuality int
}

// TileResult identifies a published tile and its encoded size. Path is a
// diagnostic path only; it is never used to access the published file.
type TileResult struct {
	Path       string
	ByteSize   int64
	Dimensions Dimensions
}

// TileComposer composes normalized source frames into JPEG trickplay tiles.
// Its root is pinned for its complete lifecycle, so every handed directory is
// proven beneath the same retained root inode before it is used.
type TileComposer struct {
	runtime       *Runtime
	rootDir       *rootfs.Dir
	limit         chan struct{}
	syncDirectory func(*os.File) error

	lifecycle sync.RWMutex
	closed    bool
	closeErr  error
}

// NewTileComposer constructs a tile composer bound to runtime. Root is created
// if needed, but every component is opened without following symlinks.
func NewTileComposer(runtime *Runtime, config TileComposerConfig) (*TileComposer, error) {
	if err := runtime.begin(); err != nil {
		return nil, err
	}
	runtime.end()
	if config.Root == "" {
		return nil, errors.New("tile composer root is required")
	}
	if config.MaxConcurrent <= 0 {
		return nil, errors.New("tile composer maximum concurrency must be positive")
	}

	syncDirectory := config.SyncDirectory
	if syncDirectory == nil {
		syncDirectory = syncTileDirectory
	}

	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve tile composer root %q: %w", config.Root, err)
	}
	root, err = canonicalOwnedRoot(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tile composer root %q: %w", config.Root, err)
	}
	rootFD, err := openSecureDirectoryTree(root, true, syncDirectory)
	if err != nil {
		return nil, fmt.Errorf("open tile composer root %q: %w", root, err)
	}
	if config.AfterRootSecureWalk != nil {
		if err := config.AfterRootSecureWalk(); err != nil {
			return nil, errors.Join(fmt.Errorf("run tile composer root walk hook: %w", err), unix.Close(rootFD))
		}
	}
	rootDir, err := rootfs.Adopt(rootFD, root)
	if err != nil {
		return nil, fmt.Errorf("adopt tile composer root %q: %w", root, err)
	}

	return &TileComposer{
		runtime: runtime, rootDir: rootDir, limit: make(chan struct{}, config.MaxConcurrent), syncDirectory: syncDirectory,
	}, nil
}

// Close waits for active Compose calls and releases the retained root
// descriptor. It is safe to call more than once.
func (composer *TileComposer) Close() error {
	if composer == nil {
		return nil
	}
	composer.lifecycle.Lock()
	defer composer.lifecycle.Unlock()
	if composer.closed {
		return composer.closeErr
	}
	composer.closed = true
	if composer.rootDir != nil {
		composer.closeErr = composer.rootDir.Close()
	}
	return composer.closeErr
}

// Compose decodes each source, center-crops it to its cell, composes all cells
// on a black canvas, and atomically publishes one complete JPEG via OutputDir.
func (composer *TileComposer) Compose(ctx context.Context, request TileRequest) (TileResult, error) {
	if err := ctx.Err(); err != nil {
		return TileResult{}, err
	}
	if composer == nil || composer.runtime == nil {
		return TileResult{}, ErrRuntimeClosed
	}
	composer.lifecycle.RLock()
	defer composer.lifecycle.RUnlock()
	if composer.closed || composer.rootDir == nil {
		return TileResult{}, errors.New("tile composer is closed")
	}

	outputPath, dimensions, capacity, err := composer.validate(request)
	if err != nil {
		return TileResult{}, err
	}
	if err := validateTileSources(request.SourceNames, capacity); err != nil {
		return TileResult{}, err
	}

	select {
	case composer.limit <- struct{}{}:
		defer func() { <-composer.limit }()
	case <-ctx.Done():
		return TileResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return TileResult{}, err
	}
	if err := composer.runtime.begin(); err != nil {
		return TileResult{}, err
	}
	defer composer.runtime.end()

	encoded, err := composeTileJPEG(ctx, request, dimensions)
	if err != nil {
		return TileResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TileResult{}, err
	}
	if err := composer.publish(ctx, request.OutputDir, request.OutputName, encoded); err != nil {
		return TileResult{}, err
	}
	return TileResult{Path: outputPath, ByteSize: int64(len(encoded)), Dimensions: dimensions}, nil
}

func (composer *TileComposer) validate(request TileRequest) (string, Dimensions, int, error) {
	if request.SourceDir == nil {
		return "", Dimensions{}, 0, errors.New("tile source directory is required")
	}
	if request.OutputDir == nil {
		return "", Dimensions{}, 0, errors.New("tile output directory is required")
	}
	if err := validTileName(request.OutputName); err != nil {
		return "", Dimensions{}, 0, fmt.Errorf("invalid tile output name: %w", err)
	}
	if extension := filepath.Ext(request.OutputName); extension != ".jpg" && extension != ".jpeg" {
		return "", Dimensions{}, 0, fmt.Errorf("tile output name %q is not a JPEG filename", request.OutputName)
	}
	if request.CellWidth <= 0 || request.CellHeight <= 0 || request.Columns <= 0 || request.Rows <= 0 {
		return "", Dimensions{}, 0, errors.New("tile cell and grid dimensions must be positive")
	}
	if request.JPEGQuality < 1 || request.JPEGQuality > 100 {
		return "", Dimensions{}, 0, fmt.Errorf("tile JPEG quality %d is outside 1..100", request.JPEGQuality)
	}

	width, ok := checkedTileProduct(request.CellWidth, request.Columns)
	if !ok {
		return "", Dimensions{}, 0, errors.New("tile width overflows native image dimensions")
	}
	height, ok := checkedTileProduct(request.CellHeight, request.Rows)
	if !ok {
		return "", Dimensions{}, 0, errors.New("tile height overflows native image dimensions")
	}
	if uint64(width) > maxTilePixels/uint64(height) {
		return "", Dimensions{}, 0, fmt.Errorf("tile output pixels exceed limit %d", maxTilePixels)
	}
	capacity, ok := checkedTileProduct(request.Columns, request.Rows)
	if !ok {
		return "", Dimensions{}, 0, errors.New("tile grid capacity overflows")
	}

	sourceContained, err := composer.rootDir.Contains(request.SourceDir, 32)
	if err != nil {
		return "", Dimensions{}, 0, fmt.Errorf("prove tile source directory containment: %w", err)
	}
	if !sourceContained {
		return "", Dimensions{}, 0, errors.New("tile source directory is outside owned root")
	}
	outputContained, err := composer.rootDir.Contains(request.OutputDir, 32)
	if err != nil {
		return "", Dimensions{}, 0, fmt.Errorf("prove tile output directory containment: %w", err)
	}
	if !outputContained {
		return "", Dimensions{}, 0, errors.New("tile output directory is outside owned root")
	}

	return filepath.Join(request.OutputDir.Path(), request.OutputName), Dimensions{Width: width, Height: height}, capacity, nil
}

func checkedTileProduct(left, right int) (int, bool) {
	if left <= 0 || right <= 0 || left > maxNativeImageDimension/right {
		return 0, false
	}
	return left * right, true
}

func validateTileSources(sources []string, capacity int) error {
	if len(sources) == 0 {
		return errors.New("tile sources are required")
	}
	if len(sources) > capacity {
		return fmt.Errorf("tile source count %d exceeds grid capacity %d", len(sources), capacity)
	}
	for _, source := range sources {
		if err := validTileName(source); err != nil {
			return fmt.Errorf("invalid tile source name: %w", err)
		}
	}
	return nil
}

func validTileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.ContainsRune(name, 0) {
		return fmt.Errorf("not a single-component name %q", name)
	}
	return nil
}

func composeTileJPEG(ctx context.Context, request TileRequest, dimensions Dimensions) ([]byte, error) {
	canvas, err := vips.Black(dimensions.Width, dimensions.Height)
	if err != nil {
		return nil, fmt.Errorf("create black tile canvas: %w", err)
	}
	defer canvas.Close()
	if err := canvas.ToColorSpace(vips.InterpretationSRGB); err != nil {
		return nil, fmt.Errorf("normalize tile canvas color space: %w", err)
	}

	for index, name := range request.SourceNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := request.SourceDir.OpenFile(name)
		if err != nil {
			return nil, fmt.Errorf("open tile source %q: %w", name, err)
		}
		data, readErr := readTileInput(file)
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read tile source %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close tile source %q: %w", name, closeErr)
		}
		cell, err := vips.NewThumbnailFromBuffer(data, request.CellWidth, request.CellHeight, vips.InterestingCentre)
		if err != nil {
			return nil, fmt.Errorf("decode and normalize tile source %q: %w", name, err)
		}
		func() {
			defer cell.Close()
			err = normalizeTileCell(cell, request.CellWidth, request.CellHeight)
			if err != nil {
				return
			}
			column := index % request.Columns
			row := index / request.Columns
			err = canvas.Insert(cell, column*request.CellWidth, row*request.CellHeight, false, nil)
		}()
		if err != nil {
			return nil, fmt.Errorf("compose tile source %q: %w", name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, _, err := canvas.ExportJpeg(&vips.JpegExportParams{Quality: request.JPEGQuality, StripMetadata: true})
	if err != nil {
		return nil, fmt.Errorf("encode tile JPEG: %w", err)
	}
	return encoded, nil
}

func readTileInput(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	if info.Size() > maxTileInputBytes {
		return nil, fmt.Errorf("source exceeds %d-byte limit", maxTileInputBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTileInputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxTileInputBytes {
		return nil, fmt.Errorf("source exceeds %d-byte limit", maxTileInputBytes)
	}
	return data, nil
}

func normalizeTileCell(cell *vips.ImageRef, width, height int) error {
	if cell.Width() != width || cell.Height() != height {
		return fmt.Errorf("normalized source dimensions = %dx%d, want %dx%d", cell.Width(), cell.Height(), width, height)
	}
	if err := cell.ToColorSpace(vips.InterpretationSRGB); err != nil {
		return fmt.Errorf("normalize source color space: %w", err)
	}
	if cell.HasAlpha() {
		if err := cell.Flatten(&vips.Color{}); err != nil {
			return fmt.Errorf("flatten source alpha: %w", err)
		}
	}
	return nil
}

func (composer *TileComposer) publish(ctx context.Context, outputDir *rootfs.Dir, outputName string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := outputDir.ReplaceFileDurable(outputName, tileTemporaryPrefix, data, 0o644, composer.syncDirectory); err != nil {
		return fmt.Errorf("publish tile: %w", err)
	}
	return nil
}

func syncTileDirectory(directory *os.File) error {
	return directory.Sync()
}

func syncTileDirectoryFD(directoryFD int, directoryPath string, syncDirectory func(*os.File) error) (err error) {
	fd, err := unix.Dup(directoryFD)
	if err != nil {
		return fmt.Errorf("duplicate tile directory %q: %w", directoryPath, err)
	}
	directory := os.NewFile(uintptr(fd), directoryPath)
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync tile directory %q: %w", directoryPath, err)
	}
	return nil
}

func canonicalOwnedRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return root, nil
	}

	current := root
	var missing []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if current == root {
				if info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("owned root must not be a symlink")
				}
				if !info.IsDir() {
					return "", errors.New("owned root is not a directory")
				}
				parent, err := filepath.EvalSymlinks(filepath.Dir(root))
				if err != nil {
					return "", err
				}
				return filepath.Join(parent, filepath.Base(root)), nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				current, err = filepath.EvalSymlinks(current)
				if err != nil {
					return "", err
				}
			} else if !info.IsDir() {
				return "", fmt.Errorf("root ancestor %q is not a directory", current)
			}
			return filepath.Join(append([]string{current}, missing...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func openSecureDirectoryTree(path string, create bool, syncDirectory func(*os.File) error) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, errors.New("directory path must be absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	current := fd
	currentPath := string(filepath.Separator)
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, created, err := openSecureDirectoryAt(current, part, create)
		if err != nil {
			_ = unix.Close(current)
			return -1, err
		}
		nextPath := filepath.Join(currentPath, part)
		if created {
			if err := syncTileDirectoryFD(current, currentPath, syncDirectory); err != nil {
				_ = unix.Close(next)
				_ = unix.Close(current)
				return -1, err
			}
			if err := syncTileDirectoryFD(next, nextPath, syncDirectory); err != nil {
				_ = unix.Close(next)
				_ = unix.Close(current)
				return -1, err
			}
		}
		if err := unix.Close(current); err != nil {
			_ = unix.Close(next)
			return -1, err
		}
		current = next
		currentPath = nextPath
	}
	return current, nil
}

func openSecureDirectoryAt(parentFD int, name string, create bool) (int, bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil || !create || !errors.Is(err, unix.ENOENT) {
		return fd, false, err
	}
	mkdirErr := unix.Mkdirat(parentFD, name, 0o755)
	created := mkdirErr == nil
	if !created && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, false, mkdirErr
	}
	fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	return fd, created, err
}
