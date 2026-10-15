// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

func TestExtractorRunPassesSourceDescriptorAndArgs(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.mkv")
	if err := os.WriteFile(input, []byte("matroska bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	path := writeFakeFFProbe(t, `cat /dev/fd/3 > "$2"; printf '%s ' "$@" >> "$2"`)
	source, err := sourcefile.Open(input, sourcefile.Expected{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err := (Extractor{Path: path}).Run(t.Context(), []string{"-nostdin", out}, source); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "matroska bytes-nostdin " + out + " "; string(got) != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestExtractorRunReportsExitCodeAndStderr(t *testing.T) {
	path := writeFakeFFProbe(t, `printf 'Invalid data found' >&2; exit 3`)
	err := (Extractor{Path: path}).Run(t.Context(), nil, nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 || !strings.Contains(exitErr.Stderr, "Invalid data found") {
		t.Fatalf("Run() error = %v, want ExitError code 3 with stderr", err)
	}
}

func TestExtractorRunCancellationKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	// The child ignores TERM and outlives the leader unless the group dies.
	path := writeFakeFFProbe(t, `sh -c 'trap "" TERM; echo $$ > "`+childPID+`"; while :; do sleep 1; done' & wait`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- (Extractor{Path: path}).Run(ctx, nil, nil) }()
	pid := waitForPID(t, childPID)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	waitForProcessExit(t, pid)
}
