// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// fakePackage serves a synthetic .nupkg with one library per architecture
// and counts the requests.
func fakePackage(t *testing.T) (manifest, *atomic.Int32) {
	t.Helper()
	libs := map[string][]byte{"amd64": []byte("amd64 library"), "arm64": []byte("arm64 library")}
	entries := map[string]string{"amd64": "runtimes/linux-x64/native/libSkiaSharp.so", "arm64": "runtimes/linux-arm64/native/libSkiaSharp.so"}
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, arch := range []string{"amd64", "arm64"} {
		w, err := archive.Create(entries[arch])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(libs[arch]); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	pin := manifest{
		Package:   "Test.Package",
		Version:   "1.0.0",
		URL:       server.URL + "/test.nupkg",
		SHA256:    sum(body),
		Libraries: map[string]library{},
	}
	for arch, data := range libs {
		pin.Libraries[arch] = library{Entry: entries[arch], SHA256: sum(data)}
	}
	return pin, &requests
}

func runTool(t *testing.T, pin manifest, args ...string) error {
	t.Helper()
	return run(context.Background(), args, pin, http.DefaultClient, io.Discard, io.Discard)
}

func TestFetchInstallsAndCaches(t *testing.T) {
	pin, requests := fakePackage(t)
	cache := t.TempDir()
	out := filepath.Join(t.TempDir(), "lib", "libSkiaSharp.so")

	if err := runTool(t, pin, "-arch", "arm64", "-cache", cache, "-o", out); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "arm64 library" {
		t.Fatalf("installed library = %q, %v", got, err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, want 0755", info.Mode().Perm())
	}
	// The download fills the cache for every architecture.
	other := filepath.Join(t.TempDir(), "libSkiaSharp.so")
	if err := runTool(t, pin, "-arch", "amd64", "-cache", cache, "-offline", "-o", other); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(other); string(got) != "amd64 library" {
		t.Fatalf("amd64 library = %q", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nupkg-") {
			t.Fatalf("package left in cache: %s", entry.Name())
		}
	}
}

func TestOfflineCacheMissFails(t *testing.T) {
	pin, requests := fakePackage(t)
	err := runTool(t, pin, "-arch", "amd64", "-cache", t.TempDir(), "-offline", "-o", filepath.Join(t.TempDir(), "lib.so"))
	if err == nil || !strings.Contains(err.Error(), "not in the cache") {
		t.Fatalf("offline miss error = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestPackageChecksumMismatchFails(t *testing.T) {
	pin, _ := fakePackage(t)
	pin.SHA256 = strings.Repeat("0", 64)
	cache := t.TempDir()
	out := filepath.Join(t.TempDir(), "lib.so")
	err := runTool(t, pin, "-arch", "amd64", "-cache", cache, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output written after a checksum mismatch: %v", err)
	}
}

func TestLibraryChecksumMismatchFails(t *testing.T) {
	pin, _ := fakePackage(t)
	lib := pin.Libraries["amd64"]
	lib.SHA256 = strings.Repeat("0", 64)
	pin.Libraries["amd64"] = lib
	cache := t.TempDir()
	err := runTool(t, pin, "-arch", "amd64", "-cache", cache, "-o", filepath.Join(t.TempDir(), "lib.so"))
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := os.Stat(pin.cachePath(cache, "amd64")); !os.IsNotExist(err) {
		t.Fatalf("unverified library cached: %v", err)
	}
}

func TestCorruptCacheIsRefetched(t *testing.T) {
	pin, requests := fakePackage(t)
	cache := t.TempDir()
	cached := pin.cachePath(cache, "amd64")
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "lib.so")
	if err := runTool(t, pin, "-arch", "amd64", "-cache", cache, "-o", out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "amd64 library" {
		t.Fatalf("installed library = %q", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestUnsupportedArchitecture(t *testing.T) {
	pin, _ := fakePackage(t)
	err := runTool(t, pin, "-arch", "ppc64le", "-cache", t.TempDir(), "-o", filepath.Join(t.TempDir(), "lib.so"))
	if err == nil || !strings.Contains(err.Error(), "unsupported architecture") {
		t.Fatalf("error = %v", err)
	}
}

// TestPinMatchesThirdPartyNotices keeps the pin and the license directory on
// the same version.
func TestPinMatchesThirdPartyNotices(t *testing.T) {
	pin := pinned()
	for _, name := range []string{"LICENSE.txt", "THIRD-PARTY-NOTICES.txt"} {
		if _, err := os.Stat(filepath.Join("..", "..", "third_party", "skia-sharp", pin.Version, name)); err != nil {
			t.Fatalf("license file for SkiaSharp %s: %v", pin.Version, err)
		}
	}
	for arch, lib := range pin.Libraries {
		if len(lib.SHA256) != 64 {
			t.Fatalf("%s sha256 %q is not 64 hex digits", arch, lib.SHA256)
		}
	}
}
