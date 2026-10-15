// SPDX-License-Identifier: GPL-2.0-only

package store

// ItemQueryPropertyOwner identifies the pinned layer responsible for an input.
// SQL ownership is deliberately distinct from current implementation status.
type ItemQueryPropertyOwner string

const (
	ItemQueryPropertyOwnerSQL             ItemQueryPropertyOwner = "sql"
	ItemQueryPropertyOwnerService         ItemQueryPropertyOwner = "service"
	ItemQueryPropertyOwnerMaterialization ItemQueryPropertyOwner = "materialization"
	ItemQueryPropertyOwnerDropped         ItemQueryPropertyOwner = "dropped"
)

// ItemQueryPropertyClassification separates source ownership from Deadfin's
// current support. SQL-owned fields with Implemented false are pinned work for
// a future translator slice, not silently unsupported behavior.
type ItemQueryPropertyClassification struct {
	Owner       ItemQueryPropertyOwner
	Implemented bool
}

// ItemQueryProperty maps one input to its Go field. SourceRef is a stable path
// and line in the pinned source corpus. Empty SourceName denotes a legacy
// Deadfin extension, never an invented upstream identity.
type ItemQueryProperty struct {
	SourceName     string
	GoName         string
	Classification ItemQueryPropertyClassification
	SourceRef      string
}

// itemQueryProperties is immutable after package initialization. It has one
// row for each public InternalItemsQuery get/set input; HasFilters and write-only
// Parent are excluded. MinSortName is intentionally dropped: the pinned tree
// only declares it (and HasFilters observes it), with no repository, service,
// or materialization consumer.
var itemQueryProperties = [...]ItemQueryProperty{
	{SourceName: "Recursive", GoName: "Recursive", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:171"},
	{SourceName: "StartIndex", GoName: "StartIndex", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:173"},
	{SourceName: "Limit", GoName: "Limit", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:175"},
	{SourceName: "User", GoName: "User", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:177"},
	{SourceName: "IsFolder", GoName: "IsFolder", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:179"},
	{SourceName: "IsFavorite", GoName: "IsFavorite", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:181"},
	{SourceName: "IsFavoriteOrLiked", GoName: "IsFavoriteOrLiked", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:183"},
	{SourceName: "IsLiked", GoName: "IsLiked", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:185"},
	{SourceName: "IsPlayed", GoName: "IsPlayed", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:187"},
	{SourceName: "IsResumable", GoName: "IsResumable", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:189"},
	{SourceName: "IncludeItemsByName", GoName: "IncludeItemsByName", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:191"},
	{SourceName: "MediaTypes", GoName: "MediaTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:193"},
	{SourceName: "IncludeItemTypes", GoName: "IncludeItemTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:195"},
	{SourceName: "ExcludeItemTypes", GoName: "ExcludeItemTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:197"},
	{SourceName: "ExcludeTags", GoName: "ExcludeTags", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:199"},
	{SourceName: "ExcludeInheritedTags", GoName: "ExcludeInheritedTags", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:201"},
	{SourceName: "IncludeInheritedTags", GoName: "IncludeInheritedTags", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:203"},
	{SourceName: "Genres", GoName: "Genres", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:205"},
	{SourceName: "IsSpecialSeason", GoName: "IsSpecialSeason", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:207"},
	{SourceName: "IsMissing", GoName: "IsMissing", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:209"},
	{SourceName: "IsUnaired", GoName: "IsUnaired", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:211"},
	{SourceName: "CollapseBoxSetItems", GoName: "CollapseBoxSetItems", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:213"},
	{SourceName: "CollapseBoxSetItemTypes", GoName: "CollapseBoxSetItemTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:219"},
	{SourceName: "NameStartsWithOrGreater", GoName: "NameStartsWithOrGreater", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:221"},
	{SourceName: "NameStartsWith", GoName: "NameStartsWith", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:223"},
	{SourceName: "NameLessThan", GoName: "NameLessThan", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:225"},
	{SourceName: "NameContains", GoName: "NameContains", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:227"},
	{SourceName: "MinSortName", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerDropped}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:229"},
	{SourceName: "PresentationUniqueKey", GoName: "PresentationUniqueKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:231"},
	{SourceName: "Path", GoName: "Path", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:233"},
	{SourceName: "Name", GoName: "Name", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:235"},
	{SourceName: "UseRawName", GoName: "UseRawName", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:237"},
	{SourceName: "Person", GoName: "Person", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:239"},
	{SourceName: "PersonIds", GoName: "PersonIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:241"},
	{SourceName: "ItemIds", GoName: "ItemIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:243"},
	{SourceName: "OwnerIds", GoName: "OwnerIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:245"},
	{SourceName: "ExtraTypes", GoName: "ExtraTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:247"},
	{SourceName: "ExcludeItemIds", GoName: "ExcludeItemIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:249"},
	{SourceName: "AdjacentTo", GoName: "AdjacentTo", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:251"},
	{SourceName: "PersonTypes", GoName: "PersonTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:253"},
	{SourceName: "Is3D", GoName: "Is3D", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:255"},
	{SourceName: "IsHD", GoName: "IsHD", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:257"},
	{SourceName: "IsLocked", GoName: "IsLocked", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:259"},
	{SourceName: "IsPlaceHolder", GoName: "IsPlaceHolder", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:261"},
	{SourceName: "HasImdbId", GoName: "HasIMDbID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:263"},
	{SourceName: "HasOverview", GoName: "HasOverview", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:265"},
	{SourceName: "HasTmdbId", GoName: "HasTMDbID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:267"},
	{SourceName: "HasOfficialRating", GoName: "HasOfficialRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:269"},
	{SourceName: "HasTvdbId", GoName: "HasTVDbID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:271"},
	{SourceName: "HasThemeSong", GoName: "HasThemeSong", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:273"},
	{SourceName: "HasThemeVideo", GoName: "HasThemeVideo", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:275"},
	{SourceName: "HasSubtitles", GoName: "HasSubtitles", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:277"},
	{SourceName: "HasSpecialFeature", GoName: "HasSpecialFeature", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:279"},
	{SourceName: "HasTrailer", GoName: "HasTrailer", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:281"},
	{SourceName: "HasParentalRating", GoName: "HasParentalRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:283"},
	{SourceName: "StudioIds", GoName: "StudioIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:285"},
	{SourceName: "GenreIds", GoName: "GenreIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:287"},
	{SourceName: "ImageTypes", GoName: "ImageTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:289"},
	{SourceName: "VideoTypes", GoName: "VideoTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:291"},
	{SourceName: "BlockUnratedItems", GoName: "BlockUnratedItems", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:293"},
	{SourceName: "Years", GoName: "Years", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:295"},
	{SourceName: "Tags", GoName: "Tags", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:297"},
	{SourceName: "OfficialRatings", GoName: "OfficialRatings", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:299"},
	{SourceName: "MinPremiereDate", GoName: "MinPremiereDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:301"},
	{SourceName: "MaxPremiereDate", GoName: "MaxPremiereDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:303"},
	{SourceName: "MinStartDate", GoName: "MinStartDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:305"},
	{SourceName: "MaxStartDate", GoName: "MaxStartDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:307"},
	{SourceName: "MinEndDate", GoName: "MinEndDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:309"},
	{SourceName: "MaxEndDate", GoName: "MaxEndDate", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:311"},
	{SourceName: "IsAiring", GoName: "IsAiring", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:313"},
	{SourceName: "IsMovie", GoName: "IsMovie", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:315"},
	{SourceName: "IsSports", GoName: "IsSports", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:317"},
	{SourceName: "IsKids", GoName: "IsKids", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:319"},
	{SourceName: "IsNews", GoName: "IsNews", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:321"},
	{SourceName: "IsSeries", GoName: "IsSeries", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:323"},
	{SourceName: "MinIndexNumber", GoName: "MinIndexNumber", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:325"},
	{SourceName: "MinParentAndIndexNumber", GoName: "MinParentAndIndexNumber", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:335"},
	{SourceName: "AiredDuringSeason", GoName: "AiredDuringSeason", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:337"},
	{SourceName: "MinCriticRating", GoName: "MinCriticRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:339"},
	{SourceName: "MinCommunityRating", GoName: "MinCommunityRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:341"},
	{SourceName: "ChannelIds", GoName: "ChannelIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:343"},
	{SourceName: "ParentIndexNumber", GoName: "ParentIndexNumber", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:345"},
	{SourceName: "ParentIndexNumberNotEquals", GoName: "ParentIndexNumberNotEquals", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:347"},
	{SourceName: "IndexNumber", GoName: "IndexNumber", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:349"},
	{SourceName: "MinParentalRating", GoName: "MinParentalRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:351"},
	{SourceName: "MaxParentalRating", GoName: "MaxParentalRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:353"},
	{SourceName: "HasDeadParentId", GoName: "HasDeadParentID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:355"},
	{SourceName: "IsVirtualItem", GoName: "IsVirtualItem", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:357"},
	{SourceName: "ParentId", GoName: "ParentID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:359"},
	{SourceName: "ParentType", GoName: "ParentType", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:361"},
	{SourceName: "AncestorIds", GoName: "AncestorIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:363"},
	{SourceName: "LinkedChildAncestorIds", GoName: "LinkedChildAncestorIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:369"},
	{SourceName: "TopParentIds", GoName: "TopParentIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:371"},
	{SourceName: "PresetViews", GoName: "PresetViews", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:373"},
	{SourceName: "TrailerTypes", GoName: "TrailerTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:375"},
	{SourceName: "SourceTypes", GoName: "SourceTypes", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:377"},
	{SourceName: "SeriesStatuses", GoName: "SeriesStatuses", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:379"},
	{SourceName: "ExternalSeriesId", GoName: "ExternalSeriesID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:381"},
	{SourceName: "ExternalId", GoName: "ExternalID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:383"},
	{SourceName: "AlbumIds", GoName: "AlbumIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:385"},
	{SourceName: "ArtistIds", GoName: "ArtistIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:387"},
	{SourceName: "ExcludeArtistIds", GoName: "ExcludeArtistIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:389"},
	{SourceName: "AncestorWithPresentationUniqueKey", GoName: "AncestorWithPresentationUniqueKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:391"},
	{SourceName: "SeriesPresentationUniqueKey", GoName: "SeriesPresentationUniqueKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:393"},
	{SourceName: "GroupByPresentationUniqueKey", GoName: "GroupByPresentationUniqueKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:395"},
	{SourceName: "GroupBySeriesPresentationUniqueKey", GoName: "GroupBySeriesPresentationUniqueKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:397"},
	{SourceName: "EnableTotalRecordCount", GoName: "EnableTotalRecordCount", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:399"},
	{SourceName: "ForceDirect", GoName: "ForceDirect", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:401"},
	{SourceName: "ExcludeProviderIds", GoName: "ExcludeProviderIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:403"},
	{SourceName: "EnableGroupByMetadataKey", GoName: "EnableGroupByMetadataKey", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:405"},
	{SourceName: "HasChapterImages", GoName: "HasChapterImages", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:407"},
	{SourceName: "OrderBy", GoName: "OrderBy", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:409"},
	{SourceName: "MinDateCreated", GoName: "MinDateCreated", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:411"},
	{SourceName: "MinDateLastSaved", GoName: "MinDateLastSaved", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:413"},
	{SourceName: "MinDateLastSavedForUser", GoName: "MinDateLastSavedForUser", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:415"},
	{SourceName: "DtoOptions", GoName: "DtoOptions", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerMaterialization}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:417"},
	{SourceName: "HasNoAudioTrackWithLanguage", GoName: "HasNoAudioTrackWithLanguage", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:419"},
	{SourceName: "HasNoInternalSubtitleTrackWithLanguage", GoName: "HasNoInternalSubtitleTrackWithLanguage", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:421"},
	{SourceName: "HasNoExternalSubtitleTrackWithLanguage", GoName: "HasNoExternalSubtitleTrackWithLanguage", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:423"},
	{SourceName: "HasNoSubtitleTrackWithLanguage", GoName: "HasNoSubtitleTrackWithLanguage", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:425"},
	{SourceName: "IsDeadArtist", GoName: "IsDeadArtist", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:427"},
	{SourceName: "IsDeadStudio", GoName: "IsDeadStudio", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:429"},
	{SourceName: "IsDeadGenre", GoName: "IsDeadGenre", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:431"},
	{SourceName: "IsDeadPerson", GoName: "IsDeadPerson", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:433"},
	{SourceName: "DisplayAlbumFolders", GoName: "DisplayAlbumFolders", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:438"},
	{SourceName: "HasAnyProviderId", GoName: "HasAnyProviderID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:457"},
	{SourceName: "HasAnyProviderIds", GoName: "HasAnyProviderIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:459"},
	{SourceName: "AlbumArtistIds", GoName: "AlbumArtistIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:461"},
	{SourceName: "BoxSetLibraryFolders", GoName: "BoxSetLibraryFolders", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:463"},
	{SourceName: "ContributingArtistIds", GoName: "ContributingArtistIDs", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:465"},
	{SourceName: "HasAired", GoName: "HasAired", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:467"},
	{SourceName: "HasOwnerId", GoName: "HasOwnerID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:469"},
	{SourceName: "IncludeOwnedItems", GoName: "IncludeOwnedItems", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:475"},
	{SourceName: "Is4K", GoName: "Is4K", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:477"},
	{SourceName: "MaxHeight", GoName: "MaxHeight", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:479"},
	{SourceName: "MaxWidth", GoName: "MaxWidth", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:481"},
	{SourceName: "MinHeight", GoName: "MinHeight", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:483"},
	{SourceName: "MinWidth", GoName: "MinWidth", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:485"},
	{SourceName: "SearchTerm", GoName: "SearchTerm", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:487"},
	{SourceName: "SeriesTimerId", GoName: "SeriesTimerID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerService}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:489"},
	{SourceName: "SkipDeserialization", GoName: "SkipDeserialization", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerMaterialization}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:491"},
	{SourceName: "IncludeExtras", GoName: "IncludeExtras", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerMaterialization}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:493"},
	{SourceName: "AudioLanguages", GoName: "AudioLanguages", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:495"},
	{SourceName: "SubtitleLanguages", GoName: "SubtitleLanguages", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL}, SourceRef: "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs:497"},
	// Existing Deadfin query extensions have no upstream source identity.
	{GoName: "Studios", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:156"},
	{GoName: "MaxCommunityRating", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:197"},
	{GoName: "MinRunTimeTicks", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:198"},
	{GoName: "MaxRunTimeTicks", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:199"},
	{GoName: "ReferenceTime", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:267"},
	{GoName: "UserID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:268"},
	{GoName: "DisableLiveTVChannelUserDataName", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:269"},
	{GoName: "SortBy", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:270"},
	{GoName: "SortOrder", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:271"},
	{GoName: "EpisodesOfSeasonID", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:309"},
	{GoName: "ExcludeSpecialsFromSeasons", Classification: ItemQueryPropertyClassification{Owner: ItemQueryPropertyOwnerSQL, Implemented: true}, SourceRef: "deadfin/internal/store/itemquery.go:314"},
}

// ItemQueryProperties returns a caller-owned copy of the immutable contract.
func ItemQueryProperties() []ItemQueryProperty {
	return append([]ItemQueryProperty(nil), itemQueryProperties[:]...)
}
