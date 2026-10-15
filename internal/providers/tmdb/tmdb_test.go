// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// Source: tests/Jellyfin.Providers.Tests/Tmdb/TmdbUtilsTests.cs
// (NormalizeLanguage_Valid_Success, NormalizeLanguage_Invalid_Equal).
func TestNormalizeLanguage(t *testing.T) {
	for _, testCase := range []struct {
		input string
		want  string
	}{
		{"de", "de"},
		{"En", "En"},
		{"de-de", "de-DE"},
		{"en-US", "en-US"},
		{"de-CH", "de"},
		{"", ""},
	} {
		if got := normalizeLanguage(testCase.input, ""); got != testCase.want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestNormalizeLanguageLatinAmericanSpanish(t *testing.T) {
	if got := normalizeLanguage("es-419", "AR"); got != "es-AR" {
		t.Errorf("normalizeLanguage(es-419, AR) = %q, want es-AR", got)
	}
	if got := normalizeLanguage("es-419", "MX"); got != "es-MX" {
		t.Errorf("normalizeLanguage(es-419, MX) = %q, want es-MX", got)
	}
}

// Source: TmdbUtilsTests.cs AdjustImageLanguage_Valid_Success.
func TestAdjustImageLanguage(t *testing.T) {
	for _, testCase := range []struct {
		imageLanguage   string
		requestLanguage string
		want            string
	}{
		{"en", "en-US", "en-US"},
		{"fr-CA", "fr-BE", "fr-CA"},
		{"fr-CA", "fr", "fr-CA"},
		{"de", "en-US", "de"},
		{"", "en-US", ""},
		{"xx", "en-US", ""},
	} {
		if got := adjustImageLanguage(testCase.imageLanguage, testCase.requestLanguage); got != testCase.want {
			t.Errorf("adjustImageLanguage(%q, %q) = %q, want %q", testCase.imageLanguage, testCase.requestLanguage, got, testCase.want)
		}
	}
}

func TestImageLanguagesParam(t *testing.T) {
	for _, testCase := range []struct {
		language string
		country  string
		want     string
	}{
		{"en", "US", "en,null"},
		{"de", "DE", "de,null,en"},
		{"", "", "null,en"},
	} {
		if got := imageLanguagesParam(testCase.language, testCase.country); got != testCase.want {
			t.Errorf("imageLanguagesParam(%q, %q) = %q, want %q", testCase.language, testCase.country, got, testCase.want)
		}
	}
}

func TestCleanNameCollapsesSeparators(t *testing.T) {
	for _, testCase := range []struct {
		input string
		want  string
	}{
		{"The.Matrix.1999", "The Matrix 1999"},
		// Underscore and parentheses are separators; TmdbUtils.CleanName does not
		// trim, so a trailing separator yields a trailing space (parity-faithful).
		{"Amélie_(2001)", "Amélie 2001 "},
		{"WALL·E", "WALL·E"},
	} {
		if got := cleanName(testCase.input); got != testCase.want {
			t.Errorf("cleanName(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

// Source: TmdbUtils.BuildParentalRating.
func TestBuildParentalRating(t *testing.T) {
	for _, testCase := range []struct {
		country string
		rating  string
		want    string
	}{
		{"US", "PG-13", "PG-13"},
		{"GB", "15", "GB-15"},
		{"DE", "16", "FSK-16"},
	} {
		if got := buildParentalRating(testCase.country, testCase.rating); got != testCase.want {
			t.Errorf("buildParentalRating(%q, %q) = %q, want %q", testCase.country, testCase.rating, got, testCase.want)
		}
	}
}

// Source: TmdbUtils.MapCrewToPersonType.
func TestMapCrewToPersonType(t *testing.T) {
	for _, testCase := range []struct {
		department string
		job        string
		wantKind   item.PersonKind
		wantOK     bool
	}{
		{"Directing", "Director", item.PersonKindDirector, true},
		{"Production", "Producer", item.PersonKindProducer, true},
		{"Writing", "Screenplay", item.PersonKindWriter, true},
		{"Writing", "Novel", item.PersonKindWriter, true},
		{"Sound", "Composer", item.PersonKindUnknown, false},
		{"Camera", "Director of Photography", item.PersonKindUnknown, false},
	} {
		kind, ok := mapCrewToPersonType(testCase.department, testCase.job)
		if kind != testCase.wantKind || ok != testCase.wantOK {
			t.Errorf("mapCrewToPersonType(%q, %q) = %v, %t; want %v, %t", testCase.department, testCase.job, kind, ok, testCase.wantKind, testCase.wantOK)
		}
	}
}

func TestIsTrailerVideo(t *testing.T) {
	if !isTrailerVideo("YouTube", "Trailer") {
		t.Error("YouTube trailer should be a trailer")
	}
	if !isTrailerVideo("youtube", "teaser") {
		t.Error("YouTube teaser should be a trailer")
	}
	if isTrailerVideo("Vimeo", "Trailer") {
		t.Error("non-YouTube trailer should be rejected")
	}
	if isTrailerVideo("YouTube", "Featurette") {
		t.Error("YouTube featurette should be rejected")
	}
}
