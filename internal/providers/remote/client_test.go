// SPDX-License-Identifier: GPL-2.0-only

package remote_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

// doerFunc adapts a function to remote.Doer.
type doerFunc func(*http.Request) (*http.Response, error)

type credentialTransportError string

func (err credentialTransportError) Error() string                   { return string(err) }
func (fn doerFunc) Do(request *http.Request) (*http.Response, error) { return fn(request) }

type closeTrackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (body *closeTrackingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func jsonResponse(status int, cacheControl, body string) *http.Response {
	header := http.Header{"Content-Type": {"application/json"}}
	if cacheControl != "" {
		header.Set("Cache-Control", cacheControl)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClientCachesFreshResponseAndSkipsRepeatFetch(t *testing.T) {
	var calls atomic.Int64
	client := remote.NewClient(remote.Config{
		Clock:        clock.NewFake(time.Unix(0, 0)),
		CacheEntries: 8,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(http.StatusOK, "max-age=3600", `{"id":1}`), nil
		}),
	})

	for range 3 {
		response, err := client.Get(t.Context(), "https://api.example/movie/1")
		if err != nil {
			t.Fatalf("get movie: %v", err)
		}
		if response.Status != http.StatusOK || string(response.Body) != `{"id":1}` {
			t.Fatalf("get movie response = %d %q", response.Status, response.Body)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 (later reads served from cache)", calls.Load())
	}
}

func TestClientRefetchesAfterCacheExpiry(t *testing.T) {
	var calls atomic.Int64
	fake := clock.NewFake(time.Unix(0, 0))
	client := remote.NewClient(remote.Config{
		Clock:        fake,
		CacheEntries: 8,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(http.StatusOK, "max-age=60", `{}`), nil
		}),
	})

	if _, err := client.Get(t.Context(), "https://api.example/x"); err != nil {
		t.Fatalf("first get: %v", err)
	}
	fake.Advance(61 * time.Second)
	if _, err := client.Get(t.Context(), "https://api.example/x"); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (cache expired between reads)", calls.Load())
	}
}

func TestClientDoesNotCacheNoStore(t *testing.T) {
	var calls atomic.Int64
	client := remote.NewClient(remote.Config{
		Clock:        clock.NewFake(time.Unix(0, 0)),
		CacheEntries: 8,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(http.StatusOK, "no-store", `{}`), nil
		}),
	})
	for range 2 {
		if _, err := client.Get(t.Context(), "https://api.example/y"); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (no-store responses are never cached)", calls.Load())
	}
}

func TestClientReturnsNonSuccessStatusWithoutError(t *testing.T) {
	client := remote.NewClient(remote.Config{
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusNotFound, "", `{"status_message":"not found"}`), nil
		}),
	})
	err := client.GetJSON(t.Context(), "https://api.example/missing", nil)
	if !remote.NotFound(err) {
		t.Fatalf("GetJSON on 404 = %v, want remote.NotFound", err)
	}
}

func TestClientAllowsNonEmptyResponseAtMaxInt64Limit(t *testing.T) {
	const body = `{"result":"valid"}`
	client := remote.NewClient(remote.Config{
		MaxResponseBytes: math.MaxInt64,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, "", body), nil
		}),
	})

	response, err := client.Get(t.Context(), "https://api.example/valid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(response.Body, []byte(body)) {
		t.Errorf("response body = %q, want %q", response.Body, body)
	}
}

func TestClientRejectsExactOversizeResponseWithoutCachingAndClosesBody(t *testing.T) {
	const responseBody = `{"ok":true}`
	maxBytes := int64(len(responseBody) - 1)
	var calls atomic.Int64
	var bodies []*closeTrackingBody
	client := remote.NewClient(remote.Config{
		CacheEntries:     1,
		MaxResponseBytes: maxBytes,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			body := &closeTrackingBody{Reader: strings.NewReader(responseBody)}
			bodies = append(bodies, body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       body,
			}, nil
		}),
	})

	for range 2 {
		response, err := client.Get(t.Context(), "https://api.example/oversize")
		if response != nil {
			t.Errorf("Get response = %#v, want nil", response)
		}
		if !errors.Is(err, remote.ErrRequestFailed) {
			t.Errorf("errors.Is(err, remote.ErrRequestFailed) = false, want true: %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("upstream calls = %d, want 2 (oversize response must not cache)", calls.Load())
	}
	for _, body := range bodies {
		if !body.closed.Load() {
			t.Error("oversize response body was not closed")
		}
	}
}

func TestClientRedactsTransportCauseAndUsesDefaultLogger(t *testing.T) {
	const secret = "tmdb-credential-opaque-6a3b"

	previousDefault := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousDefault) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	client := remote.NewClient(remote.Config{
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return nil, credentialTransportError("transport failed with api_key=" + secret)
		}),
	})
	err := client.GetJSON(t.Context(), "https://api.themoviedb.org/3/movie/42?api_key="+secret, nil)
	if err == nil {
		t.Fatal("GetJSON returned nil, want transport failure")
	}
	if !errors.Is(err, remote.ErrRequestFailed) {
		t.Errorf("errors.Is(err, remote.ErrRequestFailed) = false, want true: %v", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Errorf("errors.Unwrap(err) = %T %v, want nil", unwrapped, unwrapped)
	}
	var raw credentialTransportError
	if errors.As(err, &raw) {
		t.Errorf("errors.As exposed raw transport cause %q", raw)
	}
	for _, output := range []string{err.Error(), logs.String()} {
		if strings.Contains(output, secret) {
			t.Errorf("credential leaked in observable output: %q", output)
		}
	}
	if logs.Len() == 0 {
		t.Fatal("nil logger did not use slog default handler")
	}
}

// TestClientRedactsSensitiveURLsFromErrorsAndLogs protects TMDb credentials
// when an upstream transport error or an unsuccessful status reaches callers.
func TestClientRedactsSensitiveURLsFromErrorsAndLogs(t *testing.T) {
	const (
		apiKey     = "tmdb-key-opaque-7c2e"
		query      = "api_key=tmdb-key-opaque-7c2e&language=de-DE"
		requestURL = "https://api.themoviedb.org/3/tv/42?" + query
		statusBody = `{"status_message":"service unavailable"}`
	)

	for _, testCase := range []struct {
		name string
		doer doerFunc
	}{
		{
			name: "transport failure",
			doer: doerFunc(func(request *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("upstream dial failure for %s", request.URL)
			}),
		},
		{
			name: "non-404 status",
			doer: doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusServiceUnavailable, "", statusBody), nil
			}),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var logs bytes.Buffer
			client := remote.NewClient(remote.Config{
				Doer:   testCase.doer,
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})

			err := client.GetJSON(t.Context(), requestURL, nil)
			if err == nil {
				t.Fatal("GetJSON returned nil, want upstream failure")
			}
			if logs.Len() == 0 {
				t.Fatal("upstream failure produced no structured log record")
			}
			for _, secret := range []string{apiKey, query, requestURL} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked %q: %v", secret, err)
				}
				if strings.Contains(logs.String(), secret) {
					t.Errorf("structured logs leaked %q: %s", secret, logs.String())
				}
			}
		})
	}
}

func TestClientRateLimiterRefillsBetweenRequests(t *testing.T) {
	var calls atomic.Int64
	fake := clock.NewFake(time.Unix(0, 0))
	client := remote.NewClient(remote.Config{
		Clock:       fake,
		MinInterval: time.Second,
		Burst:       1,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(http.StatusOK, "no-store", `{}`), nil
		}),
	})

	// First request consumes the single burst token immediately.
	if _, err := client.Get(t.Context(), "https://api.example/a"); err != nil {
		t.Fatalf("first get: %v", err)
	}
	// After one interval the bucket has refilled, so the next request proceeds
	// without blocking. (Blocking-then-cancel is covered separately, and the
	// token math deterministically in ratelimit_internal_test.go.)
	fake.Advance(time.Second)
	if _, err := client.Get(t.Context(), "https://api.example/b"); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestClientRateLimiterHonorsCancellation(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0))
	client := remote.NewClient(remote.Config{
		Clock:       fake,
		MinInterval: time.Hour,
		Burst:       1,
		Doer: doerFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, "no-store", `{}`), nil
		}),
	})
	if _, err := client.Get(t.Context(), "https://api.example/a"); err != nil {
		t.Fatalf("first get: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.Get(ctx, "https://api.example/b")
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled rate-limited get returned nil, want context error")
	}
}
