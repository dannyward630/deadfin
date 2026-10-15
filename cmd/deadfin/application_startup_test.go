// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
)

// TestApplicationReturnsScheduledTaskPersistenceRootError makes sure that
// newApplication returns a nil application when the scheduled-task trigger
// directory cannot be created. A regular file blocks MkdirAll at the trigger
// root. The error must carry the "open task scheduler" context and unwrap to
// the underlying *os.PathError.
func TestApplicationReturnsScheduledTaskPersistenceRootError(t *testing.T) {
	root := t.TempDir()
	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	triggerRoot := filepath.Join(paths.Config, "ScheduledTasks")
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("mkdir config parent: %v", err)
	}
	if err := os.WriteFile(triggerRoot, nil, 0o600); err != nil {
		t.Fatalf("write trigger-root blocker file: %v", err)
	}

	app, err := newApplication(context.Background(), root)
	if app != nil {
		t.Fatalf("application = %v, want nil", app)
	}
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "open task scheduler: tasks: create trigger dir") {
		t.Fatalf("err = %v, want context %q", err, "open task scheduler: tasks: create trigger dir")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("err = %v, want an error that unwraps to *os.PathError", err)
	}
}
