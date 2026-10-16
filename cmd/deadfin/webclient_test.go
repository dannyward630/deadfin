// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeWebClient makes a web client directory with an index.html that names
// the client and an optional build.json.
func writeWebClient(t *testing.T, dir, name, buildJSON string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	if buildJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "build.json"), []byte(buildJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const testWebBuildJSON = `{"Component":"deadfin-web","Version":"0.3.0-rc.3","Channel":"rc","Revision":"303db32604f466366403354db35eb3f67d5a6747"}`

func TestResolveWebClient(t *testing.T) {
	t.Run("data root override wins", func(t *testing.T) {
		root, bundled := t.TempDir(), t.TempDir()
		writeWebClient(t, filepath.Join(root, "web"), "jellyfin-web", "")
		writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)

		got := resolveWebClient(root, bundled)
		want := webClient{Path: filepath.Join(root, "web"), Source: webSourceDataRoot}
		if got != want {
			t.Fatalf("resolveWebClient = %+v, want %+v", got, want)
		}
	})
	t.Run("bundled without override", func(t *testing.T) {
		root, bundled := t.TempDir(), t.TempDir()
		writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)

		got := resolveWebClient(root, bundled)
		want := webClient{Path: bundled, Source: webSourceBundled, Component: "deadfin-web", Version: "0.3.0-rc.3", Revision: "303db32604f466366403354db35eb3f67d5a6747"}
		if got != want {
			t.Fatalf("resolveWebClient = %+v, want %+v", got, want)
		}
	})
	t.Run("neither", func(t *testing.T) {
		root := t.TempDir()
		for _, bundled := range []string{"", filepath.Join(root, "missing")} {
			got := resolveWebClient(root, bundled)
			want := webClient{Path: filepath.Join(root, "web"), Source: webSourceNone}
			if got != want {
				t.Fatalf("resolveWebClient(%q) = %+v, want %+v", bundled, got, want)
			}
		}
	})
	t.Run("empty override directory", func(t *testing.T) {
		root, bundled := t.TempDir(), t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)

		got := resolveWebClient(root, bundled)
		if got.Path != bundled || got.Source != webSourceBundled || got.IgnoredOverride != filepath.Join(root, "web") {
			t.Fatalf("resolveWebClient = %+v, want the bundled directory with the ignored override", got)
		}
	})
	t.Run("override file is not a directory", func(t *testing.T) {
		root, bundled := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "web"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		writeWebClient(t, bundled, "deadfin-web", "")

		if got := resolveWebClient(root, bundled); got.Path != bundled || got.Source != webSourceBundled {
			t.Fatalf("resolveWebClient = %+v, want the bundled directory", got)
		}
	})
	t.Run("malformed build.json", func(t *testing.T) {
		root := t.TempDir()
		writeWebClient(t, filepath.Join(root, "web"), "deadfin-web", "{")

		got := resolveWebClient(root, "")
		if got.Source != webSourceDataRoot || got.Component != "" || got.Version != "" || got.Revision != "" {
			t.Fatalf("resolveWebClient = %+v, want data root with no build identity", got)
		}
	})
}

func TestLogWebClientWarnsAboutIgnoredOverride(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logWebClient(t.Context(), logger, webClient{Path: "/usr/share/deadfin/web", Source: webSourceBundled, IgnoredOverride: "/var/lib/deadfin/web"})

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("log lines = %d (%s), want 2", len(lines), buf.Bytes())
	}
	var record map[string]any
	if err := json.Unmarshal(lines[0], &record); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if record["level"] != "WARN" || record["path"] != "/var/lib/deadfin/web" {
		t.Fatalf("first record = %v, want WARN naming /var/lib/deadfin/web", record)
	}
}

func TestLogWebClientNamesSourcePathAndVersion(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logWebClient(t.Context(), logger, webClient{Path: "/usr/share/deadfin/web", Source: webSourceBundled, Component: "deadfin-web", Version: "0.3.0-rc.3", Revision: "303db326"})

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log: %v (%s)", err, buf.Bytes())
	}
	for key, want := range map[string]string{"msg": "web client", "source": "bundled", "path": "/usr/share/deadfin/web", "component": "deadfin-web", "version": "0.3.0-rc.3", "revision": "303db326"} {
		if record[key] != want {
			t.Fatalf("log %s = %v, want %q (record %v)", key, record[key], want, record)
		}
	}
}

// TestApplicationServesWebClientFromDataRootThenBundled composes the
// application three times: with <data root>/web, with only the bundled
// directory, and with neither.
func TestApplicationServesWebClientFromDataRootThenBundled(t *testing.T) {
	getIndex := func(t *testing.T, app *application) (int, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/web/", nil)
		request.RemoteAddr = "127.0.0.1:40000"
		app.Router().ServeHTTP(recorder, request)
		body, _ := io.ReadAll(recorder.Result().Body)
		return recorder.Code, string(body)
	}

	t.Run("override present", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "program-data")
		bundled := t.TempDir()
		writeWebClient(t, filepath.Join(root, "web"), "jellyfin-web", "")
		writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)
		app := newCompositionApplication(t, root, withBundledWebDir(bundled))

		if code, body := getIndex(t, app); code != http.StatusOK || body != "jellyfin-web" {
			t.Fatalf("GET /web/ = %d %q, want 200 jellyfin-web", code, body)
		}
	})
	t.Run("override absent", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "program-data")
		bundled := t.TempDir()
		writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)
		app := newCompositionApplication(t, root, withBundledWebDir(bundled))

		if code, body := getIndex(t, app); code != http.StatusOK || body != "deadfin-web" {
			t.Fatalf("GET /web/ = %d %q, want 200 deadfin-web", code, body)
		}
	})
	t.Run("neither", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "program-data")
		app := newCompositionApplication(t, root, withBundledWebDir(""))

		if code, _ := getIndex(t, app); code != http.StatusNotFound {
			t.Fatalf("GET /web/ = %d, want 404", code)
		}
	})
}

// TestApplicationWebClientHardeningHeaders checks the /web/ headers for the
// bundled directory and the <data root>/web override (issue #602, D-68).
// deadfin-web gets the Content-Security-Policy; jellyfin-web, which has no
// build.json, does not. Both get Referrer-Policy and nosniff.
func TestApplicationWebClientHardeningHeaders(t *testing.T) {
	const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	for _, tc := range []struct {
		name     string
		override string // build.json for <data root>/web; "-" means no override
		wantCSP  string
	}{
		{name: "bundled deadfin-web", override: "-", wantCSP: wantCSP},
		{name: "override deadfin-web", override: testWebBuildJSON, wantCSP: wantCSP},
		{name: "override jellyfin-web", override: "", wantCSP: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "program-data")
			bundled := t.TempDir()
			writeWebClient(t, bundled, "deadfin-web", testWebBuildJSON)
			if tc.override != "-" {
				writeWebClient(t, filepath.Join(root, "web"), "override", tc.override)
			}
			app := newCompositionApplication(t, root, withBundledWebDir(bundled))
			for _, path := range []string{"/web/", "/web/index.html"} {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr = "127.0.0.1:40000"
				app.Router().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("GET %s = %d, want 200", path, recorder.Code)
				}
				header := recorder.Header()
				if got := header.Get("Content-Security-Policy"); got != tc.wantCSP {
					t.Errorf("GET %s Content-Security-Policy = %q, want %q", path, got, tc.wantCSP)
				}
				if got := header.Get("Referrer-Policy"); got != "no-referrer" {
					t.Errorf("GET %s Referrer-Policy = %q, want no-referrer", path, got)
				}
				if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", path, got)
				}
			}
		})
	}
}
