// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

type fakeDrives struct {
	paths []string
	err   error
}

func (d fakeDrives) Drives(context.Context) ([]string, error) { return d.paths, d.err }

type environmentStartup struct{ completed bool }

func (s environmentStartup) StartupWizardCompleted() bool { return s.completed }

type environmentUsers struct{ policy string }

func (u environmentUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id, Policy: []byte(u.policy)}, nil
}

// newEnvironmentRouter builds the environment routes behind the real
// authorizer and policy evaluator.
func newEnvironmentRouter(t *testing.T, completed bool, identity auth.Identity, policy string, drives handlers.DriveLister) *router.Router {
	t.Helper()
	evaluator := auth.NewEvaluator(environmentStartup{completed: completed}, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(configurationResolver{identity: identity}, evaluator, environmentUsers{policy: policy})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterEnvironment(rt, drives)
	return rt
}

func adminEnvironmentRouter(t *testing.T, drives handlers.DriveLister) *router.Router {
	t.Helper()
	return newEnvironmentRouter(t, true, authenticatedConfigUser(), `{"IsAdministrator":true}`, drives)
}

func TestEnvironmentRoutesCarryFirstTimeSetupOrElevated(t *testing.T) {
	rt := router.New()
	handlers.RegisterEnvironment(rt, fakeDrives{})
	want := map[string]string{
		"GET /Environment/DirectoryContents":       "GetDirectoryContents",
		"POST /Environment/ValidatePath":           "ValidatePath",
		"GET /Environment/Drives":                  "GetDrives",
		"GET /Environment/ParentPath":              "GetParentPath",
		"GET /Environment/DefaultDirectoryBrowser": "GetDefaultDirectoryBrowser",
	}
	routes := rt.Routes()
	if len(routes) != len(want) {
		t.Fatalf("registered %d routes, want %d", len(routes), len(want))
	}
	for _, route := range routes {
		key := route.Method + " " + route.Pattern
		if want[key] != route.OperationID {
			t.Errorf("route %s operation = %q, want %q", key, route.OperationID, want[key])
		}
		if route.Policy != string(auth.PolicyFirstTimeSetupOrElevated) || route.Anonymous {
			t.Errorf("route %s policy = %q anonymous=%v, want FirstTimeSetupOrElevated", key, route.Policy, route.Anonymous)
		}
	}
}

func TestEnvironmentPolicy(t *testing.T) {
	const path = "/Environment/DefaultDirectoryBrowser"
	t.Run("setup pending allows anonymous wizard", func(t *testing.T) {
		rt := newEnvironmentRouter(t, false, auth.Identity{}, `{}`, fakeDrives{})
		assertStatus(t, request(rt, http.MethodGet, path, "", "", ""), http.StatusOK)
	})
	t.Run("setup complete rejects anonymous", func(t *testing.T) {
		rt := newEnvironmentRouter(t, true, auth.Identity{}, `{}`, fakeDrives{})
		assertStatus(t, request(rt, http.MethodGet, path, "", "", ""), http.StatusUnauthorized)
	})
	t.Run("setup complete forbids non-admin", func(t *testing.T) {
		rt := newEnvironmentRouter(t, true, authenticatedConfigUser(), `{"IsAdministrator":false}`, fakeDrives{})
		assertStatus(t, request(rt, http.MethodGet, path, "", "", ""), http.StatusForbidden)
		assertStatus(t, request(rt, http.MethodPost, "/Environment/ValidatePath", `{"Path":"/"}`, "application/json", ""), http.StatusForbidden)
	})
	t.Run("setup complete allows admin", func(t *testing.T) {
		rt := adminEnvironmentRouter(t, fakeDrives{})
		assertStatus(t, request(rt, http.MethodGet, path, "", "", ""), http.StatusOK)
	})
}

func TestEnvironmentDefaultDirectoryBrowserOmitsNullPath(t *testing.T) {
	rt := adminEnvironmentRouter(t, fakeDrives{})
	rec := request(rt, http.MethodGet, "/Environment/DefaultDirectoryBrowser", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); got != `{}` {
		t.Fatalf("body = %q, want {}", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func makeEnvironmentTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"b-dir", "A-dir", ".hidden-dir"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"c-file.mkv", "B-file.txt"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "b-dir"), filepath.Join(root, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatal(err)
	}
	return root
}

type entryInfo struct {
	Name string
	Path string
	Type string
}

func directoryContents(t *testing.T, rt http.Handler, query url.Values) []entryInfo {
	t.Helper()
	rec := request(rt, http.MethodGet, "/Environment/DirectoryContents?"+query.Encode(), "", "", "")
	assertStatus(t, rec, http.StatusOK)
	var entries []entryInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if entries == nil {
		t.Fatalf("body %q is not a JSON array", rec.Body.String())
	}
	return entries
}

func entryNames(entries []entryInfo) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name+":"+entry.Type)
	}
	return names
}

func TestEnvironmentDirectoryContentsFiltersAndSorts(t *testing.T) {
	root := makeEnvironmentTree(t)
	rt := adminEnvironmentRouter(t, fakeDrives{})

	cases := []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"defaults list nothing", url.Values{"path": {root}}, []string{}},
		{"directories", url.Values{"path": {root}, "includeDirectories": {"true"}},
			[]string{".hidden-dir:Directory", "A-dir:Directory", "b-dir:Directory", "link-dir:Directory"}},
		{"files", url.Values{"path": {root}, "includeFiles": {"true"}},
			[]string{"B-file.txt:File", "broken-link:File", "c-file.mkv:File"}},
		{"both", url.Values{"path": {root}, "includeFiles": {"true"}, "includeDirectories": {"true"}},
			[]string{".hidden-dir:Directory", "A-dir:Directory", "B-file.txt:File", "b-dir:Directory", "broken-link:File", "c-file.mkv:File", "link-dir:Directory"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := directoryContents(t, rt, tc.query)
			got := entryNames(entries)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("entries = %q, want %q", got, tc.want)
			}
			for _, entry := range entries {
				if entry.Path != filepath.Join(root, entry.Name) {
					t.Errorf("entry %q path = %q, want %q", entry.Name, entry.Path, filepath.Join(root, entry.Name))
				}
			}
		})
	}
}

func TestEnvironmentDirectoryContentsNormalizesPath(t *testing.T) {
	root := makeEnvironmentTree(t)
	rt := adminEnvironmentRouter(t, fakeDrives{})
	entries := directoryContents(t, rt, url.Values{"path": {root + "/A-dir/../"}, "includeDirectories": {"true"}})
	if len(entries) == 0 || entries[0].Path != filepath.Join(root, ".hidden-dir") {
		t.Fatalf("entries = %+v, want full paths under %q", entries, root)
	}
}

func TestEnvironmentDirectoryContentsErrors(t *testing.T) {
	root := makeEnvironmentTree(t)
	rt := adminEnvironmentRouter(t, fakeDrives{})

	t.Run("missing path is a validation error", func(t *testing.T) {
		rec := request(rt, http.MethodGet, "/Environment/DirectoryContents?includeDirectories=true", "", "", "")
		assertStatus(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), `"path":["The path field is required."]`) {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})
	t.Run("missing directory is 404", func(t *testing.T) {
		rec := request(rt, http.MethodGet, "/Environment/DirectoryContents?path="+url.QueryEscape(filepath.Join(root, "nope")), "", "", "")
		assertStatus(t, rec, http.StatusNotFound)
		if rec.Body.String() != "Error processing request." || rec.Header().Get("Content-Type") != "text/plain" {
			t.Fatalf("response = %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
		}
	})
	t.Run("file path is 404", func(t *testing.T) {
		rec := request(rt, http.MethodGet, "/Environment/DirectoryContents?path="+url.QueryEscape(filepath.Join(root, "c-file.mkv")), "", "", "")
		assertStatus(t, rec, http.StatusNotFound)
	})
	t.Run("bare UNC server is empty", func(t *testing.T) {
		entries := directoryContents(t, rt, url.Values{"path": {`\\server`}, "includeDirectories": {"true"}})
		if len(entries) != 0 {
			t.Fatalf("entries = %+v, want none", entries)
		}
	})
	t.Run("inaccessible directory is empty", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits do not restrict this user")
		}
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		entries := directoryContents(t, rt, url.Values{"path": {locked}, "includeDirectories": {"true"}})
		if len(entries) != 0 {
			t.Fatalf("entries = %+v, want none", entries)
		}
	})
}

func TestEnvironmentValidatePath(t *testing.T) {
	root := makeEnvironmentTree(t)
	file := filepath.Join(root, "c-file.mkv")
	missing := filepath.Join(root, "missing")
	rt := adminEnvironmentRouter(t, fakeDrives{})

	body := func(v map[string]any) string {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"directory", map[string]any{"Path": root}, http.StatusNoContent},
		{"file", map[string]any{"Path": file}, http.StatusNoContent},
		{"missing", map[string]any{"Path": missing}, http.StatusNotFound},
		{"null path", map[string]any{}, http.StatusNotFound},
		{"IsFile true on file", map[string]any{"Path": file, "IsFile": true}, http.StatusNoContent},
		{"IsFile true on directory", map[string]any{"Path": root, "IsFile": true}, http.StatusNotFound},
		{"IsFile false on directory", map[string]any{"Path": root, "IsFile": false}, http.StatusNoContent},
		{"IsFile false on file", map[string]any{"Path": file, "IsFile": false}, http.StatusNotFound},
		{"IsFile skips writable probe", map[string]any{"Path": root, "IsFile": false, "ValidateWritable": true}, http.StatusNoContent},
		{"writable directory", map[string]any{"Path": root, "ValidateWritable": true}, http.StatusNoContent},
		{"writable on a file", map[string]any{"Path": file, "ValidateWritable": true}, http.StatusNotFound},
		{"lowercase members", map[string]any{"path": root, "validateWritable": true}, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodPost, "/Environment/ValidatePath", body(tc.body), "application/json", "")
			assertStatus(t, rec, tc.want)
			if tc.want == http.StatusNoContent && rec.Body.Len() != 0 {
				t.Fatalf("204 body = %q", rec.Body.String())
			}
		})
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("writable probe left files behind: %d entries", len(entries))
	}

	t.Run("not found body is ProblemDetails", func(t *testing.T) {
		rec := request(rt, http.MethodPost, "/Environment/ValidatePath", body(map[string]any{"Path": missing}), "application/json", "")
		assertStatus(t, rec, http.StatusNotFound)
		if got := rec.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		var problem map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if problem["status"] != float64(404) || problem["title"] != "Not Found" || problem["type"] != "https://tools.ietf.org/html/rfc9110#section-15.5.5" {
			t.Fatalf("problem = %v", problem)
		}
	})
	t.Run("read-only directory is 500", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits do not restrict this user")
		}
		readOnly := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(readOnly, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
		rec := request(rt, http.MethodPost, "/Environment/ValidatePath", body(map[string]any{"Path": readOnly, "ValidateWritable": true}), "application/json", "")
		assertStatus(t, rec, http.StatusInternalServerError)
	})
	t.Run("empty body is 400", func(t *testing.T) {
		assertStatus(t, request(rt, http.MethodPost, "/Environment/ValidatePath", "", "application/json", ""), http.StatusBadRequest)
	})
}

func TestEnvironmentDrives(t *testing.T) {
	rt := adminEnvironmentRouter(t, fakeDrives{paths: []string{"/", "/media"}})
	rec := request(rt, http.MethodGet, "/Environment/Drives", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	want := `[{"Name":"/","Path":"/","Type":"Directory"},{"Name":"/media","Path":"/media","Type":"Directory"}]`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	rec = request(adminEnvironmentRouter(t, fakeDrives{}), http.MethodGet, "/Environment/Drives", "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); got != `[]` {
		t.Fatalf("empty drives body = %s, want []", got)
	}

	rec = request(adminEnvironmentRouter(t, fakeDrives{err: errors.New("boom")}), http.MethodGet, "/Environment/Drives", "", "", "")
	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestEnvironmentDrivesCamelCase(t *testing.T) {
	rt := adminEnvironmentRouter(t, fakeDrives{paths: []string{"/media"}})
	rec := request(rt, http.MethodGet, "/Environment/Drives", "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); got != `[{"name":"/media","path":"/media","type":"Directory"}]` {
		t.Fatalf("body = %s", got)
	}
}

func TestEnvironmentParentPath(t *testing.T) {
	rt := adminEnvironmentRouter(t, fakeDrives{})
	cases := map[string]string{
		"/media/movies":  `"/media"`,
		"/media/movies/": `"/media/movies"`,
		"/media":         `"/"`,
		"/":              `null`,
		"//media//tv":    `"/media"`,
		"/a//b/c":        `"/a/b"`,
		"media":          `""`,
		"media/tv":       `"media"`,
		`\\server\share`: `"\\\\server"`,
		`\\server`:       `null`,
	}
	for path, want := range cases {
		t.Run(path, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/Environment/ParentPath?path="+url.QueryEscape(path), "", "", "*/*")
			assertStatus(t, rec, http.StatusOK)
			if got := rec.Body.String(); got != want {
				t.Fatalf("body = %s, want %s", got, want)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
	rec := request(rt, http.MethodGet, "/Environment/ParentPath", "", "", "")
	assertStatus(t, rec, http.StatusBadRequest)
}
