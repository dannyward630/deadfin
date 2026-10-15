// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"golang.org/x/sys/unix"
)

type readerLifecycleMetadataSource struct{}

func (readerLifecycleMetadataSource) Trickplay(context.Context, dto.UUID, int) (Metadata, error) {
	return Metadata{}, ErrNotFound
}

type readerLifecycleAccess struct{}

func (readerLifecycleAccess) Visible(context.Context, dto.UUID, dto.UUID, bool) (bool, error) {
	return true, nil
}

func newReaderLifecycleService(t *testing.T) *ReaderService {
	t.Helper()
	return NewReaderService(ReaderServiceConfig{
		Metadata:    readerLifecycleMetadataSource{},
		Access:      readerLifecycleAccess{},
		Publication: NewPublicationCoordinator(),
		Root:        t.TempDir(),
	})
}

func TestReaderServiceRetainsRootDescriptorUntilIdempotentClose(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create artifact root: %v", err)
	}
	service := NewReaderService(ReaderServiceConfig{
		Metadata:    readerLifecycleMetadataSource{},
		Access:      readerLifecycleAccess{},
		Publication: NewPublicationCoordinator(),
		Root:        root,
	})
	rootFD := service.rootFD
	if rootFD < 0 {
		t.Fatalf("root descriptor = %d, want a live descriptor after successful construction", rootFD)
	}
	var rootPathStat unix.Stat_t
	if err := unix.Stat(root, &rootPathStat); err != nil {
		t.Fatalf("stat artifact root: %v", err)
	}
	var rootDescriptorStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootDescriptorStat); err != nil {
		t.Fatalf("construction root descriptor is not valid: %v", err)
	}
	if rootDescriptorStat.Dev != rootPathStat.Dev || rootDescriptorStat.Ino != rootPathStat.Ino {
		t.Fatalf("construction root descriptor identity = (%d, %d), want root identity (%d, %d)", rootDescriptorStat.Dev, rootDescriptorStat.Ino, rootPathStat.Dev, rootPathStat.Ino)
	}

	movedRoot := filepath.Join(workspace, "moved-root")
	if err := os.Rename(root, movedRoot); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skipf("root rename is unsupported on this filesystem: %v", err)
		}
		t.Fatalf("rename artifact root: %v", err)
	}
	if err := unix.Fstat(rootFD, &unix.Stat_t{}); err != nil {
		t.Fatalf("root descriptor did not remain valid after root rename: %v", err)
	}

	if err := service.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := unix.Fstat(rootFD, &unix.Stat_t{}); !errors.Is(err, unix.EBADF) {
		t.Fatalf("root descriptor after Close() error = %v, want EBADF", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if err := unix.Fstat(rootFD, &unix.Stat_t{}); !errors.Is(err, unix.EBADF) {
		t.Fatalf("root descriptor after second Close() error = %v, want EBADF", err)
	}
}

func TestReaderServiceCloseRejectsSubsequentReads(t *testing.T) {
	service := newReaderLifecycleService(t)
	if err := service.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	playlist, found, err := service.Playlist(t.Context(), ReadRequest{})
	if err == nil {
		t.Fatalf("Playlist() after Close() = (%q, %t, nil), want error", playlist, found)
	}
	if playlist != "" || found {
		t.Fatalf("Playlist() after Close() = (%q, %t), want empty not found result", playlist, found)
	}

	artifact, err := service.Tile(t.Context(), ReadRequest{}, 0)
	if err == nil {
		t.Fatal("Tile() after Close() error = nil, want error")
	}
	if artifact.Body != nil {
		_ = artifact.Body.Close()
		t.Fatal("Tile() after Close() returned an artifact")
	}
}

func TestReaderServiceCloseIsSafeForInvalidAndNilServices(t *testing.T) {
	invalid := NewReaderService(ReaderServiceConfig{})
	if err := invalid.Close(); err != nil {
		t.Fatalf("invalid service Close(): %v", err)
	}
	if err := invalid.Close(); err != nil {
		t.Fatalf("invalid service second Close(): %v", err)
	}

	var nilService *ReaderService
	if err := nilService.Close(); err != nil {
		t.Fatalf("nil service Close(): %v", err)
	}
}

func TestReaderServiceCloseIsConcurrentAndIdempotent(t *testing.T) {
	service := newReaderLifecycleService(t)
	rootFD := service.rootFD
	if rootFD < 0 {
		t.Fatalf("root descriptor = %d, want a live descriptor after successful construction", rootFD)
	}

	const closers = 16
	start := make(chan struct{})
	errorsByCloser := make(chan error, closers)
	var wait sync.WaitGroup
	wait.Add(closers)
	for range closers {
		go func() {
			defer wait.Done()
			<-start
			errorsByCloser <- service.Close()
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByCloser)
	for err := range errorsByCloser {
		if err != nil {
			t.Errorf("concurrent Close(): %v", err)
		}
	}
	if err := unix.Fstat(rootFD, &unix.Stat_t{}); !errors.Is(err, unix.EBADF) {
		t.Fatalf("root descriptor after concurrent Close() error = %v, want EBADF", err)
	}
}
