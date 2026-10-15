// SPDX-License-Identifier: GPL-2.0-only

package dto

import "time"

// SearchHint matches MediaBrowser.Model.Search.SearchHint
// (upstream/jellyfin/MediaBrowser.Model/Search/SearchHint.cs). ItemId is the
// obsolete copy of Id. ChannelId is always written: upstream writes an empty
// channel id as null (JsonNullableGuidConverter).
type SearchHint struct {
	ItemID                  UUID       `json:"ItemId"`
	ID                      UUID       `json:"Id"`
	Name                    string     `json:"Name"`
	MatchedTerm             *string    `json:"MatchedTerm,omitzero"`
	IndexNumber             *int       `json:"IndexNumber,omitzero"`
	ProductionYear          *int       `json:"ProductionYear,omitzero"`
	ParentIndexNumber       *int       `json:"ParentIndexNumber,omitzero"`
	PrimaryImageTag         *string    `json:"PrimaryImageTag,omitzero"`
	ThumbImageTag           *string    `json:"ThumbImageTag,omitzero"`
	ThumbImageItemID        *string    `json:"ThumbImageItemId,omitzero"`
	BackdropImageTag        *string    `json:"BackdropImageTag,omitzero"`
	BackdropImageItemID     *string    `json:"BackdropImageItemId,omitzero"`
	Type                    Kind       `json:"Type"`
	IsFolder                *bool      `json:"IsFolder,omitzero"`
	RunTimeTicks            *int64     `json:"RunTimeTicks,omitzero"`
	MediaType               MediaType  `json:"MediaType"`
	StartDate               *time.Time `json:"StartDate,omitzero"`
	EndDate                 *time.Time `json:"EndDate,omitzero"`
	Series                  *string    `json:"Series,omitzero"`
	Status                  *string    `json:"Status,omitzero"`
	Album                   *string    `json:"Album,omitzero"`
	AlbumID                 *UUID      `json:"AlbumId,omitzero"`
	AlbumArtist             *string    `json:"AlbumArtist,omitzero"`
	Artists                 []string   `json:"Artists"`
	SongCount               *int       `json:"SongCount,omitzero"`
	EpisodeCount            *int       `json:"EpisodeCount,omitzero"`
	ChannelID               *UUID      `json:"ChannelId"`
	ChannelName             *string    `json:"ChannelName,omitzero"`
	PrimaryImageAspectRatio *float64   `json:"PrimaryImageAspectRatio,omitzero"`
}

// SearchHintResult matches MediaBrowser.Model.Search.SearchHintResult.
type SearchHintResult struct {
	SearchHints      []SearchHint `json:"SearchHints"`
	TotalRecordCount int          `json:"TotalRecordCount"`
}
