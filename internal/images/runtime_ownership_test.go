// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
)

const nativeRuntimeOwnershipMode = "DEADFIN_NATIVE_RUNTIME_OWNERSHIP_MODE"

// libvips shutdown is terminal within one process. Each destructive lifecycle
// case therefore runs in a fresh copy of the test binary, while still using the
// public Runtime and Processor boundary a server process uses.
func TestNativeRuntimeOwnershipIsolation(t *testing.T) {
	requireNativeVips(t)

	if mode := os.Getenv(nativeRuntimeOwnershipMode); mode != "" {
		switch mode {
		case "forged":
			testForgedRuntimeCannotAffectOwner(t)
		case "copied":
			testCopiedRuntimeSharesOwnerState(t)
		default:
			t.Fatalf("unknown native runtime ownership mode %q", mode)
		}
		return
	}

	for _, mode := range []string{"forged", "copied"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestNativeRuntimeOwnershipIsolation$")
			command.Env = append(os.Environ(), nativeRuntimeOwnershipMode+"="+mode)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("runtime ownership child %q failed: %v\n%s", mode, err, output)
			}
		})
	}
}

func testForgedRuntimeCannotAffectOwner(t *testing.T) {
	t.Helper()
	runtimeOwner, processor := openOwnershipProcessor(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := runtimeOwner.Close(); err != nil {
				t.Errorf("owner Runtime.Close() during cleanup: %v", err)
			}
		}
	})

	forged := new(images.Runtime)
	if err := forged.Close(); err != nil && !errors.Is(err, images.ErrRuntimeClosed) {
		t.Errorf("zero Runtime.Close() error = %v, want only a harmless ErrRuntimeClosed or nil", err)
	}
	if _, err := images.NewProcessor(forged, images.ProcessorConfig{CacheRoot: t.TempDir(), MaxConcurrent: 1}); err == nil {
		t.Error("NewProcessor(forged Runtime) error = nil, want rejection of a runtime that did not acquire libvips ownership")
	}

	got := mustProcess(t, processor, context.Background(), nativeOptions(writePNGSource(t, t.TempDir()), images.FormatPNG, images.ResizeOptions{}))
	assertImageDimensions(t, got.Path, images.Dimensions{Width: 3, Height: 2})

	if err := runtimeOwner.Close(); err != nil {
		t.Fatalf("owner Runtime.Close() error: %v", err)
	}
	closed = true
}

func testCopiedRuntimeSharesOwnerState(t *testing.T) {
	t.Helper()
	runtimeOwner, processor := openOwnershipProcessor(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := runtimeOwner.Close(); err != nil {
				t.Errorf("owner Runtime.Close() during cleanup: %v", err)
			}
		}
	})

	copyOwner := *runtimeOwner
	if err := copyOwner.Close(); err != nil {
		t.Fatalf("copied Runtime.Close() error: %v", err)
	}
	if _, err := processor.Process(context.Background(), nativeOptions(writePNGSource(t, t.TempDir()), images.FormatPNG, images.ResizeOptions{})); !errors.Is(err, images.ErrRuntimeClosed) {
		t.Errorf("Process() after copied Runtime.Close() error = %v, want ErrRuntimeClosed from the shared owner state", err)
	}
	if _, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{CacheRoot: t.TempDir(), MaxConcurrent: 1}); err == nil {
		t.Error("NewProcessor(owner after copied close) error = nil, want rejection of the shared closed state")
	}
	if err := runtimeOwner.Close(); err != nil {
		t.Errorf("owner Runtime.Close() after copied close error: %v", err)
	}
	if err := copyOwner.Close(); err != nil {
		t.Errorf("copied Runtime.Close() second call error: %v", err)
	}
	closed = true
}

func openOwnershipProcessor(t *testing.T) (*images.Runtime, *images.Processor) {
	t.Helper()
	runtimeOwner, err := images.OpenRuntime(context.Background())
	if err != nil {
		t.Fatalf("OpenRuntime() error: %v", err)
	}
	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{CacheRoot: t.TempDir(), MaxConcurrent: 1})
	if err != nil {
		if closeErr := runtimeOwner.Close(); closeErr != nil {
			t.Errorf("Runtime.Close() after NewProcessor failure: %v", closeErr)
		}
		t.Fatalf("NewProcessor(owner) error: %v", err)
	}
	return runtimeOwner, processor
}
