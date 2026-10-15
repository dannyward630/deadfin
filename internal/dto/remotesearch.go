// SPDX-License-Identifier: GPL-2.0-only

package dto

// RemoteSearchResult is the wire shape returned by the nine typed
// POST /Items/RemoteSearch/{Type} endpoints, mirroring the pinned
// RemoteSearchResult schema (jellyfin-openapi-stable.json). Nullable fields
// are pointers, matching upstream's nullable annotations.
type RemoteSearchResult struct {
	Name               *string              `json:"Name,omitempty"`
	ProviderIDs        map[string]string    `json:"ProviderIds,omitempty"`
	ProductionYear     *int                 `json:"ProductionYear,omitempty"`
	IndexNumber        *int                 `json:"IndexNumber,omitempty"`
	IndexNumberEnd     *int                 `json:"IndexNumberEnd,omitempty"`
	ParentIndexNumber  *int                 `json:"ParentIndexNumber,omitempty"`
	PremiereDate       *Time                `json:"PremiereDate,omitempty"`
	ImageURL           *string              `json:"ImageUrl,omitempty"`
	SearchProviderName *string              `json:"SearchProviderName,omitempty"`
	Overview           *string              `json:"Overview,omitempty"`
	AlbumArtist        *RemoteSearchResult  `json:"AlbumArtist,omitempty"`
	Artists            []RemoteSearchResult `json:"Artists,omitempty"`
}

// MovieInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/Movie.
type MovieInfoRemoteSearchQuery struct {
	SearchInfo               *MovieInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID       `json:"ItemId"`
	SearchProviderName       *string    `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool       `json:"IncludeDisabledProviders"`
}

// SeriesInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/Series.
type SeriesInfoRemoteSearchQuery struct {
	SearchInfo               *SeriesInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID        `json:"ItemId"`
	SearchProviderName       *string     `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool        `json:"IncludeDisabledProviders"`
}

// BoxSetInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/BoxSet.
type BoxSetInfoRemoteSearchQuery struct {
	SearchInfo               *BoxSetInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID        `json:"ItemId"`
	SearchProviderName       *string     `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool        `json:"IncludeDisabledProviders"`
}

// PersonLookupInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/Person.
type PersonLookupInfoRemoteSearchQuery struct {
	SearchInfo               *PersonLookupInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID              `json:"ItemId"`
	SearchProviderName       *string           `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool              `json:"IncludeDisabledProviders"`
}

// BookInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/Book.
type BookInfoRemoteSearchQuery struct {
	SearchInfo               *BookInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID      `json:"ItemId"`
	SearchProviderName       *string   `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool      `json:"IncludeDisabledProviders"`
}

// MusicArtistInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/MusicArtist.
type MusicArtistInfoRemoteSearchQuery struct {
	SearchInfo               *ArtistInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID        `json:"ItemId"`
	SearchProviderName       *string     `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool        `json:"IncludeDisabledProviders"`
}

// AlbumInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/MusicAlbum.
type AlbumInfoRemoteSearchQuery struct {
	SearchInfo               *AlbumInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID       `json:"ItemId"`
	SearchProviderName       *string    `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool       `json:"IncludeDisabledProviders"`
}

// TrailerInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/Trailer.
type TrailerInfoRemoteSearchQuery struct {
	SearchInfo               *TrailerInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID         `json:"ItemId"`
	SearchProviderName       *string      `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool         `json:"IncludeDisabledProviders"`
}

// MusicVideoInfoRemoteSearchQuery is the body of POST /Items/RemoteSearch/MusicVideo.
type MusicVideoInfoRemoteSearchQuery struct {
	SearchInfo               *MusicVideoInfo `json:"SearchInfo,omitempty"`
	ItemID                   UUID            `json:"ItemId"`
	SearchProviderName       *string         `json:"SearchProviderName,omitempty"`
	IncludeDisabledProviders bool            `json:"IncludeDisabledProviders"`
}

// MovieInfo mirrors MovieInfo in the pinned OpenAPI — the lookup info for movies.
type MovieInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
}

// SeriesInfo mirrors SeriesInfo — the lookup info for series.
type SeriesInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
}

// BoxSetInfo mirrors BoxSetInfo — the lookup info for box sets.
type BoxSetInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
}

// BookInfo mirrors BookInfo — adds SeriesName for the book series it belongs to.
type BookInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
	SeriesName          *string           `json:"SeriesName,omitempty"`
}

// TrailerInfo mirrors TrailerInfo — the lookup info for trailers.
type TrailerInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
}

// MusicVideoInfo mirrors MusicVideoInfo — adds Artists.
type MusicVideoInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
	Artists             []string          `json:"Artists,omitempty"`
}

// PersonLookupInfo mirrors PersonLookupInfo — the lookup info for persons.
type PersonLookupInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
}

// ArtistInfo mirrors ArtistInfo — the lookup info for music artists. Adds SongInfos.
type ArtistInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
	SongInfos           []SongInfo        `json:"SongInfos,omitempty"`
}

// AlbumInfo mirrors AlbumInfo — adds AlbumArtists, ArtistProviderIds, SongInfos.
type AlbumInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
	AlbumArtists        []string          `json:"AlbumArtists,omitempty"`
	ArtistProviderIDs   map[string]string `json:"ArtistProviderIds,omitempty"`
	SongInfos           []SongInfo        `json:"SongInfos,omitempty"`
}

// SongInfo mirrors SongInfo — a track-level lookup for album/artist search.
type SongInfo struct {
	Name                *string           `json:"Name,omitempty"`
	OriginalTitle       *string           `json:"OriginalTitle,omitempty"`
	Path                *string           `json:"Path,omitempty"`
	MetadataLanguage    *string           `json:"MetadataLanguage,omitempty"`
	MetadataCountryCode *string           `json:"MetadataCountryCode,omitempty"`
	ProviderIDs         map[string]string `json:"ProviderIds,omitempty"`
	Year                *int              `json:"Year,omitempty"`
	IndexNumber         *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber,omitempty"`
	PremiereDate        *Time             `json:"PremiereDate,omitempty"`
	IsAutomated         bool              `json:"IsAutomated"`
	AlbumArtists        []string          `json:"AlbumArtists,omitempty"`
	Album               *string           `json:"Album,omitempty"`
	Artists             []string          `json:"Artists,omitempty"`
}
