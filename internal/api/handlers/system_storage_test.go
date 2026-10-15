// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
)

// stubTopology is a test-only storageLibraryReader.
type stubTopology struct {
	folders []library.VirtualFolder
	err     error
}

func (s *stubTopology) VirtualFolders(_ context.Context) ([]library.VirtualFolder, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.folders, nil
}

func TestSystemStorageRouteRegistration(t *testing.T) {
	rt := router.New()
	handlers.RegisterSystemStorage(rt, handlers.SystemInfoConfig{}, &stubTopology{})

	got, found := routeByOperation(rt.Routes(), "GetSystemStorage")
	if !found {
		t.Fatal("GetSystemStorage is not registered")
	}
	wantMethod := http.MethodGet
	wantPattern := "/System/Info/Storage"
	wantPolicy := string(auth.PolicyRequiresElevation)
	if got.Method != wantMethod || got.Pattern != wantPattern || got.Policy != wantPolicy {
		t.Fatalf("GetSystemStorage route = {Method: %q, Pattern: %q, Policy: %q}, want {Method: %q, Pattern: %q, Policy: %q}",
			got.Method, got.Pattern, got.Policy, wantMethod, wantPattern, wantPolicy)
	}
	if got.Anonymous {
		t.Fatal("GetSystemStorage route must not be anonymous")
	}
}

func TestSystemStorageNilReaderReturns503(t *testing.T) {
	rt := router.New()
	handlers.RegisterSystemStorage(rt, handlers.SystemInfoConfig{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Storage", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil reader status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestSystemStorageVirtualFoldersErrorReturns500(t *testing.T) {
	rt := router.New()
	handlers.RegisterSystemStorage(rt, handlers.SystemInfoConfig{}, &stubTopology{err: context.DeadlineExceeded})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Storage", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("VirtualFolders error status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSystemStorageFolderPathsAndSpace(t *testing.T) {
	root := t.TempDir()
	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.Cache, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.Cache, paths.Log, paths.Metadata, paths.Transcodes} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	webPath := filepath.Join(root, "web")
	if err := os.MkdirAll(webPath, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := handlers.SystemInfoConfig{Paths: paths, WebPath: webPath}
	rt := router.New()
	handlers.RegisterSystemStorage(rt, cfg, &stubTopology{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Storage", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}

	expectedPaths := map[string]string{
		"ProgramDataFolder":      root,
		"WebFolder":              webPath,
		"ImageCacheFolder":       filepath.Join(paths.Cache, "images"),
		"CacheFolder":            paths.Cache,
		"LogFolder":              paths.Log,
		"InternalMetadataFolder": paths.Metadata,
		"TranscodingTempFolder":  paths.Transcodes,
	}
	for field, wantPath := range expectedPaths {
		f, ok := result[field].(map[string]any)
		if !ok {
			t.Fatalf("%s is not a JSON object: %v", field, result[field])
		}
		if f["Path"].(string) != wantPath {
			t.Errorf("%s.Path = %q, want %q", field, f["Path"], wantPath)
		}
		free, ok := f["FreeSpace"].(float64)
		if !ok || free <= 0 {
			t.Errorf("%s.FreeSpace = %v, want > 0 for existing directory", field, f["FreeSpace"])
		}
		used, ok := f["UsedSpace"].(float64)
		if !ok || used <= 0 {
			t.Errorf("%s.UsedSpace = %v, want > 0 for existing directory", field, f["UsedSpace"])
		}
		if _, has := f["StorageType"]; has {
			t.Errorf("%s.StorageType present, want absent", field)
		}
		if _, has := f["DeviceId"]; has {
			t.Errorf("%s.DeviceId present, want absent", field)
		}
	}

	libs, ok := result["Libraries"].([]any)
	if !ok {
		t.Fatal("Libraries must be an allocated empty array, not null")
	}
	if len(libs) != 0 {
		t.Errorf("Libraries count = %d, want 0", len(libs))
	}
}

func TestSystemStorageMissingDirectoryReportsNegOne(t *testing.T) {
	root := t.TempDir()
	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := handlers.SystemInfoConfig{Paths: paths}
	rt := router.New()
	handlers.RegisterSystemStorage(rt, cfg, &stubTopology{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Storage", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		WebFolder struct {
			Path        string  `json:"Path"`
			FreeSpace   int64   `json:"FreeSpace"`
			UsedSpace   int64   `json:"UsedSpace"`
			StorageType *string `json:"StorageType"`
			DeviceId    *string `json:"DeviceId"`
		} `json:"WebFolder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
	if result.WebFolder.FreeSpace != -1 {
		t.Errorf("missing directory FreeSpace = %d, want -1", result.WebFolder.FreeSpace)
	}
	if result.WebFolder.UsedSpace != -1 {
		t.Errorf("missing directory UsedSpace = %d, want -1", result.WebFolder.UsedSpace)
	}
	if result.WebFolder.StorageType != nil {
		t.Errorf("missing directory StorageType = %v, want nil", result.WebFolder.StorageType)
	}
	if result.WebFolder.DeviceId != nil {
		t.Errorf("missing directory DeviceId = %v, want nil", result.WebFolder.DeviceId)
	}
}

func TestSystemStorageLibraries(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "media")
	if err := os.MkdirAll(mediaPath, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := config.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	libID := dto.UUID{1}
	topology := &stubTopology{
		folders: []library.VirtualFolder{
			{
				CollectionFolderID: libID,
				Name:               "Movies",
				MediaPaths:         []string{mediaPath},
			},
		},
	}
	cfg := handlers.SystemInfoConfig{Paths: paths}
	rt := router.New()
	handlers.RegisterSystemStorage(rt, cfg, topology)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Storage", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Libraries []struct {
			Id      string `json:"Id"`
			Name    string `json:"Name"`
			Folders []struct {
				Path        string  `json:"Path"`
				FreeSpace   int64   `json:"FreeSpace"`
				StorageType *string `json:"StorageType"`
			} `json:"Folders"`
		} `json:"Libraries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
	if len(result.Libraries) != 1 {
		t.Fatalf("Libraries count = %d, want 1", len(result.Libraries))
	}
	lib := result.Libraries[0]
	if lib.Name != "Movies" {
		t.Errorf("Library Name = %q, want %q", lib.Name, "Movies")
	}
	if len(lib.Folders) != 1 {
		t.Fatalf("Library Folders count = %d, want 1", len(lib.Folders))
	}
	if lib.Folders[0].Path != mediaPath {
		t.Errorf("Library Folder Path = %q, want %q", lib.Folders[0].Path, mediaPath)
	}
	if lib.Folders[0].FreeSpace <= 0 {
		t.Errorf("Library Folder FreeSpace = %d, want > 0", lib.Folders[0].FreeSpace)
	}
}
