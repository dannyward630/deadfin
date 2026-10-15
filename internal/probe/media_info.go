// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"encoding/json"
	"time"

	"github.com/deadfin-org/deadfin/internal/playback"
)

// MediaInfo ports MediaBrowser.Model.MediaInfo.MediaInfo. Embedding keeps the
// playback-facing source facts single-owned by playback while retaining probe
// metadata until the ItemStore satellite writer is introduced.
type MediaInfo struct {
	playback.MediaSourceInfo
	Chapters                  []Chapter
	Album                     *string
	Artists                   []string
	AlbumArtists              []string
	Studios                   []string
	Genres                    []string
	People                    []MediaPerson
	ProviderIDs               map[string]string
	Name                      *string
	ShowName                  *string
	ForcedSortName            *string
	IndexNumber               *int
	ParentIndexNumber         *int
	ProductionYear            *int
	PremiereDate              *time.Time
	OfficialRating            *string
	OfficialRatingDescription *string
	Overview                  *string
}

// MarshalJSON prevents MediaSourceInfo's promoted marshaler from eliding probe
// metadata and preserves Jellyfin's observable empty collection semantics.
func (m MediaInfo) MarshalJSON() ([]byte, error) {
	encodedSource, err := json.Marshal(m.MediaSourceInfo)
	if err != nil {
		return nil, err
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encodedSource, &wire); err != nil {
		return nil, err
	}
	var premiereDate *string
	if m.PremiereDate != nil {
		value := m.PremiereDate.UTC().Format("2006-01-02T15:04:05.0000000Z")
		premiereDate = &value
	}
	metadata := struct {
		Chapters                  []Chapter         `json:"Chapters"`
		Album                     *string           `json:"Album,omitempty"`
		Artists                   []string          `json:"Artists"`
		AlbumArtists              []string          `json:"AlbumArtists"`
		Studios                   []string          `json:"Studios"`
		Genres                    []string          `json:"Genres"`
		People                    []MediaPerson     `json:"People"`
		ProviderIDs               map[string]string `json:"ProviderIds"`
		Name                      *string           `json:"Name,omitempty"`
		ShowName                  *string           `json:"ShowName,omitempty"`
		ForcedSortName            *string           `json:"ForcedSortName,omitempty"`
		IndexNumber               *int              `json:"IndexNumber,omitempty"`
		ParentIndexNumber         *int              `json:"ParentIndexNumber,omitempty"`
		ProductionYear            *int              `json:"ProductionYear,omitempty"`
		PremiereDate              *string           `json:"PremiereDate,omitempty"`
		OfficialRating            *string           `json:"OfficialRating,omitempty"`
		OfficialRatingDescription *string           `json:"OfficialRatingDescription,omitempty"`
		Overview                  *string           `json:"Overview,omitempty"`
	}{m.Chapters, m.Album, m.Artists, m.AlbumArtists, m.Studios, m.Genres, m.People, m.ProviderIDs, m.Name, m.ShowName, m.ForcedSortName, m.IndexNumber, m.ParentIndexNumber, m.ProductionYear, premiereDate, m.OfficialRating, m.OfficialRatingDescription, m.Overview}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encodedMetadata, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		wire[key] = value
	}
	return json.Marshal(wire)
}

// MediaPerson is one tag-mined credit before the scanner materializes a stable
// person entity identity.
type MediaPerson struct {
	Name string  `json:"Name"`
	Role *string `json:"Role,omitempty"`
	Type string  `json:"Type"`
}

// Chapter is the normalized ffprobe chapter record. StartPositionTicks follows
// Jellyfin's millisecond-rounded tick convention.
type Chapter struct {
	Name               *string
	StartPositionTicks int64
}
