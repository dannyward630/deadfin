// SPDX-License-Identifier: GPL-2.0-only

// Package attest resolves the repository root and gates the source-attestation
// tests that compare Deadfin behavior against the pinned upstream inputs.
//
// The seven source-attestation tests read three kinds of input:
// the corpora and golden-record manifests, which are committed to this
// repository; the canonical fixture tree, which tools/verify-env generates;
// and the pinned Jellyfin source tree, which is never committed. `make
// upstream` fetches Jellyfin at the revision in upstream/PIN into
// upstream/jellyfin. In a checkout without that tree the tests must
// self-skip. The contract is strict: the tests skip *only* when the pinned
// upstream tree is absent. Once it is present, a missing, malformed,
// digest-mismatched, or source-mismatched input remains a hard failure. Set
// DEADFIN_REQUIRE_UPSTREAM=1 to make even the absent-tree case fatal — the
// strict CI job uses this so an accidental skip cannot mask a regression.
package attest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// UpstreamMarker identifies the pinned Jellyfin source tree. It is the stable
// file whose presence beneath the repository root means "the upstream tree at
// upstream/PIN is checked out." It is the only attestation input that is not
// committed.
const UpstreamMarker = "upstream/jellyfin/Jellyfin.sln"

// ErrRootAbsent is returned by Resolve when the pinned upstream tree is
// absent. Tests translate it into a skip unless DEADFIN_REQUIRE_UPSTREAM=1.
var ErrRootAbsent = errors.New("pinned upstream tree not found")

// RequireEnv makes an absent upstream tree fatal instead of skippable.
const RequireEnv = "DEADFIN_REQUIRE_UPSTREAM"

// Root is the resolved repository root. The pinned Jellyfin tree, corpora,
// and golden-record fixtures all live beneath it.
type Root struct {
	// Path is the absolute repository root directory, or "" when the pinned
	// upstream tree is absent.
	Path string
}

// Resolve walks up from start to find the repository root (the nearest
// ancestor containing go.mod with the deadfin module path). The root is
// considered present only when UpstreamMarker exists beneath it.
//
// When the marker is absent, Resolve returns Root{Path: ""} and ErrRootAbsent.
// Callers pass the result to Gate to honor the skip/fail contract.
func Resolve(start string) Root {
	root := findRepoRoot(start)
	if root == "" {
		return Root{}
	}
	if !markerExists(root) {
		return Root{}
	}
	return Root{Path: root}
}

// Gate enforces the source-attestation skip/fail contract for a test. It must
// be called at the top of every attestation test before any pinned input is
// read.
//
// When the upstream tree is present, Gate returns a non-empty root path the
// test uses to locate its inputs, and the test proceeds; any later
// missing/malformed/mismatched input is a hard failure.
//
// When the upstream tree is absent, Gate skips the test unless
// DEADFIN_REQUIRE_UPSTREAM=1, in which case it fatals.
func Gate(t testing.TB, root Root) string {
	t.Helper()
	if root.Path != "" {
		return root.Path
	}
	if os.Getenv(RequireEnv) == "1" {
		t.Fatalf("%s=1 but the pinned upstream tree is absent (no %s); run make upstream. Source attestation cannot run", RequireEnv, UpstreamMarker)
	}
	t.Skipf("pinned upstream tree absent (no %s); source attestation self-skips. Run make upstream, or set %s=1 to make this fatal.", UpstreamMarker, RequireEnv)
	return ""
}

// GateForTest is a convenience for tests that resolve the root from their own
// working directory. It calls Resolve then Gate and returns the repository
// root path to use for pinned inputs.
func GateForTest(t testing.TB) string {
	t.Helper()
	start, err := os.Getwd()
	if err != nil {
		t.Fatalf("attest: determine working directory: %v", err)
	}
	return Gate(t, Resolve(start))
}

// AdjacentPath joins a root-relative path against the resolved root. It is a
// thin wrapper so tests name the intent at every read site.
func AdjacentPath(root, relative string) string {
	return filepath.Join(root, relative)
}

// findRepoRoot returns the nearest ancestor of start (inclusive) that owns a
// go.mod declaring the deadfin module path, or "" if none is found. It stops
// at the filesystem boundary rather than walking past a real root.
func findRepoRoot(start string) string {
	vol := filepath.VolumeName(start)
	dir := start
	for {
		mod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(mod); err == nil {
			if isDeadfinModule(data) {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		// Stop walking above the volume root on Windows-style paths.
		if vol != "" && dir == vol+string(filepath.Separator) {
			return ""
		}
		dir = parent
	}
}

// isDeadfinModule reports whether a go.mod body declares the deadfin module.
func isDeadfinModule(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")) == "github.com/deadfin-org/deadfin"
		}
	}
	return false
}

// markerExists reports whether the upstream marker exists beneath root.
func markerExists(root string) bool {
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, UpstreamMarker))
	return err == nil
}
