// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/deadfin-org/deadfin/internal/item"
)

// FoldTitle returns the search key for an anime title. Two spellings of one
// title fold to the same key, so the key supports exact identification and
// substring search:
//
//   - NFKC maps full-width Latin and half-width katakana to their normal forms.
//   - item.CleanName applies the pinned diacritic removal (ō → o), invariant
//     lowercase, and punctuation removal that the clean_* columns use.
//   - Katakana folds to hiragana, so either kana spelling matches.
//   - Whitespace is removed, so word splits do not matter
//     ("Shingeki no Kyojin" contains "shingekino").
//   - Romanized long vowels collapse: "ou", "oo", and "uu" fold to one vowel,
//     as do doubled "a", "i", and "e" ("Shounen" and "Shōnen" both fold to
//     "shonen").
//
// The fold is lossy on purpose. It is applied the same way to stored titles
// and to search terms, so a lossy step only widens a match.
func FoldTitle(value string) string {
	cleaned := item.CleanName(norm.NFKC.String(value))
	var out strings.Builder
	out.Grow(len(cleaned))
	var previous rune
	for _, r := range cleaned {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= 'ァ' && r <= 'ヶ' {
			r -= 0x60
		}
		if longVowel(previous, r) {
			continue
		}
		out.WriteRune(r)
		previous = r
	}
	return out.String()
}

// longVowel reports whether current extends a romanized long vowel that ends
// in previous.
func longVowel(previous, current rune) bool {
	switch previous {
	case 'o':
		return current == 'o' || current == 'u'
	case 'u', 'a', 'i', 'e':
		return current == previous
	default:
		return false
	}
}
