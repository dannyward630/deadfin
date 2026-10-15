// SPDX-License-Identifier: GPL-2.0-only

package item

import "path/filepath"

// KindDef is the immutable-by-construction default definition for one Kind.
// Defaults represent a zero-value upstream item; item-aware functions below
// resolve the few capabilities whose upstream value depends on item state.
type KindDef struct {
	Kind                        Kind
	Name                        string
	DefaultIsFolder             bool
	DefaultMediaType            MediaType
	SupportsPeople              bool
	SupportsAncestors           bool
	SupportsUserData            bool
	DefaultSupportsPlayedStatus bool
	IsDisplayOnly               bool
	ClientTypeName              string
}

// Capabilities is the effective capability set for an item.
type Capabilities struct {
	IsFolder             bool
	MediaType            MediaType
	SupportsPeople       bool
	SupportsAncestors    bool
	SupportsUserData     bool
	SupportsPlayedStatus bool
	IsDisplayOnly        bool
}

// Definition returns the complete default definition for k. Its switch returns
// a fresh value on every call, so callers cannot mutate shared definition state.
func Definition(k Kind) (KindDef, bool) {
	const userData = true

	switch k {
	case KindAggregateFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindAudio:
		return kindDefinition(k, false, MediaTypeAudio, true, true, userData, true, false, ""), true
	case KindAudioBook:
		return kindDefinition(k, false, MediaTypeAudio, true, true, userData, true, false, ""), true
	case KindBasePluginFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, ""), true
	case KindBook:
		return kindDefinition(k, false, MediaTypeBook, true, true, userData, true, false, ""), true
	case KindBoxSet:
		return kindDefinition(k, true, MediaTypeUnknown, true, true, userData, true, false, ""), true
	case KindChannel:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, ""), true
	case KindChannelFolderItem:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, ""), true
	case KindCollectionFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindEpisode:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, ""), true
	case KindGenre:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, true, ""), true
	case KindManualPlaylistsFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, ""), true
	case KindMovie:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindLiveTvChannel:
		return kindDefinition(k, false, MediaTypeVideo, false, true, userData, false, false, "TvChannel"), true
	case KindLiveTvProgram:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, false, "Program"), true
	case KindMusicAlbum:
		return kindDefinition(k, true, MediaTypeUnknown, true, true, userData, false, false, ""), true
	case KindMusicArtist:
		return kindDefinition(k, false, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindMusicGenre:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, true, ""), true
	case KindMusicVideo:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindPerson:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, true, ""), true
	case KindPhoto:
		return kindDefinition(k, false, MediaTypePhoto, false, true, userData, false, false, ""), true
	case KindPhotoAlbum:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindPlaylist:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindPlaylistsFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, true, false, "ManualPlaylistsFolder"), true
	case KindProgram:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, false, ""), true
	case KindRecording:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindSeason:
		return kindDefinition(k, true, MediaTypeUnknown, true, true, userData, true, false, ""), true
	case KindSeries:
		return kindDefinition(k, true, MediaTypeUnknown, true, true, userData, true, false, ""), true
	case KindStudio:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, true, ""), true
	case KindTrailer:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindTvChannel:
		return kindDefinition(k, false, MediaTypeVideo, false, true, userData, false, false, ""), true
	case KindTvProgram:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, false, "Program"), true
	case KindUserRootFolder:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindUserView:
		return kindDefinition(k, true, MediaTypeUnknown, false, true, userData, false, false, ""), true
	case KindVideo:
		return kindDefinition(k, false, MediaTypeVideo, true, true, userData, true, false, ""), true
	case KindYear:
		return kindDefinition(k, false, MediaTypeUnknown, false, false, userData, false, true, ""), true
	default:
		return KindDef{}, false
	}
}

func kindDefinition(k Kind, folder bool, media MediaType, people bool, ancestors bool, userData bool, played bool, displayOnly bool, clientTypeName string) KindDef {
	return KindDef{
		Kind:                        k,
		Name:                        k.String(),
		DefaultIsFolder:             folder,
		DefaultMediaType:            media,
		SupportsPeople:              people,
		SupportsAncestors:           ancestors,
		SupportsUserData:            userData,
		DefaultSupportsPlayedStatus: played,
		IsDisplayOnly:               displayOnly,
		ClientTypeName:              clientTypeName,
	}
}

// DefaultCapabilities returns the default capabilities for a zero-value item
// of k. For item-dependent capabilities, use CapabilitiesOf or the narrow
// functions below.
func DefaultCapabilities(k Kind) (Capabilities, bool) {
	def, ok := Definition(k)
	if !ok {
		return Capabilities{}, false
	}
	return capabilitiesFromDefinition(def), true
}

func capabilitiesFromDefinition(def KindDef) Capabilities {
	return Capabilities{
		IsFolder:             def.DefaultIsFolder,
		MediaType:            def.DefaultMediaType,
		SupportsPeople:       def.SupportsPeople,
		SupportsAncestors:    def.SupportsAncestors,
		SupportsUserData:     def.SupportsUserData,
		SupportsPlayedStatus: def.DefaultSupportsPlayedStatus,
		IsDisplayOnly:        def.IsDisplayOnly,
	}
}

// CapabilitiesOf returns effective capabilities for it. Unknown kinds safely
// return the zero capability set.
func CapabilitiesOf(it Item) Capabilities {
	def, ok := Definition(it.Kind)
	if !ok {
		return Capabilities{}
	}

	caps := capabilitiesFromDefinition(def)
	switch it.Kind {
	case KindMusicArtist:
		caps.IsFolder = it.ParentID != nil && !it.ParentID.IsNil()
	case KindPlaylist:
		if it.MediaType != nil {
			caps.MediaType = *it.MediaType
			caps.SupportsPlayedStatus = *it.MediaType == MediaTypeVideo
		}
	case KindLiveTvChannel, KindTvChannel:
		if it.Extra.LiveTV != nil && it.Extra.LiveTV.ChannelType == ChannelTypeRadio {
			caps.MediaType = MediaTypeAudio
		} else {
			caps.MediaType = MediaTypeVideo
		}
	}
	return caps
}

// IsFolder reports whether it is effectively a folder.
func IsFolder(it Item) bool { return CapabilitiesOf(it).IsFolder }

// MediaTypeOf reports the effective media type, or MediaTypeUnknown for an
// unknown kind.
func MediaTypeOf(it Item) MediaType { return CapabilitiesOf(it).MediaType }

// SupportsPlayedStatus reports whether it supports played status.
func SupportsPlayedStatus(it Item) bool { return CapabilitiesOf(it).SupportsPlayedStatus }

// SupportsPeople reports whether it supports people metadata.
func SupportsPeople(it Item) bool { return CapabilitiesOf(it).SupportsPeople }

// SupportsAncestors reports whether it supports ancestor traversal.
func SupportsAncestors(it Item) bool { return CapabilitiesOf(it).SupportsAncestors }

// KindsWithoutAncestors returns every defined kind whose default
// SupportsAncestors capability is false. The recursive item-query read path
// uses this set to exclude taxonomy entities (Studio, Genre, Person, Year,
// MusicGenre, and the program kinds) from the default inventory. Stock
// excludes these entities because items without ancestor support never live
// under a library root. MusicArtist inherits ancestor support and is not
// included. The kind set is derived from Definition, the single authority, so
// the rule stays in sync with the capability table without a second list.
func KindsWithoutAncestors() []Kind {
	out := make([]Kind, 0, 8)
	for k := Kind(0); k <= KindYear; k++ {
		if def, ok := Definition(k); ok && !def.SupportsAncestors {
			out = append(out, k)
		}
	}
	return out
}

// SupportsUserData reports whether it supports user data.
func SupportsUserData(it Item) bool { return CapabilitiesOf(it).SupportsUserData }

// SupportsPositionTicksResume reports whether it supports storing a resume position.
func SupportsPositionTicksResume(it Item) bool {
	switch it.Kind {
	case KindBook, KindAudioBook:
		return true
	case KindEpisode, KindMovie, KindMusicVideo, KindRecording, KindTrailer, KindVideo:
		if it.ExtraType == nil {
			return true
		}
		switch *it.ExtraType {
		case ExtraTypeSample, ExtraTypeThemeVideo, ExtraTypeTrailer:
			return false
		default:
			return true
		}
	default:
		return false
	}
}

// CanDownload reports whether it has one physical local file eligible for the
// item-download endpoint. Optical-disc video is explicitly excluded because
// its directory layout is not a downloadable media file.
func CanDownload(it Item) bool {
	if it.Path == nil || *it.Path == "" || !filepath.IsAbs(*it.Path) {
		return false
	}
	switch it.Kind {
	case KindAudio, KindAudioBook, KindBook, KindPhoto:
		return true
	case KindEpisode, KindMovie, KindMusicVideo, KindRecording, KindTrailer, KindVideo:
		return it.VideoType == nil || (*it.VideoType != VideoTypeDvd && *it.VideoType != VideoTypeBluRay)
	default:
		return false
	}
}

// IsDisplayOnly reports whether k is a Deadfin display-only kind.
func IsDisplayOnly(k Kind) bool {
	def, ok := Definition(k)
	return ok && def.IsDisplayOnly
}
