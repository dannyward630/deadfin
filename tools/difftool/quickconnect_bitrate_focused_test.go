// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCaptureEquality proves that a reminted second redemption fails the
// per-host capture-equality assertion. The first redemption captures
// AccessToken; the second returns a different token. The assert_equal
// assertion must reject this so a Deadfin implementation that incorrectly
// mints a new result on replay is caught.

func TestCaptureEquality(t *testing.T) {
	directory := t.TempDir()
	corpus := filepath.Join(directory, "replay-equality.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: replay equality
steps:
  - name: first redemption
    method: POST
    path: /redeem
    capture:
      access_token: AccessToken
    operation_id: Redeem
  - name: second redemption must replay
    method: POST
    path: /redeem
    expect:
      status: 200
      assert_equal:
        AccessToken: access_token
    operation_id: Redeem
`)

	// Override the second response to return a reminted token.
	leftSecond := newRemintReplayHost(t, "left-token-v1", "left-token-v2")
	rightSecond := newRemintReplayHost(t, "right-token-v1", "right-token-v2")

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    leftSecond.server.URL,
		RightURL:   rightSecond.server.URL,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("reminted second redemption must fail the capture-equality assertion: %v", err)
	}
	for _, secret := range []string{"left-token-v1", "left-token-v2", "right-token-v1", "right-token-v2"} {
		artifacts := issueArtifactContents(t, filepath.Join(directory, "issues"))
		for path, contents := range artifacts {
			if strings.Contains(contents, secret) {
				t.Fatalf("issue artifact %s exposes token %q:\n%s", path, secret, contents)
			}
		}
	}
}

type remintReplayHost struct {
	calls       int
	firstToken  string
	secondToken string
	server      *httptest.Server
}

func newRemintReplayHost(t *testing.T, firstToken, secondToken string) *remintReplayHost {
	t.Helper()
	host := &remintReplayHost{firstToken: firstToken, secondToken: secondToken}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"fixture"}`))
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
		case "/redeem":
			host.calls++
			w.Header().Set("Content-Type", "application/json")
			if host.calls == 1 {
				_, _ = w.Write([]byte(`{"AccessToken":"` + firstToken + `"}`))
			} else {
				_, _ = w.Write([]byte(`{"AccessToken":"` + secondToken + `"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

// TestExpectedBodyBytes proves that an exact body_bytes expectation rejects a
// response that carries extra whitespace. The /QuickConnect/Enabled scalar
// must return the exact raw bytes "true", not "true\n".

func TestExpectedBodyBytes(t *testing.T) {
	directory := t.TempDir()
	corpus := filepath.Join(directory, "body-bytes.yaml")
	writeFile(t, corpus, `format: goldenrec-scenario/v1
name: body bytes
steps:
  - name: enabled scalar must be exact
    method: GET
    path: /enabled
    expect:
      status: 200
      body_bytes: "true"
    operation_id: GetEnabled
`)

	// Left returns exact "true", right returns "true\n".
	left := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/enabled": {Headers: map[string]string{"Content-Type": "application/json"}, Body: "true"},
	})
	right := newFixtureHost(t, "unused", map[string]fixtureResponse{
		"/enabled": {Headers: map[string]string{"Content-Type": "application/json"}, Body: "true\n"},
	})

	_, err := Run(context.Background(), Config{
		Corpus:     corpus,
		LeftURL:    left.server.URL,
		RightURL:   right.server.URL,
		ResultsDir: filepath.Join(directory, "results"),
		IssuesDir:  filepath.Join(directory, "issues"),
	})
	if !errors.Is(err, errDifferences) {
		t.Fatalf("true\\n must fail the exact body_bytes=true expectation: %v", err)
	}
	artifacts := issueArtifactContents(t, filepath.Join(directory, "issues"))
	if len(artifacts) == 0 {
		t.Fatal("body_bytes mismatch must produce an issue artifact")
	}
}

// TestExpectedBodyLength proves that a one-byte length mismatch fails even
// when both bodies are random, and that the upper-bound response is counted
// without entering the existing 32 MiB buffering path.

func TestExpectedBodyLength(t *testing.T) {
	t.Run("one-byte mismatch fails even when both bodies are random", func(t *testing.T) {
		directory := t.TempDir()
		corpus := filepath.Join(directory, "body-length.yaml")
		writeFile(t, corpus, `format: goldenrec-scenario/v1
name: body length
steps:
  - name: random body length must match exactly
    method: GET
    path: /bitrate
    auth_role: admin
    expect:
      status: 200
      body_length: 1024
    operation_id: GetBitrateTestBytes
`)

		// Left returns 1024 bytes, right returns 1025.
		left := newBitrateTestReplayHost(t, 1024)
		right := newBitrateTestReplayHost(t, 1025)

		_, err := Run(context.Background(), Config{
			Corpus:     corpus,
			LeftURL:    left.server.URL,
			RightURL:   right.server.URL,
			ResultsDir: filepath.Join(directory, "results"),
			IssuesDir:  filepath.Join(directory, "issues"),
			LeftAuth:   AuthConfig{Role: "admin", Username: "left", Password: "left"},
			RightAuth:  AuthConfig{Role: "admin", Username: "right", Password: "right"},
		})
		if !errors.Is(err, errDifferences) {
			t.Fatalf("a one-byte length mismatch must fail the body_length expectation: %v", err)
		}
	})

	t.Run("over-limit response is counted without draining", func(t *testing.T) {
		directory := t.TempDir()
		corpus := filepath.Join(directory, "body-length-limit.yaml")
		writeFile(t, corpus, `format: goldenrec-scenario/v1
name: body length over-limit
steps:
  - name: over-limit response does not drain
    method: GET
    path: /bitrate
    auth_role: admin
    expect:
      status: 200
      body_length: 1024
    operation_id: GetBitrateTestBytes
`)

		// The instrumented transport wraps the client that Run actually uses
		// (passed via Config.Client). Each host served 4096 bytes but the
		// limit is 1024, so the counter must consume at most 1025 bytes
		// (limit+1) per host and must not drain the remainder to EOF.
		left := newCountingBitrateTestReplayHost(t, 4096)
		right := newCountingBitrateTestReplayHost(t, 4096)
		client := newCountingClient(left, right)

		_, err := Run(context.Background(), Config{
			Corpus:     corpus,
			LeftURL:    left.server.URL,
			RightURL:   right.server.URL,
			ResultsDir: filepath.Join(directory, "results"),
			IssuesDir:  filepath.Join(directory, "issues"),
			LeftAuth:   AuthConfig{Role: "admin", Username: "left", Password: "left"},
			RightAuth:  AuthConfig{Role: "admin", Username: "right", Password: "right"},
			Client:     client,
		})
		if !errors.Is(err, errDifferences) {
			t.Fatalf("over-limit response must fail the body_length expectation: %v", err)
		}

		// Each host served 4096 bytes but the limit is 1024. The counter must
		// have read no more than 1025 bytes (limit+1) per host and must not
		// have drained the remaining 3071 bytes to EOF. The instrumented
		// transport observes the client that Run actually uses, so this
		// assertion cannot pass vacuously.
		for _, host := range []*countingBitrateTestReplayHost{left, right} {
			consumed := host.bytesConsumed()
			if consumed > 1025 {
				t.Fatalf("over-limit reader consumed %d bytes from %s, want at most 1025 (limit+1)", consumed, host.server.URL)
			}
			if consumed == 0 {
				t.Fatalf("instrumented transport observed zero bytes from %s; the counting client was not used by Run", host.server.URL)
			}
		}
	})

	t.Run("134217728-byte contract bypasses the 32 MiB buffered path", func(t *testing.T) {
		directory := t.TempDir()
		corpus := filepath.Join(directory, "body-length-contract.yaml")
		// The valid upper bound is 134217728 (128 MiB). This exceeds the 32 MiB
		// buffering limit. The body_length path must count the stream without
		// retaining the body.
		writeFile(t, corpus, `format: goldenrec-scenario/v1
name: body length upper bound contract
steps:
  - name: upper bound counted without buffering
    method: GET
    path: /bitrate
    auth_role: admin
    expect:
      status: 200
      body_length: 134217728
    operation_id: GetBitrateTestBytes
`)

		// The 134217728-byte fixture exceeds the 32 MiB buffered-body limit.
		// readBoundedResponseBody rejects this fixture. A successful test
		// proves that the counted path ran.
		left := newBitrateTestReplayHost(t, 134217728)
		right := newBitrateTestReplayHost(t, 134217728)

		_, err := Run(context.Background(), Config{
			Corpus:     corpus,
			LeftURL:    left.server.URL,
			RightURL:   right.server.URL,
			ResultsDir: filepath.Join(directory, "results"),
			IssuesDir:  filepath.Join(directory, "issues"),
			LeftAuth:   AuthConfig{Role: "admin", Username: "left", Password: "left"},
			RightAuth:  AuthConfig{Role: "admin", Username: "right", Password: "right"},
		})
		if err != nil {
			t.Fatalf("matching 134217728-byte body_length must pass without buffering: %v", err)
		}
	})

	t.Run("matching upper-bound result reports count without body bytes", func(t *testing.T) {
		directory := t.TempDir()
		corpus := filepath.Join(directory, "body-length-result.yaml")
		writeFile(t, corpus, `format: goldenrec-scenario/v1
name: body length result report
steps:
  - name: upper bound counted and reported
    method: GET
    path: /bitrate
    auth_role: admin
    expect:
      status: 200
      body_length: 134217728
    operation_id: GetBitrateTestBytes
`)

		left := newBitrateTestReplayHost(t, 134217728)
		right := newBitrateTestReplayHost(t, 134217728)

		result, err := Run(context.Background(), Config{
			Corpus:     corpus,
			LeftURL:    left.server.URL,
			RightURL:   right.server.URL,
			ResultsDir: filepath.Join(directory, "results"),
			IssuesDir:  filepath.Join(directory, "issues"),
			RunID:      "body-length-result",
			LeftAuth:   AuthConfig{Role: "admin", Username: "left", Password: "left"},
			RightAuth:  AuthConfig{Role: "admin", Username: "right", Password: "right"},
		})
		if err != nil {
			t.Fatalf("matching 134217728-byte body_length must pass: %v", err)
		}
		if len(result.Steps) != 1 {
			t.Fatalf("result steps = %d, want one", len(result.Steps))
		}
		if result.Steps[0].BodyLengthCounted != 134217728 {
			t.Fatalf("body_length_counted = %d, want 134217728", result.Steps[0].BodyLengthCounted)
		}
		// The result JSON must contain the count.
		resultPath := filepath.Join(directory, "results", "body-length-result.json")
		contents, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			t.Fatalf("read result: %v", readErr)
		}
		if !strings.Contains(string(contents), `"body_length_counted": 134217728`) {
			t.Fatalf("result JSON must contain body_length_counted 134217728: %s", string(contents))
		}
		// The result JSON must not contain body bytes.
		if strings.Contains(string(contents), "random") {
			t.Fatal("result JSON must not contain body bytes")
		}
	})
}

// TestCountBoundedResponseBody directly exercises the bounded-read contract
// with an eager reader that returns its entire payload in a single Read call.
// The cap must prevent the single Read from consuming more than limit+1 bytes,
// and the boundary semantics must be exact: over_limit is true iff the body
// contains more than limit bytes.

func TestCountBoundedResponseBody(t *testing.T) {
	t.Run("eager reader over limit reads at most limit+1", func(t *testing.T) {
		// 4096 bytes available, limit 1024. A single Read returns all 4096.
		// The cap must truncate the Read to at most 1025 (limit+1).
		body := newEagerReader(make([]byte, 4096))
		counted, overLimit, err := countBoundedResponseBody(body, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counted != 1025 {
			t.Fatalf("counted = %d, want 1025 (limit+1) — the cap must prevent an eager reader from over-consuming", counted)
		}
		if !overLimit {
			t.Fatal("overLimit = false, want true — a body with more than limit bytes is over the limit")
		}
		if unread := body.unread(); unread != 3071 {
			t.Fatalf("unread = %d, want 3071 (4096 - 1025) — the cap must leave the remainder unread", unread)
		}
	})

	t.Run("exact boundary over limit", func(t *testing.T) {
		// 1025 bytes available, limit 1024. The body exceeds the limit by one.
		// counted must be 1025 (limit+1), over_limit true, nothing unread.
		body := newEagerReader(make([]byte, 1025))
		counted, overLimit, err := countBoundedResponseBody(body, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counted != 1025 {
			t.Fatalf("counted = %d, want 1025 (limit+1) at the boundary", counted)
		}
		if !overLimit {
			t.Fatal("overLimit = false, want true — a body with exactly limit+1 bytes exceeds the limit")
		}
		if unread := body.unread(); unread != 0 {
			t.Fatalf("unread = %d, want 0 — the entire limit+1 was consumed at the boundary", unread)
		}
	})

	t.Run("at limit is not over limit", func(t *testing.T) {
		// 1024 bytes available, limit 1024. The body is exactly at the limit.
		// counted must be 1024, over_limit false.
		body := newEagerReader(make([]byte, 1024))
		counted, overLimit, err := countBoundedResponseBody(body, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counted != 1024 {
			t.Fatalf("counted = %d, want 1024 — a body at the limit must count fully", counted)
		}
		if overLimit {
			t.Fatal("overLimit = true, want false — a body with exactly limit bytes is not over the limit")
		}
		if unread := body.unread(); unread != 0 {
			t.Fatalf("unread = %d, want 0 — the entire body was consumed", unread)
		}
	})
}

// eagerReader returns its entire payload in a single Read call, regardless of
// the buffer size. This models an HTTP transport that buffers the full body
// before the first Read, so the cap in countBoundedResponseBody is the only
// protection against over-consumption.
type eagerReader struct {
	payload []byte
	offset  int
	done    bool
}

func newEagerReader(payload []byte) *eagerReader {
	return &eagerReader{payload: payload}
}

func (r *eagerReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n := copy(p, r.payload[r.offset:])
	r.offset += n
	r.done = true
	return n, nil
}

func (r *eagerReader) unread() int {
	return len(r.payload) - r.offset
}

type bitrateTestReplayHost struct {
	payloadSize int
	server      *httptest.Server
}

func newBitrateTestReplayHost(t *testing.T, payloadSize int) *bitrateTestReplayHost {
	t.Helper()
	host := &bitrateTestReplayHost{payloadSize: payloadSize}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"fixture-token"}`))
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
		case "/bitrate":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			// Write the payload in chunks so the body_length counter reads it as a stream.
			chunk := make([]byte, 4096)
			remaining := payloadSize
			for remaining > 0 {
				n := len(chunk)
				if n > remaining {
					n = remaining
				}
				_, _ = w.Write(chunk[:n])
				remaining -= n
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

type countingBitrateTestReplayHost struct {
	consumed int64
	server   *httptest.Server
}

func (h *countingBitrateTestReplayHost) bytesConsumed() int64 {
	return atomic.LoadInt64(&h.consumed)
}

func newCountingBitrateTestReplayHost(t *testing.T, payloadSize int) *countingBitrateTestReplayHost {
	t.Helper()
	host := &countingBitrateTestReplayHost{}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"fixture-token"}`))
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
		case "/bitrate":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			chunk := make([]byte, 4096)
			remaining := payloadSize
			for remaining > 0 {
				n := len(chunk)
				if n > remaining {
					n = remaining
				}
				_, _ = w.Write(chunk[:n])
				remaining -= n
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(host.server.Close)
	return host
}

// newCountingClient builds an *http.Client whose transport instruments
// response-body reads and routes the count to the matching host by request
// URL. The client is passed to Run via Config.Client so the instrumentation
// observes the bytes that the difftool's own HTTP client actually consumes,
// not a parallel client that Run never touches.
func newCountingClient(hosts ...*countingBitrateTestReplayHost) *http.Client {
	byHost := make(map[string]*countingBitrateTestReplayHost)
	for _, h := range hosts {
		byHost[hostPort(h.server.URL)] = h
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &countingTransport{base: http.DefaultTransport, byHost: byHost},
	}
}

func hostPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u.Host
}

type countingTransport struct {
	base   http.RoundTripper
	byHost map[string]*countingBitrateTestReplayHost
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if host, ok := t.byHost[req.URL.Host]; ok && req.URL.Path == "/bitrate" && resp.Body != nil {
		resp.Body = &countingReader{base: resp.Body, host: host}
	}
	return resp, err
}

type countingReader struct {
	base io.ReadCloser
	host *countingBitrateTestReplayHost
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.base.Read(p)
	atomic.AddInt64(&r.host.consumed, int64(n))
	return n, err
}

func (r *countingReader) Close() error {
	return r.base.Close()
}

// TestProblemDetailsTrace proves that the problem-details-trace normalizer
// replaces only the root traceId field in an ASP.NET ProblemDetails body.
// It preserves type, title, status, and the errors map. A non-object root
// is returned unchanged.
func TestProblemDetailsTrace(t *testing.T) {
	rule := Rule{Name: "problem-details-trace", Action: "problem_details_trace"}
	norm := newNormalizer([]Rule{rule})

	t.Run("replaces root traceId only", func(t *testing.T) {
		response := Response{
			Status:  400,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027abc\u0027 is not valid."]},"traceId":"00-6a7b083e1bda6b544a3643d4236ccbdf-1ed2af42365f690c-00"}`),
		}
		normalized, err := norm.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["traceId"] != "<trace-id>" {
			t.Fatalf("traceId = %#v, want <trace-id>", body["traceId"])
		}
		if body["type"] != "https://tools.ietf.org/html/rfc9110#section-15.5.1" {
			t.Fatalf("type = %#v, want preserved", body["type"])
		}
		if body["title"] != "One or more validation errors occurred." {
			t.Fatalf("title = %#v, want preserved", body["title"])
		}
		if body["status"] != json.Number("400") {
			t.Fatalf("status = %#v, want preserved", body["status"])
		}
		errors, ok := body["errors"].(map[string]any)
		if !ok {
			t.Fatalf("errors = %#v, want map preserved", body["errors"])
		}
		if _, ok := errors["size"]; !ok {
			t.Fatal("errors.size must be preserved")
		}
	})

	t.Run("preserves body without traceId", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"IsLocal":false,"IsInNetwork":true}`),
		}
		normalized, err := norm.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["IsLocal"] != false {
			t.Fatalf("IsLocal = %#v, want preserved", body["IsLocal"])
		}
		if body["IsInNetwork"] != true {
			t.Fatalf("IsInNetwork = %#v, want preserved", body["IsInNetwork"])
		}
		if _, ok := body["traceId"]; ok {
			t.Fatal("traceId must not be added by normalization")
		}
	})

	t.Run("returns non-object root unchanged", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"text/plain"}},
			Body:    []byte(`true`),
		}
		normalized, err := norm.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if normalized.Body != true {
			t.Fatalf("body = %#v, want true preserved", normalized.Body)
		}
	})

	t.Run("two different traceId values normalize equal", func(t *testing.T) {
		left := Response{
			Status:  400,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027abc\u0027 is not valid."]},"traceId":"00-aaa111-111-00"}`),
		}
		right := Response{
			Status:  400,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027abc\u0027 is not valid."]},"traceId":"00-bbb222-222-00"}`),
		}
		leftNorm, err := norm.normalize(left, nil)
		if err != nil {
			t.Fatalf("normalize left: %v", err)
		}
		rightNorm, err := norm.normalize(right, nil)
		if err != nil {
			t.Fatalf("normalize right: %v", err)
		}
		equal, diff := newNormalizedComparator().Compare(leftNorm, rightNorm, norm.dateJitter)
		if !equal {
			t.Fatalf("two different traceId values must normalize equal: %s", diff)
		}
	})
}
