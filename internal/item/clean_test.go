// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

// OracleTool generates testdata/normalization-oracle.json with the pinned
// Diacritics 4.1.8/.NET outputs. These cases deliberately do not reproduce
// expected normalized strings in Go: the JSON oracle is the acceptance source.
// Source pipeline: Jellyfin.Extensions StringExtensions.GetCleanValue
// (StringExtensions.cs:30-43,157-175).
func TestCleanNameOracle(t *testing.T) {
	oracle := loadSortOracle(t)
	seen := 0
	for _, c := range oracle.Cases {
		if c.Operation != "clean-name" || !c.Supported {
			continue
		}
		text := oracleText(t, c)
		var want string
		if err := json.Unmarshal(c.Output, &want); err != nil {
			t.Fatalf("oracle case %q output: %v", c.ID, err)
		}
		if got := CleanName(text); got != want {
			t.Errorf("oracle case %q: CleanName(%q) = %q, want oracle %q", c.ID, text, got, want)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("normalization oracle has no supported clean-name cases")
	}
}

// This guards the requested compatibility surface rather than choosing its
// own expected normalized outputs. The category labels are owned by the oracle
// vector manifest and make punctuation, whitespace, ligature, and decomposed
// input coverage explicit and reviewable.
func TestCleanNameOracleCoverage(t *testing.T) {
	oracle := loadSortOracle(t)
	categories := map[string]bool{
		"punctuation": false,
		"whitespace":  false,
		"ligature":    false,
		"decomposed":  false,
	}
	for _, c := range oracle.Cases {
		if c.Operation != "clean-name" {
			continue
		}
		for category := range categories {
			if strings.Contains(c.ID, category) {
				categories[category] = true
			}
		}
	}
	for category, found := range categories {
		if !found {
			t.Errorf("normalization oracle is missing clean-name %s coverage", category)
		}
	}
}

// These two inputs bind the corpus to the .NET UTF-16 regex and Unicode 16
// boundaries; deleting or substituting either would silently lose the defect.
func TestCleanNameOracleUnicodeRegressionVectors(t *testing.T) {
	oracle := loadSortOracle(t)
	expected := []struct {
		id   string
		text string
	}{
		{"clean-supplementary-deseret-removed-by-utf16-regex", "A𐐀B"},
		{"clean-unicode-16-cyrillic-tje", "AᲉB"},
	}
	inputs := make(map[string]string, len(expected))
	for _, c := range oracle.Cases {
		if c.Operation == "clean-name" {
			inputs[c.ID] = oracleText(t, c)
		}
	}
	for _, want := range expected {
		got, found := inputs[want.id]
		if !found {
			t.Errorf("normalization oracle is missing Unicode regression case %q", want.id)
			continue
		}
		if got != want.text {
			t.Errorf("normalization oracle case %q input = %q, want regression input %q", want.id, got, want.text)
		}
	}
}

// RemoveDiacritics is an independently observable upstream operation: clean-name
// tests must not conceal a mapping or malformed-UTF-16 compatibility regression.
func TestRemoveDiacriticsOracle(t *testing.T) {
	oracle := loadSortOracle(t)
	seen := 0
	for _, c := range oracle.Cases {
		if c.Operation != "remove-diacritics" || !c.Supported {
			continue
		}
		text := oracleText(t, c)
		var want string
		if err := json.Unmarshal(c.Output, &want); err != nil {
			t.Fatalf("oracle case %q output: %v", c.ID, err)
		}
		if got := removeDiacritics(text); got != want {
			t.Errorf("oracle case %q: removeDiacritics(%q) = %q, want oracle %q", c.ID, text, got, want)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("normalization oracle has no supported remove-diacritics cases")
	}
}

func oracleText(t *testing.T, c sortOracleCase) string {
	t.Helper()
	var input struct {
		Text  *string  `json:"text"`
		UTF16 []uint16 `json:"utf16"`
	}
	if err := json.Unmarshal(c.Input, &input); err != nil {
		t.Fatalf("oracle case %q input: %v", c.ID, err)
	}
	if input.Text == nil && input.UTF16 == nil {
		t.Fatalf("oracle case %q has neither text nor UTF-16 input", c.ID)
	}
	if input.Text != nil {
		return *input.Text
	}
	// utf16.Decode turns unpaired code units into U+FFFD, which the upstream
	// malformed-character regex removes before mapping.
	return string(utf16.Decode(input.UTF16))
}
