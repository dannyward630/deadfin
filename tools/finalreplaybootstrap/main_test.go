// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/library"
)

func TestBootstrapCompletesStartupWizardDurablyAndProjectsItFromSystemHandlers(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	mediaRoot := bootstrapMediaRoot(t)
	options := bootstrapOptions{
		mediaRoot: mediaRoot,
		mediaProbe: func(context.Context) (library.MediaProbe, error) {
			return bootstrapNoopMediaProbe{}, nil
		},
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := bootstrapWithOptions(t.Context(), state, options); err != nil {
			t.Fatalf("bootstrap attempt %d: %v", attempt, err)
		}
	}

	paths, err := config.NewPaths(state)
	if err != nil {
		t.Fatalf("derive reopened configuration paths: %v", err)
	}
	changes := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() {
		if err := changes.Close(context.Background()); err != nil {
			t.Errorf("close reopened configuration changes: %v", err)
		}
	})
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("reopen configuration manager: %v", err)
	}
	if !manager.System().IsStartupWizardCompleted {
		t.Fatal("reopened configuration manager reports an incomplete startup wizard")
	}

	rt := router.New()
	handlers.RegisterSystem(rt, config.DefaultServerIdentity(), manager)
	for _, path := range []string{"/System/Info/Public", "/System/Info"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d; body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode GET %s response: %v; body=%s", path, err, rec.Body.String())
			}
			value, found := body["StartupWizardCompleted"]
			if !found {
				t.Fatalf("GET %s response omits StartupWizardCompleted: %s", path, rec.Body.String())
			}
			var completed bool
			if err := json.Unmarshal(value, &completed); err != nil {
				t.Fatalf("decode GET %s StartupWizardCompleted: %v", path, err)
			}
			if !completed {
				t.Fatalf("GET %s StartupWizardCompleted = false, want true", path)
			}
		})
	}
}

func bootstrapMediaRoot(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "media")
	for _, name := range []string{"movies", "tv", "music", "photos"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("create bootstrap media directory %q: %v", name, err)
		}
	}
	return root
}

type bootstrapNoopMediaProbe struct{}

func (bootstrapNoopMediaProbe) Probe(context.Context, library.MediaProbeInput) (library.MediaProbeResult, error) {
	return library.MediaProbeResult{}, nil
}
