// SPDX-License-Identifier: GPL-2.0-only

package dto

// CountryInfo describes an ISO country/region recognized by Jellyfin.
type CountryInfo struct {
	Name                     string `json:"Name"`
	DisplayName              string `json:"DisplayName"`
	TwoLetterISORegionName   string `json:"TwoLetterISORegionName"`
	ThreeLetterISORegionName string `json:"ThreeLetterISORegionName"`
}

// CultureDto describes an ISO 639 language entry recognized by Jellyfin.
type CultureDto struct {
	Name                        string   `json:"Name"`
	DisplayName                 string   `json:"DisplayName"`
	TwoLetterISOLanguageName    string   `json:"TwoLetterISOLanguageName"`
	ThreeLetterISOLanguageName  *string  `json:"ThreeLetterISOLanguageName"`
	ThreeLetterISOLanguageNames []string `json:"ThreeLetterISOLanguageNames"`
}

// ParentalRatingScore is a rating age and its optional system-specific
// sub-rating. Jellyfin explicitly names these nested properties score and
// subScore, so they remain lowercase even under the Pascal JSON formatter.
type ParentalRatingScore struct {
	Score    int  `json:"score"`
	SubScore *int `json:"subScore,omitempty"`
}

// ParentalRating is one selectable official rating.
type ParentalRating struct {
	Name        string               `json:"Name"`
	Value       *int                 `json:"Value,omitempty"`
	RatingScore *ParentalRatingScore `json:"RatingScore,omitempty"`
}

// LocalizationOption is one available UI translation.
type LocalizationOption struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}
