// SPDX-License-Identifier: GPL-2.0-only

package render

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Casing selects the negotiated JSON property casing.
type Casing int

const (
	CasingPascal Casing = iota // default — upstream's PropertyNamingPolicy is null for the default formatter
	CasingCamel
)

// NegotiateCasing selects a casing from one Accept field value.
// It serves callers that already have one field value.
// HTTP requests use NegotiateRequest so every repeated Accept field is considered.
func NegotiateCasing(accept string) Casing {
	return negotiateCasingValues([]string{accept})
}

// NegotiateRequest selects the response casing from every Accept field-value.
func NegotiateRequest(r *http.Request) Casing {
	return negotiateCasingValues(r.Header.Values("Accept"))
}

// negotiateCasingValues mirrors the pinned ASP.NET formatter pipeline:
// candidates are ordered by descending q-value, with field and entry order
// breaking ties. Media parameters before q must match the formatter; parameters
// after q are accept extensions and do not participate in matching.
func negotiateCasingValues(acceptValues []string) Casing {
	var best struct {
		casing  Casing
		quality float64
		set     bool
	}

	for _, accept := range acceptValues {
		for _, entry := range splitMediaRanges(accept) {
			casing, quality, ok := casingCandidate(entry)
			if !ok || (best.set && quality <= best.quality) {
				continue
			}
			best.casing = casing
			best.quality = quality
			best.set = true
		}
	}

	if best.set {
		return best.casing
	}
	return CasingPascal
}

// casingCandidate recognizes the three media ranges that can select Jellyfin's
// JSON formatters. A non-matching range or malformed parameter is deliberately
// skipped, letting the next Accept entry participate in selection.
func casingCandidate(entry string) (Casing, float64, bool) {
	parts := splitMediaParameters(entry)
	if len(parts) == 0 {
		return CasingPascal, 0, false
	}

	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "application/json", "application/*", "*/*":
	default:
		return CasingPascal, 0, false
	}

	casing := CasingPascal
	quality := 1.0
	hasQuality := false
	hasProfile := false
	for _, part := range parts[1:] {
		key, value, ok := headerParameter(part)
		if !ok {
			return CasingPascal, 0, false
		}

		if strings.EqualFold(key, "q") {
			parsed, ok := parseQuality(value)
			if !ok {
				return CasingPascal, 0, false
			}
			quality = parsed
			hasQuality = true
			continue
		}
		if hasQuality {
			continue
		}

		if !strings.EqualFold(key, "profile") {
			return CasingPascal, 0, false
		}

		var profile Casing
		switch {
		case strings.EqualFold(value, "CamelCase"):
			profile = CasingCamel
		case strings.EqualFold(value, "PascalCase"):
			profile = CasingPascal
		default:
			return CasingPascal, 0, false
		}
		if hasProfile && casing != profile {
			return CasingPascal, 0, false
		}
		casing = profile
		hasProfile = true
	}

	return casing, quality, true
}

// parseQuality implements the stock media type parser's observed q-value
// behavior. Decimal q-values contain digits with at most one decimal point;
// signs, exponents, NaN, and infinity are not decimal q-values. The parser
// nevertheless accepts its special positive Infinity token case-insensitively,
// with an optional leading plus. A grammar-valid decimal outside float64 range
// follows .NET Double.Parse: overflow selects as +Infinity and underflow is 0.
func parseQuality(value string) (float64, bool) {
	if strings.EqualFold(value, "Infinity") || strings.EqualFold(value, "+Infinity") {
		return math.Inf(1), true
	}

	digits := false
	decimalPoint := false
	for i := range len(value) {
		switch value[i] {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			digits = true
		case '.':
			if decimalPoint {
				return 0, false
			}
			decimalPoint = true
		default:
			return 0, false
		}
	}
	if !digits {
		return 0, false
	}
	quality, _ := strconv.ParseFloat(value, 64)
	return quality, true
}

// headerParameter validates a media-type parameter even after q, because a
// malformed accept extension makes the entire stock media range unavailable.
func headerParameter(part string) (string, string, bool) {
	key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !ok || !isToken(key) || value == "" {
		return "", "", false
	}
	if value[0] == '"' {
		unquoted, ok := quotedString(value)
		return key, unquoted, ok
	}
	if !isToken(value) {
		return "", "", false
	}
	return key, value, true
}

// splitMediaRanges preserves valid quoted commas. If its final range contains
// an unterminated quoted string, stock ignores that malformed range while still
// considering its comma-delimited neighbors. Preserve already balanced ranges
// and recover only the malformed tail.
func splitMediaRanges(value string) []string {
	ranges, balanced := splitHeaderValues(value, ',')
	if balanced {
		return ranges
	}
	last := len(ranges) - 1
	return append(ranges[:last], strings.Split(ranges[last], ",")...)
}

// splitMediaParameters preserves valid quoted semicolons. A malformed quoted
// parameter is split for recovery and then rejected by headerParameter.
func splitMediaParameters(value string) []string {
	parameters, balanced := splitHeaderValues(value, ';')
	if balanced {
		return parameters
	}
	return strings.Split(value, ";")
}

func splitHeaderValues(value string, delimiter byte) ([]string, bool) {
	values := make([]string, 0, 1)
	start := 0
	quoted := false
	escaped := false
	for i := range len(value) {
		char := value[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if char == delimiter && !quoted {
			values = append(values, value[start:i])
			start = i + 1
		}
	}
	return append(values, value[start:]), !quoted && !escaped
}

func isToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			!strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char)) {
			return false
		}
	}
	return true
}

func quotedString(value string) (string, bool) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	for i := 1; i < len(value)-1; i++ {
		char := value[i]
		if char == '\\' {
			i++
			if i >= len(value)-1 || value[i] < ' ' || value[i] > '~' {
				return "", false
			}
			continue
		}
		if char == '"' || char == '\r' || char == '\n' || char < '\t' || char > '~' {
			return "", false
		}
	}
	return value[1 : len(value)-1], true
}

// JSON writes v with negotiated casing and upstream's content type.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) error {
	casing := NegotiateRequest(r)
	var (
		body        []byte
		contentType = "application/json; charset=utf-8"
		err         error
	)
	if casing == CasingCamel {
		body, err = MarshalCamel(v)
		contentType = `application/json; profile="CamelCase"; charset=utf-8`
	} else {
		body, err = json.Marshal(v)
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}
