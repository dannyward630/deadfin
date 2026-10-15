// SPDX-License-Identifier: GPL-2.0-only

// Package remotetest provides recorded-HTTP cassettes for provider tests, so
// metadata providers are exercised against pinned fixtures instead of live APIs
// (P8.md: no live calls in CI). A cassette is a reviewable JSON file of
// request/response interactions; the [Cassette] replayer satisfies the
// remote.Doer interface, and [Recorder] captures fresh cassettes against a live
// API for the manual smoke path.
package remotetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

// Interaction is one recorded request/response pair. Body is stored as raw
// bytes (usually JSON) so cassettes stay human-readable under review.
type Interaction struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Request is the JSON request body of a POST, such as a GraphQL query.
	// When set, a request matches only with an equal body (compared after
	// JSON compaction).
	Request  json.RawMessage     `json:"request,omitempty"`
	Status   int                 `json:"status"`
	Header   map[string][]string `json:"header,omitempty"`
	Body     json.RawMessage     `json:"body,omitempty"`
	BodyText string              `json:"bodyText,omitempty"`
}

// Cassette replays recorded interactions. It matches requests by method and by
// a normalized URL that excludes the api_key query parameter, so cassettes are
// independent of whichever key recorded them and tests run keyless.
type Cassette struct {
	byKey map[string]Interaction
}

// NewCassette builds a replayer from in-memory interactions.
func NewCassette(interactions ...Interaction) *Cassette {
	cassette := &Cassette{byKey: make(map[string]Interaction, len(interactions))}
	for _, interaction := range interactions {
		cassette.byKey[matchKey(interaction.Method, interaction.URL)+bodyKey(interaction.Request)] = interaction
	}
	return cassette
}

// Load reads a cassette JSON file (an array of interactions).
func Load(path string) (*Cassette, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("remotetest: read cassette %q: %w", path, err)
	}
	var interactions []Interaction
	if err := json.Unmarshal(contents, &interactions); err != nil {
		return nil, fmt.Errorf("remotetest: decode cassette %q: %w", path, err)
	}
	return NewCassette(interactions...), nil
}

// Do returns the recorded response for request, or an error naming the
// unmatched request so a missing cassette entry fails loudly.
func (cassette *Cassette) Do(request *http.Request) (*http.Response, error) {
	requestBody, err := readRequestBody(request)
	if err != nil {
		return nil, err
	}
	key := matchKey(request.Method, request.URL.String()) + bodyKey(requestBody)
	interaction, found := cassette.byKey[key]
	if !found {
		return nil, fmt.Errorf("remotetest: no cassette interaction for %s %s", request.Method, redact(request.URL))
	}
	body := interaction.Body
	if body == nil && interaction.BodyText != "" {
		body = json.RawMessage(interaction.BodyText)
	}
	header := make(http.Header, len(interaction.Header))
	for name, values := range interaction.Header {
		header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	return &http.Response{
		StatusCode: interaction.Status,
		Status:     http.StatusText(interaction.Status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

// Recorder wraps a live Doer, capturing every interaction for later save. It is
// used only by the manual smoke script that records cassettes against a real
// API key; CI never constructs one.
type Recorder struct {
	inner http.RoundTripper

	mu           sync.Mutex
	interactions []Interaction
}

// NewRecorder wraps inner (nil selects http.DefaultTransport).
func NewRecorder(inner http.RoundTripper) *Recorder {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &Recorder{inner: inner}
}

// Do executes request live and records the response.
func (recorder *Recorder) Do(request *http.Request) (*http.Response, error) {
	requestBody, err := readRequestBody(request)
	if err != nil {
		return nil, err
	}
	response, err := recorder.inner.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))

	header := make(map[string][]string, len(response.Header))
	for _, name := range []string{"Cache-Control", "Expires", "Date", "Content-Type", "Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if value := response.Header.Values(name); len(value) != 0 {
			header[name] = append([]string(nil), value...)
		}
	}
	recorder.mu.Lock()
	recorder.interactions = append(recorder.interactions, Interaction{
		Method:  request.Method,
		URL:     redact(request.URL),
		Request: requestBody,
		Status:  response.StatusCode,
		Header:  header,
		Body:    json.RawMessage(body),
	})
	recorder.mu.Unlock()
	return response, nil
}

// Save writes the recorded interactions to path as an indented JSON array.
func (recorder *Recorder) Save(path string) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	encoded, err := json.MarshalIndent(recorder.interactions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

// matchKey normalizes a method and URL into a cassette lookup key that ignores
// the api_key parameter and query-parameter ordering.
func matchKey(method, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToUpper(method) + " " + rawURL
	}
	query := parsed.Query()
	query.Del("api_key")
	names := make([]string, 0, len(query))
	for name := range query {
		names = append(names, name)
	}
	sort.Strings(names)
	var normalized strings.Builder
	for _, name := range names {
		values := query[name]
		sort.Strings(values)
		for _, value := range values {
			normalized.WriteString(name)
			normalized.WriteByte('=')
			normalized.WriteString(value)
			normalized.WriteByte('&')
		}
	}
	return strings.ToUpper(method) + " " + parsed.Host + parsed.Path + "?" + normalized.String()
}

// redact removes the api_key query parameter from a URL for recording and error
// messages, so no key is ever written to a cassette or log.
func redact(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	clone := *parsed
	query := clone.Query()
	if query.Has("api_key") {
		query.Set("api_key", "REDACTED")
		clone.RawQuery = query.Encode()
	}
	return clone.String()
}

// readRequestBody reads and restores a request body. It returns nil for a
// request without one.
func readRequestBody(request *http.Request) (json.RawMessage, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("remotetest: read request body: %w", err)
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return nil, nil
	}
	return json.RawMessage(body), nil
}

// bodyKey normalizes a JSON request body into a match-key suffix.
func bodyKey(body json.RawMessage) string {
	if len(body) == 0 {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return " " + string(body)
	}
	return " " + compact.String()
}
