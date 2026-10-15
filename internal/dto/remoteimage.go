// SPDX-License-Identifier: GPL-2.0-only

package dto

// RemoteImageResult is the wire shape of GET /Items/{itemId}/RemoteImages,
// mirroring the pinned RemoteImageResult schema.
type RemoteImageResult struct {
	Images           []RemoteImageInfo `json:"Images"`
	TotalRecordCount int               `json:"TotalRecordCount"`
	Providers        []string          `json:"Providers"`
}

// ImageProviderInfo is the wire shape of GET /Items/{itemId}/RemoteImages/Providers,
// mirroring the pinned ImageProviderInfo schema. SupportedImages is a non-nullable
// array of ImageType names.
type ImageProviderInfo struct {
	Name            string   `json:"Name"`
	SupportedImages []string `json:"SupportedImages"`
}

// RemoteImageInfo is one remote image record, mirroring the pinned
// RemoteImageInfo schema. Type is a non-nullable ImageType name string;
// RatingType is a non-nullable RatingType name string.
type RemoteImageInfo struct {
	ProviderName    *string  `json:"ProviderName,omitempty"`
	URL             *string  `json:"Url,omitempty"`
	ThumbnailURL    *string  `json:"ThumbnailUrl,omitempty"`
	Height          *int     `json:"Height,omitempty"`
	Width           *int     `json:"Width,omitempty"`
	CommunityRating *float64 `json:"CommunityRating,omitempty"`
	VoteCount       *int     `json:"VoteCount,omitempty"`
	Language        *string  `json:"Language,omitempty"`
	Type            string   `json:"Type"`
	RatingType      string   `json:"RatingType"`
}
