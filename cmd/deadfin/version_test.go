// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/buildinfo"
)

const overlaySHA = "0123456789abcdef0123456789abcdef01234567"

// TestVersionCommandReportsCheckedInDefaults proves a plain build (no
// overlay) prints the unknown/local identity and never opens a listener.
func TestVersionCommandReportsCheckedInDefaults(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	output := runDeadfinRoutes(t, "--listen", occupied.Addr().String(), "version")
	if got, want := string(output), "deadfin-server 0.0.0-local (local, unknown)\n"; got != want {
		t.Fatalf("deadfin version = %q, want %q", got, want)
	}
}

func TestPrintVersionCompiledIdentity(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	if buildinfo.Revision != buildinfo.UnknownRevision || !buildinfo.Dirty {
		t.Skip("test binary built with an overlay; the checked-in defaults test covers plain builds")
	}
	if got := buf.String(); got != "deadfin-server 0.0.0-local (local, unknown)\n" {
		t.Fatalf("printVersion = %q", got)
	}
}

// TestLogBuildIdentityAttributes captures the startup record and asserts
// every attribute key and value.
func TestLogBuildIdentityAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logBuildIdentity(context.Background(), logger)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode record %q: %v", buf.String(), err)
	}
	want := map[string]any{
		"level":                "INFO",
		"msg":                  "deadfin build",
		"component":            buildinfo.Component,
		"version":              buildinfo.Version,
		"channel":              buildinfo.Channel,
		"revision":             buildinfo.Revision,
		"dirty":                buildinfo.Dirty,
		"build_time":           buildinfo.BuildTime,
		"compatibility_target": buildinfo.CompatibilityTarget,
		"api_version":          float64(buildinfo.APIVersion),
		"capabilities":         []any{"build-info"},
	}
	for key, value := range want {
		got, ok := record[key]
		if !ok {
			t.Errorf("record lacks %q: %s", key, buf.String())
			continue
		}
		if list, isList := value.([]any); isList {
			gotList, _ := got.([]any)
			if len(gotList) != len(list) || gotList[0] != list[0] {
				t.Errorf("%s = %v, want %v", key, got, value)
			}
			continue
		}
		if got != value {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got, got, value, value)
		}
	}
}

// overlayBuild stages an explicit identity with tools/buildinfo-gen, builds
// the real binary with -overlay, and returns the binary path. ok is false
// when the generator rejected the identity.
func overlayBuild(t *testing.T, env map[string]string) (binary string, ok bool) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	gen := exec.Command("go", "run", "./tools/buildinfo-gen", "-o", staging)
	gen.Dir = root
	gen.Env = append(os.Environ(), "REVISION=", "VERSION=", "CHANNEL=", "DIRTY=", "SOURCE_DATE_EPOCH=1700000000")
	for key, value := range env {
		gen.Env = append(gen.Env, key+"="+value)
	}
	if out, err := gen.CombinedOutput(); err != nil {
		t.Logf("buildinfo-gen rejected: %s", out)
		if _, statErr := os.Stat(filepath.Join(staging, "overlay.json")); statErr == nil {
			t.Fatalf("generator failed but wrote overlay.json")
		}
		return "", false
	}
	binary = filepath.Join(staging, "deadfin")
	build := exec.Command("go", "build", "-overlay", filepath.Join(staging, "overlay.json"), "-o", binary, "./cmd/deadfin")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -overlay: %v\n%s", err, out)
	}
	return binary, true
}

func runBinary(t *testing.T, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestVersionCommandReportsOverlayIdentity proves an overlay-built binary
// reports the staged identity, a dirty overlay prints the suffix, and a
// dirty release is rejected before any binary exists. Like this package,
// the binary needs the cgo toolchain and libvips.
func TestVersionCommandReportsOverlayIdentity(t *testing.T) {
	train := map[string]string{"REVISION": overlaySHA, "VERSION": "v0.2.3-dev-" + overlaySHA[:12], "CHANNEL": "train", "DIRTY": "false"}
	binary, ok := overlayBuild(t, train)
	if !ok {
		t.Fatal("generator rejected a valid train identity")
	}
	if got, want := runBinary(t, binary, "version"), "deadfin-server v0.2.3-dev-"+overlaySHA[:12]+" (train, "+overlaySHA[:12]+")\n"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
	routes := runBinary(t, binary, "routes", "--json")
	if !strings.Contains(routes, `"NATIVE:GetBuild"`) {
		t.Fatalf("routes --json lacks NATIVE:GetBuild: %s", routes)
	}

	train["DIRTY"] = "-dirty"
	binary, ok = overlayBuild(t, train)
	if !ok {
		t.Fatal("generator rejected a dirty train identity")
	}
	if got, want := runBinary(t, binary, "version"), "deadfin-server v0.2.3-dev-"+overlaySHA[:12]+" (train, "+overlaySHA[:12]+", dirty)\n"; got != want {
		t.Fatalf("dirty version = %q, want %q", got, want)
	}

	if _, ok := overlayBuild(t, map[string]string{"REVISION": overlaySHA, "VERSION": "v0.2.3", "CHANNEL": "release", "DIRTY": "true"}); ok {
		t.Fatal("generator accepted a dirty release")
	}
}
