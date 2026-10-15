// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"
)

// maxBody bounds the bytes that a response may keep in memory. Larger
// bodies are counted and dropped.
const maxBody = 64 << 20

// server is one side of the comparison.
type server struct {
	label string
	base  string // scheme://host[/baseurl] with no trailing slash
	token string
	http  *http.Client
}

func newServer(label, base, token string, timeout time.Duration) *server {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	return &server{
		label: label,
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: timeout, Transport: transport},
	}
}

// response is one timed exchange. Body holds the body only when the caller
// asked to keep it.
type response struct {
	Status int
	Body   []byte
	Bytes  int64
	TTFB   time.Duration
	Total  time.Duration
}

// resolve turns a server-relative path ("/Items?...") or an absolute URL into
// an absolute URL on this server.
func (s *server) resolve(ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	return s.base + ref
}

// do sends one request and times it. Total covers the request through the
// last body byte. TTFB covers the request through the first response byte.
func (s *server) do(ctx context.Context, method, ref string, body []byte, keep bool) (response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.resolve(ref), reader)
	if err != nil {
		return response{}, err
	}
	// Token-only MediaBrowser header: Jellyfin 10.11+ refuses legacy
	// X-Emby-Token auth. No Client or Device fields, so the run does not
	// register a new device on either server.
	req.Header.Set("Authorization", `MediaBrowser Token="`+s.token+`"`)
	req.Header.Set("Accept", "application/json, */*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	var first time.Time
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { first = time.Now() }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	start := time.Now()
	resp, err := s.http.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()

	var out response
	out.Status = resp.StatusCode
	if keep {
		var buf bytes.Buffer
		n, err := io.Copy(&buf, io.LimitReader(resp.Body, maxBody))
		out.Bytes = n
		if err != nil {
			return out, err
		}
		rest, err := io.Copy(io.Discard, resp.Body)
		out.Bytes += rest
		if err != nil {
			return out, err
		}
		out.Body = buf.Bytes()
	} else {
		n, err := io.Copy(io.Discard, resp.Body)
		out.Bytes = n
		if err != nil {
			return out, err
		}
	}
	out.Total = time.Since(start)
	if !first.IsZero() {
		out.TTFB = first.Sub(start)
	}
	return out, nil
}

// getJSON fetches ref and decodes a 200 response into v.
func (s *server) getJSON(ctx context.Context, ref string, v any) error {
	resp, err := s.do(ctx, http.MethodGet, ref, nil, true)
	if err != nil {
		return fmt.Errorf("%s GET %s: %w", s.label, pathOnly(ref), err)
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("%s GET %s: status %d", s.label, pathOnly(ref), resp.Status)
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return fmt.Errorf("%s GET %s: decode: %w", s.label, pathOnly(ref), err)
	}
	return nil
}

// pathOnly drops the query so that errors and reports never echo query
// parameters, which can carry an ApiKey in server-built URLs.
func pathOnly(ref string) string {
	if u, err := url.Parse(ref); err == nil {
		return u.Path
	}
	if i := strings.IndexByte(ref, '?'); i >= 0 {
		return ref[:i]
	}
	return ref
}
