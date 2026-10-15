// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"crypto/md5" // #nosec G501 -- Jellyfin's persisted item identifier algorithm.
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

const (
	aggregateFolderCLRType = "MediaBrowser.Controller.Entities.AggregateFolder"
	userRootFolderCLRType  = "MediaBrowser.Controller.Entities.UserRootFolder"
	playlistsFolderCLRType = "Emby.Server.Implementations.Playlists.PlaylistsFolder"
)

// BootstrapSystemFolders persists the source-shaped roots required by itemId=0
// and playlist discovery. Callers create the backing directories; this method
// owns deterministic item identities and their idempotent store hierarchy.
func (s *ItemStore) BootstrapSystemFolders(ctx context.Context, programData string) error {
	aggregatePath := filepath.Join(programData, "root", "default")
	rootPath := aggregatePath
	playlistsPath := filepath.Join(programData, "data", "playlists")
	aggregateID := sourceItemID(aggregateFolderCLRType, aggregatePath, programData)
	rootID := sourceItemID(userRootFolderCLRType, rootPath, programData)
	playlistsID := sourceItemID(playlistsFolderCLRType, playlistsPath, programData)
	aggregateName, rootName, playlistsName := "default", "default", "Playlists"

	aggregate := item.Item{
		ID:       aggregateID,
		Kind:     item.KindAggregateFolder,
		Name:     &aggregateName,
		Path:     &aggregatePath,
		IsFolder: true,
	}
	if err := s.createSystemFolderIfMissing(ctx, aggregate); err != nil {
		return fmt.Errorf("bootstrap aggregate folder: %w", err)
	}
	root := item.Item{
		ID:       rootID,
		Kind:     item.KindUserRootFolder,
		Name:     &rootName,
		Path:     &rootPath,
		IsFolder: true,
	}
	if err := s.createSystemFolderIfMissing(ctx, root); err != nil {
		return fmt.Errorf("bootstrap user root folder: %w", err)
	}
	playlists := item.Item{
		ID:          playlistsID,
		Kind:        item.KindPlaylistsFolder,
		ParentID:    &aggregateID,
		TopParentID: &aggregateID,
		Name:        &playlistsName,
		Path:        &playlistsPath,
		IsFolder:    true,
	}
	if err := s.createSystemFolderIfMissing(ctx, playlists); err != nil {
		return fmt.Errorf("bootstrap playlists folder: %w", err)
	}
	return nil
}

// createSystemFolderIfMissing never refreshes an existing system root: a
// restart must retain scanner metadata, timestamps, and normalized satellites.
func (s *ItemStore) createSystemFolderIfMissing(ctx context.Context, value item.Item) error {
	_, err := s.Get(ctx, value.ID)
	if err == nil {
		return nil
	}
	if !errorsIsNoRows(err) {
		return err
	}
	return s.Upsert(ctx, value)
}

func sourceItemID(clrType, path, programData string) dto.UUID {
	key, err := filepath.Rel(programData, path)
	if err != nil || strings.HasPrefix(key, "..") {
		key = path
	}
	key = strings.ReplaceAll(key, "/", "\\")
	key = strings.ToLower(key)
	units := utf16.Encode([]rune(clrType + key))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	sum := md5.Sum(encoded)
	return dto.UUID{sum[3], sum[2], sum[1], sum[0], sum[5], sum[4], sum[7], sum[6], sum[8], sum[9], sum[10], sum[11], sum[12], sum[13], sum[14], sum[15]}
}
