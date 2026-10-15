// SPDX-License-Identifier: GPL-2.0-only

package localization_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/localization"
)

const (
	countriesPayloadSHA256 = "c8e7173d541078eba07369fd9d98701b15336ad77d35e398acb28b11416a581f"
	optionsPayloadSHA256   = "8fe0befe8ebbafbd5d35d16d139f045630729d4422e6f23cf4390ca7054d9204"
	usRatingsSHA256        = "c29807c80ab095172015027e6f54bb414bca7e82b612aa432fce7ef714dd146d"
	deRatingsSHA256        = "7b61bc2f416e3083d030f910110fe55108f46a064fc11aae6bd0ac8c54229876"
)

func TestServiceCountriesPreservesPinnedPayloadOrderAndFields(t *testing.T) {
	service := newService(t)
	countries := service.Countries()

	if got, want := len(countries), 140; got != want {
		t.Fatalf("Countries count = %d, want all %d pinned countries", got, want)
	}
	if got, want := countries[0], (dto.CountryInfo{
		Name: "AF", DisplayName: "Afghanistan", TwoLetterISORegionName: "AF", ThreeLetterISORegionName: "AFG",
	}); got != want {
		t.Errorf("first country = %#v, want %#v", got, want)
	}
	if got, want := countries[len(countries)-1], (dto.CountryInfo{
		Name: "ZW", DisplayName: "Zimbabwe", TwoLetterISORegionName: "ZW", ThreeLetterISORegionName: "ZWE",
	}); got != want {
		t.Errorf("last country = %#v, want %#v", got, want)
	}
	for _, tc := range []struct {
		name, display string
	}{
		{name: "029", display: "Caribbean"},
		{name: "419", display: "Latin America"},
		{name: "DE", display: "Germany"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			country, ok := countryByName(countries, tc.name)
			if !ok {
				t.Fatalf("country %q missing from pinned payload", tc.name)
			}
			if country.DisplayName != tc.display {
				t.Errorf("country %q display = %q, want %q", tc.name, country.DisplayName, tc.display)
			}
		})
	}
	assertDigest(t, countries, countriesPayloadSHA256, "Countries canonical DTO payload")

	countries[0].Name = "MUTATED"
	if fresh := service.Countries(); fresh[0].Name != "AF" {
		t.Errorf("mutating one Countries result changed a later result: got first name %q", fresh[0].Name)
	}
}

func TestServiceCulturesParsesEveryISORecordAndClonesNestedValues(t *testing.T) {
	service := newService(t)
	cultures := service.Cultures()

	if got, want := len(cultures), 496; got != want {
		t.Fatalf("Cultures count = %d, want all %d ISO 639-2 source rows", got, want)
	}
	if got, want := cultures[0], (dto.CultureDto{
		Name: "Afar", DisplayName: "Afar", TwoLetterISOLanguageName: "aa", ThreeLetterISOLanguageName: stringPtr("aar"), ThreeLetterISOLanguageNames: []string{"aar"},
	}); !equalCulture(got, want) {
		t.Errorf("first culture = %#v, want %#v", got, want)
	}
	german, ok := cultureByTwoLetter(cultures, "de")
	if !ok {
		t.Fatal("German ISO record missing")
	}
	if got, want := german.Name, "German"; got != want {
		t.Errorf("German Name = %q, want %q", got, want)
	}
	if german.ThreeLetterISOLanguageName == nil || *german.ThreeLetterISOLanguageName != "deu" || !equalStrings(german.ThreeLetterISOLanguageNames, []string{"deu", "ger"}) {
		t.Errorf("German ISO codes = primary=%v all=%#v, want deu with deu/ger aliases", german.ThreeLetterISOLanguageName, german.ThreeLetterISOLanguageNames)
	}
	for _, code := range []string{"zh-cn", "zh-hans", "zh-tw", "zh-hant"} {
		culture, found := cultureByTwoLetter(cultures, code)
		if !found {
			t.Errorf("culture %q missing from ISO source", code)
			continue
		}
		if culture.Name != code {
			t.Errorf("culture %q Name = %q, want culture-tag name", code, culture.Name)
		}
	}

	german.ThreeLetterISOLanguageNames[0] = "mutated"
	*german.ThreeLetterISOLanguageName = "mutated"
	fresh, _ := cultureByTwoLetter(service.Cultures(), "de")
	if fresh.ThreeLetterISOLanguageName == nil || *fresh.ThreeLetterISOLanguageName != "deu" || !equalStrings(fresh.ThreeLetterISOLanguageNames, []string{"deu", "ger"}) {
		t.Errorf("mutating nested culture fields changed a later result: %#v", fresh)
	}
}

func TestServiceOptionsMatchesCompletePinnedNativeLabelPayload(t *testing.T) {
	service := newService(t)
	options := service.Options()

	if got, want := len(options), 105; got != want {
		t.Errorf("Options count = %d, want stock fixture count %d with Melayu/ms retained after Malti", got, want)
	}
	assertDigest(t, options, optionsPayloadSHA256, "Options canonical DTO payload")
	for index, want := range []dto.LocalizationOption{
		{Name: "Abkhazian", Value: "ab"},
		{Name: "Afrikaans", Value: "af"},
		{Name: "azərbaycan", Value: "az"},
		{Name: "bosanski", Value: "bs"},
	} {
		if index >= len(options) {
			t.Errorf("Options missing source sequence entry at index %d, want %#v after skipped Bahasa Melayu/ms", index, want)
			continue
		}
		if got := options[index]; got != want {
			t.Errorf("Options[%d] = %#v, want source sequence %#v after skipped Bahasa Melayu/ms", index, got, want)
		}
	}
	if containsOption(options, dto.LocalizationOption{Name: "Bahasa Melayu", Value: "ms"}) {
		t.Error("Options unexpectedly contains excluded stock entry Bahasa Melayu/ms")
	}
	for _, tc := range []struct {
		index  int
		option dto.LocalizationOption
	}{
		{39, dto.LocalizationOption{Name: "Malti", Value: "mt"}},
		{40, dto.LocalizationOption{Name: "Melayu", Value: "ms"}},
	} {
		if tc.index >= len(options) {
			t.Errorf("Options missing source sequence entry at index %d, want %#v after Malti", tc.index, tc.option)
			continue
		}
		if got := options[tc.index]; got != tc.option {
			t.Errorf("Options[%d] = %#v, want source sequence %#v", tc.index, got, tc.option)
		}
	}
	for _, tc := range []struct {
		index  int
		option dto.LocalizationOption
	}{
		{100, dto.LocalizationOption{Name: "中文（中国）", Value: "zh-CN"}},
		{101, dto.LocalizationOption{Name: "中文（中國香港特別行政區）", Value: "zh-HK"}},
		{102, dto.LocalizationOption{Name: "中文（台灣）", Value: "zh-TW"}},
	} {
		if tc.index >= len(options) {
			t.Errorf("Options missing Chinese source sequence entry at index %d, want %#v", tc.index, tc.option)
			continue
		}
		if got := options[tc.index]; got != tc.option {
			t.Errorf("Options[%d] = %#v, want Chinese source sequence %#v", tc.index, got, tc.option)
		}
	}
	for _, tc := range []dto.LocalizationOption{
		{Name: "English", Value: "en-US"},
		{Name: "español (Latinoamérica)", Value: "es_419"},
		{Name: "עברית (ישראל)", Value: "he_IL"},
		{Name: "Lojban", Value: "jbo"},
		{Name: "pr", Value: "pr"},
		{Name: "中文（中国）", Value: "zh-CN"},
		{Name: "中文（中國香港特別行政區）", Value: "zh-HK"},
		{Name: "한국어", Value: "ko"},
	} {
		if !containsOption(options, tc) {
			t.Errorf("Options missing exact pinned native option %#v", tc)
		}
	}

	options[0].Name = "mutated"
	if fresh := service.Options(); fresh[0].Name == "mutated" {
		t.Error("mutating one Options result changed a later result")
	}
}

func TestServiceParentalRatingsPreservesCountrySystemsMandatoryFallbacksAndOrder(t *testing.T) {
	service := newService(t)
	for _, tc := range []struct {
		name    string
		country string
		count   int
		digest  string
		checks  []ratingCheck
	}{
		{
			name: "United States aliases and sub-scores", country: "US", count: 56, digest: usRatingsSHA256,
			checks: []ratingCheck{{"Unrated", nil, nil}, {"TV-Y7", intPtr(7), intPtr(0)}, {"TV-Y7-FV", intPtr(7), intPtr(1)}, {"TV-MA", intPtr(17), intPtr(1)}, {"Banned", intPtr(1001), nil}},
		},
		{
			name: "Germany uses FSK and common additions", country: "de", count: 24, digest: deRatingsSHA256,
			checks: []ratingCheck{{"Unrated", nil, nil}, {"FSK-12", intPtr(12), nil}, {"10", intPtr(10), nil}, {"13", intPtr(13), nil}, {"14", intPtr(14), nil}, {"21", intPtr(21), nil}, {"XXX", intPtr(1000), nil}, {"Banned", intPtr(1001), nil}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ratings := service.ParentalRatings(tc.country)
			if got := len(ratings); got != tc.count {
				t.Fatalf("ParentalRatings(%q) count = %d, want %d", tc.country, got, tc.count)
			}
			assertRatingOrder(t, ratings)
			assertRatingDigest(t, ratings, tc.digest, tc.country)
			for _, check := range tc.checks {
				assertRating(t, ratings, check)
			}
		})
	}

	for _, country := range []string{"", "not-a-country"} {
		t.Run("mandatory-only for "+fmt.Sprintf("%q", country), func(t *testing.T) {
			ratings := service.ParentalRatings(country)
			if got, want := len(ratings), 8; got != want {
				t.Fatalf("ParentalRatings(%q) count = %d, want mandatory-only %d", country, got, want)
			}
			want := []ratingCheck{
				{"Unrated", nil, nil},
				{"Approved", intPtr(0), nil},
				{"10", intPtr(10), nil},
				{"13", intPtr(13), nil},
				{"14", intPtr(14), nil},
				{"21", intPtr(21), nil},
				{"XXX", intPtr(1000), nil},
				{"Banned", intPtr(1001), nil},
			}
			for index, check := range want {
				if ratings[index].Name != check.name {
					t.Errorf("ParentalRatings(%q)[%d] = %q, want %q", country, index, ratings[index].Name, check.name)
				}
				assertRating(t, ratings, check)
			}
		})
	}

	ratings := service.ParentalRatings("us")
	*ratings[1].Value = 999
	*ratings[1].RatingScore.SubScore = 999
	fresh := service.ParentalRatings("us")
	if fresh[1].Value == nil || *fresh[1].Value == 999 || fresh[1].RatingScore == nil || fresh[1].RatingScore.SubScore == nil || *fresh[1].RatingScore.SubScore == 999 {
		t.Errorf("mutating nested parental rating fields changed a later result: %#v", fresh[1])
	}
}

func TestServiceSupportsConcurrentIndependentReads(t *testing.T) {
	service := newService(t)
	const workers = 48
	start := make(chan struct{})
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for range 32 {
				countries := service.Countries()
				cultures := service.Cultures()
				ratings := service.ParentalRatings("US")
				options := service.Options()
				countries[0].Name = "mutated"
				cultures[0].ThreeLetterISOLanguageNames[0] = "mutated"
				*ratings[1].Value = 999
				options[0].Name = "mutated"
				if service.Countries()[0].Name != "AF" || service.Cultures()[0].ThreeLetterISOLanguageNames[0] != "aar" || *service.ParentalRatings("US")[1].Value != 0 || service.Options()[0].Name == "mutated" {
					errs <- fmt.Errorf("one reader mutation leaked into another result")
					return
				}
			}
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

type ratingCheck struct {
	name     string
	score    *int
	subScore *int
}

func assertRating(t *testing.T, ratings []dto.ParentalRating, want ratingCheck) {
	t.Helper()
	for _, rating := range ratings {
		if rating.Name != want.name {
			continue
		}
		if want.score == nil {
			if rating.Value != nil || rating.RatingScore != nil {
				t.Errorf("rating %q = %#v, want Unrated null value and score", want.name, rating)
			}
			return
		}
		if rating.Value == nil || *rating.Value != *want.score || rating.RatingScore == nil || rating.RatingScore.Score != *want.score || !equalIntPointers(rating.RatingScore.SubScore, want.subScore) {
			t.Errorf("rating %q = %#v, want score=%v subscore=%v", want.name, rating, want.score, want.subScore)
		}
		return
	}
	t.Errorf("rating %q missing", want.name)
}

func assertRatingOrder(t *testing.T, ratings []dto.ParentalRating) {
	t.Helper()
	for index := 1; index < len(ratings); index++ {
		if compareRatings(ratings[index-1], ratings[index]) > 0 {
			t.Errorf("ratings[%d]=%q sorts after ratings[%d]=%q", index-1, ratings[index-1].Name, index, ratings[index].Name)
		}
	}
}

func compareRatings(left, right dto.ParentalRating) int {
	if left.RatingScore == nil {
		if right.RatingScore == nil {
			return 0
		}
		return -1
	}
	if right.RatingScore == nil {
		return 1
	}
	if left.RatingScore.Score != right.RatingScore.Score {
		return left.RatingScore.Score - right.RatingScore.Score
	}
	if left.RatingScore.SubScore == nil {
		if right.RatingScore.SubScore == nil {
			return 0
		}
		return -1
	}
	if right.RatingScore.SubScore == nil {
		return 1
	}
	return *left.RatingScore.SubScore - *right.RatingScore.SubScore
}

type ratingDigest struct {
	Name     string
	Value    *int
	Score    *int
	SubScore *int
}

func assertRatingDigest(t *testing.T, ratings []dto.ParentalRating, want, country string) {
	t.Helper()
	payload := make([]ratingDigest, len(ratings))
	for i, rating := range ratings {
		payload[i] = ratingDigest{Name: rating.Name, Value: rating.Value}
		if rating.RatingScore != nil {
			score := rating.RatingScore.Score
			payload[i].Score = &score
			payload[i].SubScore = rating.RatingScore.SubScore
		}
	}
	assertDigest(t, payload, want, "ParentalRatings("+country+") semantic payload")
}

func assertDigest(t *testing.T, value any, want, subject string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", subject, err)
	}
	digest := sha256.Sum256(body)
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Errorf("%s SHA-256 = %s, want pinned %s", subject, got, want)
	}
}

func newService(t *testing.T) *localization.Service {
	t.Helper()
	service, err := localization.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func countryByName(countries []dto.CountryInfo, name string) (dto.CountryInfo, bool) {
	for _, country := range countries {
		if country.Name == name {
			return country, true
		}
	}
	return dto.CountryInfo{}, false
}

func cultureByTwoLetter(cultures []dto.CultureDto, code string) (dto.CultureDto, bool) {
	for _, culture := range cultures {
		if strings.EqualFold(culture.TwoLetterISOLanguageName, code) {
			return culture, true
		}
	}
	return dto.CultureDto{}, false
}

func containsOption(options []dto.LocalizationOption, want dto.LocalizationOption) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}

func equalCulture(left, right dto.CultureDto) bool {
	return left.Name == right.Name && left.DisplayName == right.DisplayName && left.TwoLetterISOLanguageName == right.TwoLetterISOLanguageName && equalIntentionallyStringPointers(left.ThreeLetterISOLanguageName, right.ThreeLetterISOLanguageName) && equalStrings(left.ThreeLetterISOLanguageNames, right.ThreeLetterISOLanguageNames)
}

func equalIntentionallyStringPointers(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalIntPointers(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func intPtr(value int) *int          { return &value }
func stringPtr(value string) *string { return &value }

// TestServiceFindLanguageInfoMatchesUpstreamLookupOrder pins
// LocalizationManager.FindLanguageInfo: the first culture whose display name,
// name, three-letter code, or two-letter code equals the input, ignoring case.
func TestServiceFindLanguageInfoMatchesUpstreamLookupOrder(t *testing.T) {
	service := newService(t)
	for _, tc := range []struct{ language, want string }{
		{"eng", "English"},
		{"EN", "English"},
		{"english", "English"},
		{"ger", "German"},
		{"deu", "German"},
		{"chi", "Chinese"},
		{"zh-TW", "Chinese (Traditional)"},
		{"und", "Undetermined"},
	} {
		culture, found := service.FindLanguageInfo(tc.language)
		if !found || culture.DisplayName != tc.want {
			t.Errorf("FindLanguageInfo(%q) = %q, %t; want %q", tc.language, culture.DisplayName, found, tc.want)
		}
	}
	for _, language := range []string{"", "xx", "klingon"} {
		if culture, found := service.FindLanguageInfo(language); found {
			t.Errorf("FindLanguageInfo(%q) = %q, want no culture", language, culture.DisplayName)
		}
	}
}
