// SPDX-License-Identifier: GPL-2.0-only

package attest_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/attest"
)

// TestGateForTest_ProceedsWhenUpstreamPresent exercises the strict CI path:
// when the pinned upstream tree is present, Gate returns the repository root
// and the test proceeds. This is the case the CI source-attestation job
// relies on.
func TestGateForTest_ProceedsWhenUpstreamPresent(t *testing.T) {
	start, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := attest.Resolve(start)
	if root.Path == "" {
		t.Skip("pinned upstream tree absent in this checkout; cannot exercise the present path")
	}
	gated := attest.Gate(t, root)
	if gated != root.Path {
		t.Fatalf("Gate returned %q, want resolved root %q", gated, root.Path)
	}
	if _, err := os.Stat(filepath.Join(gated, attest.UpstreamMarker)); err != nil {
		t.Fatalf("gated root marker missing: %v", err)
	}
}

// TestGate_SkipsWhenUpstreamAbsent verifies the plain-checkout contract: with
// no upstream tree and no require flag, Gate skips rather than fails.
func TestGate_SkipsWhenUpstreamAbsent(t *testing.T) {
	if err := os.Unsetenv(attest.RequireEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	rec := &fakeTB{}
	if skipped := callGateAndReportSkip(rec, attest.Root{}); !skipped {
		t.Fatalf("expected Gate on absent upstream to skip; fatal=%v", rec.fatal)
	}
	if rec.fatal {
		t.Fatalf("Gate on absent upstream without %s=1 must not fatal", attest.RequireEnv)
	}
}

// TestGate_FatalsWhenUpstreamAbsentAndRequired verifies DEADFIN_REQUIRE_UPSTREAM=1
// makes an absent upstream tree fatal — the strict CI guarantee that skips
// cannot mask a missing checkout.
func TestGate_FatalsWhenUpstreamAbsentAndRequired(t *testing.T) {
	t.Setenv(attest.RequireEnv, "1")
	rec := &fakeTB{}
	callGateAndReportSkip(rec, attest.Root{})
	if !rec.fatal {
		t.Fatalf("expected Gate with %s=1 and absent upstream to fatal, it did not", attest.RequireEnv)
	}
}

// TestResolve_FindsRepoRoot confirms the helper locates the repository root
// from a subdirectory. This guards the upward-walk logic that every
// attestation test depends on.
func TestResolve_FindsRepoRoot(t *testing.T) {
	start, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := attest.Resolve(start)
	if root.Path == "" {
		t.Skip("pinned upstream tree absent in this checkout")
	}
	mod, err := os.ReadFile(filepath.Join(root.Path, "go.mod"))
	if err != nil {
		t.Fatalf("Resolve root %q does not contain go.mod: %v", root.Path, err)
	}
	if !strings.HasPrefix(string(mod), "module github.com/deadfin-org/deadfin") {
		t.Fatalf("Resolve root %q has unexpected go.mod", root.Path)
	}
	if _, err := os.Stat(filepath.Join(root.Path, "docs", "00-overview.md")); err != nil {
		t.Fatalf("Resolve root %q does not hold the plan: %v", root.Path, err)
	}
}

// callGateAndReportSkip runs attest.Gate against a fake TB in a goroutine so
// the fakeTB's Skipf/Fatalf (which call runtime.Goexit to stop the test) can
// be observed. It reports whether Gate chose to skip.
func callGateAndReportSkip(rec *fakeTB, root attest.Root) (skipped bool) {
	done := make(chan struct{})
	go func() {
		defer func() { recover(); close(done) }()
		attest.Gate(rec, root)
	}()
	<-done
	return rec.skipped
}

type fakeTB struct {
	testing.TB
	fatal   bool
	skipped bool
}

func (f *fakeTB) Helper()               {}
func (f *fakeTB) Fatalf(string, ...any) { f.fatal = true; runtime.Goexit() }
func (f *fakeTB) Skipf(string, ...any)  { f.skipped = true; runtime.Goexit() }
func (f *fakeTB) Skip(...any)           { f.skipped = true; runtime.Goexit() }
