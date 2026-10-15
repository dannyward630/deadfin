// SPDX-License-Identifier: GPL-2.0-only

package dto

import "time"

// BaseItemDto is the pinned client-facing representation of a media item.
// Field order and Pascal JSON names mirror MediaBrowser.Model.Dto.BaseItemDto.
type BaseItemDto struct {
	Name                         string                              `json:"Name,omitzero"`
	OriginalTitle                string                              `json:"OriginalTitle,omitzero"`
	ServerID                     string                              `json:"ServerId,omitzero"`
	ID                           UUID                                `json:"Id"`
	Etag                         string                              `json:"Etag,omitzero"`
	SourceType                   string                              `json:"SourceType,omitzero"`
	PlaylistItemID               string                              `json:"PlaylistItemId,omitzero"`
	DateCreated                  *time.Time                          `json:"DateCreated,omitzero"`
	DateLastMediaAdded           *Time                               `json:"DateLastMediaAdded,omitempty"`
	ExtraType                    *ExtraType                          `json:"ExtraType,omitzero"`
	AirsBeforeSeasonNumber       *int                                `json:"AirsBeforeSeasonNumber,omitzero"`
	AirsAfterSeasonNumber        *int                                `json:"AirsAfterSeasonNumber,omitzero"`
	AirsBeforeEpisodeNumber      *int                                `json:"AirsBeforeEpisodeNumber,omitzero"`
	CanDelete                    *bool                               `json:"CanDelete,omitzero"`
	CanDownload                  *bool                               `json:"CanDownload,omitzero"`
	HasLyrics                    *bool                               `json:"HasLyrics,omitzero"`
	HasSubtitles                 *bool                               `json:"HasSubtitles,omitzero"`
	PreferredMetadataLanguage    string                              `json:"PreferredMetadataLanguage,omitzero"`
	PreferredMetadataCountryCode string                              `json:"PreferredMetadataCountryCode,omitzero"`
	Container                    string                              `json:"Container,omitzero"`
	SortName                     string                              `json:"SortName,omitzero"`
	ForcedSortName               string                              `json:"ForcedSortName,omitzero"`
	Video3DFormat                *Video3DFormat                      `json:"Video3DFormat,omitzero"`
	PremiereDate                 *ItemPremiereDate                   `json:"PremiereDate,omitzero"`
	ExternalURLs                 []ExternalURL                       `json:"ExternalUrls,omitzero"`
	MediaSources                 []MediaSourceInfo                   `json:"MediaSources,omitzero"`
	CriticRating                 *float32                            `json:"CriticRating,omitzero"`
	ProductionLocations          []string                            `json:"ProductionLocations,omitzero"`
	Path                         string                              `json:"Path,omitzero"`
	EnableMediaSourceDisplay     *bool                               `json:"EnableMediaSourceDisplay,omitzero"`
	OfficialRating               string                              `json:"OfficialRating,omitzero"`
	CustomRating                 string                              `json:"CustomRating,omitzero"`
	ChannelID                    *UUID                               `json:"ChannelId"`
	ChannelName                  string                              `json:"ChannelName,omitzero"`
	Overview                     string                              `json:"Overview,omitzero"`
	Taglines                     []string                            `json:"Taglines,omitzero"`
	Genres                       []string                            `json:"Genres,omitzero"`
	CommunityRating              *float32                            `json:"CommunityRating,omitzero"`
	CumulativeRunTimeTicks       *int64                              `json:"CumulativeRunTimeTicks,omitzero"`
	RunTimeTicks                 *int64                              `json:"RunTimeTicks,omitzero"`
	PlayAccess                   *PlayAccess                         `json:"PlayAccess,omitzero"`
	AspectRatio                  string                              `json:"AspectRatio,omitzero"`
	ProductionYear               *int                                `json:"ProductionYear,omitzero"`
	IsPlaceHolder                *bool                               `json:"IsPlaceHolder,omitzero"`
	Number                       string                              `json:"Number,omitzero"`
	ChannelNumber                string                              `json:"ChannelNumber,omitzero"`
	IndexNumber                  *int                                `json:"IndexNumber,omitzero"`
	IndexNumberEnd               *int                                `json:"IndexNumberEnd,omitzero"`
	ParentIndexNumber            *int                                `json:"ParentIndexNumber,omitzero"`
	RemoteTrailers               []MediaURL                          `json:"RemoteTrailers,omitzero"`
	ProviderIDs                  map[string]string                   `json:"ProviderIds,omitzero"`
	IsHD                         *bool                               `json:"IsHD,omitzero"`
	IsFolder                     *bool                               `json:"IsFolder,omitzero"`
	ParentID                     *UUID                               `json:"ParentId,omitzero"`
	Type                         Kind                                `json:"Type"`
	People                       []BaseItemPerson                    `json:"People,omitzero"`
	Studios                      []NameGuidPair                      `json:"Studios,omitzero"`
	GenreItems                   []NameGuidPair                      `json:"GenreItems,omitzero"`
	ParentLogoItemID             *UUID                               `json:"ParentLogoItemId,omitzero"`
	ParentBackdropItemID         *UUID                               `json:"ParentBackdropItemId,omitzero"`
	ParentBackdropImageTags      []string                            `json:"ParentBackdropImageTags,omitzero"`
	LocalTrailerCount            *int                                `json:"LocalTrailerCount,omitzero"`
	UserData                     *UserItemDataDto                    `json:"UserData,omitzero"`
	RecursiveItemCount           *int                                `json:"RecursiveItemCount,omitzero"`
	ChildCount                   *int                                `json:"ChildCount,omitzero"`
	SeriesName                   string                              `json:"SeriesName,omitzero"`
	SeriesID                     *UUID                               `json:"SeriesId,omitzero"`
	SeasonID                     *UUID                               `json:"SeasonId,omitzero"`
	SpecialFeatureCount          *int                                `json:"SpecialFeatureCount,omitzero"`
	DisplayPreferencesID         string                              `json:"DisplayPreferencesId,omitzero"`
	Status                       string                              `json:"Status,omitzero"`
	AirTime                      string                              `json:"AirTime,omitzero"`
	AirDays                      []time.Weekday                      `json:"AirDays,omitzero"`
	Tags                         []string                            `json:"Tags,omitzero"`
	PrimaryImageAspectRatio      *float64                            `json:"PrimaryImageAspectRatio,omitzero"`
	Artists                      []string                            `json:"Artists,omitzero"`
	ArtistItems                  []NameGuidPair                      `json:"ArtistItems,omitzero"`
	Album                        string                              `json:"Album,omitzero"`
	CollectionType               *CollectionType                     `json:"CollectionType,omitzero"`
	DisplayOrder                 string                              `json:"DisplayOrder,omitzero"`
	AlbumID                      *UUID                               `json:"AlbumId,omitzero"`
	AlbumPrimaryImageTag         string                              `json:"AlbumPrimaryImageTag,omitzero"`
	SeriesPrimaryImageTag        string                              `json:"SeriesPrimaryImageTag,omitzero"`
	AlbumArtist                  string                              `json:"AlbumArtist,omitzero"`
	AlbumArtists                 []NameGuidPair                      `json:"AlbumArtists,omitzero"`
	SeasonName                   string                              `json:"SeasonName,omitzero"`
	MediaStreams                 []MediaStreamInfo                   `json:"MediaStreams,omitzero"`
	VideoType                    *VideoType                          `json:"VideoType,omitzero"`
	PartCount                    *int                                `json:"PartCount,omitzero"`
	MediaSourceCount             *int                                `json:"MediaSourceCount,omitzero"`
	ImageTags                    map[string]string                   `json:"ImageTags,omitzero"`
	BackdropImageTags            []string                            `json:"BackdropImageTags,omitzero"`
	ScreenshotImageTags          []string                            `json:"ScreenshotImageTags,omitzero"`
	ParentLogoImageTag           string                              `json:"ParentLogoImageTag,omitzero"`
	ParentArtItemID              *UUID                               `json:"ParentArtItemId,omitzero"`
	ParentArtImageTag            string                              `json:"ParentArtImageTag,omitzero"`
	SeriesThumbImageTag          string                              `json:"SeriesThumbImageTag,omitzero"`
	ImageBlurHashes              map[string]map[string]string        `json:"ImageBlurHashes,omitzero"`
	SeriesStudio                 string                              `json:"SeriesStudio,omitzero"`
	ParentThumbItemID            *UUID                               `json:"ParentThumbItemId,omitzero"`
	ParentThumbImageTag          string                              `json:"ParentThumbImageTag,omitzero"`
	ParentPrimaryImageItemID     *UUID                               `json:"ParentPrimaryImageItemId,omitzero"`
	ParentPrimaryImageTag        string                              `json:"ParentPrimaryImageTag,omitzero"`
	Chapters                     []ChapterInfo                       `json:"Chapters,omitzero"`
	Trickplay                    map[string]map[int]TrickplayInfoDto `json:"Trickplay,omitzero"`
	LocationType                 *LocationType                       `json:"LocationType,omitzero"`
	IsoType                      *IsoType                            `json:"IsoType,omitzero"`
	MediaType                    MediaType                           `json:"MediaType"`
	EndDate                      *time.Time                          `json:"EndDate,omitzero"`
	LockedFields                 []MetadataField                     `json:"LockedFields,omitzero"`
	TrailerCount                 *int                                `json:"TrailerCount,omitzero"`
	MovieCount                   *int                                `json:"MovieCount,omitzero"`
	SeriesCount                  *int                                `json:"SeriesCount,omitzero"`
	ProgramCount                 *int                                `json:"ProgramCount,omitzero"`
	EpisodeCount                 *int                                `json:"EpisodeCount,omitzero"`
	SongCount                    *int                                `json:"SongCount,omitzero"`
	AlbumCount                   *int                                `json:"AlbumCount,omitzero"`
	ArtistCount                  *int                                `json:"ArtistCount,omitzero"`
	MusicVideoCount              *int                                `json:"MusicVideoCount,omitzero"`
	LockData                     *bool                               `json:"LockData,omitzero"`
	Width                        *int                                `json:"Width,omitzero"`
	Height                       *int                                `json:"Height,omitzero"`
	CameraMake                   string                              `json:"CameraMake,omitzero"`
	CameraModel                  string                              `json:"CameraModel,omitzero"`
	Software                     string                              `json:"Software,omitzero"`
	ExposureTime                 *float64                            `json:"ExposureTime,omitzero"`
	FocalLength                  *float64                            `json:"FocalLength,omitzero"`
	ImageOrientation             *ImageOrientation                   `json:"ImageOrientation,omitzero"`
	Aperture                     *float64                            `json:"Aperture,omitzero"`
	ShutterSpeed                 *float64                            `json:"ShutterSpeed,omitzero"`
	Latitude                     *float64                            `json:"Latitude,omitzero"`
	Longitude                    *float64                            `json:"Longitude,omitzero"`
	Altitude                     *float64                            `json:"Altitude,omitzero"`
	IsoSpeedRating               *int                                `json:"IsoSpeedRating,omitzero"`
	SeriesTimerID                string                              `json:"SeriesTimerId,omitzero"`
	ProgramID                    string                              `json:"ProgramId,omitzero"`
	ChannelPrimaryImageTag       string                              `json:"ChannelPrimaryImageTag,omitzero"`
	StartDate                    *time.Time                          `json:"StartDate,omitzero"`
	CompletionPercentage         *float64                            `json:"CompletionPercentage,omitzero"`
	IsRepeat                     *bool                               `json:"IsRepeat,omitzero"`
	EpisodeTitle                 string                              `json:"EpisodeTitle,omitzero"`
	ChannelType                  *ChannelType                        `json:"ChannelType,omitzero"`
	Audio                        *ProgramAudio                       `json:"Audio,omitzero"`
	IsMovie                      *bool                               `json:"IsMovie,omitzero"`
	IsSports                     *bool                               `json:"IsSports,omitzero"`
	IsSeries                     *bool                               `json:"IsSeries,omitzero"`
	IsLive                       *bool                               `json:"IsLive,omitzero"`
	IsNews                       *bool                               `json:"IsNews,omitzero"`
	IsKids                       *bool                               `json:"IsKids,omitzero"`
	IsPremiere                   *bool                               `json:"IsPremiere,omitzero"`
	TimerID                      string                              `json:"TimerId,omitzero"`
	NormalizationGain            *float32                            `json:"NormalizationGain,omitzero"`
	AlbumNormalizationGain       *float32                            `json:"AlbumNormalizationGain,omitzero"`
	CurrentProgram               *BaseItemDto                        `json:"CurrentProgram,omitzero"`
	OriginalLanguage             string                              `json:"OriginalLanguage,omitzero"`
}

// QueryResult is a paged API result. Items is always an allocated slice when
// constructed through NewQueryResult so JSON publishes [] rather than null.
type QueryResult[T any] struct {
	Items            []T `json:"Items"`
	TotalRecordCount int `json:"TotalRecordCount"`
	StartIndex       int `json:"StartIndex"`
}

// NewQueryResult creates an API result with an empty, non-nil item list.
func NewQueryResult[T any]() QueryResult[T] {
	return QueryResult[T]{Items: make([]T, 0)}
}

// ThemeMediaResult is one paged theme-media sub-result. It embeds
// QueryResult[BaseItemDto] and adds the owner that supplied the themes.
type ThemeMediaResult struct {
	QueryResult[BaseItemDto]
	OwnerID UUID `json:"OwnerId"`
}

// AllThemeMediaResult is the GetThemeMedia response. It carries the three
// theme-media sub-results per the pinned AllThemeMediaResult schema.
type AllThemeMediaResult struct {
	ThemeVideosResult     ThemeMediaResult `json:"ThemeVideosResult"`
	ThemeSongsResult      ThemeMediaResult `json:"ThemeSongsResult"`
	SoundtrackSongsResult ThemeMediaResult `json:"SoundtrackSongsResult"`
}

// The following support types are the public item-projection wire model. They
// intentionally do not alias internal/playback so response shape remains
// independent from playback orchestration.
type (
	ExternalURL struct {
		URL  string `json:"Url"`
		Name string `json:"Name"`
	}
	MediaURL struct {
		URL  string `json:"Url"`
		Name string `json:"Name"`
	}
)

type MediaSourceInfo struct {
	Protocol                            *MediaProtocol            `json:"Protocol,omitzero"`
	Path                                *string                   `json:"Path,omitzero"`
	EncoderPath                         *string                   `json:"EncoderPath,omitzero"`
	EncoderProtocol                     *MediaProtocol            `json:"EncoderProtocol,omitzero"`
	Type                                *MediaSourceType          `json:"Type,omitzero"`
	Container                           *string                   `json:"Container,omitzero"`
	Size                                *int64                    `json:"Size,omitzero"`
	Name                                *string                   `json:"Name,omitzero"`
	IsRemote                            *bool                     `json:"IsRemote,omitzero"`
	ETag                                *string                   `json:"ETag,omitzero"`
	RunTimeTicks                        *int64                    `json:"RunTimeTicks,omitzero"`
	ReadAtNativeFramerate               *bool                     `json:"ReadAtNativeFramerate,omitzero"`
	IgnoreDts                           *bool                     `json:"IgnoreDts,omitzero"`
	IgnoreIndex                         *bool                     `json:"IgnoreIndex,omitzero"`
	GenPtsInput                         *bool                     `json:"GenPtsInput,omitzero"`
	SupportsTranscoding                 *bool                     `json:"SupportsTranscoding,omitzero"`
	SupportsDirectStream                *bool                     `json:"SupportsDirectStream,omitzero"`
	SupportsDirectPlay                  *bool                     `json:"SupportsDirectPlay,omitzero"`
	IsInfiniteStream                    *bool                     `json:"IsInfiniteStream,omitzero"`
	UseMostCompatibleTranscodingProfile *bool                     `json:"UseMostCompatibleTranscodingProfile,omitzero"`
	RequiresOpening                     *bool                     `json:"RequiresOpening,omitzero"`
	OpenToken                           *string                   `json:"OpenToken,omitzero"`
	RequiresClosing                     *bool                     `json:"RequiresClosing,omitzero"`
	LiveStreamID                        *string                   `json:"LiveStreamId,omitzero"`
	BufferMs                            *int                      `json:"BufferMs,omitzero"`
	RequiresLooping                     *bool                     `json:"RequiresLooping,omitzero"`
	SupportsProbing                     *bool                     `json:"SupportsProbing,omitzero"`
	VideoType                           *MediaVideoType           `json:"VideoType,omitzero"`
	IsoType                             *MediaIsoType             `json:"IsoType,omitzero"`
	Video3DFormat                       *MediaVideo3DFormat       `json:"Video3DFormat,omitzero"`
	Bitrate                             *int                      `json:"Bitrate,omitzero"`
	FallbackMaxStreamingBitrate         *int                      `json:"FallbackMaxStreamingBitrate,omitzero"`
	Timestamp                           *TransportStreamTimestamp `json:"Timestamp,omitzero"`
	TranscodingURL                      *string                   `json:"TranscodingUrl,omitzero"`
	TranscodingSubProtocol              *MediaStreamProtocol      `json:"TranscodingSubProtocol,omitzero"`
	TranscodingContainer                *string                   `json:"TranscodingContainer,omitzero"`
	AnalyzeDurationMs                   *int                      `json:"AnalyzeDurationMs,omitzero"`
	DefaultAudioStreamIndex             *int                      `json:"DefaultAudioStreamIndex,omitzero"`
	DefaultSubtitleStreamIndex          *int                      `json:"DefaultSubtitleStreamIndex,omitzero"`
	HasSegments                         *bool                     `json:"HasSegments,omitzero"`
	ID                                  *string                   `json:"Id,omitzero"`
	MediaStreams                        []MediaStreamInfo         `json:"MediaStreams,omitzero"`
	MediaAttachments                    []MediaAttachment         `json:"MediaAttachments,omitzero"`
	Formats                             []string                  `json:"Formats,omitzero"`
	RequiredHTTPHeaders                 map[string]string         `json:"RequiredHttpHeaders,omitzero"`
}

type MediaAttachment struct {
	Codec       *string `json:"Codec,omitzero"`
	CodecTag    *string `json:"CodecTag,omitzero"`
	Comment     *string `json:"Comment,omitzero"`
	Index       int     `json:"Index"`
	FileName    *string `json:"FileName,omitzero"`
	MimeType    *string `json:"MimeType,omitzero"`
	DeliveryURL *string `json:"DeliveryUrl,omitzero"`
}

type MediaStreamInfo struct {
	Codec                     *string                 `json:"Codec,omitzero"`
	CodecTag                  *string                 `json:"CodecTag,omitzero"`
	Language                  *string                 `json:"Language,omitzero"`
	ColorRange                *string                 `json:"ColorRange,omitzero"`
	ColorSpace                *string                 `json:"ColorSpace,omitzero"`
	ColorTransfer             *string                 `json:"ColorTransfer,omitzero"`
	ColorPrimaries            *string                 `json:"ColorPrimaries,omitzero"`
	DvVersionMajor            *int                    `json:"DvVersionMajor,omitzero"`
	DvVersionMinor            *int                    `json:"DvVersionMinor,omitzero"`
	DvProfile                 *int                    `json:"DvProfile,omitzero"`
	DvLevel                   *int                    `json:"DvLevel,omitzero"`
	RpuPresentFlag            *int                    `json:"RpuPresentFlag,omitzero"`
	ElPresentFlag             *int                    `json:"ElPresentFlag,omitzero"`
	BlPresentFlag             *int                    `json:"BlPresentFlag,omitzero"`
	DvBlSignalCompatibilityID *int                    `json:"DvBlSignalCompatibilityId,omitzero"`
	Rotation                  *int                    `json:"Rotation,omitzero"`
	Comment                   *string                 `json:"Comment,omitzero"`
	TimeBase                  *string                 `json:"TimeBase,omitzero"`
	CodecTimeBase             *string                 `json:"CodecTimeBase,omitzero"`
	Title                     *string                 `json:"Title,omitzero"`
	Hdr10PlusPresentFlag      *bool                   `json:"Hdr10PlusPresentFlag,omitzero"`
	NalLengthSize             *string                 `json:"NalLengthSize,omitzero"`
	IsInterlaced              *bool                   `json:"IsInterlaced,omitzero"`
	IsAVC                     *bool                   `json:"IsAVC,omitzero"`
	ChannelLayout             *string                 `json:"ChannelLayout,omitzero"`
	BitRate                   *int                    `json:"BitRate,omitzero"`
	BitDepth                  *int                    `json:"BitDepth,omitzero"`
	RefFrames                 *int                    `json:"RefFrames,omitzero"`
	PacketLength              *int                    `json:"PacketLength,omitzero"`
	Channels                  *int                    `json:"Channels,omitzero"`
	SampleRate                *int                    `json:"SampleRate,omitzero"`
	IsDefault                 *bool                   `json:"IsDefault,omitzero"`
	IsForced                  *bool                   `json:"IsForced,omitzero"`
	IsHearingImpaired         *bool                   `json:"IsHearingImpaired,omitzero"`
	IsOriginal                *bool                   `json:"IsOriginal,omitzero"`
	Height                    *int                    `json:"Height,omitzero"`
	Width                     *int                    `json:"Width,omitzero"`
	AverageFrameRate          *float32                `json:"AverageFrameRate,omitzero"`
	RealFrameRate             *float32                `json:"RealFrameRate,omitzero"`
	ReferenceFrameRate        *float32                `json:"ReferenceFrameRate,omitzero"`
	Profile                   *string                 `json:"Profile,omitzero"`
	Type                      MediaStreamType         `json:"Type"`
	AspectRatio               *string                 `json:"AspectRatio,omitzero"`
	Index                     int                     `json:"Index"`
	Score                     *int                    `json:"Score,omitzero"`
	IsExternal                *bool                   `json:"IsExternal,omitzero"`
	DeliveryMethod            *SubtitleDeliveryMethod `json:"DeliveryMethod,omitzero"`
	DeliveryUrl               *string                 `json:"DeliveryUrl,omitzero"`
	IsExternalURL             *bool                   `json:"IsExternalUrl,omitzero"`
	SupportsExternalStream    *bool                   `json:"SupportsExternalStream,omitzero"`
	Path                      *string                 `json:"Path,omitzero"`
	PixelFormat               *string                 `json:"PixelFormat,omitzero"`
	Level                     *float64                `json:"Level,omitzero"`
	IsAnamorphic              *bool                   `json:"IsAnamorphic,omitzero"`
	LocalizedUndefined        *string                 `json:"LocalizedUndefined,omitzero"`
	LocalizedDefault          *string                 `json:"LocalizedDefault,omitzero"`
	LocalizedForced           *string                 `json:"LocalizedForced,omitzero"`
	LocalizedExternal         *string                 `json:"LocalizedExternal,omitzero"`
	LocalizedHearingImpaired  *string                 `json:"LocalizedHearingImpaired,omitzero"`
	LocalizedLanguage         *string                 `json:"LocalizedLanguage,omitzero"`
	LocalizedOriginal         *string                 `json:"LocalizedOriginal,omitzero"`
	VideoRange                *string                 `json:"VideoRange,omitzero"`
	VideoRangeType            *string                 `json:"VideoRangeType,omitzero"`
	VideoDoViTitle            *string                 `json:"VideoDoViTitle,omitzero"`
	AudioSpatialFormat        *string                 `json:"AudioSpatialFormat,omitzero"`
	DisplayTitle              *string                 `json:"DisplayTitle,omitzero"`
	IsTextSubtitleStream      *bool                   `json:"IsTextSubtitleStream,omitzero"`
}

type (
	MediaProtocol            string
	MediaSourceType          string
	MediaVideoType           string
	MediaIsoType             string
	MediaVideo3DFormat       string
	MediaStreamProtocol      string
	TransportStreamTimestamp string
	MediaStreamType          string
	SubtitleDeliveryMethod   string
)

type (
	BaseItemPerson struct {
		ID              UUID                         `json:"Id"`
		Name            string                       `json:"Name"`
		Role            string                       `json:"Role"`
		Type            string                       `json:"Type"`
		PrimaryImageTag *string                      `json:"PrimaryImageTag,omitempty"`
		ImageBlurHashes map[string]map[string]string `json:"ImageBlurHashes,omitempty"`
	}
	NameGuidPair struct {
		Name string `json:"Name"`
		ID   UUID   `json:"Id"`
	}
	UserItemDataDto struct {
		Rating                *float32 `json:"Rating,omitzero"`
		PlayedPercentage      *float64 `json:"PlayedPercentage,omitzero"`
		UnplayedItemCount     *int     `json:"UnplayedItemCount,omitzero"`
		PlaybackPositionTicks int64    `json:"PlaybackPositionTicks"`
		PlayCount             int      `json:"PlayCount"`
		IsFavorite            bool     `json:"IsFavorite"`
		Likes                 *bool    `json:"Likes,omitzero"`
		LastPlayedDate        *Time    `json:"LastPlayedDate,omitzero"`
		Played                bool     `json:"Played"`
		Key                   string   `json:"Key"`
		ItemID                UUID     `json:"ItemId"`
	}
)

type (
	ChapterInfo struct {
		StartPositionTicks int64  `json:"StartPositionTicks"`
		Name               string `json:"Name"`
	}
	TrickplayInfoDto struct {
		Width  int `json:"Width"`
		Height int `json:"Height"`
	}
	PlayAccess   string
	LocationType string
)

// These scalar enums preserve BaseItemDto's independent wire model without
// importing internal/item, which depends on dto.
type (
	ExtraType        string
	Video3DFormat    string
	Kind             string
	CollectionType   string
	VideoType        string
	IsoType          string
	MediaType        string
	MetadataField    string
	ImageOrientation string
	ChannelType      string
	ProgramAudio     string
)
