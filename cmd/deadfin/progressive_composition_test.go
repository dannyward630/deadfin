// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// TestApplicationComposesAndOwnsOneProgressiveManager proves the process
// manager is an application resource, not a per-route allocation. A valid
// progressive HTTP request reaches the composed service (and fails only at its
// missing item lookup), then application shutdown makes that exact manager
// unusable.
func TestApplicationComposesAndOwnsOneProgressiveManager(t *testing.T) {
	root := filepath.Join(t.TempDir(), "program-data")
	app, err := newApplication(t.Context(), root)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	defer func() {
		if app != nil {
			if err := app.Close(t.Context()); err != nil {
				t.Errorf("cleanup application: %v", err)
			}
		}
	}()

	if app.progressive == nil {
		t.Fatal("application progressive streamer = nil, want one composed streamer")
	}
	manager := app.transcodes
	if manager == nil {
		t.Fatal("application transcode manager = nil, want one application-owned manager")
	}

	itemID := composedItemEnrichmentUUID(t, 0xd900)
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Videos/"+itemID.Dashed()+"/stream?Container=ts&VideoCodec=copy&AudioCodec=copy&PlaySessionId=composed-session", nil, token)
	if record.Code != http.StatusNotFound {
		t.Fatalf("composed progressive GET status = %d, want %d (streamer registered; item lookup is expected to fail); body=%q", record.Code, http.StatusNotFound, record.Body.String())
	}

	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatalf("new configuration paths: %v", err)
	}
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("close application: %v", err)
	}
	if app.transcodes != nil {
		t.Error("application transcode manager remains owned after successful Close")
	}
	app = nil
	if manager == nil {
		t.Fatal("captured manager unexpectedly nil")
	}
	output := filepath.Join(paths.Transcodes, "must-not-start-after-close.ts")
	_, err = manager.OpenProgressive(t.Context(), ffmpeg.ProgressiveRequest{
		OutputPath:     output,
		Args:           []string{"-f", "mpegts", output},
		PlaySessionID:  "closed-session",
		DeviceID:       "closed-device",
		SourceRevision: "closed-source-revision",
		OpenSource: func() (*sourcefile.Source, error) {
			return nil, errors.New("closed manager must not open a source")
		},
	})
	if !errors.Is(err, ffmpeg.ErrManagerClosed) {
		t.Fatalf("manager OpenProgressive after application Close = %v, want %v", err, ffmpeg.ErrManagerClosed)
	}
}
