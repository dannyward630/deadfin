// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	issueArtifactHeading = "# Differential replay failure\n"
	redactedValue        = "<redacted>"
)

var (
	unsafeFilename        = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	issueArtifactFilename = regexp.MustCompile(`^[0-9]+-.+\.md$`)
	artifactURLPattern    = regexp.MustCompile(`https?://[^[:space:]<>"']+`)
)

type issueDirectory struct {
	path string
	root *os.Root
}

func openIssueDirectory(directory string) (issues *issueDirectory, resultErr error) {
	parentPath, name := filepath.Dir(directory), filepath.Base(directory)
	if name == "." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("issue directory %q has no final directory component", directory)
	}
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return nil, err
	}

	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := parent.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close issue parent directory: %w", closeErr))
			if issues != nil {
				if issueCloseErr := issues.Close(); issueCloseErr != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("close issue directory: %w", issueCloseErr))
				}
				issues = nil
			}
		}
	}()

	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("issue directory %q is not a real directory", directory)
	}

	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, openedErr := root.Lstat(".")
	currentInfo, currentErr := parent.Lstat(name)
	if openedErr != nil || currentErr != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || !os.SameFile(openedInfo, currentInfo) {
		if openedErr != nil {
			return nil, errors.Join(openedErr, root.Close())
		}
		if currentErr != nil {
			return nil, errors.Join(currentErr, root.Close())
		}
		return nil, errors.Join(fmt.Errorf("issue directory %q changed while opening", directory), root.Close())
	}
	return &issueDirectory{path: directory, root: root}, nil
}

func (directory *issueDirectory) clear() error {
	entriesFile, err := directory.root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := entriesFile.ReadDir(-1)
	closeErr := entriesFile.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() || !issueArtifactFilename.MatchString(entry.Name()) {
			continue
		}
		contents, err := directory.readFile(entry.Name())
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(contents), issueArtifactHeading) {
			if err := directory.root.Remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (directory *issueDirectory) Close() error {
	return directory.root.Close()
}

func (directory *issueDirectory) readFile(name string) (contents []byte, resultErr error) {
	file, err := directory.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close issue artifact %q: %w", name, closeErr))
		}
	}()
	return io.ReadAll(file)
}

func writeIssue(directory string, ordinal int, scenario Scenario, step Step, leftRaw, rightRaw Response, leftNormalized, rightNormalized normalizedResponse, difference string, captures ...[]string) (issuePath string, resultErr error) {
	issues, err := openIssueDirectory(directory)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := issues.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close issue directory: %w", closeErr))
		}
	}()
	return issues.write(ordinal, scenario, step, leftRaw, rightRaw, leftNormalized, rightNormalized, difference, captures...)
}

func (directory *issueDirectory) write(ordinal int, scenario Scenario, step Step, leftRaw, rightRaw Response, leftNormalized, rightNormalized normalizedResponse, difference string, captures ...[]string) (issuePath string, resultErr error) {
	var captured []string
	if len(captures) != 0 {
		captured = captures[0]
	}
	name := fmt.Sprintf("%03d-%s-%s.md", ordinal, safeFilename(scenario.Name), safeFilename(step.Name))
	request := redactCapturedArtifactValue(redactArtifactValue(map[string]any{
		"method":       step.Method,
		"path":         step.Path,
		"params":       step.Params,
		"headers":      step.Headers,
		"body":         step.Body,
		"auth_role":    step.AuthRole,
		"auth_carrier": step.AuthCarrier,
		"capture":      step.Capture,
	}), captured)
	contents := issueArtifactHeading + "\n" +
		"## Scenario\n\n" + scenario.Name + " / " + step.Name + "\n\n" +
		"## Ledger row\n\n" + step.OperationID + "\n\n" +
		"## Request\n\n```json\n" + marshalPretty(request) + "\n```\n\n" +
		"## Difference\n\n" + redactCapturedString(redactString(difference), captured) + "\n\n" +
		"## Left raw response\n\n```http\n" + formatRawResponse(redactCapturedResponse(leftRaw, captured)) + "\n```\n\n" +
		"## Right raw response\n\n```http\n" + formatRawResponse(redactCapturedResponse(rightRaw, captured)) + "\n```\n\n" +
		"## Left normalized response\n\n```json\n" + marshalPretty(redactCapturedNormalizedResponse(leftNormalized, captured)) + "\n```\n\n" +
		"## Right normalized response\n\n```json\n" + marshalPretty(redactCapturedNormalizedResponse(rightNormalized, captured)) + "\n```\n"

	if err := directory.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := directory.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write([]byte(contents))
	closeErr := file.Close()
	if writeErr == nil && closeErr == nil {
		return filepath.Join(directory.path, name), nil
	}
	removeErr := directory.root.Remove(name)
	return "", errors.Join(
		issueArtifactOperationError("write", name, writeErr),
		issueArtifactOperationError("close", name, closeErr),
		issueArtifactOperationError("remove incomplete", name, removeErr),
	)
}

func issueArtifactOperationError(operation, name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s issue artifact %q: %w", operation, name, err)
}

func requestErrorDiff(left, right error) string {
	parts := []string{}
	if left != nil {
		parts = append(parts, "left request error: "+left.Error())
	}
	if right != nil {
		parts = append(parts, "right request error: "+right.Error())
	}
	return strings.Join(parts, "\n")
}

func redactArtifactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, entry := range typed {
			clean[key] = redactNamedValue(key, entry)
		}
		return clean
	case map[string]string:
		clean := make(map[string]string, len(typed))
		for key, entry := range typed {
			if isSensitiveName(key) {
				clean[key] = redactedValue
			} else {
				clean[key] = redactString(entry)
			}
		}
		return clean
	case map[string][]string:
		clean := make(map[string][]string, len(typed))
		for key, entries := range typed {
			clean[key] = redactNamedStrings(key, entries)
		}
		return clean
	case http.Header:
		return redactHTTPHeaders(typed)
	case []any:
		clean := make([]any, len(typed))
		for index, entry := range typed {
			clean[index] = redactArtifactValue(entry)
		}
		return clean
	case []string:
		clean := make([]string, len(typed))
		for index, entry := range typed {
			clean[index] = redactString(entry)
		}
		return clean
	case string:
		return redactString(typed)
	default:
		return value
	}
}

func redactNamedValue(name string, value any) any {
	if isSensitiveName(name) {
		return redactedValue
	}
	return redactArtifactValue(value)
}

func redactNamedStrings(name string, values []string) []string {
	clean := make([]string, len(values))
	for index, value := range values {
		if isSensitiveName(name) {
			clean[index] = redactedValue
		} else {
			clean[index] = redactString(value)
		}
	}
	return clean
}

func redactHTTPHeaders(headers http.Header) http.Header {
	clean := make(http.Header, len(headers))
	for name, values := range headers {
		clean[name] = redactNamedStrings(name, values)
	}
	return clean
}

func redactNormalizedResponse(response normalizedResponse) normalizedResponse {
	headers := make(map[string][]string, len(response.Headers))
	for name, values := range response.Headers {
		headers[name] = redactNamedStrings(name, values)
	}
	return normalizedResponse{
		Status:  response.Status,
		Headers: headers,
		Body:    redactArtifactValue(response.Body),
		IsJSON:  response.IsJSON,
	}
}

func isSensitiveName(name string) bool {
	if decoded, err := url.QueryUnescape(name); err == nil {
		name = decoded
	}
	compact := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(name))
	if strings.Contains(compact, "token") || strings.Contains(compact, "secret") || strings.Contains(compact, "password") || strings.Contains(compact, "credential") || strings.Contains(compact, "cookie") {
		return true
	}
	if strings.HasSuffix(compact, "apikey") || strings.HasSuffix(compact, "accesskey") || strings.HasSuffix(compact, "privatekey") || strings.HasSuffix(compact, "clientkey") {
		return true
	}
	return strings.Contains(compact, "authorization") || compact == "key" || compact == "pw"
}

func redactString(value string) string {
	if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//")) {
		return redactURL(parsed)
	}
	return artifactURLPattern.ReplaceAllStringFunc(value, func(rawURL string) string {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return redactMalformedURL(rawURL)
		}
		return redactURL(parsed)
	})
}

func redactURL(parsed *url.URL) string {
	redacted := *parsed
	if redacted.User != nil {
		if _, hasPassword := redacted.User.Password(); hasPassword {
			redacted.User = url.UserPassword(redactedValue, redactedValue)
		} else {
			redacted.User = url.User(redactedValue)
		}
	}
	redacted.RawQuery = redactURLParameters(redacted.RawQuery)
	redacted.Fragment = redactURLParameters(redacted.Fragment)
	redacted.RawFragment = ""
	return redacted.String()
}

func redactURLParameters(raw string) string {
	if raw == "" {
		return raw
	}

	var redacted strings.Builder
	remaining := raw
	for {
		separatorIndex := strings.IndexAny(remaining, "&;")
		parameter := remaining
		if separatorIndex >= 0 {
			parameter = remaining[:separatorIndex]
		}

		name, _, hasValue := strings.Cut(parameter, "=")
		switch {
		case !isSensitiveName(name):
			redacted.WriteString(parameter)
		case hasValue:
			redacted.WriteString(name)
			redacted.WriteByte('=')
			redacted.WriteString(url.QueryEscape(redactedValue))
		default:
			redacted.WriteString(url.QueryEscape(redactedValue))
		}

		if separatorIndex < 0 {
			break
		}
		redacted.WriteByte(remaining[separatorIndex])
		remaining = remaining[separatorIndex+1:]
	}
	return redacted.String()
}

func redactMalformedURL(rawURL string) string {
	beforeFragment, fragment, hasFragment := strings.Cut(rawURL, "#")
	beforeQuery, query, hasQuery := strings.Cut(beforeFragment, "?")

	if schemeEnd := strings.Index(beforeQuery, "://"); schemeEnd >= 0 {
		authorityStart := schemeEnd + len("://")
		authorityEnd := strings.IndexByte(beforeQuery[authorityStart:], '/')
		if authorityEnd < 0 {
			authorityEnd = len(beforeQuery)
		} else {
			authorityEnd += authorityStart
		}
		if userinfoEnd := strings.LastIndex(beforeQuery[authorityStart:authorityEnd], "@"); userinfoEnd >= 0 {
			userinfoEnd += authorityStart
			beforeQuery = beforeQuery[:authorityStart] + redactedValue + beforeQuery[userinfoEnd:]
		}
	}

	if hasQuery {
		beforeQuery += "?" + redactURLParameters(query)
	}
	if hasFragment {
		beforeQuery += "#" + redactURLParameters(fragment)
	}
	return beforeQuery
}

func formatRawResponse(response Response) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "HTTP %d\n", response.Status)
	builder.WriteString(formatHeaders(redactHTTPHeaders(response.Headers)))
	builder.WriteByte('\n')
	if response.BodyLengthMode {
		_, _ = fmt.Fprintf(&builder, "<body length counted=%d over_limit=%t>\n", response.BodyLengthCounted, response.BodyLengthOverLimit)
		return builder.String()
	}
	builder.Write(redactResponseBody(response.Body))
	return builder.String()
}

func redactResponseBody(body []byte) []byte {
	var payload any
	if err := json.Unmarshal(body, &payload); err == nil {
		if redacted, err := json.Marshal(redactArtifactValue(payload)); err == nil {
			return redacted
		}
	}
	return []byte(redactString(string(body)))
}

func formatHeaders(headers http.Header) string {
	keys := make([]string, 0, len(headers))
	for name := range headers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, name := range keys {
		values := append([]string(nil), headers[name]...)
		sort.Strings(values)
		for _, value := range values {
			builder.WriteString(name)
			builder.WriteString(": ")
			builder.WriteString(value)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func marshalPretty(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "<unable to marshal>"
	}
	return string(encoded)
}

func safeFilename(value string) string {
	value = unsafeFilename.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return "unnamed"
	}
	return value
}
