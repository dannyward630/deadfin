// SPDX-License-Identifier: GPL-2.0-only

package item

import "unicode/utf8"

// InvariantUpper applies the pinned .NET Rune.ToUpperInvariant mapping used by
// identity normalization and item normalization support code.
func InvariantUpper(value string) string {
	result := make([]rune, 0, len(value))
	for _, scalar := range value {
		result = append(result, invariantUpperScalar(scalar))
	}
	return string(result)
}

// IsInvariantLetterOrNumber reports the pinned .NET UTF-16 letter-or-number
// classification for a BMP scalar.
func IsInvariantLetterOrNumber(scalar rune) bool {
	return scalar >= 0 && scalar <= 0xffff && isUnicode16LetterOrNumberBMP(scalar)
}

// equalOrdinalIgnoreCase reports whether a and b are equal under
// StringComparer.OrdinalIgnoreCase. Inputs must be valid UTF-8 strings; this
// scalar seam deliberately excludes malformed UTF-16 inputs.
func equalOrdinalIgnoreCase(a, b string) bool {
	if a == b {
		return true
	}

	for len(a) != 0 && len(b) != 0 {
		aRune, aSize := utf8.DecodeRuneInString(a)
		bRune, bSize := utf8.DecodeRuneInString(b)
		if ordinalIgnoreCaseScalar(aRune) != ordinalIgnoreCaseScalar(bRune) {
			return false
		}
		a = a[aSize:]
		b = b[bSize:]
	}

	return len(a) == 0 && len(b) == 0
}
