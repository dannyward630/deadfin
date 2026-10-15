// SPDX-License-Identifier: GPL-2.0-only

// Package subtitleedit ports the external subtitle writes of
// SubtitleController: UploadSubtitle and DeleteSubtitle
// (SubtitleManager.UploadSubtitle and SubtitleManager.DeleteSubtitles). It
// writes or removes the subtitle file, then stores the item's external
// subtitle streams again from disk, so MediaStreams and PlaybackInfo show the
// change without a library scan.
package subtitleedit

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// maxSubtitleBytes bounds a decoded upload. Upstream relies on Kestrel's
// request limit (about 28.6 MB of Base64 text).
const maxSubtitleBytes = 32 << 20

// ErrItemNotFound reports a missing item, an item the user cannot see, or an
// item that is not a video.
var ErrItemNotFound = errors.New("subtitleedit: item not found")

// ErrInvalidRequest reports a request upstream rejects with an
// ArgumentException (400): an unsupported format, a language with a path
// separator, or a body that is not Base64.
var ErrInvalidRequest = errors.New("subtitleedit: invalid request")

// ErrSubtitleNotFound reports a subtitle index the item does not have.
var ErrSubtitleNotFound = errors.New("subtitleedit: subtitle stream not found")

// ErrEmbeddedSubtitle reports a delete of a subtitle stream inside the media
// file. Only external subtitle files can be deleted.
var ErrEmbeddedSubtitle = errors.New("subtitleedit: subtitle stream is not an external file")

// Items is the item persistence seam. *store.ItemStore satisfies it.
type Items interface {
	GetBatch(context.Context, []dto.UUID) ([]item.Item, error)
	Streams(context.Context, dto.UUID) ([]playback.MediaStream, error)
	ReplaceStreams(context.Context, dto.UUID, []playback.MediaStream) error
}

// Libraries lists the configured libraries. *library.Topology satisfies it.
type Libraries interface {
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// Access filters item IDs to those a user may see.
// store.PlaybackItemAccessReader satisfies it.
type Access interface {
	AccessiblePlaybackItemIDs(ctx context.Context, userID dto.UUID, ids []dto.UUID) ([]dto.UUID, error)
}

// Publisher receives item-updated notifications. *library.ChangeBus
// satisfies it.
type Publisher interface {
	PublishItemUpdated(parentID, collectionFolderID dto.UUID, ids []dto.UUID)
}

// Options constructs a Service. Items and MetadataPath are required.
// Libraries, Access, Publisher, and Logger may be nil.
type Options struct {
	Items     Items
	Libraries Libraries
	Access    Access
	Publisher Publisher
	// MetadataPath is the internal metadata root (config.Paths.Metadata).
	MetadataPath string
	Logger       *slog.Logger
}

// Service uploads and deletes external subtitle files.
type Service struct {
	items        Items
	libraries    Libraries
	access       Access
	publisher    Publisher
	metadataPath string
	logger       *slog.Logger
}

// New constructs the subtitle edit service.
func New(options Options) *Service {
	if options.Items == nil {
		panic("subtitleedit: items are required")
	}
	if options.MetadataPath == "" {
		panic("subtitleedit: metadata path is required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		items:        options.Items,
		libraries:    options.Libraries,
		access:       options.Access,
		publisher:    options.Publisher,
		metadataPath: options.MetadataPath,
		logger:       logger,
	}
}

// Upload is the UploadSubtitleDto body after binding. Data is Base64 text.
type Upload struct {
	Language          string
	Format            string
	IsForced          bool
	IsHearingImpaired bool
	Data              string
}

// Upload saves an uploaded subtitle (SubtitleManager.TrySaveSubtitle). The
// file goes beside the media when the library sets SaveSubtitlesWithMedia,
// otherwise into the item's internal metadata directory. When the media
// folder cannot be written (a read-only mount), the file goes into the
// internal metadata directory instead (divergence D-47). userID is the
// requesting user, or the nil UUID for an API key.
func (s *Service) Upload(ctx context.Context, userID, id dto.UUID, upload Upload) error {
	target, err := s.loadVideo(ctx, userID, id)
	if err != nil {
		return err
	}
	format := strings.ToLower(upload.Format)
	if !allowedFormat(format) {
		return fmt.Errorf("%w: unsupported subtitle format: '%s'", ErrInvalidRequest, upload.Format)
	}
	language := strings.ToLower(upload.Language)
	if strings.ContainsRune(language, '/') || strings.ContainsRune(language, filepath.Separator) {
		return fmt.Errorf("%w: language contains invalid characters", ErrInvalidRequest)
	}
	data, err := decodeBase64(upload.Data)
	if err != nil {
		return err
	}

	mediaPath := *target.Path
	saveName := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath)) + "." + language
	if upload.IsForced {
		saveName += ".forced"
	}
	if upload.IsHearingImpaired {
		saveName += ".sdh"
	}
	metadataDir := library.InternalMetadataDir(s.metadataPath, target.ID)
	directories := []string{metadataDir}
	withMedia, err := s.saveSubtitlesWithMedia(ctx, target)
	if err != nil {
		return err
	}
	if withMedia {
		directories = []string{filepath.Dir(mediaPath), metadataDir}
	}

	var saveErr error
	for _, directory := range directories {
		path, err := saveSubtitle(directory, saveName, format, data)
		if err == nil {
			s.logger.Info("saved subtitle", "item", target.ID.String(), "path", path)
			saveErr = nil
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		s.logger.Warn("save subtitle", "item", target.ID.String(), "directory", directory, "error", err)
		saveErr = errors.Join(saveErr, err)
	}
	if saveErr != nil {
		return saveErr
	}
	return s.refreshStreams(ctx, target)
}

// Delete removes an external subtitle file (SubtitleManager.DeleteSubtitles)
// and stores the item's streams again. index is the subtitle's stream index.
// A subtitle inside the media file is not deleted.
func (s *Service) Delete(ctx context.Context, userID, id dto.UUID, index int) error {
	target, err := s.loadVideo(ctx, userID, id)
	if err != nil {
		return err
	}
	streams, err := s.items.Streams(ctx, target.ID)
	if err != nil {
		return err
	}
	var selected *playback.MediaStream
	for position := range streams {
		if streams[position].Type == playback.StreamTypeSubtitle && streams[position].Index == index {
			selected = &streams[position]
			break
		}
	}
	if selected == nil {
		return ErrSubtitleNotFound
	}
	if !selected.IsExternal || selected.Path == "" {
		return ErrEmbeddedSubtitle
	}
	path := filepath.Clean(selected.Path)
	metadataDir := library.InternalMetadataDir(s.metadataPath, target.ID)
	if filepath.Dir(path) != filepath.Dir(*target.Path) && filepath.Dir(path) != metadataDir {
		// A stored external stream always comes from one of these two
		// directories. Refuse anything else rather than delete it.
		return fmt.Errorf("subtitleedit: subtitle path %q is outside the item's folders", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.logger.Info("deleted subtitle", "item", target.ID.String(), "path", path)
	return s.refreshStreams(ctx, target)
}

// loadVideo ports GetItemById<Video>(itemId, userId): a missing item, an item
// the user cannot see, and a non-video item are all not found.
func (s *Service) loadVideo(ctx context.Context, userID, id dto.UUID) (item.Item, error) {
	loaded, err := s.items.GetBatch(ctx, []dto.UUID{id})
	if err != nil {
		return item.Item{}, err
	}
	if len(loaded) == 0 || !isVideoFile(loaded[0]) {
		return item.Item{}, ErrItemNotFound
	}
	if userID != dto.NilUUID && s.access != nil {
		accessible, err := s.access.AccessiblePlaybackItemIDs(ctx, userID, []dto.UUID{id})
		if err != nil {
			return item.Item{}, err
		}
		if !containsID(accessible, id) {
			return item.Item{}, ErrItemNotFound
		}
	}
	return loaded[0], nil
}

// saveSubtitlesWithMedia reads LibraryOptions.SaveSubtitlesWithMedia from the
// item's library. An item outside a configured library uses the default
// (true), as LibraryManager.GetLibraryOptions returns new LibraryOptions().
func (s *Service) saveSubtitlesWithMedia(ctx context.Context, target item.Item) (bool, error) {
	if s.libraries == nil || target.TopParentID == nil {
		return true, nil
	}
	folders, err := s.libraries.VirtualFolders(ctx)
	if err != nil {
		return false, err
	}
	for _, folder := range folders {
		if folder.CollectionFolderID == *target.TopParentID {
			return folder.Options.SaveSubtitlesWithMedia, nil
		}
	}
	return true, nil
}

// refreshStreams stores the external subtitle streams found on disk in place
// of the stored ones. Upstream queues a metadata refresh that re-reads the
// external files; Deadfin does the external-file part synchronously.
func (s *Service) refreshStreams(ctx context.Context, target item.Item) error {
	external, err := library.ReadExternalSubtitleStreams(*target.Path, library.InternalMetadataDir(s.metadataPath, target.ID))
	if err != nil {
		return err
	}
	previous, err := s.items.Streams(ctx, target.ID)
	if err != nil {
		return err
	}
	if err := s.items.ReplaceStreams(ctx, target.ID, library.ReplaceExternalSubtitleStreams(previous, external)); err != nil {
		return err
	}
	if s.publisher != nil {
		parentID, collectionFolderID := dto.NilUUID, dto.NilUUID
		if target.ParentID != nil {
			parentID = *target.ParentID
		}
		if target.TopParentID != nil {
			collectionFolderID = *target.TopParentID
		}
		s.publisher.PublishItemUpdated(parentID, collectionFolderID, []dto.UUID{target.ID})
	}
	return nil
}

// saveSubtitle ports SubtitleManager.TrySaveToFiles for one directory. An
// existing file is never replaced: the name takes a counter
// (<name>.0.<ext>, <name>.1.<ext>, ...) as upstream does.
func saveSubtitle(directory, saveName, format string, data []byte) (string, error) {
	base := filepath.Join(directory, saveName)
	if filepath.Dir(base) != filepath.Clean(directory) {
		return "", fmt.Errorf("%w: subtitle path is outside %q", ErrInvalidRequest, directory)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	path := base + "." + format
	for counter := 0; ; counter++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			path = base + "." + strconv.Itoa(counter) + "." + format
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
}

func decodeBase64(value string) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(value)) > maxSubtitleBytes+3 {
		return nil, fmt.Errorf("%w: subtitle body exceeds %d bytes", ErrInvalidRequest, maxSubtitleBytes)
	}
	// FromBase64Transform ignores white space.
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, value)
	data, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle data is not Base64: %v", ErrInvalidRequest, err)
	}
	return data, nil
}

// allowedFormat ports _allowedSubtitleFormats: the naming subtitle
// extensions without the dot.
func allowedFormat(format string) bool {
	if format == "" {
		return false
	}
	for _, extension := range naming.DefaultOptions().SubtitleFileExtensions {
		if strings.TrimPrefix(extension, ".") == format {
			return true
		}
	}
	return false
}

func isVideoFile(value item.Item) bool {
	if value.IsFolder || value.Path == nil || *value.Path == "" || !filepath.IsAbs(*value.Path) {
		return false
	}
	definition, found := item.Definition(value.Kind)
	return found && definition.DefaultMediaType == item.MediaTypeVideo
}

func containsID(values []dto.UUID, id dto.UUID) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}
