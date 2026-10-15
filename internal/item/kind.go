// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Kind is the canonical Jellyfin BaseItemKind wire value.
type Kind uint8

const (
	KindAggregateFolder Kind = iota
	KindAudio
	KindAudioBook
	KindBasePluginFolder
	KindBook
	KindBoxSet
	KindChannel
	KindChannelFolderItem
	KindCollectionFolder
	KindEpisode
	KindFolder
	KindGenre
	KindManualPlaylistsFolder
	KindMovie
	KindLiveTvChannel
	KindLiveTvProgram
	KindMusicAlbum
	KindMusicArtist
	KindMusicGenre
	KindMusicVideo
	KindPerson
	KindPhoto
	KindPhotoAlbum
	KindPlaylist
	KindPlaylistsFolder
	KindProgram
	KindRecording
	KindSeason
	KindSeries
	KindStudio
	KindTrailer
	KindTvChannel
	KindTvProgram
	KindUserRootFolder
	KindUserView
	KindVideo
	KindYear
)

// String returns the canonical BaseItemKind name, or empty for an unknown kind.
func (k Kind) String() string {
	switch k {
	case KindAggregateFolder:
		return "AggregateFolder"
	case KindAudio:
		return "Audio"
	case KindAudioBook:
		return "AudioBook"
	case KindBasePluginFolder:
		return "BasePluginFolder"
	case KindBook:
		return "Book"
	case KindBoxSet:
		return "BoxSet"
	case KindChannel:
		return "Channel"
	case KindChannelFolderItem:
		return "ChannelFolderItem"
	case KindCollectionFolder:
		return "CollectionFolder"
	case KindEpisode:
		return "Episode"
	case KindFolder:
		return "Folder"
	case KindGenre:
		return "Genre"
	case KindManualPlaylistsFolder:
		return "ManualPlaylistsFolder"
	case KindMovie:
		return "Movie"
	case KindLiveTvChannel:
		return "LiveTvChannel"
	case KindLiveTvProgram:
		return "LiveTvProgram"
	case KindMusicAlbum:
		return "MusicAlbum"
	case KindMusicArtist:
		return "MusicArtist"
	case KindMusicGenre:
		return "MusicGenre"
	case KindMusicVideo:
		return "MusicVideo"
	case KindPerson:
		return "Person"
	case KindPhoto:
		return "Photo"
	case KindPhotoAlbum:
		return "PhotoAlbum"
	case KindPlaylist:
		return "Playlist"
	case KindPlaylistsFolder:
		return "PlaylistsFolder"
	case KindProgram:
		return "Program"
	case KindRecording:
		return "Recording"
	case KindSeason:
		return "Season"
	case KindSeries:
		return "Series"
	case KindStudio:
		return "Studio"
	case KindTrailer:
		return "Trailer"
	case KindTvChannel:
		return "TvChannel"
	case KindTvProgram:
		return "TvProgram"
	case KindUserRootFolder:
		return "UserRootFolder"
	case KindUserView:
		return "UserView"
	case KindVideo:
		return "Video"
	case KindYear:
		return "Year"
	default:
		return ""
	}
}

// ParseKind parses a canonical BaseItemKind name without regard to case.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "aggregatefolder":
		return KindAggregateFolder, nil
	case "audio":
		return KindAudio, nil
	case "audiobook":
		return KindAudioBook, nil
	case "basepluginfolder":
		return KindBasePluginFolder, nil
	case "book":
		return KindBook, nil
	case "boxset":
		return KindBoxSet, nil
	case "channel":
		return KindChannel, nil
	case "channelfolderitem":
		return KindChannelFolderItem, nil
	case "collectionfolder":
		return KindCollectionFolder, nil
	case "episode":
		return KindEpisode, nil
	case "folder":
		return KindFolder, nil
	case "genre":
		return KindGenre, nil
	case "manualplaylistsfolder":
		return KindManualPlaylistsFolder, nil
	case "movie":
		return KindMovie, nil
	case "livetvchannel":
		return KindLiveTvChannel, nil
	case "livetvprogram":
		return KindLiveTvProgram, nil
	case "musicalbum":
		return KindMusicAlbum, nil
	case "musicartist":
		return KindMusicArtist, nil
	case "musicgenre":
		return KindMusicGenre, nil
	case "musicvideo":
		return KindMusicVideo, nil
	case "person":
		return KindPerson, nil
	case "photo":
		return KindPhoto, nil
	case "photoalbum":
		return KindPhotoAlbum, nil
	case "playlist":
		return KindPlaylist, nil
	case "playlistsfolder":
		return KindPlaylistsFolder, nil
	case "program":
		return KindProgram, nil
	case "recording":
		return KindRecording, nil
	case "season":
		return KindSeason, nil
	case "series":
		return KindSeries, nil
	case "studio":
		return KindStudio, nil
	case "trailer":
		return KindTrailer, nil
	case "tvchannel":
		return KindTvChannel, nil
	case "tvprogram":
		return KindTvProgram, nil
	case "userrootfolder":
		return KindUserRootFolder, nil
	case "userview":
		return KindUserView, nil
	case "video":
		return KindVideo, nil
	case "year":
		return KindYear, nil
	default:
		return 0, fmt.Errorf("invalid item kind %q", s)
	}
}

// MarshalText returns the canonical item-kind text.
func (k Kind) MarshalText() ([]byte, error) {
	if s := k.String(); s != "" {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("invalid item kind %d", k)
}

// UnmarshalText parses case-insensitive item-kind text. On error, it leaves k unchanged.
func (k *Kind) UnmarshalText(text []byte) error {
	parsed, err := ParseKind(string(text))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// MarshalJSON returns the canonical item-kind JSON string.
func (k Kind) MarshalJSON() ([]byte, error) {
	text, err := k.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

// UnmarshalJSON parses a case-insensitive JSON item-kind string or an upstream
// integer ordinal. On error, it leaves k unchanged.
func (k *Kind) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("invalid item kind JSON: empty value")
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("invalid item kind JSON: %w", err)
		}
		return k.UnmarshalText([]byte(text))
	}

	ordinal, ok := parseKindOrdinal(data)
	if !ok {
		return fmt.Errorf("invalid item kind JSON: %s", data)
	}
	*k = Kind(ordinal)
	return nil
}

func parseKindOrdinal(data []byte) (uint8, bool) {
	if len(data) == 0 || data[0] < '0' || data[0] > '9' {
		return 0, false
	}
	if len(data) > 1 && data[0] == '0' {
		return 0, false
	}

	ordinal := uint8(0)
	for _, digit := range data {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		if ordinal > 3 || ordinal == 3 && digit > '6' {
			return 0, false
		}
		ordinal = ordinal*10 + digit - '0'
	}
	return ordinal, true
}
