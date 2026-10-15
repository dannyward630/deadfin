// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// bitrateTestResolver admits requests that carry the test auth header, so
// the composed router can exercise both the authenticated and
// unauthenticated BitrateTest paths.
type bitrateTestResolver struct{}

func (bitrateTestResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	if request.Header.Get("X-Test-Actor") == "authenticated" {
		return auth.Identity{Authenticated: true, UserID: dto.UUID{0x01}}, nil
	}
	return auth.Identity{}, nil
}

type bitrateTestEvaluator struct{}

func (bitrateTestEvaluator) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if request.Identity.Authenticated {
		return auth.DecisionAllowed, nil
	}
	return auth.DecisionUnauthenticated, nil
}

type bitrateTestUsers struct{}

func (bitrateTestUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id, Policy: []byte(`{}`)}, nil
}

func newBitrateTestRouter() http.Handler {
	authorizer := middleware.NewAuthorizer(bitrateTestResolver{}, bitrateTestEvaluator{}, bitrateTestUsers{})
	rt := router.New(middleware.NoCache("GetBitrateTestBytes", "GetEndpointInfo"), authorizer.Wrap)
	handlers.RegisterPlaybackInfo(rt, nil, nil)
	return rt
}

func bitrateTestRequest(handler http.Handler, path string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		req.Header.Set("X-Test-Actor", "authenticated")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestBitrateTestRoutesRegisterExactMetadata(t *testing.T) {
	t.Parallel()
	rt := router.New()
	handlers.RegisterPlaybackInfo(rt, nil, nil)
	want := router.Route{
		Method:      http.MethodGet,
		Pattern:     "/Playback/BitrateTest",
		OperationID: "GetBitrateTestBytes",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}
	got, found := routeByOperation(rt.Routes(), want.OperationID)
	if !found {
		t.Fatalf("operation %q is not registered", want.OperationID)
	}
	if got != want {
		t.Fatalf("BitrateTest route = %#v, want %#v", got, want)
	}
}

func TestBitrateTestDefaultAndPowerOfTwoSizes(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	for _, tc := range []struct {
		name    string
		path    string
		wantLen int
	}{
		{name: "default 102400 to 131072", path: "/Playback/BitrateTest", wantLen: 131072},
		{name: "size=1 to 16", path: "/Playback/BitrateTest?size=1", wantLen: 16},
		{name: "size=1024 to 1024", path: "/Playback/BitrateTest?size=1024", wantLen: 1024},
		{name: "size=1000 to 1024", path: "/Playback/BitrateTest?size=1000", wantLen: 1024},
		{name: "size=100000000 to 134217728", path: "/Playback/BitrateTest?size=100000000", wantLen: 134217728},
		{name: "case-insensitive SIZE=1 to 16", path: "/Playback/BitrateTest?SIZE=1", wantLen: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, byteCount := bitrateTestRequestCounting(handler, tc.path, true)
			if rec.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.status)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want %q", got, "application/octet-stream")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
			}
			if byteCount != tc.wantLen {
				t.Fatalf("body byte count = %d, want %d", byteCount, tc.wantLen)
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(tc.wantLen) {
				t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(tc.wantLen))
			}
		})
	}
}

func TestBitrateTestOutOfRangeValidationProducesProblemDetails(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "size=0", path: "/Playback/BitrateTest?size=0"},
		{name: "size=-1", path: "/Playback/BitrateTest?size=-1"},
		{name: "size=100000001", path: "/Playback/BitrateTest?size=100000001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := bitrateTestRequest(handler, tc.path, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
				t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(rec.Body.Len()))
			}
			body := spliceTraceID(t, rec.Body.Bytes())
			want := `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The requested size must be greater than or equal to 1 and less than or equal to 100000000"]},"traceId":"<traceId>"}`
			if body != want {
				t.Fatalf("body = %s\nwant = %s", body, want)
			}
		})
	}
}

func TestBitrateTestUnparseableValidationProducesProblemDetailsWithEscapedQuotes(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "size=abc",
			path: "/Playback/BitrateTest?size=abc",
			want: `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027abc\u0027 is not valid."]},"traceId":"<traceId>"}`,
		},
		{
			name: "size= empty",
			path: "/Playback/BitrateTest?size=",
			want: `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027\u0027 is not valid."]},"traceId":"<traceId>"}`,
		},
		{
			name: "quoted input",
			path: `/Playback/BitrateTest?size=%22attack%22`,
			want: `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027\"attack\"\u0027 is not valid."]},"traceId":"<traceId>"}`,
		},
		{
			name: "backslash input",
			path: `/Playback/BitrateTest?size=a%5Cb`,
			want: `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027a\\b\u0027 is not valid."]},"traceId":"<traceId>"}`,
		},
		{
			name: "apostrophe and markup input",
			path: `/Playback/BitrateTest?size=%27%5D%7D%3Cx%3E`,
			want: `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"size":["The value \u0027\u0027]}\u003cx\u003e\u0027 is not valid."]},"traceId":"<traceId>"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := bitrateTestRequest(handler, tc.path, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
				t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(rec.Body.Len()))
			}
			body := spliceTraceID(t, rec.Body.Bytes())
			if body != tc.want {
				t.Fatalf("body = %s\nwant = %s", body, tc.want)
			}
		})
	}
}

func TestBitrateTestTraceIdIsPerRequest(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	rec1 := bitrateTestRequest(handler, "/Playback/BitrateTest?size=0", true)
	rec2 := bitrateTestRequest(handler, "/Playback/BitrateTest?size=0", true)
	trace1 := extractTraceID(t, rec1.Body.Bytes())
	trace2 := extractTraceID(t, rec2.Body.Bytes())
	if trace1 == trace2 {
		t.Fatalf("two consecutive requests produced the same traceId: %s", trace1)
	}
}

func TestBitrateTestUnauthenticatedReturns401WithNoCacheHeader(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	resp, err := client.Get(server.URL + "/Playback/BitrateTest")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body length = %d, want 0 (bodiless 401)", len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want empty", ct)
	}
	if got := resp.Header.Get("Content-Length"); got != "0" {
		t.Errorf("Content-Length = %q, want %q", got, "0")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

func TestBitrateTestRandomBodyIsNotAllZero(t *testing.T) {
	t.Parallel()
	handler := newBitrateTestRouter()
	rec := bitrateTestRequest(handler, "/Playback/BitrateTest?size=1024", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if isAllZero(rec.Body.Bytes()) {
		t.Fatalf("body is all zero bytes; expected random data")
	}
}

func TestBitrateTestTraceIDPatternRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "too short", value: "00-abc-00"},
		{name: "uppercase hex", value: "00-0123456789ABCDEF0123456789ABCDEF-0123456789ABCDEF-00"},
		{name: "wrong version", value: "01-0123456789abcdef0123456789abcdef-0123456789abcdef-00"},
		{name: "missing dash", value: "00-0123456789abcdef0123456789abcdef0123456789abcdef-00"},
		{name: "trailing suffix", value: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-00-extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if traceIDPattern.MatchString(tc.value) {
				t.Fatalf("traceIDPattern matched %q, want rejection", tc.value)
			}
		})
	}
}

// isAllZero reports whether every byte in buf is 0.
func isAllZero(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return true
}

// traceIDPattern matches the W3C traceparent form: 00-<32 hex>-<16 hex>-00.
var traceIDPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-00$`)

// spliceTraceID replaces the traceId value in the body with the placeholder
// <traceId> so the remainder of the ProblemDetails body compares byte-exact.
func spliceTraceID(t *testing.T, body []byte) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not JSON: %v; body=%q", err, body)
	}
	traceRaw, ok := doc["traceId"]
	if !ok {
		t.Fatalf("body has no traceId field: %s", body)
	}
	var trace string
	if err := json.Unmarshal(traceRaw, &trace); err != nil {
		t.Fatalf("traceId is not a string: %v; body=%q", err, body)
	}
	if !traceIDPattern.MatchString(trace) {
		t.Fatalf("traceId = %q, want W3C traceparent form", trace)
	}
	return traceIDReplacement.ReplaceAllString(string(body), `"traceId":"<traceId>"`)
}

var traceIDReplacement = regexp.MustCompile(`"traceId":"[^"]*"`)

// extractTraceID returns the raw traceId string from a ProblemDetails body.
func extractTraceID(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not JSON: %v; body=%q", err, body)
	}
	return doc.TraceID
}

// countingResponseWriter is a minimal http.ResponseWriter that counts the
// bytes written without buffering them, so the 128 MiB success case never
// holds the full body in memory.
type countingResponseWriter struct {
	header    http.Header
	status    int
	byteCount int
	wroteHead bool
}

func (c *countingResponseWriter) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *countingResponseWriter) Write(data []byte) (int, error) {
	if !c.wroteHead {
		c.WriteHeader(http.StatusOK)
	}
	c.byteCount += len(data)
	return len(data), nil
}

func (c *countingResponseWriter) WriteHeader(status int) {
	if c.wroteHead {
		return
	}
	c.status = status
	c.wroteHead = true
}

// bitrateTestRequestCounting sends an authenticated request through the
// composed router using a discarding counter so large bodies never buffer.
func bitrateTestRequestCounting(handler http.Handler, path string, authenticated bool) (*countingResponseWriter, int) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		req.Header.Set("X-Test-Actor", "authenticated")
	}
	rec := &countingResponseWriter{}
	handler.ServeHTTP(rec, req)
	return rec, rec.byteCount
}

// Ensure io is used for the interface satisfaction check.
var _ io.Writer = (*countingResponseWriter)(nil)
