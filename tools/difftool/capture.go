// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var captureVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CamelCase profiles re-case DTO properties, not keys held by these response
// dictionaries. Keep their descendants exact during capture traversal.
var captureDictionaryProperties = map[string]struct{}{
	"CustomPrefs":         {},
	"ImageBlurHashes":     {},
	"ImageTags":           {},
	"ProviderIds":         {},
	"RequiredHttpHeaders": {},
	"Trickplay":           {},
	"customPrefs":         {},
	"imageBlurHashes":     {},
	"imageTags":           {},
	"providerIds":         {},
	"requiredHttpHeaders": {},
	"trickplay":           {},
}

func validateCapture(captures map[string]string) error {
	for _, name := range sortedCaptureNames(captures) {
		if !captureVariableName.MatchString(name) {
			return fmt.Errorf("invalid capture variable %q", name)
		}
		if _, err := parseCapturePath(captures[name]); err != nil {
			return fmt.Errorf("capture %q: %w", name, err)
		}
	}
	return nil
}

func sortedCaptureNames(captures map[string]string) []string {
	names := make([]string, 0, len(captures))
	for name := range captures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseCapturePath(path string) ([]string, error) {
	segments := strings.Split(path, ".")
	for _, segment := range segments {
		if segment == "" || strings.ContainsAny(segment, "[]") {
			return nil, fmt.Errorf("invalid JSON object path %q", path)
		}
	}
	return segments, nil
}

// extractCapturePath traverses a decoded JSON payload following a dotted path
// whose segments are object keys or canonical array indices. It reuses the
// dictionary-aware casing rules shared by capture and assertion traversal so
// the two paths cannot diverge. It returns the terminal node, or an error
// describing the first segment that could not be resolved.
func extractCapturePath(name, path string, payload any) (any, error) {
	segments, err := parseCapturePath(path)
	if err != nil {
		return nil, fmt.Errorf("capture %q at %q: invalid path", name, path)
	}
	value := payload
	insideDictionary := false
	for _, segment := range segments {
		switch node := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = captureObjectValue(node, segment, !insideDictionary)
			if !ok {
				return nil, fmt.Errorf("capture %q at %q: path is absent", name, path)
			}
			if _, ok := captureDictionaryProperties[segment]; ok {
				insideDictionary = true
			}
		case []any:
			index, err := captureArrayIndex(segment, len(node))
			if err != nil {
				return nil, fmt.Errorf("capture %q at %q: %w", name, path, err)
			}
			value = node[index]
		default:
			return nil, fmt.Errorf("capture %q at %q: cannot traverse scalar at segment %q", name, path, segment)
		}
	}
	return value, nil
}

func captureResponse(step Step, response Response, values map[string]string) error {
	if len(step.Capture) == 0 {
		return nil
	}
	names := sortedCaptureNames(step.Capture)
	if !json.Valid(response.Body) {
		return fmt.Errorf("capture %q at %q: response is not JSON", names[0], step.Capture[names[0]])
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("capture %q at %q: response is not JSON", names[0], step.Capture[names[0]])
	}
	for _, name := range names {
		value, err := extractCapturePath(name, step.Capture[name], payload)
		if err != nil {
			return err
		}
		scalar, ok := captureScalar(value)
		if !ok {
			return fmt.Errorf("capture %q at %q: value is not a scalar", name, step.Capture[name])
		}
		values[name] = scalar
	}
	return nil
}

// captureArrayIndex accepts only canonical non-negative decimal indices. The
// traversal context determines whether a numeric segment is an array index or
// an object key, so map keys retain their exact string semantics.
func captureArrayIndex(segment string, length int) (int, error) {
	if segment == "0" {
		if length == 0 {
			return 0, fmt.Errorf("array index %q is out of range", segment)
		}
		return 0, nil
	}
	if segment == "" || segment[0] < '1' || segment[0] > '9' {
		return 0, fmt.Errorf("array index %q is invalid", segment)
	}
	for index := 1; index < len(segment); index++ {
		if segment[index] < '0' || segment[index] > '9' {
			return 0, fmt.Errorf("array index %q is invalid", segment)
		}
	}
	if length == 0 {
		return 0, fmt.Errorf("array index %q is out of range", segment)
	}

	index := 0
	limit := length - 1
	for _, digit := range []byte(segment) {
		value := int(digit - '0')
		if index > limit/10 || (index == limit/10 && value > limit%10) {
			return 0, fmt.Errorf("array index %q is out of range", segment)
		}
		index = index*10 + value
	}
	return index, nil
}

// captureObjectValue follows profile casing for DTO property names while
// retaining exact JSON object keys, including dictionary entries.
func captureObjectValue(object map[string]any, segment string, allowProfileCasing bool) (any, bool) {
	if value, ok := object[segment]; ok {
		return value, true
	}
	if !allowProfileCasing {
		return nil, false
	}
	value, ok := object[captureCamelCase(segment)]
	return value, ok
}

// captureCamelCase mirrors System.Text.Json's JsonNamingPolicy.CamelCase.
func captureCamelCase(segment string) string {
	runes := []rune(segment)
	for index := 0; index < len(runes) && (index == 0 || unicode.IsUpper(runes[index])); index++ {
		hasNext := index+1 < len(runes)
		if index > 0 && hasNext && !unicode.IsUpper(runes[index+1]) {
			break
		}
		runes[index] = unicode.ToLower(runes[index])
	}
	return string(runes)
}

func captureScalar(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	case bool:
		return fmt.Sprintf("%t", value), true
	default:
		return "", false
	}
}

func substituteStep(step Step, values map[string]string) (Step, error) {
	substituted := step
	var err error
	if substituted.Path, err = substituteString(step.Path, values); err != nil {
		return Step{}, err
	}
	if step.Params != nil {
		substituted.Params = make(map[string]any, len(step.Params))
		for name, value := range step.Params {
			if substituted.Params[name], err = substituteValue(value, values); err != nil {
				return Step{}, err
			}
		}
	}
	if substituted.Headers != nil {
		substituted.Headers = make(map[string]string, len(step.Headers))
		for name, value := range step.Headers {
			if substituted.Headers[name], err = substituteString(value, values); err != nil {
				return Step{}, err
			}
		}
	}
	if substituted.Body, err = substituteBody(step.Body, values); err != nil {
		return Step{}, err
	}
	return substituted, nil
}

func substituteBody(body any, values map[string]string) (any, error) {
	raw, ok := body.(string)
	if !ok || !json.Valid([]byte(raw)) {
		return substituteValue(body, values)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return substituteValue(decoded, values)
}

func substituteValue(value any, values map[string]string) (any, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case string:
		return substituteString(value, values)
	case map[string]any:
		substituted := make(map[string]any, len(value))
		for key, entry := range value {
			mapped, err := substituteValue(entry, values)
			if err != nil {
				return nil, err
			}
			substituted[key] = mapped
		}
		return substituted, nil
	case map[string]string:
		substituted := make(map[string]string, len(value))
		for key, entry := range value {
			mapped, err := substituteString(entry, values)
			if err != nil {
				return nil, err
			}
			substituted[key] = mapped
		}
		return substituted, nil
	case []any:
		substituted := make([]any, len(value))
		for index, entry := range value {
			mapped, err := substituteValue(entry, values)
			if err != nil {
				return nil, err
			}
			substituted[index] = mapped
		}
		return substituted, nil
	case []string:
		substituted := make([]string, len(value))
		for index, entry := range value {
			mapped, err := substituteString(entry, values)
			if err != nil {
				return nil, err
			}
			substituted[index] = mapped
		}
		return substituted, nil
	default:
		return value, nil
	}
}

func substituteString(value string, values map[string]string) (string, error) {
	var substituted strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			substituted.WriteString(value)
			return substituted.String(), nil
		}
		substituted.WriteString(value[:start])
		value = value[start+2:]
		end := strings.IndexByte(value, '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated capture variable")
		}
		name := value[:end]
		if !captureVariableName.MatchString(name) {
			return "", fmt.Errorf("invalid capture variable %q", name)
		}
		captured, ok := values[name]
		if !ok {
			return "", fmt.Errorf("unknown capture variable %q", name)
		}
		substituted.WriteString(captured)
		value = value[end+1:]
	}
}

// redactionValues filters incidental capture candidates. A capture can be an
// ordinary ID, boolean, or small numeric field, so very short values are not
// made global substitutions.
func redactionValues(values ...string) []string {
	return collectRedactionValues(4, values...)
}

// secretRedactionValues retains every nonempty configured credential or
// bootstrap token. Unlike a dynamic capture, a one-character password is
// still a secret and must never reach an artifact or error.
func secretRedactionValues(values ...string) []string {
	return collectRedactionValues(1, values...)
}

func collectRedactionValues(minimumLength int, values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(strings.TrimSpace(value)) < minimumLength {
			continue
		}
		seen[value] = struct{}{}
	}
	redacted := make([]string, 0, len(seen))
	for value := range seen {
		redacted = append(redacted, value)
	}
	sort.Slice(redacted, func(i, j int) bool { return len(redacted[i]) > len(redacted[j]) })
	return redacted
}

// Inputs have already been classified as known secrets or nontrivial capture
// values. Preserve the former while deduplicating the combined issue list.
func mergeRedactionValues(groups ...[]string) []string {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	values := make([]string, 0, count)
	for _, group := range groups {
		values = append(values, group...)
	}
	return secretRedactionValues(values...)
}

func captureValues(captures ...map[string]string) []string {
	incidental := make([]string, 0)
	knownSecrets := make([]string, 0)
	for _, variables := range captures {
		for name, value := range variables {
			if isSensitiveName(name) {
				knownSecrets = append(knownSecrets, value)
			} else {
				incidental = append(incidental, value)
			}
		}
	}
	return mergeRedactionValues(secretRedactionValues(knownSecrets...), redactionValues(incidental...))
}

func redactCapturedString(value string, captured []string) string {
	for _, secret := range captured {
		value = strings.ReplaceAll(value, secret, redactedValue)
	}
	return value
}

func redactCapturedArtifactValue(value any, captured []string) any {
	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for key, entry := range value {
			redacted[key] = redactCapturedArtifactValue(entry, captured)
		}
		return redacted
	case map[string]string:
		redacted := make(map[string]string, len(value))
		for key, entry := range value {
			redacted[key] = redactCapturedString(entry, captured)
		}
		return redacted
	case map[string][]string:
		redacted := make(map[string][]string, len(value))
		for key, entries := range value {
			redacted[key] = make([]string, len(entries))
			for index, entry := range entries {
				redacted[key][index] = redactCapturedString(entry, captured)
			}
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for index, entry := range value {
			redacted[index] = redactCapturedArtifactValue(entry, captured)
		}
		return redacted
	case []string:
		redacted := make([]string, len(value))
		for index, entry := range value {
			redacted[index] = redactCapturedString(entry, captured)
		}
		return redacted
	case string:
		return redactCapturedString(value, captured)
	default:
		return value
	}
}

func redactCapturedResponse(response Response, captured []string) Response {
	redacted := Response{Status: response.Status, Headers: response.Headers.Clone()}
	for _, values := range redacted.Headers {
		for index, value := range values {
			values[index] = redactCapturedString(value, captured)
		}
	}
	var payload any
	if err := json.Unmarshal(response.Body, &payload); err == nil {
		if body, err := json.Marshal(redactCapturedArtifactValue(redactArtifactValue(payload), captured)); err == nil {
			redacted.Body = body
			return redacted
		}
	}
	redacted.Body = []byte(redactCapturedString(redactString(string(response.Body)), captured))
	return redacted
}

func redactCapturedNormalizedResponse(response normalizedResponse, captured []string) normalizedResponse {
	redacted := redactNormalizedResponse(response)
	redacted.Headers = redactCapturedArtifactValue(redacted.Headers, captured).(map[string][]string)
	redacted.Body = redactCapturedArtifactValue(redacted.Body, captured)
	return redacted
}
