// SPDX-License-Identifier: GPL-2.0-only

package item

import "strings"

// CleanName returns the upstream-compatible searchable name value.
func CleanName(value string) string {
	if isWhitespaceOnly(value) {
		return value
	}

	value = invariantLower(removeDiacritics(value))

	var out strings.Builder
	out.Grow(len(value))
	pendingWhitespace := true
	for _, r := range value {
		if !isCleanNameCharacter(r) {
			r = ' '
		}
		if isUnicode16WhitespaceBMP(r) {
			if !pendingWhitespace {
				out.WriteByte(' ')
				pendingWhitespace = true
			}
			continue
		}
		out.WriteRune(r)
		pendingWhitespace = false
	}
	return strings.TrimSuffix(out.String(), " ")
}

func removeDiacritics(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for _, r := range value {
		if r != '\uFFFD' {
			out.WriteString(diacriticsReplacement(r))
		}
	}
	return out.String()
}

func invariantLower(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for _, r := range value {
		r = invariantLowerScalar(r)
		out.WriteRune(r)
	}
	return out.String()
}

func isCleanNameCharacter(r rune) bool {
	// .NET Regex category evaluation observes UTF-16 chars, so a
	// supplementary scalar is two non-letter/non-number surrogate chars.
	return r <= 0xFFFF && (isUnicode16LetterOrNumberBMP(r) || isUnicode16WhitespaceBMP(r))
}

func isWhitespaceOnly(value string) bool {
	if value == "" {
		return true
	}
	for _, r := range value {
		if r > 0xFFFF || !isUnicode16WhitespaceBMP(r) {
			return false
		}
	}
	return true
}
