// SPDX-License-Identifier: GPL-2.0-only

package imageserve_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/imageserve"
)

type fakeDownloader struct {
	body  []byte
	err   error
	calls int
}

func (f *fakeDownloader) Fetch(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

func TestRemoteCacheDownloadsOnceThenServesFromDisk(t *testing.T) {
	payload := []byte("\xff\xd8\xff\xe0jpeg-bytes")
	downloader := &fakeDownloader{body: payload}
	cache := imageserve.NewRemoteCache(downloader, t.TempDir())

	const url = "https://image.tmdb.org/t/p/original/abc123.jpg"
	path, modified, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("first Local: %v", err)
	}
	if modified.IsZero() {
		t.Error("first Local modified time is zero, want the published file mtime")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Errorf("published bytes = %q, want the downloaded payload", stored)
	}
	if got := path[len(path)-4:]; got != ".jpg" {
		t.Errorf("published path extension = %q, want .jpg preserved from URL", got)
	}

	secondPath, _, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("second Local: %v", err)
	}
	if secondPath != path {
		t.Errorf("second Local path = %q, want stable %q", secondPath, path)
	}
	if downloader.calls != 1 {
		t.Errorf("downloader calls = %d, want exactly one download for a cached image", downloader.calls)
	}
}

func TestRemoteCacheDefaultsExtensionWhenURLHasNone(t *testing.T) {
	cache := imageserve.NewRemoteCache(&fakeDownloader{body: []byte("bytes")}, t.TempDir())
	path, _, err := cache.Local(context.Background(), "https://cdn.example/image?size=orig")
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if got := path[len(path)-4:]; got != ".jpg" {
		t.Errorf("extension = %q, want default .jpg", got)
	}
}

func TestRemoteCachePropagatesDownloadError(t *testing.T) {
	wantErr := errors.New("boom")
	cache := imageserve.NewRemoteCache(&fakeDownloader{err: wantErr}, t.TempDir())
	if _, _, err := cache.Local(context.Background(), "https://cdn.example/x.jpg"); !errors.Is(err, wantErr) {
		t.Errorf("Local error = %v, want the download error", err)
	}
}

// TestRemoteCacheStoreWritesAndServes verifies Store writes validated bytes
// to the same cache the serving path reads, so a subsequent Local call for
// the same URL serves the stored copy without downloading.
func TestRemoteCacheStoreWritesAndServes(t *testing.T) {
	payload := []byte("\xff\xd8\xff\xe0stored-jpeg")
	downloader := &fakeDownloader{err: errors.New("should not download")}
	cache := imageserve.NewRemoteCache(downloader, t.TempDir())

	const url = "https://image.tmdb.org/t/p/original/poster.jpg"
	path, _, err := cache.Store(url, payload)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(stored) != string(payload) {
		t.Errorf("stored bytes = %q, want %q", stored, payload)
	}

	// A subsequent Local call must serve the stored copy without downloading.
	servedPath, _, err := cache.Local(context.Background(), url)
	if err != nil {
		t.Fatalf("Local after Store: %v", err)
	}
	if servedPath != path {
		t.Errorf("served path = %q, want %q (same as stored)", servedPath, path)
	}
	if downloader.calls != 0 {
		t.Errorf("downloader calls = %d, want 0 (served from Store)", downloader.calls)
	}
}
