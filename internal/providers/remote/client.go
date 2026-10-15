// SPDX-License-Identifier: GPL-2.0-only

package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// defaultMaxResponseBytes bounds how much of an upstream body the client will
// read, so a hostile or broken provider cannot exhaust memory.
const defaultMaxResponseBytes = 32 << 20

// ErrRequestFailed identifies a remote request failure without exposing an
// upstream transport or decode error, which can contain credentials.
var ErrRequestFailed = errors.New("remote request failed")

// Doer executes a single HTTP request. *http.Client satisfies it; tests supply
// a cassette replayer so no live API is contacted (P8.md: no live calls in CI).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Response is a fully-read, replayable upstream response. It holds the whole
// body in memory (bounded by Config.MaxResponseBytes) so it survives caching
// and is trivial to reason about; providers decode it themselves.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Config parameters the shared transport. The zero value is usable: it yields a
// real-clock, unlimited, uncached client over http.DefaultClient.
type Config struct {
	// Doer executes requests; nil selects an http.Client with RequestTimeout.
	Doer Doer
	// Clock supplies time for rate limiting and cache expiry; nil selects real.
	Clock clock.Clock
	// UserAgent is sent on every request when non-empty.
	UserAgent string
	// MinInterval is the minimum spacing between requests; zero disables pacing.
	MinInterval time.Duration
	// Burst is how many requests may be issued back-to-back before pacing.
	Burst int
	// CacheEntries bounds the response cache; zero disables caching.
	CacheEntries int
	// CacheFallback is the freshness applied to a cacheable response that omits
	// its own max-age/Expires; zero means only self-describing responses cache.
	CacheFallback time.Duration
	// RequestTimeout bounds one outbound call end-to-end, including any
	// rate-limiter wait. With a caller-supplied Doer, zero leaves that caller's
	// timeout policy unchanged.
	RequestTimeout time.Duration
	// Logger records credential-safe request observations. Nil selects slog.Default.
	Logger *slog.Logger
	// MaxResponseBytes bounds the read body; zero selects defaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Client is a shared, rate-limited, response-caching HTTP client for internet
// metadata providers. It owns no goroutines; its cache and limiter are passive
// and safe for concurrent use.
// LogURLRedactor returns a credential-safe endpoint for request observations.
// It must not return raw credentials from a provider-specific URL path.
type LogURLRedactor func(string) string

type Client struct {
	doer           Doer
	clock          clock.Clock
	limiter        *rateLimiter
	cache          *responseCache
	logger         *slog.Logger
	logURL         LogURLRedactor
	userAgent      string
	maxBytes       int64
	requestTimeout time.Duration
}

// NewClient constructs a shared transport from config.
func NewClient(config Config) *Client {
	clk := config.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := config.RequestTimeout
	doer := config.Doer
	if doer == nil {
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		doer = &http.Client{Timeout: timeout}
	}
	maxBytes := config.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	return &Client{
		doer:           doer,
		clock:          clk,
		limiter:        newRateLimiter(clk, config.MinInterval, config.Burst),
		cache:          newResponseCache(clk, config.CacheEntries, config.CacheFallback),
		logger:         logger,
		userAgent:      config.UserAgent,
		maxBytes:       maxBytes,
		requestTimeout: timeout,
	}
}

// WithLogURLRedactor returns a client view that shares the transport's pacing
// and cache state while using redactor for credential-safe observations.
func (client *Client) WithLogURLRedactor(redactor LogURLRedactor) *Client {
	if client == nil {
		return nil
	}
	copy := *client
	copy.logURL = redactor
	return &copy
}

// Get fetches url, returning a cached copy when one is fresh. It paces the
// request through the limiter, reads the whole (bounded) body, and caches the
// result when the response headers permit. A non-2xx status is returned to the
// caller as a Response, not an error — providers distinguish 404 from failure.
func (client *Client) Get(ctx context.Context, url string) (*Response, error) {
	return client.do(ctx, http.MethodGet, url, nil)
}

// Post sends body as an application/json POST to url, as a GraphQL query
// does. It shares Get's pacing, size bound, and logging. A successful
// response is cached under the URL and a digest of the body, so an identical
// query is answered from the cache while it is fresh.
func (client *Client) Post(ctx context.Context, url string, body []byte) (*Response, error) {
	return client.do(ctx, http.MethodPost, url, body)
}

func (client *Client) do(ctx context.Context, method, url string, body []byte) (*Response, error) {
	if client == nil {
		return nil, errClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheKey := url
	if method != http.MethodGet {
		digest := sha256.Sum256(body)
		cacheKey = method + " " + url + " " + hex.EncodeToString(digest[:])
	}
	if entry, ok := client.cache.get(cacheKey); ok {
		client.logRequest(ctx, method, slog.LevelDebug, url, entry.status, 0, 0, "cache hit")
		return &Response{Status: entry.status, Header: entry.header, Body: entry.body}, nil
	}
	if client.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, client.requestTimeout)
		defer cancel()
	}
	started := client.clock.Now()
	if err := client.limiter.wait(ctx); err != nil {
		client.logRequest(ctx, method, slog.LevelWarn, url, 0, 0, client.clock.Now().Sub(started), "rate limit wait failed")
		return nil, err
	}
	waited := client.clock.Now().Sub(started)
	// A concurrent caller may have populated the cache while we waited.
	if entry, ok := client.cache.get(cacheKey); ok {
		client.logRequest(ctx, method, slog.LevelDebug, url, entry.status, waited, client.clock.Now().Sub(started), "cache hit")
		return &Response{Status: entry.status, Header: entry.header, Body: entry.body}, nil
	}

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		client.logRequest(ctx, method, slog.LevelWarn, url, 0, waited, client.clock.Now().Sub(started), "request construction failed")
		return nil, safeRemoteError("build request", err)
	}
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.doer.Do(request)
	if err != nil {
		client.logRequest(ctx, method, slog.LevelWarn, url, 0, waited, client.clock.Now().Sub(started), "request failed")
		return nil, safeRemoteError("request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	readLimit := client.maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, readLimit))
	if err != nil {
		client.logRequest(ctx, method, slog.LevelWarn, url, response.StatusCode, waited, client.clock.Now().Sub(started), "response read failed")
		return nil, safeRemoteError("read response", err)
	}
	if int64(len(responseBody)) > client.maxBytes {
		client.logRequest(ctx, method, slog.LevelWarn, url, response.StatusCode, waited, client.clock.Now().Sub(started), "response exceeds size limit")
		return nil, safeRemoteError("response exceeds size limit", nil)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		client.cache.store(cacheKey, response.StatusCode, response.Header, responseBody)
	}
	level := slog.LevelDebug
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		level = slog.LevelWarn
	}
	client.logRequest(ctx, method, level, url, response.StatusCode, waited, client.clock.Now().Sub(started), "")
	return &Response{Status: response.StatusCode, Header: response.Header, Body: responseBody}, nil
}

// logRequest emits a credential-safe endpoint and fixed error classifications.
// Upstream errors are intentionally never attached: *url.Error and provider
// errors commonly embed the original request URL and its credentials.
func (client *Client) logRequest(ctx context.Context, method string, level slog.Level, rawURL string, status int, wait, elapsed time.Duration, failure string) {
	if !client.logger.Enabled(ctx, level) {
		return
	}
	endpoint := redactURL(rawURL)
	if client.logURL != nil {
		endpoint = client.logURL(rawURL)
	}
	attrs := []slog.Attr{
		slog.String("method", method),
		slog.String("url", endpoint),
		slog.Int("status", status),
		slog.Duration("wait", wait),
		slog.Duration("elapsed", elapsed),
	}
	if failure != "" {
		attrs = append(attrs, slog.String("error", failure))
	}
	client.logger.LogAttrs(ctx, level, "remote request", attrs...)
}

// redactURL preserves an endpoint for observability but never its query,
// fragment, or user info. An unparseable input is not echoed.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// safeRemoteError preserves a safe error identity for callers while its Error
// text remains query-free. It never retains the raw cause because transport
// and decode errors may embed a request URL and its provider credential.
func safeRemoteError(operation string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return redactedError{message: "remote: " + operation}
	}
}

type redactedError struct {
	message string
}

func (err redactedError) Error() string { return err.message }

// Is retains the approved safe identity without exposing the raw cause via
// errors.Unwrap or errors.As.
func (redactedError) Is(target error) bool { return target == ErrRequestFailed }

// GetJSON fetches url and decodes a successful body into destination. It
// returns errStatusNotOK for any non-2xx response so callers can branch on
// not-found without inspecting the Response themselves.
func (client *Client) GetJSON(ctx context.Context, url string, destination any) error {
	response, err := client.Get(ctx, url)
	if err != nil {
		return err
	}
	if response.Status < 200 || response.Status >= 300 {
		return &StatusError{Status: response.Status}
	}
	if destination == nil {
		return nil
	}
	if err := json.Unmarshal(response.Body, destination); err != nil {
		return safeRemoteError("decode response", err)
	}
	return nil
}

// StatusError reports a non-2xx upstream response from GetJSON. It deliberately
// retains no request URL because TMDb URLs carry API keys in their query string.
type StatusError struct{ Status int }

func (err *StatusError) Error() string {
	return fmt.Sprintf("remote: upstream returned status %d", err.Status)
}

// NotFound reports whether err is a 404 status from GetJSON.
func NotFound(err error) bool {
	var status *StatusError
	return errors.As(err, &status) && status.Status == http.StatusNotFound
}
