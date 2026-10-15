// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// buffer. Tests parse the buffer to count and inspect the structured WARN the
// manual RefreshExistingMetadata plan emits for a disabled library.
type captureHandler struct {
	buf *bytes.Buffer
}

func newCaptureHandler(buf *bytes.Buffer) *captureHandler {
	return &captureHandler{buf: buf}
}

func (h *captureHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		return true
	})
	_, err := h.buf.WriteString(r.Level.String() + " " + r.Message + b.String() + "\n")
	return err
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

// newCapturingBackfillScanner closes the harness scanner and builds a new one
// for the remote-backfill mode with the real provider registry and a capturing
// logger. The harness library keeps EnableInternetProviders=false.
func newCapturingBackfillScanner(t *testing.T, harness *scannerHarness, refresher library.Refresher, logger *slog.Logger) {
	t.Helper()
	now := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)
	chain := library.NewChain(backfillMovieResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = chain
	harness.scanner = newScanner(t, harness.topology, harness.items, chain, library.ScannerOptions{
		Concurrency: 1,
		Now:         func() time.Time { return now },
		Refresher:   refresher,
		Logger:      logger,
	})
}

// TestScannerRemoteMetadataIgnoresObsoleteInternetProvidersFlag proves that
// LibraryOptions.EnableInternetProviders does not gate remote metadata. A
// library created or edited by the web client always holds false, because the
// client never sends the flag, and upstream marks it obsolete and never reads
// it. A plain library scan of such a library runs the remote providers, and
// the registry records each run at debug level.
func TestScannerRemoteMetadataIgnoresObsoleteInternetProvidersFlag(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	matrix := scannerPath(t, harness.source, "the-matrix.mkv")
	writeScannerFile(t, matrix, "the matrix")

	var buf bytes.Buffer
	logger := slog.New(newCaptureHandler(&buf))
	remote := &fakeRemoteProvider{}
	refresher := providers.NewRegistry(remote).WithLogger(logger)
	newCapturingBackfillScanner(t, harness, refresher, logger)

	folders, err := harness.topology.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 || folders[0].Options.EnableInternetProviders {
		t.Fatalf("virtual folders = %#v, %v; want one library with EnableInternetProviders=false", folders, err)
	}

	result, err := harness.scanner.ScanWithOptions(t.Context(), library.DefaultRefreshOptions())
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("library scan: %v %v", err, result.Errors)
	}
	if remote.calls != 1 {
		t.Fatalf("library scan remote calls = %d, want 1 for the movie", remote.calls)
	}
	movie := scannerItemAtPath(t, harness.items, matrix)
	if value, ok := movie.ProviderIDs.Get("Tmdb"); !ok || value != "603" {
		t.Fatalf("scanned movie Tmdb id = %q, %t; want the remote provider result", value, ok)
	}
	if movie.Overview == nil || *movie.Overview != "A provider-sourced overview" {
		t.Fatalf("scanned movie overview = %v, want the remote provider result", movie.Overview)
	}
	if !strings.Contains(buf.String(), "DEBUG remote metadata provider ran provider=fake-remote") || !strings.Contains(buf.String(), "handled=true") {
		t.Fatalf("log does not record the remote provider run:\n%s", buf.String())
	}
}
