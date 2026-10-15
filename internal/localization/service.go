// SPDX-License-Identifier: GPL-2.0-only

// Package localization serves Jellyfin's pinned, read-only localization data.
package localization

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// The embedded sources are pinned to the workspace Jellyfin revision. The
// options payload is additionally bound to the observed stock response: its
// embedded-resource enumeration omits Bahasa Melayu/ms despite the source tree
// carrying Core/ms.json. Digests make accidental payload drift fail at service
// construction rather than changing API results at request time.
const (
	iso6392Digest       = "64988b05e5bc577614a23fa589f8e0eab6a0e7706ff6ffe495cb845cf67af060"
	countriesDigest     = "5847c8458cd8b2b26eaa2deb837132b5240d9c4f0aaa422f00a2cd6b1fb48449"
	optionsDigest       = "1d5ef49a1dfac50bb357cfcf4dca243b292b3ce35143cdb36fff34c56f577144"
	ratingsDigest       = "5785873b3b481efd92c6dbf6774c41d1bebd80fcc9656ab7bfa6648f6cce754a"
	defaultCountryCode  = "us"
	expectedRatingFiles = 45
)

//go:embed assets/iso6392.txt assets/countries.json assets/options.json assets/ratings/*.json
var assets embed.FS

// Country is the API country DTO.
type Country = dto.CountryInfo

// Culture is the API culture DTO.
type Culture = dto.CultureDto

// ParentalRatingScore is the API rating score DTO.
type ParentalRatingScore = dto.ParentalRatingScore

// ParentalRating is the API parental-rating DTO.
type ParentalRating = dto.ParentalRating

// LocalizationOption is the API UI-culture option DTO.
type LocalizationOption = dto.LocalizationOption

// Service holds validated immutable localization data.
// It has no package-level mutable state. Each accessor clones its result
// for independent callers.
type Service struct {
	countries       []Country
	cultures        []Culture
	languageAliases map[string]string
	ratings         map[string][]ParentalRating
	commonRatings   []ParentalRating
	options         []LocalizationOption
}

// NewService loads and validates every embedded localization source once. A
// construction error is intentional: a bad pinned corpus must not degrade an
// HTTP request into a silent empty response.
func NewService() (*Service, error) {
	iso, err := readVerified("assets/iso6392.txt", iso6392Digest)
	if err != nil {
		return nil, err
	}
	cultures, err := parseCultures(iso)
	if err != nil {
		return nil, err
	}

	countryData, err := readVerified("assets/countries.json", countriesDigest)
	if err != nil {
		return nil, err
	}
	var countries []Country
	if err := json.Unmarshal(countryData, &countries); err != nil {
		return nil, fmt.Errorf("localization: parse countries: %w", err)
	}
	if err := validateCountries(countries); err != nil {
		return nil, err
	}

	optionData, err := readVerified("assets/options.json", optionsDigest)
	if err != nil {
		return nil, err
	}
	var options []LocalizationOption
	if err := json.Unmarshal(optionData, &options); err != nil {
		return nil, fmt.Errorf("localization: parse options: %w", err)
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}

	ratings, err := parseRatings()
	if err != nil {
		return nil, err
	}
	for country, source := range ratings {
		ratings[country] = augmentAndOrder(source)
	}

	languageAliases := buildLanguageAliases(cultures)
	return &Service{
		countries:       countries,
		cultures:        cultures,
		languageAliases: languageAliases,
		ratings:         ratings,
		commonRatings:   augmentAndOrder(nil),
		options:         options,
	}, nil
}

// Countries returns the pinned country list in upstream source order.
func (s *Service) Countries() []dto.CountryInfo {
	if s == nil {
		return nil
	}
	return append([]dto.CountryInfo(nil), s.countries...)
}

// Cultures returns the parsed ISO 639-2 list in upstream source order.
func (s *Service) Cultures() []dto.CultureDto {
	if s == nil {
		return nil
	}
	result := make([]dto.CultureDto, len(s.cultures))
	for i, culture := range s.cultures {
		result[i] = cloneCulture(culture)
	}
	return result
}

// FindLanguageInfo returns the first culture whose display name, name, a
// three-letter code, or two-letter code equals language, ignoring case. It
// ports LocalizationManager.FindLanguageInfo
// (upstream/jellyfin/Emby.Server.Implementations/Localization/LocalizationManager.cs:235-262).
func (s *Service) FindLanguageInfo(language string) (dto.CultureDto, bool) {
	if s == nil || language == "" {
		return dto.CultureDto{}, false
	}
	for _, culture := range s.cultures {
		if strings.EqualFold(language, culture.DisplayName) || strings.EqualFold(language, culture.Name) ||
			slices.ContainsFunc(culture.ThreeLetterISOLanguageNames, func(name string) bool { return strings.EqualFold(language, name) }) ||
			strings.EqualFold(language, culture.TwoLetterISOLanguageName) {
			return cloneCulture(culture), true
		}
	}
	return dto.CultureDto{}, false
}

// NormalizeLanguage maps a known ISO 639 alias to the culture's primary
// three-letter code. Unknown values remain untouched so persisted extensions
// preserve their original identity.
func (s *Service) NormalizeLanguage(value string) string {
	if s == nil {
		return value
	}
	if normalized, found := s.languageAliases[value]; found {
		return normalized
	}
	for alias, normalized := range s.languageAliases {
		if strings.EqualFold(alias, value) {
			return normalized
		}
	}
	return value
}

// ParentalRatings returns a country system's selectable ratings. An empty or
// unknown country uses only Jellyfin's mandatory common selections. The common
// selection ratings are augmented exactly once and the final stable order is
// score, sub-score, then source insertion order for ties.
func (s *Service) ParentalRatings(countryCode string) []dto.ParentalRating {
	if s == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(countryCode))
	ratings, ok := s.ratings[key]
	if !ok {
		ratings = s.commonRatings
	}
	return cloneRatings(ratings)
}

// Options returns the upstream UI-localization options in pinned ordinal
// case-insensitive display-name order.
func (s *Service) Options() []dto.LocalizationOption {
	if s == nil {
		return nil
	}
	return append([]dto.LocalizationOption(nil), s.options...)
}

func readVerified(name, wantDigest string) ([]byte, error) {
	data, err := assets.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("localization: read %s: %w", name, err)
	}
	if got := sha256Hex(data); got != wantDigest {
		return nil, fmt.Errorf("localization: %s SHA-256 = %s, want %s", name, got, wantDigest)
	}
	return data, nil
}

func parseCultures(data []byte) ([]Culture, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	cultures := make([]Culture, 0, len(lines))
	for lineNumber, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 5 {
			return nil, fmt.Errorf("localization: iso6392 line %d has %d fields, want 5", lineNumber+1, len(parts))
		}
		if strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[3]) == "" {
			return nil, fmt.Errorf("localization: iso6392 line %d has required empty field", lineNumber+1)
		}

		name := parts[3]
		twoLetter := parts[2]
		if strings.Contains(twoLetter, "-") {
			name = twoLetter
		}
		threeLetter := []string{parts[0]}
		if parts[1] != "" {
			threeLetter = append(threeLetter, parts[1])
		}
		first := threeLetter[0]
		cultures = append(cultures, Culture{
			Name:                        name,
			DisplayName:                 parts[3],
			TwoLetterISOLanguageName:    twoLetter,
			ThreeLetterISOLanguageName:  &first,
			ThreeLetterISOLanguageNames: threeLetter,
		})
	}
	if len(cultures) == 0 {
		return nil, errors.New("localization: iso6392 contains no cultures")
	}
	return cultures, nil
}

func buildLanguageAliases(cultures []Culture) map[string]string {
	aliases := make(map[string]string, len(cultures)*3)
	for _, culture := range cultures {
		if culture.ThreeLetterISOLanguageName == nil {
			continue
		}
		canonical := *culture.ThreeLetterISOLanguageName
		aliases[culture.TwoLetterISOLanguageName] = canonical
		for _, alias := range culture.ThreeLetterISOLanguageNames {
			aliases[alias] = canonical
		}
	}
	return aliases
}

func validateCountries(countries []Country) error {
	if len(countries) == 0 {
		return errors.New("localization: countries contains no entries")
	}
	seen := make(map[string]struct{}, len(countries))
	for index, country := range countries {
		if country.Name == "" || country.DisplayName == "" || country.TwoLetterISORegionName == "" || country.ThreeLetterISORegionName == "" {
			return fmt.Errorf("localization: country %d has an empty required field", index)
		}
		key := strings.ToLower(country.TwoLetterISORegionName)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("localization: duplicate country %q", country.TwoLetterISORegionName)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateOptions(options []LocalizationOption) error {
	if len(options) == 0 {
		return errors.New("localization: options contains no entries")
	}
	seen := make(map[string]struct{}, len(options))
	for index, option := range options {
		if option.Name == "" || option.Value == "" {
			return fmt.Errorf("localization: option %d has an empty required field", index)
		}
		key := strings.ToLower(option.Value)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("localization: duplicate option %q", option.Value)
		}
		seen[key] = struct{}{}
	}
	return nil
}

type ratingFile struct {
	CountryCode string        `json:"countryCode"`
	Ratings     []ratingEntry `json:"ratings"`
}

type ratingEntry struct {
	RatingStrings []string                `json:"ratingStrings"`
	RatingScore   dto.ParentalRatingScore `json:"ratingScore"`
}

func parseRatings() (map[string][]ParentalRating, error) {
	paths, err := fs.Glob(assets, "assets/ratings/*.json")
	if err != nil {
		return nil, fmt.Errorf("localization: list ratings: %w", err)
	}
	if len(paths) != expectedRatingFiles {
		return nil, fmt.Errorf("localization: ratings file count = %d, want %d", len(paths), expectedRatingFiles)
	}
	sort.Strings(paths)

	manifest := make([]byte, 0, len(paths)*(len("assets/ratings/")+1+sha256.Size))
	systems := make(map[string][]ParentalRating, len(paths))
	for _, path := range paths {
		data, err := assets.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("localization: read %s: %w", path, err)
		}
		name := strings.TrimPrefix(path, "assets/ratings/")
		digest := sha256.Sum256(data)
		manifest = append(manifest, name...)
		manifest = append(manifest, 0)
		manifest = append(manifest, digest[:]...)

		var source ratingFile
		if err := json.Unmarshal(data, &source); err != nil {
			return nil, fmt.Errorf("localization: parse %s: %w", path, err)
		}
		country := strings.ToLower(strings.TrimSpace(source.CountryCode))
		if country == "" || len(source.Ratings) == 0 {
			return nil, fmt.Errorf("localization: %s has no country or ratings", path)
		}
		// Jellyfin assigns each parsed file to a country-keyed dictionary.
		// Aliases such as gb.json and uk.json therefore overwrite an earlier
		// entry for the same country.

		ratings := make([]ParentalRating, 0)
		for entryIndex, entry := range source.Ratings {
			if len(entry.RatingStrings) == 0 {
				return nil, fmt.Errorf("localization: %s rating %d has no names", path, entryIndex)
			}
			for _, name := range entry.RatingStrings {
				if strings.TrimSpace(name) == "" {
					return nil, fmt.Errorf("localization: %s rating %d has an empty name", path, entryIndex)
				}
				score := cloneScore(&entry.RatingScore)
				value := score.Score
				ratings = append(ratings, ParentalRating{Name: name, Value: &value, RatingScore: score})
			}
		}
		systems[country] = ratings
	}
	if got := sha256Hex(manifest); got != ratingsDigest {
		return nil, fmt.Errorf("localization: ratings SHA-256 = %s, want %s", got, ratingsDigest)
	}
	if _, ok := systems[defaultCountryCode]; !ok {
		return nil, fmt.Errorf("localization: default country %q has no ratings", defaultCountryCode)
	}
	return systems, nil
}

func augmentAndOrder(source []ParentalRating) []ParentalRating {
	ratings := cloneRatings(source)
	containsNil := false
	containsScore := make(map[int]bool, len(ratings))
	containsAtLeast21 := false
	for _, rating := range ratings {
		if rating.RatingScore == nil {
			containsNil = true
			continue
		}
		containsScore[rating.RatingScore.Score] = true
		containsAtLeast21 = containsAtLeast21 || rating.RatingScore.Score >= 21
	}
	if !containsNil {
		ratings = append(ratings, ParentalRating{Name: "Unrated"})
	}
	for _, addition := range []struct {
		name  string
		score int
	}{{"Approved", 0}, {"10", 10}, {"13", 13}, {"14", 14}} {
		if !containsScore[addition.score] {
			ratings = append(ratings, numberedRating(addition.name, addition.score))
			containsScore[addition.score] = true
		}
	}
	if !containsAtLeast21 {
		ratings = append(ratings, numberedRating("21", 21))
	}
	if !containsScore[1000] {
		ratings = append(ratings, numberedRating("XXX", 1000))
		containsScore[1000] = true
	}
	if !containsScore[1001] {
		ratings = append(ratings, numberedRating("Banned", 1001))
	}

	// sort.SliceStable preserves each rating system's JSON order for entries
	// sharing score/sub-score, matching LINQ OrderBy(...).ThenBy(...).
	sort.SliceStable(ratings, func(i, j int) bool {
		return ratingOrder(ratings[i], ratings[j]) < 0
	})
	return ratings
}

func numberedRating(name string, number int) ParentalRating {
	score := number
	return ParentalRating{
		Name:        name,
		Value:       &score,
		RatingScore: &ParentalRatingScore{Score: number},
	}
}

func ratingOrder(left, right ParentalRating) int {
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
		if left.RatingScore.Score < right.RatingScore.Score {
			return -1
		}
		return 1
	}
	// Nullable<int>.CompareTo orders null before a value.
	if left.RatingScore.SubScore == nil {
		if right.RatingScore.SubScore == nil {
			return 0
		}
		return -1
	}
	if right.RatingScore.SubScore == nil {
		return 1
	}
	if *left.RatingScore.SubScore < *right.RatingScore.SubScore {
		return -1
	}
	if *left.RatingScore.SubScore > *right.RatingScore.SubScore {
		return 1
	}
	return 0
}

func cloneCulture(source Culture) Culture {
	result := source
	result.ThreeLetterISOLanguageNames = append([]string(nil), source.ThreeLetterISOLanguageNames...)
	if source.ThreeLetterISOLanguageName != nil {
		value := *source.ThreeLetterISOLanguageName
		result.ThreeLetterISOLanguageName = &value
	}
	return result
}

func cloneRatings(source []ParentalRating) []ParentalRating {
	result := make([]ParentalRating, len(source))
	for i, rating := range source {
		result[i] = rating
		if rating.Value != nil {
			value := *rating.Value
			result[i].Value = &value
		}
		result[i].RatingScore = cloneScore(rating.RatingScore)
	}
	return result
}

func cloneScore(source *ParentalRatingScore) *ParentalRatingScore {
	if source == nil {
		return nil
	}
	result := *source
	if source.SubScore != nil {
		subScore := *source.SubScore
		result.SubScore = &subScore
	}
	return &result
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
