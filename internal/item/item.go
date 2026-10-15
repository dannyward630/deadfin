// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// Item is the pure persisted and queryable compatibility representation of a
// Jellyfin media item. Nullable storage facts are pointers.
type Item struct {
	// Identity and hierarchy.
	ID                  dto.UUID
	Kind                Kind
	ParentID            *dto.UUID
	TopParentID         *dto.UUID
	SeriesID            *dto.UUID
	SeasonID            *dto.UUID
	ChannelID           *dto.UUID
	PrimaryVersionID    *dto.UUID
	OwnerID             *dto.UUID
	PlaylistOwnerUserID dto.UUID

	// Text.
	Path                         *string
	Name                         *string
	SortName                     *string
	CleanName                    *string
	ForcedSortName               *string
	OriginalTitle                *string
	OriginalLanguage             *string
	Overview                     *string
	Tagline                      *string
	OfficialRating               *string
	CustomRating                 *string
	PresentationUniqueKey        *string
	SeriesPresentationUniqueKey  *string
	Album                        *string
	ExternalID                   *string
	ExternalSeriesID             *string
	ExternalServiceID            *string
	PreferredMetadataLanguage    *string
	PreferredMetadataCountryCode *string
	SeriesName                   *string
	SeasonName                   *string
	EpisodeTitle                 *string
	ShowID                       *string
	Container                    *string
	MediaSourceContainer         *string

	// Dates.
	StartDate          *time.Time
	EndDate            *time.Time
	PremiereDate       *time.Time
	DateCreated        *time.Time
	DateModified       *time.Time
	FileChangedAt      *time.Time
	DateLastRefreshed  *time.Time
	DateLastSaved      *time.Time
	DateLastMediaAdded *time.Time

	// Numbers.
	ProductionYear                  *int
	IndexNumber                     *int
	ParentIndexNumber               *int
	Width                           *int
	Height                          *int
	TotalBitrate                    *int
	InheritedParentalRatingValue    *int
	InheritedParentalRatingSubValue *int
	AirsBeforeSeasonNumber          *int
	AirsAfterSeasonNumber           *int
	AirsBeforeEpisodeNumber         *int
	RunTimeTicks                    *int64
	Size                            *int64
	FileDevice                      *int64
	FileInode                       *int64
	CommunityRating                 *float32
	CriticRating                    *float32
	LUFS                            *float32
	NormalizationGain               *float32

	// Enums and classification.
	UnratedType   *UnratedItem
	SourceType    SourceType
	MediaType     *MediaType
	ExtraType     *ExtraType
	VideoType     *VideoType
	IsoType       *IsoType
	Video3DFormat *Video3DFormat
	SeriesStatus  *SeriesStatus
	ProgramAudio  *ProgramAudio

	// Booleans.
	IsFolder        bool
	IsVirtualItem   bool
	IsInMixedFolder bool
	IsLocked        bool
	IsMovie         bool
	IsSeries        bool
	IsRepeat        bool
	IsPlaceHolder   bool

	// Normalized satellites.
	ProviderIDs    ProviderIDs
	ImageInfos     []ImageInfo
	LockedFields   []MetadataField
	Values         ItemValues
	OpenAccess     bool
	People         []PersonCredit
	TrailerTypes   []TrailerType
	LinkedChildren []LinkedChildRef
	// AdditionalParts preserves a Video stack's secondary source paths in source order.
	// The primary video remains the item itself; these paths resolve to sibling Video items.
	AdditionalParts []string

	// Non-query per-kind payload.
	Extra ExtraFields
}

// UserDataInputs supplies resolved state that upstream gets from services.
type UserDataInputs struct {
	Series                           *Item
	DisableLiveTVChannelUserDataName bool
}

// SeriesGroupingInputs supplies the explicit configuration and ordered folder
// context used to construct a series presentation key.
type SeriesGroupingInputs struct {
	EnableAutomaticGrouping   bool
	PreferredMetadataLanguage string
	CollectionFolderIDs       []dto.UUID
}

// PresentationInputs supplies resolved state needed for presentation keys.
type PresentationInputs struct {
	Series         *Item
	SeriesGrouping SeriesGroupingInputs
}

// SortOptions controls the pure sort-name compatibility operation. Nil token
// slices select upstream defaults; non-nil empty slices disable that rule.
type SortOptions struct {
	RemoveWords           []string
	RemoveCharacters      []string
	ReplaceCharacters     []string
	TransliterateNonASCII func(string) string
}
