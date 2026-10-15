// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const mediaBrowserAuthorization = `MediaBrowser Client="Jellyport difftool", Device="Jellyport difftool", DeviceId="jellyport-difftool", Version="1.0.0"`

const maxResponseBodyBytes = 32 << 20

var errCrossOriginRedirect = errors.New("cross-origin redirect rejected")

func readBoundedResponseBody(body io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxResponseBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d MiB", maxResponseBodyBytes>>20)
	}
	return contents, nil
}

// countBoundedResponseBody counts the response stream without retaining the
// body. It consumes at most limit+1 bytes from the reader, so a single eager
// Read cannot overshoot the boundary. The response is over the limit when it
// contains more than limit bytes; the count is capped at limit+1 and the
// remainder is left unread. The caller compares the counted total; the body
// itself is discarded so a large random payload never enters the buffered
// comparison path. The reader is not drained to EOF: a faulty or unbounded
// response must not stream indefinitely, and the caller closes the connection.
func countBoundedResponseBody(body io.Reader, limit int) (counted int, overLimit bool, err error) {
	if limit < 0 {
		limit = 0
	}
	cap := int64(limit + 1)
	buffer := make([]byte, 32*1024)
	for {
		remaining := cap - int64(counted)
		if remaining < int64(len(buffer)) {
			buffer = buffer[:remaining]
		}
		n, readErr := body.Read(buffer)
		counted += n
		if int64(counted) >= cap {
			// The response has at least limit+1 bytes: it exceeds the limit.
			// The boundary is exact because the cap prevents over-reading.
			return counted, true, readErr
		}
		if readErr == io.EOF {
			return counted, false, nil
		}
		if readErr != nil {
			return counted, false, readErr
		}
	}
}

type httpHost struct {
	baseURL        *url.URL
	client         *http.Client
	auth           AuthConfig
	token          string
	userID         string
	itemIDs        map[string]string
	destinationIDs map[string]string
	sourceIDs      map[string]string
}

func newHTTPHost(ctx context.Context, rawURL string, auth AuthConfig, client *http.Client) (*httpHost, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("base URL must be an absolute HTTP URL: %q", redactString(rawURL))
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("base URL must use http or https: %q", redactString(rawURL))
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// A caller can reuse its client across hosts. Keep its transport and other
	// settings, but scope redirect handling to this host's origin so replay
	// credentials never leave it.
	hostClient := *client
	hostClient.CheckRedirect = sameOriginRedirectPolicy(baseURL, client.CheckRedirect)
	host := &httpHost{
		baseURL:        baseURL,
		client:         &hostClient,
		auth:           auth,
		itemIDs:        map[string]string{},
		destinationIDs: map[string]string{},
		sourceIDs:      map[string]string{},
	}
	if auth.Username == "" {
		return host, nil
	}
	if auth.Role == "" {
		host.auth.Role = "default"
	}
	if err := host.bootstrap(ctx); err != nil {
		return nil, err
	}
	return host, nil
}

func sameOriginRedirectPolicy(origin *url.URL, nextPolicy func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(next *http.Request, via []*http.Request) error {
		if !sameHTTPOrigin(origin, next.URL) {
			return errCrossOriginRedirect
		}
		if nextPolicy != nil {
			return nextPolicy(next, via)
		}
		return nil
	}
}

func sameHTTPOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveHTTPPort(left) == effectiveHTTPPort(right)
}

func effectiveHTTPPort(rawURL *url.URL) string {
	if port := rawURL.Port(); port != "" {
		return port
	}
	switch strings.ToLower(rawURL.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func (h *httpHost) bootstrap(ctx context.Context) (resultErr error) {
	payload, err := json.Marshal(map[string]string{"Username": h.auth.Username, "Pw": h.auth.Password})
	if err != nil {
		return err
	}
	requestURL, err := h.resolve("/Users/AuthenticateByName", nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", mediaBrowserAuthorization)
	response, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("bootstrap authentication: %s", h.redactError(err))
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close bootstrap authentication response: %s", h.redactError(closeErr)))
		}
	}()
	body, err := readBoundedResponseBody(response.Body)
	if err != nil {
		return fmt.Errorf("read bootstrap authentication response: %s", h.redactError(err))
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("bootstrap authentication returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(h.redactString(string(redactResponseBody(body)))))
	}
	var token struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return fmt.Errorf("bootstrap authentication response lacks AccessToken")
	}
	h.token = token.AccessToken
	h.userID = token.User.ID
	return nil
}

// replayValues exposes credentials only to the in-memory scenario substitution
// map. Corpus files refer to symbolic names so credentials are supplied when a
// replay is invoked and can be redacted alongside captured response values.
func (h *httpHost) replayValues() map[string]string {
	values := make(map[string]string, 4)
	if h.auth.Username != "" {
		values["auth_username"] = h.auth.Username
		values["auth_password"] = h.auth.Password
	}
	if h.auth.DisabledUsername != "" {
		values["disabled_username"] = h.auth.DisabledUsername
		values["disabled_password"] = h.auth.DisabledPassword
	}
	return values
}

// redactionValues returns every runtime credential this host owns. The slice
// is used only while constructing diagnostics; it is never attached to a
// replay step, result, or artifact.
func (h *httpHost) redactionValues() []string {
	return mergeRedactionValues(
		secretRedactionValues(h.token, h.auth.Password, h.auth.DisabledPassword),
		redactionValues(h.auth.Username, h.auth.DisabledUsername),
	)
}

func (h *httpHost) redactString(value string) string {
	return redactCapturedString(redactString(value), h.redactionValues())
}

func (h *httpHost) redactError(err error) string {
	return h.redactString(err.Error())
}

func (h *httpHost) encodeRequestBody(value any) ([]byte, error) {
	if raw, ok := value.(string); ok && json.Valid([]byte(raw)) {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		value = decoded
	}
	return json.Marshal(value)
}

func (h *httpHost) Request(ctx context.Context, step Step, profile string) (result Response, resultErr error) {
	carrier, err := parseAuthCarrier(step.AuthCarrier)
	if err != nil {
		return Response{}, err
	}
	if !step.itemIDsRewritten {
		step, err = h.rewriteStep(step)
		if err != nil {
			return Response{}, fmt.Errorf("rewrite request: %s", h.redactError(err))
		}
	}
	if step.AuthRole != "" {
		if h.auth.Role != step.AuthRole || h.token == "" {
			return Response{}, fmt.Errorf("step requires auth role %q, but this host has no matching bootstrap credential", step.AuthRole)
		}
	}
	requestURL, err := h.resolve(step.Path, step.Params)
	if err != nil {
		return Response{}, fmt.Errorf("resolve request: %s", h.redactError(err))
	}
	var body io.Reader
	if step.Body != nil {
		encoded, err := h.encodeRequestBody(step.Body)
		if err != nil {
			return Response{}, fmt.Errorf("encode request body: %s", h.redactError(err))
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, step.Method, requestURL.String(), body)
	if err != nil {
		return Response{}, fmt.Errorf("build request: %s", h.redactError(err))
	}
	for name, value := range step.Headers {
		req.Header.Set(name, value)
	}
	if profile == "CamelCase" {
		req.Header.Set("Accept", `application/json; profile="CamelCase"`)
	} else if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if step.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if step.AuthRole != "" {
		applyAuthCarrier(req, carrier, h.token)
	}
	// When a step expects a 3xx status, copy the host client by value into a
	// request-local client that returns the initial redirect response. The
	// copy keeps the transport, timeout, cookie jar, and other configuration,
	// but disables redirect following. The host client stays unchanged and
	// still follows same-origin redirects for every other step.
	client := h.client
	if step.Expect != nil && step.Expect.Status != nil && *step.Expect.Status >= 300 && *step.Expect.Status <= 399 {
		nonFollowing := *h.client
		nonFollowing.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &nonFollowing
	}
	response, err := client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("perform request: %s", h.redactError(err))
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close response for step %q: %s", step.Name, h.redactError(closeErr)))
		}
	}()
	if step.Expect != nil && step.Expect.BodyLength != nil {
		counted, overLimit, err := countBoundedResponseBody(response.Body, *step.Expect.BodyLength)
		if err != nil {
			return Response{}, fmt.Errorf("count response for step %q: %s", step.Name, h.redactError(err))
		}
		return Response{Status: response.StatusCode, Headers: response.Header.Clone(), BodyLengthCounted: counted, BodyLengthOverLimit: overLimit, BodyLengthMode: true}, nil
	}
	bodyBytes, err := readBoundedResponseBody(response.Body)
	if err != nil {
		return Response{}, fmt.Errorf("read response for step %q: %s", step.Name, h.redactError(err))
	}
	return Response{Status: response.StatusCode, Headers: response.Header.Clone(), Body: bodyBytes}, nil
}

func applyAuthCarrier(req *http.Request, carrier AuthCarrier, token string) {
	switch carrier {
	case AuthCarrierAuthorization:
		req.Header.Set("Authorization", mediaBrowserAuthorization+`, Token="`+token+`"`)
	case AuthCarrierXEmbyAuthorization:
		req.Header.Set("X-Emby-Authorization", mediaBrowserAuthorization+`, Token="`+token+`"`)
	case AuthCarrierXEmbyToken:
		req.Header.Set("X-Emby-Token", token)
	case AuthCarrierXMediaBrowserToken:
		req.Header.Set("X-MediaBrowser-Token", token)
	case AuthCarrierQueryAPIKey:
		query := req.URL.Query()
		query.Set("ApiKey", token)
		req.URL.RawQuery = query.Encode()
	case AuthCarrierQueryAPIKeyLower:
		query := req.URL.Query()
		query.Set("api_key", token)
		req.URL.RawQuery = query.Encode()
	}
}

func (h *httpHost) resolve(rawPath string, params map[string]any) (*url.URL, error) {
	if err := validateStepPath(rawPath); err != nil {
		return nil, err
	}
	ref, err := url.Parse(rawPath)
	if err != nil {
		return nil, fmt.Errorf("parse request path %q: %s", h.redactString(rawPath), h.redactError(err))
	}
	resolved := h.baseURL.ResolveReference(ref)
	query := resolved.Query()
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch value := params[key].(type) {
		case []any:
			for _, entry := range value {
				query.Add(key, fmt.Sprint(entry))
			}
		case []string:
			for _, entry := range value {
				query.Add(key, entry)
			}
		default:
			query.Set(key, fmt.Sprint(value))
		}
	}
	resolved.RawQuery = query.Encode()
	return resolved, nil
}

func (h *httpHost) BuildItemMap(ctx context.Context) (map[string]string, error) {
	const pageSize = 10_000

	mapped := make(map[string]string)
	canonicalIDs := make(map[string]string)
	for startIndex := 0; ; {
		step := Step{
			Name:     "item-map",
			Method:   http.MethodGet,
			Path:     "/Items?Recursive=true&Fields=Path",
			Params:   map[string]any{"Limit": pageSize, "StartIndex": startIndex},
			AuthRole: h.auth.Role,
		}
		if h.token == "" {
			step.AuthRole = ""
		}
		response, err := h.Request(ctx, step, "")
		if err != nil {
			return nil, err
		}
		if response.Status < 200 || response.Status > 299 {
			return map[string]string{}, nil
		}
		var payload struct {
			Items []struct {
				ID   string `json:"Id"`
				Kind string `json:"Type"`
				Path string `json:"Path"`
			} `json:"Items"`
			TotalRecordCount int `json:"TotalRecordCount"`
		}
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return nil, fmt.Errorf("decode item map: %w", err)
		}
		for _, item := range payload.Items {
			if item.ID == "" || item.Kind == "" || item.Path == "" {
				continue
			}
			canonical := item.Kind + ":" + item.Path
			if previousID, exists := canonicalIDs[canonical]; exists && previousID != item.ID {
				return nil, fmt.Errorf("ambiguous item-ID map key %q has IDs %q and %q", canonical, previousID, item.ID)
			}
			canonicalIDs[canonical] = item.ID
			mapped[item.ID] = canonical
		}
		if startIndex+len(payload.Items) >= payload.TotalRecordCount {
			return mapped, nil
		}
		if len(payload.Items) == 0 {
			return nil, fmt.Errorf("item map pagination stalled at StartIndex %d before TotalRecordCount %d", startIndex, payload.TotalRecordCount)
		}
		nextStartIndex := startIndex + len(payload.Items)
		if nextStartIndex <= startIndex {
			return nil, fmt.Errorf("item map pagination overflow at StartIndex %d", startIndex)
		}
		startIndex = nextStartIndex
	}
}

func (h *httpHost) SetItemIDs(ids map[string]string) {
	h.itemIDs = make(map[string]string, len(ids))
	h.destinationIDs = make(map[string]string, len(ids))
	for destinationID, canonical := range ids {
		h.itemIDs[destinationID] = canonical
		h.destinationIDs[canonical] = destinationID
	}
}

// rewriteStep translates only literals authored in a corpus Step. Runtime
// values are injected by substituteStep afterwards and must retain their exact
// host-local bytes, even when they happen to contain a stock item ID.
func (h *httpHost) rewriteStep(step Step) (Step, error) {
	rewritten := step
	var err error
	if rewritten.Path, err = h.rewriteString(step.Path); err != nil {
		return Step{}, err
	}
	if step.Params != nil {
		rewritten.Params = make(map[string]any, len(step.Params))
		for key, value := range step.Params {
			if rewritten.Params[key], err = h.rewriteValue(value); err != nil {
				return Step{}, err
			}
		}
	}
	if step.Headers != nil {
		rewritten.Headers = make(map[string]string, len(step.Headers))
		for key, value := range step.Headers {
			if rewritten.Headers[key], err = h.rewriteString(value); err != nil {
				return Step{}, err
			}
		}
	}
	if rewritten.Body, err = h.rewriteValue(step.Body); err != nil {
		return Step{}, err
	}
	rewritten.itemIDsRewritten = true
	return rewritten, nil
}

func (h *httpHost) rewriteString(raw string) (string, error) {
	if strings.Contains(raw, "<user-id>") {
		if h.userID == "" {
			return "", fmt.Errorf("unresolved <user-id> placeholder: host has no bootstrapped user")
		}
		raw = strings.ReplaceAll(raw, "<user-id>", h.userID)
	}
	if len(h.sourceIDs) == 0 || len(h.destinationIDs) == 0 {
		return raw, nil
	}
	ids := make([]string, 0, len(h.sourceIDs))
	for id := range h.sourceIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
	for _, sourceID := range ids {
		canonical := h.sourceIDs[sourceID]
		if destinationID, ok := h.destinationIDs[canonical]; ok {
			raw = strings.ReplaceAll(raw, sourceID, destinationID)
		}
	}
	return raw, nil
}

func (h *httpHost) rewriteValue(value any) (any, error) {
	switch value := value.(type) {
	case string:
		return h.rewriteString(value)
	case map[string]any:
		rewritten := make(map[string]any, len(value))
		for key, entry := range value {
			mapped, err := h.rewriteValue(entry)
			if err != nil {
				return nil, err
			}
			rewritten[key] = mapped
		}
		return rewritten, nil
	case map[string]string:
		rewritten := make(map[string]string, len(value))
		for key, entry := range value {
			mapped, err := h.rewriteString(entry)
			if err != nil {
				return nil, err
			}
			rewritten[key] = mapped
		}
		return rewritten, nil
	case []any:
		rewritten := make([]any, len(value))
		for index, entry := range value {
			mapped, err := h.rewriteValue(entry)
			if err != nil {
				return nil, err
			}
			rewritten[index] = mapped
		}
		return rewritten, nil
	case []string:
		rewritten := make([]string, len(value))
		for index, entry := range value {
			mapped, err := h.rewriteString(entry)
			if err != nil {
				return nil, err
			}
			rewritten[index] = mapped
		}
		return rewritten, nil
	default:
		return value, nil
	}
}

func (h *httpHost) setSourceIDs(ids map[string]string) {
	h.sourceIDs = make(map[string]string, len(ids))
	for sourceID, canonical := range ids {
		h.sourceIDs[sourceID] = canonical
	}
}
