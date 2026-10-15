// SPDX-License-Identifier: GPL-2.0-only

// Package itemdelete ports the upstream item delete operations
// (LibraryController.DeleteItem and DeleteItems, LibraryManager.DeleteItem).
// A delete removes the item and its descendants from the store, removes their
// internal metadata and trickplay directories, and removes the media path on
// disk.
package itemdelete

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

var (
	// ErrNotFound reports a missing user, or an item that is missing or not
	// visible to the user.
	ErrNotFound = errors.New("item delete: not found")
	// ErrUnauthorized reports a user who may not delete the item.
	ErrUnauthorized = errors.New("item delete: unauthorized")
)

// Items is the store seam a delete needs.
type Items interface {
	Get(context.Context, dto.UUID) (item.Item, error)
	Query(context.Context, store.ItemQuery) (store.ItemQueryResult, error)
	Delete(context.Context, dto.UUID) error
}

// Users loads the acting user.
type Users interface {
	Get(context.Context, dto.UUID) (store.User, error)
}

// Libraries lists the libraries, so a delete never removes a library media
// path or a folder above one.
type Libraries interface {
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// Publisher reports a removed item to LibraryChanged consumers.
type Publisher interface {
	PublishItemRemoved(parentID, collectionFolderID, id dto.UUID)
}

// Options configures a Service.
type Options struct {
	Items     Items
	Users     Users
	Libraries Libraries
	Publisher Publisher
	// MetadataPath is the internal metadata root. Each item owns
	// library/<id[:2]>/<id> below it.
	MetadataPath string
	// TrickplayPath is the trickplay root. Each item owns
	// <dashed id[:2]>/<dashed id> below it.
	TrickplayPath string
	Logger        *slog.Logger
}

// Actor is the authenticated caller.
type Actor struct {
	APIKey bool
	UserID dto.UUID
}

// Service deletes items.
type Service struct {
	items         Items
	users         Users
	libraries     Libraries
	publisher     Publisher
	metadataPath  string
	trickplayPath string
	logger        *slog.Logger
}

// New returns a Service. It panics when Items or Users is nil.
func New(options Options) *Service {
	if options.Items == nil || options.Users == nil || options.Libraries == nil {
		panic("itemdelete: Items, Users, and Libraries are required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{items: options.Items, users: options.Users, libraries: options.Libraries, publisher: options.Publisher, metadataPath: options.MetadataPath, trickplayPath: options.TrickplayPath, logger: logger}
}

// DeleteItem ports LibraryController.DeleteItem. An API key without a user
// acts without a user; any other caller acts as its user.
func (s *Service) DeleteItem(ctx context.Context, actor Actor, id dto.UUID) error {
	var user *store.User
	if !(actor.UserID.IsNil() && actor.APIKey) {
		loaded, err := s.user(ctx, actor.UserID)
		if err != nil {
			return err
		}
		user = loaded
	}
	return s.deleteOne(ctx, user, id)
}

// DeleteItems ports LibraryController.DeleteItems. An API key always acts
// without a user. The items are checked and deleted in order, so a failure
// stops the request after the items before it are gone, as upstream does.
func (s *Service) DeleteItems(ctx context.Context, actor Actor, ids []dto.UUID) error {
	var user *store.User
	if !actor.APIKey {
		if actor.UserID.IsNil() {
			return ErrUnauthorized
		}
		loaded, err := s.user(ctx, actor.UserID)
		if err != nil {
			return err
		}
		user = loaded
	}
	for _, id := range ids {
		if err := s.deleteOne(ctx, user, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) user(ctx context.Context, id dto.UUID) (*store.User, error) {
	if id.IsNil() {
		return nil, ErrNotFound
	}
	user, err := s.users.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Service) deleteOne(ctx context.Context, user *store.User, id dto.UUID) error {
	target, err := s.visibleItem(ctx, user, id)
	if err != nil {
		return err
	}
	if user != nil {
		allowed, err := s.canDelete(ctx, *user, target)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrUnauthorized
		}
	}
	return s.delete(ctx, target)
}

// visibleItem ports LibraryManager.GetItemById<T>(id, user): an item the
// user cannot see is absent.
func (s *Service) visibleItem(ctx context.Context, user *store.User, id dto.UUID) (item.Item, error) {
	if user != nil {
		result, err := s.items.Query(ctx, store.ItemQuery{ItemIDs: []dto.UUID{id}, User: user, UserID: &user.ID, IncludeOwnedItems: true})
		if err != nil {
			return item.Item{}, err
		}
		if len(result.Items) == 0 {
			return item.Item{}, ErrNotFound
		}
	}
	target, err := s.items.Get(ctx, id)
	if isNotFound(err) {
		return item.Item{}, ErrNotFound
	}
	return target, err
}

// deletePolicy is the part of the user policy that BaseItem.CanDelete reads.
type deletePolicy struct {
	IsAdministrator                  bool
	EnableContentDeletion            bool
	EnableContentDeletionFromFolders []string
	EnableCollectionManagement       bool
}

// canDelete ports BaseItem.CanDelete(User): the item kind allows a delete and
// the user is authorized for it.
func (s *Service) canDelete(ctx context.Context, user store.User, target item.Item) (bool, error) {
	if !kindCanDelete(target) {
		return false, nil
	}
	var policy deletePolicy
	if len(user.Policy) != 0 {
		if err := json.Unmarshal(user.Policy, &policy); err != nil {
			return false, fmt.Errorf("item delete: user policy: %w", err)
		}
	}
	switch target.Kind {
	case item.KindBoxSet:
		// BoxSet.IsAuthorizedToDelete.
		return true, nil
	case item.KindPlaylist:
		// Playlist.IsAuthorizedToDelete.
		return policy.IsAdministrator || policy.EnableCollectionManagement, nil
	}
	if policy.EnableContentDeletion {
		return true, nil
	}
	allowed := make(map[dto.UUID]struct{}, len(policy.EnableContentDeletionFromFolders))
	for _, value := range policy.EnableContentDeletionFromFolders {
		if id, err := dto.ParseUUID(value); err == nil {
			allowed[id] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return false, nil
	}
	folders, err := s.collectionFolders(ctx, target)
	if err != nil {
		return false, err
	}
	for _, id := range folders {
		if _, found := allowed[id]; found {
			return true, nil
		}
	}
	return false, nil
}

// collectionFolders ports LibraryManager.GetCollectionFolders(item): the
// libraries above the item.
func (s *Service) collectionFolders(ctx context.Context, target item.Item) ([]dto.UUID, error) {
	var folders []dto.UUID
	seen := map[dto.UUID]struct{}{target.ID: {}}
	current := target
	for current.ParentID != nil && !current.ParentID.IsNil() {
		parentID := *current.ParentID
		if _, loop := seen[parentID]; loop {
			break
		}
		seen[parentID] = struct{}{}
		parent, err := s.items.Get(ctx, parentID)
		if isNotFound(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		if parent.Kind == item.KindCollectionFolder {
			folders = append(folders, parent.ID)
		}
		current = parent
	}
	if target.TopParentID != nil && !target.TopParentID.IsNil() {
		folders = append(folders, *target.TopParentID)
	}
	return folders, nil
}

// kindCanDelete ports the BaseItem.CanDelete() overrides. A channel item
// asks its channel, and Deadfin has no channel that allows a delete.
func kindCanDelete(target item.Item) bool {
	switch target.Kind {
	case item.KindAggregateFolder, item.KindBasePluginFolder, item.KindChannel, item.KindCollectionFolder,
		item.KindGenre, item.KindLiveTvChannel, item.KindLiveTvProgram, item.KindManualPlaylistsFolder,
		item.KindMusicGenre, item.KindPerson, item.KindPlaylistsFolder, item.KindStudio, item.KindUserRootFolder,
		item.KindUserView, item.KindYear:
		return false
	case item.KindMusicArtist:
		// MusicArtist.IsAccessedByName: an artist without a parent is a
		// by-name entity.
		if target.ParentID == nil || target.ParentID.IsNil() {
			return false
		}
	}
	if target.SourceType == item.SourceTypeChannel {
		return false
	}
	return isFileProtocol(target.Path)
}

// isFileProtocol ports BaseItem.IsFileProtocol through
// MediaSourceManager.GetPathProtocol. An empty path has no protocol.
func isFileProtocol(path *string) bool {
	if path == nil || *path == "" {
		return false
	}
	lower := strings.ToLower(*path)
	for _, prefix := range []string{"rtsp", "rtmp", "http", "rtp", "ftp", "udp"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return !strings.Contains(*path, "://")
}

func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound) || store.IsNotFound(err)
}
