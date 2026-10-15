// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testSHA  = "0123456789abcdef0123456789abcdef01234567"
	otherSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakeForge is a minimal Gitea tag/release API for one repository.
type fakeForge struct {
	mu       sync.Mutex
	tags     map[string]string // tag -> commit sha
	releases map[string]int64  // tag -> id
	failRel  bool              // POST /releases returns 500 once
	log      []string
	auth     []string
}

func newFakeForge() *fakeForge {
	return &fakeForge{tags: map[string]string{}, releases: map[string]int64{}}
}

func (f *fakeForge) handler(t *testing.T) http.Handler {
	const prefix = "/api/v1/repos/deadfin/deadfin"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, prefix)
		f.log = append(f.log, r.Method+" "+path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/tags/"):
			sha, ok := f.tags[strings.TrimPrefix(path, "/tags/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"not found"}`)
				return
			}
			fmt.Fprintf(w, `{"name":"x","commit":{"sha":%q}}`, sha)
		case r.Method == http.MethodPost && path == "/tags":
			var body struct {
				TagName string `json:"tag_name"`
				Target  string `json:"target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode tag body: %v", err)
			}
			if _, exists := f.tags[body.TagName]; exists {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.tags[body.TagName] = body.Target
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"name":%q,"commit":{"sha":%q}}`, body.TagName, body.Target)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/releases/tags/"):
			id, ok := f.releases[strings.TrimPrefix(path, "/releases/tags/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"not found"}`)
				return
			}
			fmt.Fprintf(w, `{"id":%d}`, id)
		case r.Method == http.MethodPost && path == "/releases":
			if f.failRel {
				f.failRel = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var body struct {
				TagName    string `json:"tag_name"`
				Target     string `json:"target_commitish"`
				Draft      bool   `json:"draft"`
				Prerelease bool   `json:"prerelease"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode release body: %v", err)
			}
			if body.Draft || body.Prerelease {
				t.Errorf("release must be non-draft, non-prerelease: %+v", body)
			}
			if body.Target != f.tags[body.TagName] {
				t.Errorf("release target %s != tag target %s", body.Target, f.tags[body.TagName])
			}
			id := int64(len(f.releases) + 1)
			f.releases[body.TagName] = id
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%d,"tag_name":%q}`, id, body.TagName)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func writeVersion(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "VERSION")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runHelper(t *testing.T, srv *httptest.Server, versionFile, token string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run([]string{
		"-forge", srv.URL,
		"-repo", "deadfin/deadfin",
		"-sha", testSHA,
		"-version-file", versionFile,
	}, token, &out)
	return out.String(), err
}

func TestFreshCreation(t *testing.T) {
	f := newFakeForge()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	out, err := runHelper(t, srv, writeVersion(t, "0.1.2\n"), "secret")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.tags["v0.1.2"] != testSHA {
		t.Fatalf("tag v0.1.2 -> %q, want %s", f.tags["v0.1.2"], testSHA)
	}
	if _, ok := f.releases["v0.1.2"]; !ok {
		t.Fatal("release v0.1.2 not created")
	}
	want := []string{"GET /tags/v0.1.2", "POST /tags", "GET /releases/tags/v0.1.2", "POST /releases"}
	if strings.Join(f.log, ",") != strings.Join(want, ",") {
		t.Fatalf("requests %v, want %v", f.log, want)
	}
	for _, a := range f.auth {
		if a != "token secret" {
			t.Fatalf("authorization header %q", a)
		}
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("output leaks token: %q", out)
	}
	if !strings.Contains(out, "created tag v0.1.2") || !strings.Contains(out, "created release v0.1.2") {
		t.Fatalf("output %q", out)
	}
}

func TestRerunWithBothPresent(t *testing.T) {
	f := newFakeForge()
	f.tags["v0.1.2"] = testSHA
	f.releases["v0.1.2"] = 7
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	out, err := runHelper(t, srv, writeVersion(t, "0.1.2\n"), "secret")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, req := range f.log {
		if strings.HasPrefix(req, "POST") {
			t.Fatalf("rerun performed mutation %s", req)
		}
	}
	if !strings.Contains(out, "already exists (id 7)") {
		t.Fatalf("output %q", out)
	}
}

func TestRecoveryFromTagOnly(t *testing.T) {
	f := newFakeForge()
	f.tags["v0.1.2"] = testSHA
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	if _, err := runHelper(t, srv, writeVersion(t, "0.1.2\n"), "secret"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := f.releases["v0.1.2"]; !ok {
		t.Fatal("release not created on recovery")
	}
	for _, req := range f.log {
		if req == "POST /tags" {
			t.Fatal("recovery re-created the tag")
		}
	}
}

func TestReleaseFailureThenRerun(t *testing.T) {
	f := newFakeForge()
	f.failRel = true
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	vf := writeVersion(t, "0.1.2\n")

	if _, err := runHelper(t, srv, vf, "secret"); err == nil {
		t.Fatal("expected release creation failure")
	}
	if f.tags["v0.1.2"] != testSHA {
		t.Fatal("tag must exist after first attempt")
	}
	if _, err := runHelper(t, srv, vf, "secret"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, ok := f.releases["v0.1.2"]; !ok {
		t.Fatal("release not created on rerun")
	}
}

func TestExistingTagOtherCommit(t *testing.T) {
	f := newFakeForge()
	f.tags["v0.1.2"] = otherSHA
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	_, err := runHelper(t, srv, writeVersion(t, "0.1.2\n"), "secret")
	if err == nil || !strings.Contains(err.Error(), "bump VERSION") {
		t.Fatalf("err = %v, want forgotten-bump refusal", err)
	}
	if f.tags["v0.1.2"] != otherSHA {
		t.Fatal("tag was moved")
	}
	if len(f.releases) != 0 {
		t.Fatal("release was created")
	}
	if len(f.log) != 1 {
		t.Fatalf("requests %v, want only the tag lookup", f.log)
	}
}

func TestMissingToken(t *testing.T) {
	f := newFakeForge()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	_, err := runHelper(t, srv, writeVersion(t, "0.1.2\n"), "")
	if err == nil || !strings.Contains(err.Error(), "FORGE_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if len(f.log) != 0 {
		t.Fatalf("requests made without token: %v", f.log)
	}
}

func TestMalformedVersion(t *testing.T) {
	f := newFakeForge()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	for _, content := range []string{
		"",
		"\n",
		"v0.1.2\n",
		" 0.1.2\n",
		"0.1.2 \n",
		"0.1.2\n\n",
		"0.1.2\n0.1.3\n",
		"0.1\n",
		"0.1.2-rc1\n",
		"01.1.2\n",
		"0.1.2\r\n",
	} {
		_, err := runHelper(t, srv, writeVersion(t, content), "secret")
		if err == nil {
			t.Errorf("VERSION %q accepted", content)
		}
	}
	if len(f.log) != 0 {
		t.Fatalf("requests made for malformed VERSION: %v", f.log)
	}
}

func TestParseVersionAccepts(t *testing.T) {
	for _, content := range []string{"0.1.2", "0.1.2\n", "10.20.30\n"} {
		if _, err := parseVersion([]byte(content)); err != nil {
			t.Errorf("VERSION %q rejected: %v", content, err)
		}
	}
}

func TestBadArguments(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{
		{"-repo", "deadfin/deadfin", "-sha", testSHA},
		{"-forge", "http://x", "-sha", testSHA},
		{"-forge", "http://x", "-repo", "deadfin/deadfin"},
		{"-forge", "http://x", "-repo", "deadfin/deadfin", "-sha", "abc"},
		{"-forge", "http://x", "-repo", "deadfin", "-sha", testSHA},
		{"-forge", "x", "-repo", "deadfin/deadfin", "-sha", testSHA},
	} {
		if err := run(args, "secret", &out); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}
