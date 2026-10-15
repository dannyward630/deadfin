// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/user"
)

func TestRouteLedgerRegistersLibraryRefreshWithoutStartingServices(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)

	rt := routeLedger()
	assertLibraryRefreshCompositionRoute(t, rt.Routes())

	entries, err := os.ReadDir(workingDirectory)
	if err != nil {
		t.Fatalf("read route-ledger working directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("route ledger created persistent entries, want none")
	}

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Library/Refresh", nil))
	if got, want := recorder.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("nil route-ledger scanner response = %d, want %d", got, want)
	}
}

func TestRegisterRoutesInvokesInjectedLibraryRefreshTrigger(t *testing.T) {
	trigger := &compositionRefreshTrigger{}
	rt := router.New()
	registerRoutes(rt, routeServices{
		identity: config.DefaultServerIdentity(),
		startup:  incompleteStartup{},
		users:    &user.Service{},
		clock:    clock.NewReal(),
		refresh:  trigger,
	})

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/Library/Refresh", nil))
	if got, want := recorder.Code, http.StatusNoContent; got != want {
		t.Fatalf("composed Library/Refresh status = %d, want %d", got, want)
	}
	if trigger.calls != 1 {
		t.Fatalf("composed Library/Refresh trigger calls = %d, want 1", trigger.calls)
	}
}

type compositionRefreshTrigger struct {
	calls        int
	refreshCalls int
}

func (t *compositionRefreshTrigger) Trigger() error {
	t.calls++
	return nil
}

func (t *compositionRefreshTrigger) TriggerRefresh(library.RefreshOptions) error {
	t.refreshCalls++
	return nil
}

func (t *compositionRefreshTrigger) RefreshStates() map[dto.UUID]library.FolderRefreshState {
	return map[dto.UUID]library.FolderRefreshState{}
}

func assertLibraryRefreshCompositionRoute(t *testing.T, routes []router.Route) {
	t.Helper()
	for _, route := range routes {
		if route.Method == http.MethodPost && route.Pattern == "/Library/Refresh" {
			if got, want := route.OperationID, "RefreshLibrary"; got != want {
				t.Fatalf("Library/Refresh operation ID = %q, want %q", got, want)
			}
			if got, want := route.Policy, string(auth.PolicyRequiresElevation); got != want {
				t.Fatalf("Library/Refresh policy = %q, want %q", got, want)
			}
			if route.Anonymous {
				t.Fatal("Library/Refresh must not be anonymous")
			}
			return
		}
	}
	t.Fatal("route ledger did not register POST /Library/Refresh")
}
