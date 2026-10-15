// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// client talks to one Deadfin server with an API key. It never writes the
// key to output: every URL and error it returns passes through redact.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token string, timeout time.Duration) *client {
	return &client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: timeout}}
}

// secretParams are the query parameters that carry credentials in Jellyfin
// URLs. Matching is case-insensitive.
var secretParams = regexp.MustCompile(`(?i)([?&](?:api_?key|x-emby-token|x-mediabrowser-token)=)[^&#\s"]*`)

// redact removes credentials from s: secret query parameters and the
// literal token.
func (c *client) redact(s string) string {
	s = secretParams.ReplaceAllString(s, "${1}REDACTED")
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "REDACTED")
	}
	return s
}

// stripSecrets removes secret query parameters from a URL, so that the
// result can be written to a plan or a report. The probe adds the key
// again at run time.
func stripSecrets(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return secretParams.ReplaceAllString(raw, "${1}REDACTED")
	}
	q := u.Query()
	for k := range q {
		switch strings.ToLower(k) {
		case "api_key", "apikey", "x-emby-token", "x-mediabrowser-token":
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *client) authHeader(deviceID string) string {
	return fmt.Sprintf(`MediaBrowser Client="qamatrix", Device="qamatrix", DeviceId=%q, Version="1", Token=%q`, deviceID, c.token)
}

func (c *client) resolve(ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	return c.base + ref
}

// do sends one request and returns the body. Non-2xx statuses are errors.
func (c *client) do(ctx context.Context, method, ref, deviceID string, body any, hdr http.Header) ([]byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.resolve(ref), rd)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", c.redact(err.Error()))
	}
	req.Header.Set("Authorization", c.authHeader(deviceID))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", c.redact(err.Error()))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %s", method, c.redact(ref), c.redact(err.Error()))
	}
	if resp.StatusCode/100 != 2 {
		snippet := string(b)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return b, resp.Header, fmt.Errorf("%s %s: HTTP %d: %s", method, c.redact(ref), resp.StatusCode, c.redact(snippet))
	}
	return b, resp.Header, nil
}

func (c *client) getJSON(ctx context.Context, ref string, out any) error {
	b, _, err := c.do(ctx, http.MethodGet, ref, "qamatrix", nil, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
