// SPDX-License-Identifier: GPL-2.0-only

package dto

// QueryFilters matches MediaBrowser.Model.Querying.QueryFilters. The
// constructor allocates every list, so each one is always emitted
// (upstream/jellyfin/MediaBrowser.Model/Querying/QueryFilters.cs:12-18).
type QueryFilters struct {
	Genres            []NameGuidPair  `json:"Genres"`
	Tags              []string        `json:"Tags"`
	AudioLanguages    []NameValuePair `json:"AudioLanguages"`
	SubtitleLanguages []NameValuePair `json:"SubtitleLanguages"`
}

// NewQueryFilters returns QueryFilters with every list empty, not nil.
func NewQueryFilters() QueryFilters {
	return QueryFilters{Genres: []NameGuidPair{}, Tags: []string{}, AudioLanguages: []NameValuePair{}, SubtitleLanguages: []NameValuePair{}}
}
