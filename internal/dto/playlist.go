// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"bytes"
	"encoding/json"
	"strings"
)

// PlaylistUserPermissions is MediaBrowser.Model.Entities.PlaylistUserPermissions:
// one user's share of a playlist.
type PlaylistUserPermissions struct {
	UserID  UUID `json:"UserId"`
	CanEdit bool `json:"CanEdit"`
}

// PlaylistDto is MediaBrowser.Model.Dto.PlaylistDto.
type PlaylistDto struct {
	OpenAccess bool                      `json:"OpenAccess"`
	Shares     []PlaylistUserPermissions `json:"Shares"`
	ItemIDs    []UUID                    `json:"ItemIds"`
}

// PlaylistCreationResult is MediaBrowser.Model.Playlists.PlaylistCreationResult.
// Its Id is the N-format string the manager returns, not a Guid.
type PlaylistCreationResult struct {
	ID string `json:"Id"`
}

// CreatePlaylistDto is Jellyfin.Api.Models.PlaylistDtos.CreatePlaylistDto.
// IsPublic defaults to true when the body omits it (CreatePlaylistDto.cs).
type CreatePlaylistDto struct {
	Name      *string                   `json:"Name"`
	IDs       CommaDelimitedUUIDs       `json:"Ids"`
	UserID    *UUID                     `json:"UserId"`
	MediaType *string                   `json:"MediaType"`
	Users     []PlaylistUserPermissions `json:"Users"`
	IsPublic  *bool                     `json:"IsPublic"`
}

// UpdatePlaylistDto is Jellyfin.Api.Models.PlaylistDtos.UpdatePlaylistDto.
// Absent members leave the playlist unchanged.
type UpdatePlaylistDto struct {
	Name     *string                    `json:"Name"`
	IDs      *CommaDelimitedUUIDs       `json:"Ids"`
	Users    *[]PlaylistUserPermissions `json:"Users"`
	IsPublic *bool                      `json:"IsPublic"`
}

// UpdatePlaylistUserDto is Jellyfin.Api.Models.PlaylistDtos.UpdatePlaylistUserDto.
type UpdatePlaylistUserDto struct {
	CanEdit *bool `json:"CanEdit"`
}

// CommaDelimitedUUIDs reproduces JsonCommaDelimitedCollectionConverterFactory
// for Guid lists: a JSON array or one comma-separated string.
type CommaDelimitedUUIDs []UUID

func (values *CommaDelimitedUUIDs) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*values = nil
		return nil
	}
	var joined string
	if err := json.Unmarshal(data, &joined); err == nil {
		out := CommaDelimitedUUIDs{}
		for _, part := range strings.Split(joined, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			id, err := ParseUUID(part)
			if err != nil {
				return err
			}
			out = append(out, id)
		}
		*values = out
		return nil
	}
	var array []UUID
	if err := json.Unmarshal(data, &array); err != nil {
		return err
	}
	*values = array
	return nil
}

// CollectionCreationResult is MediaBrowser.Model.Collections.CollectionCreationResult.
type CollectionCreationResult struct {
	ID UUID `json:"Id"`
}
