// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

func TestBackupRoutesDeclarePinnedOperationsAndElevationPolicy(t *testing.T) {
	rt := router.New()
	handlers.RegisterBackup(rt)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Backup", OperationID: "ListBackups", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Backup/Create", OperationID: "CreateBackup", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodGet, Pattern: "/Backup/Manifest", OperationID: "GetBackup", Policy: string(auth.PolicyRequiresElevation)},
		{Method: http.MethodPost, Pattern: "/Backup/Restore", OperationID: "StartRestoreBackup", Policy: string(auth.PolicyRequiresElevation)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got != want {
				t.Errorf("route %q = %#v, want %#v", want.OperationID, got, want)
			}
		})
	}
}

func TestBackupListReturnsEmptyArray(t *testing.T) {
	rt := router.New()
	handlers.RegisterBackup(rt)
	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Backup", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /Backup status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var values []json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &values); err != nil {
		t.Fatalf("GET /Backup response is not an array: %v; body=%q", err, recorder.Body.String())
	}
	if values == nil || len(values) != 0 {
		t.Errorf("GET /Backup response = %s, want []", recorder.Body.String())
	}
}

func TestBackupCreateReportsUnavailable(t *testing.T) {
	rt := router.New()
	handlers.RegisterBackup(rt)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/Backup/Create", strings.NewReader(`{"Metadata":true}`))
	request.Header.Set("Content-Type", "application/json")
	rt.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /Backup/Create status = %d, want %d; body=%q", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	problem := assertProblemDetailsWireCasing(t, recorder.Body.Bytes(), http.StatusServiceUnavailable)
	if !strings.Contains(strings.ToLower(problem["detail"]), "backup support is not available") {
		t.Errorf("POST /Backup/Create detail = %q, want unavailable detail", problem["detail"])
	}
}

func TestMissingBackupManifestAndRestoreReturnNotFound(t *testing.T) {
	rt := router.New()
	handlers.RegisterBackup(rt)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/Backup/Manifest?path=missing.zip", nil),
		httptest.NewRequest(http.MethodPost, "/Backup/Restore", strings.NewReader(`{"ArchiveFileName":"missing.zip"}`)),
	} {
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		rt.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d; body=%q", request.Method, request.URL.Path, recorder.Code, http.StatusNotFound, recorder.Body.String())
		}
		problem := assertProblemDetailsWireCasing(t, recorder.Body.Bytes(), http.StatusNotFound)
		if problem["detail"] == "" {
			t.Errorf("%s %s response = %q, want populated 404 ProblemDetails", request.Method, request.URL.Path, recorder.Body.String())
		}
	}
}
