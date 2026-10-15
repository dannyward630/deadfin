// SPDX-License-Identifier: GPL-2.0-only

package imageserve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
)

// Downloader fetches the raw bytes of a remote image. The concrete
// implementation enforces the remote-image HTTPS, address, size, and content
// checks; this seam keeps imageserve independent and testable.
type Downloader interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// RemoteCache downloads remote provider image URLs into a local cache directory
// and serves the cached copy on every subsequent request, so serving never
// hot-links and never re-downloads a poster that is already on disk. It matches
// upstream's remote-image contract (download once, then serve the resized local
// copy through the normal image pipeline).
type RemoteCache struct {
	downloader Downloader
	dir        string
}

// NewRemoteCache constructs a RemoteCache that stores downloads beneath dir.
func NewRemoteCache(downloader Downloader, dir string) *RemoteCache {
	return &RemoteCache{downloader: downloader, dir: dir}
}

// Local returns a local path for url, downloading and caching it on first use.
// The returned modification time is the cached file's, so it stays stable across
// server restarts (the file persists) and feeds the resize cache identity.
func (cache *RemoteCache) Local(ctx context.Context, url string) (string, time.Time, error) {
	if cache == nil || cache.downloader == nil {
		return "", time.Time{}, ErrRemoteUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}

	path := cache.pathFor(url)
	if info, err := os.Stat(path); err == nil {
		return path, info.ModTime(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", time.Time{}, fmt.Errorf("stat remote image cache %q: %w", path, err)
	}

	body, err := cache.downloader.Fetch(ctx, url)
	if err != nil {
		return "", time.Time{}, err
	}
	if len(body) == 0 {
		return "", time.Time{}, fmt.Errorf("remote image %q: empty body", url)
	}
	if err := writeAtomically(path, body); err != nil {
		return "", time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stat published remote image %q: %w", path, err)
	}
	return path, info.ModTime(), nil
}

// Store writes validated image bytes into the same cache the serving path reads,
// reusing pathFor + writeAtomically so the bytes land at the same on-disk
// location a Local() call would use. It does not download: the caller has already
// fetched and validated the bytes. The item row is updated only after this
// succeeds — a failed write persists nothing.
func (cache *RemoteCache) Store(url string, body []byte) (string, time.Time, error) {
	if cache == nil || len(body) == 0 {
		return "", time.Time{}, ErrRemoteUnavailable
	}
	path := cache.pathFor(url)
	if err := writeAtomically(path, body); err != nil {
		return "", time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stat published remote image %q: %w", path, err)
	}
	return path, info.ModTime(), nil
}

// pathFor derives the on-disk location for url. The MD5GUID digest matches the
// resize cache's fan-out layout (one hex-nibble subdirectory), and the URL's
// extension is preserved so the processor decodes the download natively.
func (cache *RemoteCache) pathFor(url string) string {
	digest := images.MD5GUID(url)
	return filepath.Join(cache.dir, digest[:1], digest+remoteExtension(url))
}

// remoteExtension extracts a decodable image extension from a URL, defaulting to
// .jpg (TMDb and most provider CDNs serve JPEG) when the URL carries none.
func remoteExtension(url string) string {
	trimmed := url
	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	switch ext := strings.ToLower(filepath.Ext(trimmed)); ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".svg":
		return ext
	default:
		return ".jpg"
	}
}

// writeAtomically publishes body to path via a temporary file and rename, so a
// concurrent reader never observes a partial download.
func writeAtomically(path string, body []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create remote image cache directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".deadfin-remote-image-*")
	if err != nil {
		return fmt.Errorf("create temporary remote image file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary remote image permissions: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		return fmt.Errorf("write temporary remote image file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary remote image file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary remote image file: %w", err)
	}
	temporary = nil
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish remote image file: %w", err)
	}
	return nil
}
