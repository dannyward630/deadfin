// SPDX-License-Identifier: GPL-2.0-only

// Package path provides virtual-path expansion for item and image paths.
//
// Stock stores portable placeholders in the database and expands them before a
// path leaves the store boundary. Deadfin keeps the raw tokens in SQL and in
// the item model. It expands the tokens before API responses and file
// operations.
package path

import (
	"strings"
	"unicode/utf8"
)

// appDataPathToken is the storage-layer placeholder for the application data
// root. Stock expands this token first.
const appDataPathToken = "%AppDataPath%"

// metadataPathToken is the storage-layer placeholder for the metadata root.
// Stock expands this token after the application data root.
const metadataPathToken = "%MetadataPath%"

// VirtualPathExpander replaces virtual path placeholders with configured
// physical roots. The zero value expands nothing because both replacement
// roots are empty. Construct one with NewVirtualPathExpander from the
// composition root.
type VirtualPathExpander struct {
	data     string
	metadata string
}

// NewVirtualPathExpander constructs an expander that replaces %AppDataPath%
// with dataPath and %MetadataPath% with metadataPath. The replacements apply
// in that order, case-insensitively, to every occurrence. Pass the exact
// values from config.Paths so the expander carries no package-level state.
func NewVirtualPathExpander(dataPath, metadataPath string) VirtualPathExpander {
	return VirtualPathExpander{data: dataPath, metadata: metadataPath}
}

// Expand replaces every virtual path placeholder with its configured physical
// root. It applies %AppDataPath% first and %MetadataPath% second. This order
// matches upstream ApplicationHost. The match is case-insensitive for the token
// name. The zero value expands nothing, so callers that do not construct an
// expander leave paths unchanged. An empty input returns an empty string. A
// path with no token returns byte-for-byte unchanged. Literal percent
// sequences that do not form an exact token remain unchanged.
func (e VirtualPathExpander) Expand(value string) string {
	if value == "" {
		return ""
	}
	result := value
	result = replaceIgnoreCase(result, appDataPathToken, e.data)
	result = replaceIgnoreCase(result, metadataPathToken, e.metadata)
	return result
}

// ExpandPtr expands a pointer path. A nil pointer returns nil so the API can
// keep its nullable semantics. An empty-string pointer returns an empty
// string. This mirrors Expand for the pointer fields the DTO uses.
func (e VirtualPathExpander) ExpandPtr(value *string) *string {
	if value == nil {
		return nil
	}
	expanded := e.Expand(*value)
	return &expanded
}

// replaceIgnoreCase replaces every occurrence of the token with the
// replacement. It matches the token case-insensitively. When the replacement
// is empty, no substitution occurs. The token must be a non-empty literal.
// The scan decodes the input one rune at a time with utf8.DecodeRuneInString
// so that invalid UTF-8 bytes copy through unchanged. Unicode case folding
// cannot shift a byte index because the scan never lowercases the input
// into a separate copy.
func replaceIgnoreCase(input, token, replacement string) string {
	if token == "" || replacement == "" {
		return input
	}
	lowerToken := strings.ToLower(token)
	tokenBytes := []byte(lowerToken)
	tokenLen := len(tokenBytes)
	var result strings.Builder
	result.Grow(len(input) + len(replacement))
	i := 0
	for i < len(input) {
		if i+tokenLen <= len(input) && equalFoldASCII([]byte(input[i:i+tokenLen]), tokenBytes) {
			result.WriteString(replacement)
			i += tokenLen
			continue
		}
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size == 1 {
			result.WriteByte(input[i])
			i++
		} else {
			result.WriteRune(r)
			i += size
		}
	}
	return result.String()
}

// equalFoldASCII reports whether a lowercases to b byte-for-byte. The caller
// lowercases b first. Only ASCII letters fold; all other bytes compare
// directly so that invalid UTF-8 never matches a token.
func equalFoldASCII(a, lowerB []byte) bool {
	for i := range a {
		ca := a[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if ca != lowerB[i] {
			return false
		}
	}
	return true
}
