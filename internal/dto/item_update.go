// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ItemUpdateRequest binds the BaseItemDto body of POST /Items/{itemId}. It
// holds only the fields ItemUpdateController.UpdateItem reads
// (ItemUpdateController.cs:229-488). Nullable fields are pointers and slices,
// so the update can tell an absent or null value from an empty one where
// upstream does: a null list leaves the stored list unchanged, and a null
// scalar clears the stored value.
type ItemUpdateRequest struct {
	Name                         *string            `json:"Name"`
	ForcedSortName               *string            `json:"ForcedSortName"`
	OriginalTitle                *string            `json:"OriginalTitle"`
	OriginalLanguage             *string            `json:"OriginalLanguage"`
	CriticRating                 *float32           `json:"CriticRating"`
	CommunityRating              *float32           `json:"CommunityRating"`
	IndexNumber                  *int               `json:"IndexNumber"`
	ParentIndexNumber            *int               `json:"ParentIndexNumber"`
	Overview                     *string            `json:"Overview"`
	Genres                       []string           `json:"Genres"`
	AirsAfterSeasonNumber        *int               `json:"AirsAfterSeasonNumber"`
	AirsBeforeEpisodeNumber      *int               `json:"AirsBeforeEpisodeNumber"`
	AirsBeforeSeasonNumber       *int               `json:"AirsBeforeSeasonNumber"`
	Height                       *int               `json:"Height"`
	Taglines                     []string           `json:"Taglines"`
	Studios                      []ItemUpdateName   `json:"Studios"`
	DateCreated                  *Time              `json:"DateCreated"`
	SeriesName                   *string            `json:"SeriesName"`
	EndDate                      *Time              `json:"EndDate"`
	PremiereDate                 *Time              `json:"PremiereDate"`
	ProductionYear               *int               `json:"ProductionYear"`
	OfficialRating               *string            `json:"OfficialRating"`
	CustomRating                 *string            `json:"CustomRating"`
	Tags                         []string           `json:"Tags"`
	ProductionLocations          []string           `json:"ProductionLocations"`
	PreferredMetadataCountryCode *string            `json:"PreferredMetadataCountryCode"`
	PreferredMetadataLanguage    *string            `json:"PreferredMetadataLanguage"`
	DisplayOrder                 *string            `json:"DisplayOrder"`
	AspectRatio                  *string            `json:"AspectRatio"`
	LockData                     *bool              `json:"LockData"`
	LockedFields                 []string           `json:"LockedFields"`
	RunTimeTicks                 *int64             `json:"RunTimeTicks"`
	ProviderIDs                  ProviderIDPairs    `json:"ProviderIds"`
	Video3DFormat                *string            `json:"Video3DFormat"`
	AlbumArtists                 []ItemUpdateName   `json:"AlbumArtists"`
	ArtistItems                  []ItemUpdateName   `json:"ArtistItems"`
	Album                        *string            `json:"Album"`
	Status                       *string            `json:"Status"`
	AirDays                      []string           `json:"AirDays"`
	AirTime                      *string            `json:"AirTime"`
	People                       []ItemUpdatePerson `json:"People"`
}

// ItemUpdateName is the Name member of a NameGuidPair in an update body. The
// update reads only the name, so a missing or malformed Id does not reject
// the request.
type ItemUpdateName struct {
	Name string `json:"Name"`
}

// ItemUpdatePerson is the part of a BaseItemPerson that UpdateItem reads
// (ItemUpdateController.cs:94-103).
type ItemUpdatePerson struct {
	Name string  `json:"Name"`
	Role *string `json:"Role"`
	Type string  `json:"Type"`
}

// NameValuePair matches MediaBrowser.Model.Dto.NameValuePair.
type NameValuePair struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

// MetadataEditorInfo matches MediaBrowser.Model.Dto.MetadataEditorInfo. The
// lists are always allocated; ContentType is omitted when null.
type MetadataEditorInfo struct {
	ParentalRatingOptions []ParentalRating `json:"ParentalRatingOptions"`
	Countries             []CountryInfo    `json:"Countries"`
	Cultures              []CultureDto     `json:"Cultures"`
	ExternalIdInfos       []ExternalIdInfo `json:"ExternalIdInfos"`
	ContentType           *string          `json:"ContentType,omitempty"`
	ContentTypeOptions    []NameValuePair  `json:"ContentTypeOptions"`
}

// ProviderIDPairs binds a ProviderIds JSON object in document order, as
// upstream's Dictionary keeps insertion order. A null value binds as empty.
type ProviderIDPairs []ProviderIDPair

// ProviderIDPair is one ProviderIds member.
type ProviderIDPair struct {
	Name  string
	Value string
}

// UnmarshalJSON decodes the object members in document order.
func (p *ProviderIDPairs) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		*p = nil
		return nil
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("ProviderIds: want object, got %v", token)
	}
	pairs := ProviderIDPairs{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, _ := keyToken.(string)
		var value *string
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		pair := ProviderIDPair{Name: key}
		if value != nil {
			pair.Value = *value
		}
		pairs = append(pairs, pair)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*p = pairs
	return nil
}
